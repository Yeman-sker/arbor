package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	Date      string
	Refs      []string
	Subject   string
	Body      string
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

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
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

	// Branch & ahead/behind
	if bOut, err := runGit(root, "status", "--porcelain=v2", "--branch", "-uall"); err == nil {
		parseStatusV2(bOut, snap)
	}

	// Local branches
	if brOut, err := runGit(root, "for-each-ref", "--sort=-committerdate", "refs/heads/", "--format=%(refname:short)"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(brOut), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				snap.Branches = append(snap.Branches, line)
			}
		}
	}

	// Stashes
	if stOut, err := runGit(root, "stash", "list", "--format=%gd%x09%gs"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(stOut), "\n") {
			if line == "" {
				continue
			}
			ref, msg, _ := strings.Cut(line, "\t")
			snap.Stashes = append(snap.Stashes, StashEntry{Ref: ref, Message: msg})
		}
	}

	// Commits & Graph
	// shortcut: caps log at 400 commits per load for instant frame times; increase or paginate for deep history
	if logOut, err := runGit(root, "log", "--all", "--topo-order", "--date=short", "-n", "400",
		"--pretty=format:%H%x09%P%x09%h%x09%an%x09%ad%x09%D%x09%s"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 7)
			if len(parts) < 7 {
				continue
			}
			var parents []string
			if parts[1] != "" {
				parents = strings.Fields(parts[1])
			}
			var refs []string
			if parts[5] != "" {
				for _, r := range strings.Split(parts[5], ",") {
					if r = strings.TrimSpace(r); r != "" {
						refs = append(refs, r)
					}
				}
			}
			snap.Commits = append(snap.Commits, Commit{
				Hash:      parts[0],
				Parents:   parents,
				ShortHash: parts[2],
				Author:    parts[3],
				Date:      parts[4],
				Refs:      refs,
				Subject:   parts[6],
			})
		}
		snap.Graph = ComputeGraph(snap.Commits)
	}

	buildWorkspaceTree(root, snap)
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
	case ".git", "node_modules", ".mygo", "build", "dist", ".next", "target", "vendor":
		return true
	}
	return false
}

func buildWorkspaceTree(root string, snap *RepoSnapshot) {
	var walk func(relDir string) []string
	walk = func(relDir string) []string {
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
	return out
}

func ReadWorktreeFile(root, relPath string) string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		return ""
	}
	return string(data)
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
