package git

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClassifyCheckoutErrors(t *testing.T) {
	conflict := &Error{Args: []string{"checkout", "x"}, Msg: "error: Your local changes to the following files would be overwritten by checkout:\n\ta.txt\n\tdir/b.txt\nPlease commit your changes or stash them before you switch branches.\nAborting"}
	var lc *LocalChangesError
	if err := classifyCheckoutError("", "x", conflict); !errors.As(err, &lc) || !slices.Equal(lc.Files, []string{"a.txt", "dir/b.txt"}) {
		t.Fatalf("conflict classified as %#v", err)
	}
	untracked := &Error{Msg: "error: The following untracked working tree files would be overwritten by checkout:\n\tnew.txt\nPlease move or remove them before you switch branches.\nAborting"}
	if err := classifyCheckoutError("", "x", untracked); !errors.As(err, &lc) || !slices.Equal(lc.Files, []string{"new.txt"}) {
		t.Fatalf("untracked classified as %#v", err)
	}
	for _, msg := range []string{
		"fatal: 'feature' is already used by worktree at '/tmp/some dir/wt'",
		"fatal: 'feature' is already checked out at '/tmp/some dir/wt'",
	} {
		var wb *WorktreeBusyError
		if err := classifyCheckoutError("", "feature", &Error{Msg: msg}); !errors.As(err, &wb) || wb.Path != "/tmp/some dir/wt" {
			t.Fatalf("%q classified as %#v", msg, err)
		}
	}
	plain := &Error{Msg: "error: pathspec 'nope' did not match any file(s) known to git"}
	if err := classifyCheckoutError("", "nope", plain); err != error(plain) {
		t.Fatalf("plain error was wrapped: %#v", err)
	}
}

// Git is translated on this machine; the errors we recognise are matched by
// their English text, so every Git command runs with LC_ALL=C.
func TestGitMessagesAreEnglishWhateverTheLocale(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LANGUAGE", "zh_CN")

	env := Env()
	if got := lastEnv(env, "LC_ALL"); got != "C" {
		t.Fatalf("LC_ALL = %q for Git, want C", got)
	}
	if got := lastEnv(env, "GIT_OPTIONAL_LOCKS"); got != "0" {
		t.Fatalf("GIT_OPTIONAL_LOCKS = %q", got)
	}

	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	_, err := Run(dir, "checkout", "no-such-branch")
	var ge *Error
	if !errors.As(err, &ge) || !strings.Contains(ge.Msg, "did not match") {
		t.Fatalf("message is not English: %v", err)
	}

	// The two messages the interface reacts to, in the translated environment.
	mustGit(t, dir, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, dir, "worktree", "add", wt, "feature")
	realWt, _ := filepath.EvalSymlinks(wt)
	err = CheckoutBranch(dir, "feature", false)
	if !errors.As(err, &ge) {
		t.Fatalf("checkout of a branch in another worktree: %v", err)
	}
	if path, ok := parseOccupied(ge.Msg); !ok || path != realWt {
		t.Fatalf("worktree not found in the message %q", ge.Msg)
	}

	mustGit(t, dir, "checkout", "-q", "-b", "other")
	writeFile(t, dir, "a.txt", "other\n")
	mustGit(t, dir, "commit", "-qam", "other change")
	mustGit(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "dirty\n")
	err = CheckoutBranch(dir, "other", false)
	if !errors.As(err, &ge) {
		t.Fatalf("checkout over local changes: %v", err)
	}
	if files, ok := parseOverwritten(ge.Msg); !ok || !slices.Equal(files, []string{"a.txt"}) {
		t.Fatalf("local changes not found in the message %q", ge.Msg)
	}
}

// lastEnv is the value a child process sees for key: the last entry wins.
func lastEnv(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

func TestWorktreeOfFindsTheOtherWorktree(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	mustGit(t, dir, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, dir, "worktree", "add", wt, "feature")
	realWt, _ := filepath.EvalSymlinks(wt)
	if got := worktreeOf(dir, "feature"); got != realWt {
		t.Fatalf("worktreeOf = %q, want %q", got, realWt)
	}
	if got := worktreeOf(dir, "main"); got != "" {
		t.Fatalf("main is this worktree's own branch, got %q", got)
	}
	// An error whose text names no path still finds the worktree.
	var wb *WorktreeBusyError
	err := classifyCheckoutError(dir, "feature", &Error{Msg: "fatal: feature is busy in another worktree"})
	if !errors.As(err, &wb) || wb.Path != realWt {
		t.Fatalf("classified as %#v", err)
	}
}

func TestParseBranches(t *testing.T) {
	out := strings.Join([]string{
		" \tfeature/old\t1000\t",
		"*\tmain\t2000\t/repo",
		" \tfeature/in-tree\t3000\t/tmp/other tree/wt",
		" \tno-date\t\t",
		" \tzeta\t3000\t",
		"",
		"garbage",
		" \t\t5\t",
	}, "\n")
	got := parseBranches(out)
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	// The current branch first, then the newest tip, ties by name; a branch
	// with no date last.
	if want := []string{"main", "feature/in-tree", "zeta", "feature/old", "no-date"}; !slices.Equal(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	if !got[0].Current || got[0].Worktree != "" {
		t.Fatalf("the current branch is %+v: its own worktree is not 'elsewhere'", got[0])
	}
	if got[1].Worktree != "/tmp/other tree/wt" {
		t.Fatalf("worktree = %q", got[1].Worktree)
	}
	if !got[3].Time.Equal(time.Unix(1000, 0)) || !got[4].Time.IsZero() {
		t.Fatalf("times = %v, %v", got[3].Time, got[4].Time)
	}
}
