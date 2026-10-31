package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// GitError is a failed Git command. Msg is what Git wrote to stderr.
type GitError struct {
	Args []string
	Msg  string
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Msg)
}

// gitMessage is the text of err for a person: what Git said, without the
// command line in front, on one line and not longer than a toast can show.
func gitMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var ge *GitError
	if errors.As(err, &ge) {
		msg = ge.Msg
	}
	msg = strings.Join(strings.Fields(msg), " ")
	const maxRunes = 280
	if r := []rune(msg); len(r) > maxRunes {
		msg = string(r[:maxRunes-1]) + "…"
	}
	return msg
}

// localChangesError is a checkout that Git refused because local changes (or
// untracked files) would be overwritten. Stashing them lets it go through.
type localChangesError struct {
	Branch string
	Files  []string
	err    error
}

func (e *localChangesError) Error() string { return e.err.Error() }
func (e *localChangesError) Unwrap() error { return e.err }

// worktreeBusyError is a checkout that Git refused because another worktree
// has the branch checked out.
type worktreeBusyError struct {
	Branch string
	Path   string
	err    error
}

func (e *worktreeBusyError) Error() string { return e.err.Error() }
func (e *worktreeBusyError) Unwrap() error { return e.err }

var (
	// error: Your local changes to the following files would be overwritten by checkout:
	// error: The following untracked working tree files would be overwritten by checkout:
	overwrittenRe = regexp.MustCompile(`would be overwritten by checkout`)
	// fatal: 'x' is already checked out at '/p'       (Git before 2.42)
	// fatal: 'x' is already used by worktree at '/p'
	// fatal: 'x' is already being rebased at '/p'
	occupiedRe = regexp.MustCompile(`is already [a-z ]+ at '(.+)'`)
)

// parseOverwritten returns the files Git lists (one per tab-indented line)
// when err is a checkout that would overwrite local changes.
func parseOverwritten(msg string) ([]string, bool) {
	if !overwrittenRe.MatchString(msg) {
		return nil, false
	}
	var files []string
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "\t") {
			if f := strings.TrimSpace(line); f != "" {
				files = append(files, f)
			}
		}
	}
	return files, true
}

// parseOccupied returns the worktree path from Git's "already checked out"
// error.
func parseOccupied(msg string) (string, bool) {
	if m := occupiedRe.FindStringSubmatch(msg); m != nil {
		return m[1], true
	}
	return "", false
}

// samePath reports whether a and b name the same directory, through
// symlinks (macOS's /tmp is /private/tmp).
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// worktreeOf returns the path of a worktree other than root's that has branch
// checked out, from `git worktree list --porcelain`, whose format does not
// depend on the locale.
func worktreeOf(root, branch string) string {
	out, err := runGit(root, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	var path string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			if !samePath(path, root) {
				return path
			}
		}
	}
	return ""
}

// classifyCheckoutError wraps err in a localChangesError or worktreeBusyError
// when it is one of the failures the interface handles specially, and returns
// it as it is otherwise.
func classifyCheckoutError(root, branch string, err error) error {
	if err == nil {
		return nil
	}
	var ge *GitError
	msg := err.Error()
	if errors.As(err, &ge) {
		msg = ge.Msg
	}
	if files, ok := parseOverwritten(msg); ok {
		return &localChangesError{Branch: branch, Files: files, err: err}
	}
	if path, ok := parseOccupied(msg); ok {
		return &worktreeBusyError{Branch: branch, Path: path, err: err}
	}
	if strings.Contains(msg, "worktree") {
		if path := worktreeOf(root, branch); path != "" {
			return &worktreeBusyError{Branch: branch, Path: path, err: err}
		}
	}
	return err
}

// GitCheckout is GitCheckoutBranch with its failures classified, for callers
// that offer a way forward (stashing) or a better message.
func GitCheckout(root, branch string, create bool) error {
	return classifyCheckoutError(root, branch, GitCheckoutBranch(root, branch, create))
}

// GitStashAndCheckout stashes the local changes (untracked files included)
// and then checks out branch. The stash is left in the stash list: nothing
// pops it.
func GitStashAndCheckout(root, branch string) error {
	if err := GitStashPush(root, "Arbor: before switching to "+branch); err != nil {
		return err
	}
	if err := GitCheckoutBranch(root, branch, false); err != nil {
		// Report it as plain text: asking to stash again would not help.
		return fmt.Errorf("%s (your changes are safe in the stash)", gitMessage(err))
	}
	return nil
}
