package git

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FileStatus struct {
	Path    string
	Orig    string
	Index   byte // Staged status: 'M', 'A', 'D', 'R', '?'
	Work    byte // Working tree status: 'M', 'D', '?'
	Staged  bool
	Untrack bool
}

func (f FileStatus) Badge() string {
	c := f.Work
	if f.Staged {
		c = f.Index
	}
	if c == '?' {
		return "U"
	}
	if c == 0 || c == ' ' {
		return "M"
	}
	return string(c)
}

type Commit struct {
	Hash      string
	ShortHash string
	Parents   []string
	Author    string
	Email     string
	Date      string    // short author date, for the list
	Time      time.Time // author date with its offset; zero when unparsed
	Refs      []string
	Subject   string
	Body      string // message after the subject, without trailers
	CoAuthors []CoAuthor
}

type CommitFile struct {
	Status string // "M", "A", "D", "R"
	Path   string
}

type StashEntry struct {
	Ref     string // e.g. "stash@{0}"
	Message string
}

// Branch is a local branch, as the branch picker lists it.
type Branch struct {
	Name string
	// Current is set for the branch the repository's working tree had checked
	// out when it was read. RepoSnapshot.Branch is kept up to date by a
	// refresh of the working tree, so it is the one to compare names with.
	Current bool
	// Worktree is the path of another worktree that has the branch checked
	// out, which keeps this one from switching to it. It is empty for a branch
	// nobody has checked out and for the current one.
	Worktree string
	// Time is the committer date of the branch's tip, zero when unknown.
	Time time.Time
}

type RepoSnapshot struct {
	Root        string
	Branch      string
	Ahead       int
	Behind      int
	Branches    []Branch
	Stashes     []StashEntry
	Staged      []FileStatus
	Unstaged    []FileStatus
	StatusByRel map[string]string // rel path -> badge ("M", "A", "D", "U")
	// Ignored holds what Git ignores, and for each path whether it is a
	// directory. What is in an ignored directory is not listed, here or in the
	// tree: node_modules alone can hold more files than the rest of the
	// repository by orders of magnitude, so the view reads it when it opens.
	Ignored   map[string]bool
	TreeRoots []string
	TreeDirs  map[string][]string
	// AllFiles is the sorted list of the files Git tracks or could track,
	// without the ignored ones: it is what a window opens a file from, and what
	// the performance log sizes a repository by.
	AllFiles []string
	Commits  []Commit
	Graph    []GraphRow
}

func findWorktreeDotGitRoot(dir string) string {
	cur, err := filepath.Abs(dir)
	if err != nil {
		cur = filepath.Clean(dir)
	}
	for {
		if st, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			if st.IsDir() {
				return cur
			}
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// gitEnv is the environment every Git command runs in:
//
//   - GIT_OPTIONAL_LOCKS=0 stops read-only commands such as `git status` from
//     rewriting .git/index to refresh its stat cache, which our own file
//     watcher would see as a change.
//   - LC_ALL=C keeps Git's messages in English whatever the user's locale is:
//     the checkout errors we recognise (see classifyCheckoutError) are matched
//     by their text.
func Env() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

// gitArgs keeps non-ASCII paths readable: by default Git prints them as
// quoted octal escapes in status and diff output.
func gitArgs(args ...string) []string {
	return append([]string{"-c", "core.quotePath=false"}, args...)
}

func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", gitArgs(args...)...)
	cmd.Dir = dir
	cmd.Env = Env()
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Fallback when a hook/tool temporarily flips core.bare=true on a standard .git worktree
		if root := findWorktreeDotGitRoot(dir); root != "" {
			wtArgs := append([]string{"--git-dir=" + filepath.Join(root, ".git"), "--work-tree=" + root}, args...)
			wtCmd := exec.Command("git", gitArgs(wtArgs...)...)
			wtCmd.Dir = root
			wtCmd.Env = Env()
			var wtOut, wtErr bytes.Buffer
			wtCmd.Stdout = &wtOut
			wtCmd.Stderr = &wtErr
			if err2 := wtCmd.Run(); err2 == nil {
				return wtOut.String(), nil
			}
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &Error{Args: args, Msg: msg}
	}
	return out.String(), nil
}

func LoadRepo(dir string) (*RepoSnapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rootOut, err := Run(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(rootOut)
	snap := &RepoSnapshot{
		Root:        root,
		StatusByRel: make(map[string]string),
		TreeDirs:    make(map[string][]string),
	}

	var (
		bOut, brOut, stOut, logOut, lsOut string
		bErr, brErr, stErr, logErr, lsErr error
		wg                                sync.WaitGroup
	)
	wg.Add(5)
	go func() {
		defer wg.Done()
		bOut, bErr = Run(root, statusArgs...)
	}()
	go func() {
		defer wg.Done()
		brOut, brErr = Run(root, "for-each-ref", "--sort=-committerdate", "refs/heads/", "--format="+branchFormat)
	}()
	go func() {
		defer wg.Done()
		stOut, stErr = Run(root, "stash", "list", "--format=%gd%x09%gs")
	}()
	go func() {
		defer wg.Done()
		// shortcut: caps log at 400 commits per load for instant frame times; increase or paginate for deep history
		logOut, logErr = Run(root, "log", "--all", "--topo-order", "--date=short", "-n", "400",
			"--pretty=format:"+commitLogFormat)
	}()
	go func() {
		defer wg.Done()
		lsOut, lsErr = Run(root, "ls-files", "-co", "--exclude-standard", "-z")
	}()
	wg.Wait()

	// Branch & ahead/behind
	if bErr == nil {
		parseStatusV2(bOut, snap)
	}

	// Local branches
	if brErr == nil {
		snap.Branches = parseBranches(brOut)
	}

	// Stashes
	if stErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(stOut), "\n") {
			if line == "" {
				continue
			}
			ref, msg, _ := strings.Cut(line, "\t")
			snap.Stashes = append(snap.Stashes, StashEntry{Ref: ref, Message: msg})
		}
	}

	// Commits & Graph
	if logErr == nil {
		snap.Commits = parseCommitLog(logOut)
		snap.Graph = ComputeGraph(snap.Commits)
	}

	if lsErr == nil {
		buildWorkspaceTreeFromList(lsOut, snap)
	} else {
		buildWorkspaceTreeFallback(root, snap)
	}
	return snap, nil
}

// statusArgs is how the repository's status is read. With --ignored=matching
// the same walk also names what Git ignores: an ignored directory as one line,
// however much is in it, where -uall alone or --ignored=traditional would list
// every file of node_modules.
var statusArgs = []string{"status", "--porcelain=v2", "--branch", "-uall", "--ignored=matching"}

// branchFormat is the for-each-ref format parseBranches reads: whether the
// branch is checked out here (*), its name, the committer date of its tip and
// the worktree that has it checked out, separated by tabs. The path is last
// because it may hold anything but a newline.
const branchFormat = "%(HEAD)%09%(refname:lstrip=2)%09%(committerdate:unix)%09%(worktreepath)"

// parseBranches reads the output of for-each-ref with branchFormat. The
// current branch comes first and the others follow by the date of their tip,
// newest first, whatever order Git listed them in.
func parseBranches(out string) []Branch {
	var branches []Branch
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 2 || f[1] == "" {
			continue
		}
		b := Branch{Name: f[1], Current: strings.TrimSpace(f[0]) == "*"}
		if len(f) > 2 {
			if secs, err := strconv.ParseInt(f[2], 10, 64); err == nil && secs > 0 {
				b.Time = time.Unix(secs, 0)
			}
		}
		if len(f) > 3 && !b.Current {
			b.Worktree = f[3]
		}
		branches = append(branches, b)
	}
	sort.SliceStable(branches, func(i, j int) bool {
		a, b := branches[i], branches[j]
		switch {
		case a.Current != b.Current:
			return a.Current
		case !a.Time.Equal(b.Time):
			return a.Time.After(b.Time)
		}
		return a.Name < b.Name
	})
	return branches
}

func parseStatusV2(out string, snap *RepoSnapshot) {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# branch.head ") {
			snap.Branch = strings.TrimPrefix(line, "# branch.head ")
		} else if strings.HasPrefix(line, "# branch.ab ") {
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			for _, f := range fields {
				if strings.HasPrefix(f, "+") {
					snap.Ahead, _ = strconv.Atoi(f[1:])
				} else if strings.HasPrefix(f, "-") {
					snap.Behind, _ = strconv.Atoi(f[1:])
				}
			}
		} else if strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 ") {
			// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
			// 2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>\t<origPath>
			fields := strings.SplitN(line, " ", 9)
			if len(fields) < 9 {
				continue
			}
			xy := fields[1]
			if len(xy) < 2 {
				continue
			}
			x, y := xy[0], xy[1]
			rest := fields[8]
			path := rest
			orig := ""
			if fields[0] == "2" {
				// fields[8] has "<X><score> <path>\t<origPath>"
				if _, after, ok := strings.Cut(rest, " "); ok {
					path, orig, _ = strings.Cut(after, "\t")
				}
			}
			if x != '.' {
				snap.Staged = append(snap.Staged, FileStatus{
					Path:   path,
					Orig:   orig,
					Index:  x,
					Work:   y,
					Staged: true,
				})
				snap.StatusByRel[path] = string(x)
			}
			if y != '.' {
				snap.Unstaged = append(snap.Unstaged, FileStatus{
					Path:  path,
					Orig:  orig,
					Index: x,
					Work:  y,
				})
				snap.StatusByRel[path] = string(y)
			}
		} else if strings.HasPrefix(line, "? ") {
			path := strings.TrimPrefix(line, "? ")
			snap.Unstaged = append(snap.Unstaged, FileStatus{
				Path:    path,
				Index:   '?',
				Work:    '?',
				Untrack: true,
			})
			snap.StatusByRel[path] = "U"
		} else if rest, ok := strings.CutPrefix(line, "! "); ok {
			addIgnoredPath(snap, rest)
		}
	}

	// Propagate modified badge up to ancestor directories
	var dirsToMark [][2]string
	for p, badge := range snap.StatusByRel {
		for d := filepath.Dir(p); d != "." && d != "/" && d != ""; d = filepath.Dir(d) {
			dirsToMark = append(dirsToMark, [2]string{filepath.ToSlash(d), badge})
		}
	}
	for _, dm := range dirsToMark {
		if _, exists := snap.StatusByRel[dm[0]]; !exists {
			snap.StatusByRel[dm[0]] = "M"
		}
	}
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".mygo", "build", "dist", ".next", "target", "vendor", ".venv", "venv", "__pycache__":
		return true
	}
	return false
}

// addTreePath records p as a child of its directory, and those directories as
// children of theirs. seen maps a directory to what is in it.
func addTreePath(seen map[string]map[string]bool, p string, isDir bool) {
	if isDir && seen[p] == nil {
		seen[p] = make(map[string]bool)
	}
	for cur := p; ; {
		parent := parentDir(cur)
		m := seen[parent]
		if m == nil {
			m = make(map[string]bool)
			seen[parent] = m
		}
		m[cur] = true
		if parent == "" {
			return
		}
		cur = parent
	}
}

// buildWorkspaceTreeFromList builds the tree from the output of ls-files and
// what parseStatusV2 found to be ignored.
func buildWorkspaceTreeFromList(nulOut string, snap *RepoSnapshot) {
	seenDirs := make(map[string]map[string]bool)
	var allFiles []string
	for _, raw := range strings.Split(nulOut, "\x00") {
		raw = filepath.ToSlash(raw)
		// A path ending in a slash is a repository inside this one, which Git
		// does not look into: a directory, not a file to open.
		p, isDir := strings.CutSuffix(raw, "/")
		if p == "" || hiddenEntry(path.Base(p)) {
			continue
		}
		if !isDir {
			allFiles = append(allFiles, p)
		}
		addTreePath(seenDirs, p, isDir)
	}
	sort.Strings(allFiles)
	snap.AllFiles = slices.Compact(allFiles)

	// A directory that holds nothing but ignored files is as good as ignored
	// itself, though no pattern names it: *.log and a logs/ of nothing else.
	var impliedDirs []string
	for p := range snap.Ignored {
		for d := parentDir(p); d != ""; d = parentDir(d) {
			if _, listed := seenDirs[d]; listed {
				break
			}
			impliedDirs = append(impliedDirs, d)
		}
	}
	for p, isDir := range snap.Ignored {
		addTreePath(seenDirs, p, isDir)
	}
	for _, d := range impliedDirs {
		snap.Ignored[d] = true
	}

	for dir, childrenMap := range seenDirs {
		entries := make([]DirEntry, 0, len(childrenMap))
		for child := range childrenMap {
			_, isDir := seenDirs[child]
			entries = append(entries, DirEntry{Path: child, IsDir: isDir})
		}
		sortDirEntries(entries)
		combined := make([]string, len(entries))
		for i, e := range entries {
			combined[i] = e.Path
		}
		if dir == "" {
			snap.TreeRoots = combined
		} else {
			snap.TreeDirs[dir] = combined
		}
	}
}

func buildWorkspaceTreeFallback(root string, snap *RepoSnapshot) {
	const maxFallbackFiles = 10000
	var walk func(relDir string) []string
	walk = func(relDir string) []string {
		if len(snap.AllFiles) >= maxFallbackFiles {
			return nil
		}
		absDir := root
		if relDir != "" {
			absDir = filepath.Join(root, filepath.FromSlash(relDir))
		}
		entries, err := os.ReadDir(absDir)
		if err != nil {
			return nil
		}
		var dirs, files []string
		for _, e := range entries {
			if len(snap.AllFiles) >= maxFallbackFiles {
				break
			}
			name := e.Name()
			if e.IsDir() {
				if ignoredDir(name) {
					continue
				}
				childRel := name
				if relDir != "" {
					childRel = relDir + "/" + name
				}
				children := walk(childRel)
				snap.TreeDirs[childRel] = children
				dirs = append(dirs, childRel)
			} else {
				childRel := name
				if relDir != "" {
					childRel = relDir + "/" + name
				}
				files = append(files, childRel)
				snap.AllFiles = append(snap.AllFiles, childRel)
			}
		}
		slices.SortFunc(dirs, compareNatural)
		slices.SortFunc(files, compareNatural)
		return append(dirs, files...)
	}
	snap.TreeRoots = walk("")
	sort.Strings(snap.AllFiles)
}

const maxPreviewBytes = 512 * 1024

func sanitizePreviewText(data []byte) string {
	if looksBinary(data) {
		return "// Binary file not shown"
	}
	if len(data) > maxPreviewBytes {
		return string(data[:maxPreviewBytes]) + "\n// … file truncated at 512 KB …\n"
	}
	return string(data)
}

func LoadCommitDetails(root, hash string) (body string, files []CommitFile, err error) {
	bOut, err := Run(root, "show", "-s", "--format=%B", hash)
	if err != nil {
		return "", nil, err
	}
	body = strings.TrimSpace(bOut)

	fOut, err := Run(root, "diff-tree", "--root", "--no-commit-id", "--name-status", "-r", hash)
	if err != nil {
		return body, nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(fOut), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			status := parts[0]
			if len(status) > 1 {
				status = status[:1]
			}
			path := parts[len(parts)-1]
			files = append(files, CommitFile{Status: status, Path: path})
		}
	}
	return body, files, nil
}

func ReadFileAtRef(root, ref, relPath string) string {
	out, err := Run(root, "show", ref+":"+relPath)
	if err != nil {
		return ""
	}
	return sanitizePreviewText([]byte(out))
}

// ReadTextAtRef returns all of a text file at a ref, to compare with a file shown
// whole, or "" when it has none: cut short, every line after the cut would count as
// a change. Past the size of what the Files view holds whole, or if it is not text,
// there is nothing to compare it with.
func ReadTextAtRef(root, ref, relPath string) string {
	out, err := Run(root, "show", ref+":"+relPath)
	if err != nil || len(out) > previewPartBytes || looksBinary([]byte(out[:min(len(out), binarySniffBytes)])) {
		return ""
	}
	return out
}

// ChangedLines returns how many lines of a file were added and removed since HEAD,
// and whether Git could say. Git counts them without loading both versions into
// the app, which makes this the cheap way to learn how much a comparison would take.
func ChangedLines(root, relPath string) (int, bool) {
	out, err := Run(root, "--literal-pathspecs", "diff", "--numstat", "HEAD", "--", relPath)
	if err != nil {
		return 0, false
	}
	if out == "" {
		return 0, true
	}
	return parseNumstat(out)
}

// parseNumstat reads the first line of git diff --numstat, which counts its added
// and removed lines, or has a dash for each in a file Git takes as binary.
func parseNumstat(out string) (int, bool) {
	fields := strings.SplitN(out, "\t", 3)
	if len(fields) < 3 {
		return 0, false
	}
	added, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	removed, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return added + removed, true
}

func ReadWorktreeFile(root, relPath string) string {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPreviewBytes+1))
	if err != nil {
		return ""
	}
	return sanitizePreviewText(data)
}

func LoadWorkingDiffTexts(root string, st FileStatus) (oldText, newText string) {
	if st.Staged {
		oldText = ReadFileAtRef(root, "HEAD", st.Path)
		newText = ReadFileAtRef(root, ":0", st.Path)
		return oldText, newText
	}
	if st.Untrack {
		return "", ReadWorktreeFile(root, st.Path)
	}
	// Unstaged change: compare index (:0) vs worktree; fallback to HEAD if not in index
	oldText = ReadFileAtRef(root, ":0", st.Path)
	if oldText == "" {
		oldText = ReadFileAtRef(root, "HEAD", st.Path)
	}
	if st.Work == 'D' {
		return oldText, ""
	}
	return oldText, ReadWorktreeFile(root, st.Path)
}

func LoadCommitFileDiffTexts(root, hash string, cf CommitFile) (oldText, newText string) {
	if cf.Status != "A" {
		oldText = ReadFileAtRef(root, hash+"~1", cf.Path)
	}
	if cf.Status != "D" {
		newText = ReadFileAtRef(root, hash, cf.Path)
	}
	return oldText, newText
}

// Git mutation commands for daily Git workflow

func StageFile(root, path string) error {
	_, err := Run(root, "add", "--", path)
	return err
}

func StageAll(root string) error {
	_, err := Run(root, "add", "-A")
	return err
}

func UnstageFile(root, path string) error {
	if _, err := Run(root, "restore", "--staged", "--", path); err != nil {
		_, err2 := Run(root, "reset", "HEAD", "--", path)
		return err2
	}
	return nil
}

func UnstageAll(root string) error {
	_, err := Run(root, "reset")
	return err
}

func DiscardFile(root string, st FileStatus) error {
	if st.Untrack {
		return os.RemoveAll(filepath.Join(root, filepath.FromSlash(st.Path)))
	}
	_, err := Run(root, "checkout", "--", st.Path)
	return err
}

func CreateCommit(root, message string, amend bool) error {
	args := []string{"commit", "-m", message}
	if amend {
		args = append(args, "--amend")
	}
	_, err := Run(root, args...)
	return err
}

func CheckoutBranch(root, branch string, create bool) error {
	if create {
		_, err := Run(root, "checkout", "-b", branch)
		return err
	}
	_, err := Run(root, "checkout", branch)
	return err
}

func Fetch(root string) error {
	_, err := Run(root, "fetch", "--all", "--prune")
	return err
}

func Pull(root string) error {
	_, err := Run(root, "pull", "--rebase")
	return err
}

func Push(root string) error {
	_, err := Run(root, "push")
	return err
}

func StashPush(root, message string) error {
	args := []string{"stash", "push", "-u"}
	if strings.TrimSpace(message) != "" {
		args = append(args, "-m", strings.TrimSpace(message))
	}
	_, err := Run(root, args...)
	return err
}

func StashPop(root, ref string) error {
	if ref == "" {
		ref = "stash@{0}"
	}
	_, err := Run(root, "stash", "pop", ref)
	return err
}

func StashDrop(root, ref string) error {
	_, err := Run(root, "stash", "drop", ref)
	return err
}
