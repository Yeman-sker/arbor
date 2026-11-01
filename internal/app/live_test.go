package app

import (
	"arbor/internal/git"
	"arbor/perf"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// conflictRepo is a repository on main with a.txt modified in the working
// tree, and a branch feature that changes a.txt too: switching to it would
// overwrite the modification.
func conflictRepo(t *testing.T) string {
	t.Helper()
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n", "keep.txt": "keep\n"})
	mustGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "a.txt", "feature\n")
	mustGit(t, dir, "commit", "-am", "feature change")
	mustGit(t, dir, "checkout", "main")
	writeFile(t, dir, "a.txt", "dirty\n")
	return dir
}

func stashCount(t *testing.T, dir string) int {
	t.Helper()
	out := strings.TrimSpace(mustGit(t, dir, "stash", "list"))
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func TestCheckoutBlockedByLocalChangesOffersStash(t *testing.T) {
	locales := map[string][]string{
		"default": nil,
		// Git's messages are translated; the check must not depend on them.
		"zh_CN": {"LC_ALL=zh_CN.UTF-8", "LANG=zh_CN.UTF-8", "LANGUAGE=zh_CN"},
	}
	for name, env := range locales {
		t.Run(name, func(t *testing.T) {
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			dir := conflictRepo(t)
			h := newHarness(t, dir)
			a := h.a

			a.checkoutBranch("feature", false)
			h.settle()
			if !a.live.askOpen || a.live.ask == nil {
				t.Fatalf("no stash question; toasts=%v", a.live.toasts)
			}
			if !h.tt.HasText("Stash & Switch") || !h.tt.HasText("Cancel") {
				t.Fatalf("dialog buttons missing: %v", h.tt.Texts())
			}
			if a.repo.Branch != "main" {
				t.Fatalf("branch = %q before the user answered", a.repo.Branch)
			}
			if hasTextContaining(h.tt, "Checkout feature failed") {
				t.Fatal("conflict was shown as an error toast too")
			}

			// Cancel: nothing changes.
			if err := h.tt.Click("Cancel"); err != nil {
				t.Fatal(err)
			}
			h.settle()
			if a.live.askOpen || a.live.ask != nil {
				t.Fatal("dialog still open after Cancel")
			}
			if a.repo.Branch != "main" || readFile(t, dir, "a.txt") != "dirty\n" || stashCount(t, dir) != 0 {
				t.Fatalf("Cancel changed things: branch=%s a.txt=%q stashes=%d", a.repo.Branch, readFile(t, dir, "a.txt"), stashCount(t, dir))
			}
			if h.tt.HasText("Stash & Switch") {
				t.Fatal("dialog still on screen after Cancel")
			}

			// Stash & Switch: stashes, switches, and leaves the stash alone.
			a.checkoutBranch("feature", false)
			h.settle()
			if err := h.tt.Click("Stash & Switch"); err != nil {
				t.Fatal(err)
			}
			h.settle()
			if a.repo.Branch != "feature" {
				t.Fatalf("branch = %q after Stash & Switch; toasts=%v", a.repo.Branch, a.live.toasts)
			}
			if got := readFile(t, dir, "a.txt"); got != "feature\n" {
				t.Fatalf("a.txt = %q, want the feature version", got)
			}
			if n := stashCount(t, dir); n != 1 {
				t.Fatalf("%d stashes, want 1 (never popped automatically)", n)
			}
			if len(a.repo.Stashes) != 1 {
				t.Fatalf("snapshot shows %d stashes", len(a.repo.Stashes))
			}
			if got := a.codeState.Buffer.String(); got != "feature\n" {
				t.Fatalf("open file shows %q", got)
			}
			if a.live.askOpen || h.tt.HasText("Stash & Switch") {
				t.Fatal("dialog left open")
			}
		})
	}
}

func TestCheckoutBlockedByUntrackedFileOffersStash(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	mustGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "new.txt", "tracked on feature\n")
	mustGit(t, dir, "add", "new.txt")
	mustGit(t, dir, "commit", "-m", "add new.txt")
	mustGit(t, dir, "checkout", "main")
	writeFile(t, dir, "new.txt", "untracked on main\n")

	h := newHarness(t, dir)
	h.a.checkoutBranch("feature", false)
	h.settle()
	if !h.a.live.askOpen {
		t.Fatalf("no stash question; toasts=%v", h.a.live.toasts)
	}
	if err := h.tt.Click("Stash & Switch"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.a.repo.Branch != "feature" || stashCount(t, dir) != 1 {
		t.Fatalf("branch=%s stashes=%d", h.a.repo.Branch, stashCount(t, dir))
	}
}

func TestCheckoutOfBranchInAnotherWorktreeNamesIt(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	mustGit(t, dir, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "other-worktree")
	mustGit(t, dir, "worktree", "add", wt, "feature")
	realWt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, dir)
	h.a.checkoutBranch("feature", false)
	h.settle()

	if h.a.live.askOpen {
		t.Fatal("a branch in another worktree must not offer to stash")
	}
	if !hasTextContaining(h.tt, realWt) {
		t.Fatalf("toast does not name the worktree %s: %v", realWt, h.tt.Texts())
	}
	if !hasTextContaining(h.tt, "feature") {
		t.Fatalf("toast does not name the branch: %v", h.tt.Texts())
	}
	if h.a.repo.Branch != "main" {
		t.Fatalf("branch = %q", h.a.repo.Branch)
	}
}

func TestGitErrorsReachTheUser(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	h := newHarness(t, dir)

	// Through runAsyncGit, which every operation uses.
	h.a.runAsyncGit("Stage", func(root string) error { return git.StageFile(root, "no-such-file.txt") })
	h.settle()
	if !h.tt.HasText("Stage failed") || !hasTextContaining(h.tt, "no-such-file.txt") {
		t.Fatalf("stage error not shown: %v", h.tt.Texts())
	}

	// A plain error, not from Git.
	h.a.runAsyncGit("Discard", func(string) error { return errors.New("permission denied: a.txt") })
	h.settle()
	if !h.tt.HasText("Discard failed") || !hasTextContaining(h.tt, "permission denied: a.txt") {
		t.Fatalf("discard error not shown: %v", h.tt.Texts())
	}

	// From the buttons: this repository has no remote.
	for _, tc := range []struct{ button, title string }{
		{"Push (git push)", "Push failed"},
		{"Pull (git pull --rebase)", "Pull failed"},
	} {
		if err := h.tt.Click(tc.button); err != nil {
			t.Fatal(err)
		}
		h.settle()
		if !h.tt.HasText(tc.title) {
			t.Fatalf("%s not shown after %s: %v", tc.title, tc.button, h.tt.Texts())
		}
	}

	// A commit with nothing to commit.
	h.a.runGitOp(perf.OpCommit, "Commit", func(root string) error { return git.CreateCommit(root, "empty", false) })
	h.settle()
	if !h.tt.HasText("Commit failed") {
		t.Fatalf("commit error not shown: %v", h.tt.Texts())
	}

	// The failures did not leave the window busy.
	if h.a.writeBusy() || h.a.busyText != "" {
		t.Fatalf("busy after failures: writes=%d %q", h.a.live.writes, h.a.busyText)
	}
}

func TestMissingFileIsReplacedAfterSwitchingBranch(t *testing.T) {
	dir := newTestRepo(t, map[string]string{
		"a.txt":             "a\n",
		"dir/only-main.txt": "only on main\n",
		"dir/shared.txt":    "shared\n",
	})
	mustGit(t, dir, "checkout", "-b", "feature")
	mustGit(t, dir, "rm", "-q", "dir/only-main.txt")
	mustGit(t, dir, "commit", "-m", "remove only-main")
	mustGit(t, dir, "checkout", "main")

	h := newHarness(t, dir)
	a := h.a
	a.selectExplorerFile("dir/only-main.txt")
	h.settle()
	if got := a.codeState.Buffer.String(); got != "only on main\n" {
		t.Fatalf("open file shows %q", got)
	}

	a.checkoutBranch("feature", false)
	h.settle()
	if a.repo.Branch != "feature" {
		t.Fatalf("branch = %q; toasts=%v", a.repo.Branch, a.live.toasts)
	}
	if a.selectedFile != "dir/shared.txt" {
		t.Fatalf("selected %q, want its neighbour dir/shared.txt", a.selectedFile)
	}
	if got := a.codeState.Buffer.String(); got != "shared\n" {
		t.Fatalf("editor shows %q, not the replacement file", got)
	}
	if !h.tt.HasText("shared.txt") {
		t.Fatalf("title does not show the replacement: %v", h.tt.Texts())
	}
}

func TestNearestFile(t *testing.T) {
	snap := &git.RepoSnapshot{
		AllFiles: []string{"README.md", "cmd/main.go", "cmd/run.go", "pkg/a/x.go", "pkg/b.go", "z.txt"},
		TreeDirs: map[string][]string{"cmd": nil, "pkg": nil, "pkg/a": nil},
	}
	for rel, want := range map[string]string{
		"cmd/gone.go":     "cmd/main.go", // the next one in its directory
		"cmd/zzz.go":      "cmd/run.go",  // none after it: the one before
		"pkg/a/y.go":      "pkg/a/x.go",  // the only file left in its directory
		"pkg/c/deep/f.go": "pkg/b.go",    // directory gone: the closest directory above
		"other/f.go":      "pkg/a/x.go",  // nothing in other/: the nearest file of the repository
	} {
		got := nearestFile(snap, rel)
		if got != want {
			t.Errorf("nearestFile(%q) = %q, want %q", rel, got, want)
		}
	}
	if got := nearestFile(&git.RepoSnapshot{}, "x"); got != "" {
		t.Errorf("empty repository gave %q", got)
	}
	if inTree(snap, "cmd") || !inTree(snap, "cmd/run.go") || inTree(snap, "cmd/nope.go") {
		t.Error("inTree wrong")
	}
}

func TestNoFilesLeftClearsTheEditor(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	mustGit(t, dir, "checkout", "-q", "--orphan", "empty")
	mustGit(t, dir, "rm", "-rfq", ".")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "empty")
	mustGit(t, dir, "checkout", "-q", "main")

	h := newHarness(t, dir)
	if h.a.selectedFile != "a.txt" {
		t.Fatalf("selected %q", h.a.selectedFile)
	}
	h.a.checkoutBranch("empty", false)
	h.settle()
	if h.a.selectedFile != "" || h.a.codeState.Buffer.String() != "" {
		t.Fatalf("selected %q, editor %q", h.a.selectedFile, h.a.codeState.Buffer.String())
	}
	if !h.tt.HasText("No file selected") {
		t.Fatalf("empty state not shown: %v", h.tt.Texts())
	}
}

func TestPerfSpansForGitOperations(t *testing.T) {
	var events []perf.Event
	perf.SetSink(func(e perf.Event) { events = append(events, e) })
	defer perf.SetSink(nil)

	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	mustGit(t, dir, "branch", "feature")
	h := newHarness(t, dir)
	a := h.a
	events = nil

	phases := func(e perf.Event) []perf.Phase {
		var ps []perf.Phase
		for _, p := range e.Phases[:e.NPhases] {
			ps = append(ps, p.Phase)
		}
		return ps
	}
	expect := func(what string, op perf.Op, res perf.Result, ps ...perf.Phase) {
		t.Helper()
		h.settle()
		if len(events) != 1 {
			t.Fatalf("%s: %d events: %+v", what, len(events), events)
		}
		e := events[0]
		events = nil
		ps = append(ps, perf.PhasePaint)
		if e.Op != op || e.Result != res || !slices.Equal(phases(e), ps) {
			t.Fatalf("%s: op=%v result=%v phases=%v, want %v %v %v", what, e.Op, e.Result, phases(e), op, res, ps)
		}
		if e.Size != perf.SizeSmall {
			t.Fatalf("%s: size = %v", what, e.Size)
		}
	}

	a.checkoutBranch("feature", false)
	expect("switch branch", perf.OpSwitchBranch, perf.ResultOK, perf.PhaseGitCheckout, perf.PhaseLoadRepo, perf.PhaseApply)

	writeFile(t, dir, "b.txt", "b\n")
	a.runGitOp(perf.OpStage, "Stage All", git.StageAll)
	expect("stage", perf.OpStage, perf.ResultOK, perf.PhaseGitWrite, perf.PhaseLoadRepo, perf.PhaseApply)

	a.commitMsg = "add b"
	a.runGitOp(perf.OpCommit, "Commit", func(root string) error { return git.CreateCommit(root, "add b", false) })
	expect("commit", perf.OpCommit, perf.ResultOK, perf.PhaseGitWrite, perf.PhaseLoadRepo, perf.PhaseApply)

	if err := h.tt.Click("Refresh (⌘R)"); err != nil {
		t.Fatal(err)
	}
	expect("refresh", perf.OpRefresh, perf.ResultOK, perf.PhaseLoadRepo, perf.PhaseApply)

	a.externalChange(scopeStatus)
	expect("external status", perf.OpExternalChange, perf.ResultOK, perf.PhaseGitStatus, perf.PhaseApply)
	a.externalChange(scopeFull)
	expect("external full", perf.OpExternalChange, perf.ResultOK, perf.PhaseLoadRepo, perf.PhaseApply)

	a.checkoutBranch("no-such-branch", false)
	expect("failed switch", perf.OpSwitchBranch, perf.ResultError, perf.PhaseGitCheckout, perf.PhaseLoadRepo, perf.PhaseApply)
}

func TestReadOnlyGitCommandsDoNotRewriteTheIndex(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	// Same content, newer mtime: status has to refresh the index entry.
	future := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(dir, ".git", "index")
	read := func() []byte {
		b, err := os.ReadFile(idx)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := read()

	if _, err := git.LoadRepo(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := git.LoadWorktreeState(dir); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(), before) {
		t.Fatal("our commands rewrote the index")
	}

	// The premise: a plain status does rewrite it.
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=1")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(read(), before) {
		t.Skip("this Git does not rewrite the index on status; nothing to guard against")
	}
}

func TestAppsDoNotShareBackgroundState(t *testing.T) {
	// Two windows' worth of apps in one process: work and sessions of one do
	// not leak into the other.
	dirA := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	dirB := newTestRepo(t, map[string]string{"b.txt": "b\n"})
	ha, hb := newHarness(t, dirA), newHarness(t, dirB)
	var loadsA, loadsB atomic.Int32
	ha.a.live.loadRepo = func(d string) (*git.RepoSnapshot, error) { loadsA.Add(1); return git.LoadRepo(d) }
	hb.a.live.loadRepo = func(d string) (*git.RepoSnapshot, error) { loadsB.Add(1); return git.LoadRepo(d) }

	ha.a.externalChange(scopeFull)
	ha.settle()
	if loadsA.Load() != 1 || loadsB.Load() != 0 {
		t.Fatalf("loads: a=%d b=%d", loadsA.Load(), loadsB.Load())
	}
	ha.a.endSession()
	hb.a.externalChange(scopeFull)
	hb.settle()
	if loadsB.Load() != 1 || hb.a.repo == nil || hb.a.repo.Root == ha.a.repo.Root {
		t.Fatalf("second app broken: loads=%d", loadsB.Load())
	}
}
