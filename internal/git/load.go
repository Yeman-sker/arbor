package git

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LoadWorktreeState reads what a change in the working tree can alter: the
// branch and its status, and the list of files. It is the cheap half of
// LoadRepo; the returned snapshot has no commits, graph, branches or
// stashes, and mergeWorktreeState lays it over the snapshot it refreshes.
func LoadWorktreeState(root string) (*RepoSnapshot, error) {
	snap := &RepoSnapshot{
		Root:        root,
		StatusByRel: make(map[string]string),
		TreeDirs:    make(map[string][]string),
	}
	var (
		stOut, lsOut string
		stErr, lsErr error
		wg           sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		stOut, stErr = Run(root, "status", "--porcelain=v2", "--branch", "-uall")
	}()
	go func() {
		defer wg.Done()
		lsOut, lsErr = Run(root, "ls-files", "-co", "--exclude-standard", "-z")
	}()
	wg.Wait()
	if stErr != nil {
		return nil, stErr
	}
	parseStatusV2(stOut, snap)
	if lsErr == nil {
		buildWorkspaceTreeFromList(lsOut, snap)
	} else {
		buildWorkspaceTreeFallback(root, snap)
	}
	return snap, nil
}

// mergeWorktreeState returns prev with the working tree state of fresh
// (from LoadWorktreeState). prev is not modified: snapshots are shared with
// the view and never changed in place.
func MergeWorktreeState(prev, fresh *RepoSnapshot) *RepoSnapshot {
	next := *prev
	next.Branch = fresh.Branch
	next.Ahead = fresh.Ahead
	next.Behind = fresh.Behind
	next.Staged = fresh.Staged
	next.Unstaged = fresh.Unstaged
	next.StatusByRel = fresh.StatusByRel
	next.TreeRoots = fresh.TreeRoots
	next.TreeDirs = fresh.TreeDirs
	next.AllFiles = fresh.AllFiles
	return &next
}

// IgnoredDirs returns the directories (slash separated, relative to root)
// that Git ignores as a whole, such as node_modules. The watcher does not
// look into them.
func IgnoredDirs(root string) map[string]bool {
	out, err := Run(root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil
	}
	dirs := make(map[string]bool)
	for _, p := range strings.Split(out, "\x00") {
		if d, ok := strings.CutSuffix(filepath.ToSlash(p), "/"); ok && d != "" {
			dirs[d] = true
		}
	}
	return dirs
}

// gitDirs returns the git directory of the working tree at root and the
// common one, which differ in a linked worktree: HEAD and the index are in
// the first, refs and packed-refs in the second.
func Dirs(root string) (gitDir, commonDir string, err error) {
	out, err := Run(root, "rev-parse", "--git-dir", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	abs := func(p string) string {
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		return filepath.Clean(p)
	}
	return abs(lines[0]), abs(lines[1]), nil
}

// gitFingerprint summarizes the files of the git directories that tell a
// commit, checkout, stage or fetch happened: HEAD, the index, packed-refs
// and the directories of refs/ (Git replaces a ref by renaming a lock file
// over it, which touches its directory). Lock files and everything else in
// the git directory do not show in it.
func Fingerprint(gitDir, commonDir string) string {
	var sb strings.Builder
	stat := func(label, path string) {
		fi, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&sb, "%s:-;", label)
			return
		}
		fmt.Fprintf(&sb, "%s:%d:%d;", label, fi.Size(), fi.ModTime().UnixNano())
	}
	stat("HEAD", filepath.Join(gitDir, "HEAD"))
	stat("index", filepath.Join(gitDir, "index"))
	stat("packed-refs", filepath.Join(commonDir, "packed-refs"))
	seen := map[string]bool{}
	for _, base := range []string{commonDir, gitDir} {
		refs := filepath.Join(base, "refs")
		if seen[refs] {
			continue
		}
		seen[refs] = true
		_ = filepath.WalkDir(refs, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				rel, _ := filepath.Rel(base, p)
				fmt.Fprintf(&sb, "%s:%d;", filepath.ToSlash(rel), fi.ModTime().UnixNano())
			}
			return nil
		})
	}
	return sb.String()
}
