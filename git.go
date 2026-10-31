package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

type RepoSnapshot struct {
	Root        string
	Branch      string
	Ahead       int
	Behind      int
	Branches    []string
	Stashes     []StashEntry
	Staged      []FileStatus
	Unstaged    []FileStatus
	StatusByRel map[string]string // rel path -> badge ("M", "A", "D", "U")
	TreeRoots   []string
	TreeDirs    map[string][]string
	AllFiles    []string
	Commits     []Commit
	Graph       []GraphRow
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
func gitEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

// gitArgs keeps non-ASCII paths readable: by default Git prints them as
// quoted octal escapes in status and diff output.
func gitArgs(args ...string) []string {
	return append([]string{"-c", "core.quotePath=false"}, args...)
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", gitArgs(args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Fallback when a hook/tool temporarily flips core.bare=true on a standard .git worktree
		if root := findWorktreeDotGitRoot(dir); root != "" {
			wtArgs := append([]string{"--git-dir=" + filepath.Join(root, ".git"), "--work-tree=" + root}, args...)
			wtCmd := exec.Command("git", gitArgs(wtArgs...)...)
			wtCmd.Dir = root
			wtCmd.Env = gitEnv()
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
		return "", &GitError{Args: args, Msg: msg}
	}
	return out.String(), nil
}

func LoadRepo(dir string) (*RepoSnapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rootOut, err := runGit(abs, "rev-parse", "--show-toplevel")
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
		bOut, bErr = runGit(root, "status", "--porcelain=v2", "--branch", "-uall")
	}()
	go func() {
		defer wg.Done()
		brOut, brErr = runGit(root, "for-each-ref", "--sort=-committerdate", "refs/heads/", "--format=%(refname:short)")
	}()
	go func() {
		defer wg.Done()
		stOut, stErr = runGit(root, "stash", "list", "--format=%gd%x09%gs")
	}()
	go func() {
		defer wg.Done()
		// shortcut: caps log at 400 commits per load for instant frame times; increase or paginate for deep history
		logOut, logErr = runGit(root, "log", "--all", "--topo-order", "--date=short", "-n", "400",
			"--pretty=format:"+commitLogFormat)
	}()
	go func() {
		defer wg.Done()
		lsOut, lsErr = runGit(root, "ls-files", "-co", "--exclude-standard", "-z")
	}()
	wg.Wait()

	// Branch & ahead/behind
	if bErr == nil {
		parseStatusV2(bOut, snap)
	}

	// Local branches
	if brErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(brOut), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				snap.Branches = append(snap.Branches, line)
			}
		}
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

func buildWorkspaceTreeFromList(nulOut string, snap *RepoSnapshot) {
	seenDirs := make(map[string]map[string]bool)
	var allFiles []string
	for _, p := range strings.Split(nulOut, "\x00") {
		p = strings.TrimSuffix(filepath.ToSlash(p), "/")
		if p == "" {
			continue
		}
		allFiles = append(allFiles, p)
		cur := p
		for {
			parent := filepath.ToSlash(filepath.Dir(cur))
			if parent == "." || parent == "/" || parent == "" {
				parent = ""
			}
			m := seenDirs[parent]
			if m == nil {
				m = make(map[string]bool)
				seenDirs[parent] = m
			}
			m[cur] = true
			if parent == "" {
				break
			}
			cur = parent
		}
	}
	sort.Strings(allFiles)
	dedup := allFiles[:0]
	for i, f := range allFiles {
		if i == 0 || f != allFiles[i-1] {
			dedup = append(dedup, f)
		}
	}
	snap.AllFiles = dedup

	for dir, childrenMap := range seenDirs {
		var dirs, files []string
		for child := range childrenMap {
			if _, isDir := seenDirs[child]; isDir {
				dirs = append(dirs, child)
			} else {
				files = append(files, child)
			}
		}
		sort.Strings(dirs)
		sort.Strings(files)
		combined := append(dirs, files...)
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
		sort.Strings(dirs)
		sort.Strings(files)
		return append(dirs, files...)
	}
	snap.TreeRoots = walk("")
	sort.Strings(snap.AllFiles)
}

const maxPreviewBytes = 512 * 1024

func sanitizePreviewText(data []byte) string {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	if bytes.IndexByte(probe, 0) != -1 {
		return "// Binary file not shown"
	}
	if len(data) > maxPreviewBytes {
		return string(data[:maxPreviewBytes]) + "\n// … file truncated at 512 KB …\n"
	}
	return string(data)
}

func LoadCommitDetails(root, hash string) (body string, files []CommitFile, err error) {
	bOut, err := runGit(root, "show", "-s", "--format=%B", hash)
	if err != nil {
		return "", nil, err
	}
	body = strings.TrimSpace(bOut)

	fOut, err := runGit(root, "diff-tree", "--root", "--no-commit-id", "--name-status", "-r", hash)
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
	out, err := runGit(root, "show", ref+":"+relPath)
	if err != nil {
		return ""
	}
	return sanitizePreviewText([]byte(out))
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

func GitStageFile(root, path string) error {
	_, err := runGit(root, "add", "--", path)
	return err
}

func GitStageAll(root string) error {
	_, err := runGit(root, "add", "-A")
	return err
}

func GitUnstageFile(root, path string) error {
	if _, err := runGit(root, "restore", "--staged", "--", path); err != nil {
		_, err2 := runGit(root, "reset", "HEAD", "--", path)
		return err2
	}
	return nil
}

func GitUnstageAll(root string) error {
	_, err := runGit(root, "reset")
	return err
}

func GitDiscardFile(root string, st FileStatus) error {
	if st.Untrack {
		return os.RemoveAll(filepath.Join(root, filepath.FromSlash(st.Path)))
	}
	_, err := runGit(root, "checkout", "--", st.Path)
	return err
}

func GitCommit(root, message string, amend bool) error {
	args := []string{"commit", "-m", message}
	if amend {
		args = append(args, "--amend")
	}
	_, err := runGit(root, args...)
	return err
}

func GitCheckoutBranch(root, branch string, create bool) error {
	if create {
		_, err := runGit(root, "checkout", "-b", branch)
		return err
	}
	_, err := runGit(root, "checkout", branch)
	return err
}

func GitFetch(root string) error {
	_, err := runGit(root, "fetch", "--all", "--prune")
	return err
}

func GitPull(root string) error {
	_, err := runGit(root, "pull", "--rebase")
	return err
}

func GitPush(root string) error {
	_, err := runGit(root, "push")
	return err
}

func GitStashPush(root, message string) error {
	args := []string{"stash", "push", "-u"}
	if strings.TrimSpace(message) != "" {
		args = append(args, "-m", strings.TrimSpace(message))
	}
	_, err := runGit(root, args...)
	return err
}

func GitStashPop(root, ref string) error {
	if ref == "" {
		ref = "stash@{0}"
	}
	_, err := runGit(root, "stash", "pop", ref)
	return err
}

func GitStashDrop(root, ref string) error {
	_, err := runGit(root, "stash", "drop", ref)
	return err
}
