package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// fakeSource is a watch whose batches the test sends. Sends are not
// buffered: when send returns, the watcher has taken the batch, and the
// one before it has been handled.
type fakeSource struct {
	ch     chan []mygo.FileEvent
	closed chan struct{}
	once   sync.Once
}

func (s *fakeSource) Next(ctx context.Context) ([]mygo.FileEvent, error) {
	select {
	case evs := <-s.ch:
		return evs, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s *fakeSource) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeSource) send(t *testing.T, evs ...mygo.FileEvent) {
	t.Helper()
	select {
	case s.ch <- evs:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher is not reading its source")
	}
}

func hint(path string) mygo.FileEvent {
	return mygo.FileEvent{Op: mygo.FileHint, Path: path, IsDir: true}
}

// fakeWatches starts fakeSources in place of mygo.WatchFiles and remembers
// them by directory.
type fakeWatches struct {
	mu      sync.Mutex
	sources map[string]*fakeSource
	opts    map[string]mygo.FileWatchOptions
}

func (f *fakeWatches) start(ctx context.Context, dir string, opts mygo.FileWatchOptions) (hintSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sources == nil {
		f.sources = map[string]*fakeSource{}
		f.opts = map[string]mygo.FileWatchOptions{}
	}
	s := &fakeSource{ch: make(chan []mygo.FileEvent), closed: make(chan struct{})}
	f.sources[dir] = s
	f.opts[dir] = opts
	return s, nil
}

func (f *fakeWatches) source(t *testing.T, dir string) *fakeSource {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sources[dir]
	if s == nil {
		t.Fatalf("no watch on %s; watching %v", dir, f.dirs())
	}
	return s
}

func (f *fakeWatches) dirs() []string {
	var ds []string
	for d := range f.sources {
		ds = append(ds, d)
	}
	return ds
}

// watch starts the watchers of the harness's repository on fake sources.
func (h *harness) watch() *fakeWatches {
	h.t.Helper()
	fw := &fakeWatches{}
	h.a.live.startWatch = fw.start
	h.a.stopWatch()
	h.a.ensureWatch()
	<-h.a.live.w.ready
	return fw
}

// countLoads wraps the loaders of the app to count their calls.
func (h *harness) countLoads() (repo, status *atomic.Int32) {
	repo, status = new(atomic.Int32), new(atomic.Int32)
	h.a.live.loadRepo = func(d string) (*RepoSnapshot, error) { repo.Add(1); return LoadRepo(d) }
	h.a.live.loadStatus = func(d string) (*RepoSnapshot, error) { status.Add(1); return LoadWorktreeState(d) }
	return repo, status
}

func TestWatchersCoverTheWorktreeAndTheGitDirs(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n", ".gitignore": "build/\nnode_modules/\n"})
	writeFile(t, dir, "build/out.o", "x")
	writeFile(t, dir, "node_modules/pkg/index.js", "x")
	writeFile(t, dir, "src/main.go", "package main\n")
	h := newHarness(t, dir)
	fw := h.watch()
	root := h.a.repo.Root

	if got := h.a.live.w.sources.Load(); got != 2 {
		t.Fatalf("%d watches started (%v), want the worktree and the git dir", got, fw.dirs())
	}
	tree := fw.opts[root]
	if !tree.HintsOnly || !tree.Recursive || tree.Debounce != 300*time.Millisecond || tree.MaxDelay != 2*time.Second {
		t.Fatalf("worktree watch options = %+v", tree)
	}
	for p, want := range map[string]bool{
		".git":              true,
		"sub/.git":          true,
		"build":             true,
		"node_modules":      true,
		"src":               false,
		"node_modules/pkg":  false, // inside an ignored directory, which is never opened
		"not-ignored/build": false,
	} {
		if got := tree.Ignore(p, true); got != want {
			t.Errorf("worktree Ignore(%q) = %v, want %v", p, got, want)
		}
	}

	gitDir := filepath.Join(root, ".git")
	g := fw.opts[gitDir]
	if !g.HintsOnly || !g.Recursive || g.Debounce != 300*time.Millisecond || g.MaxDelay != 2*time.Second {
		t.Fatalf("git dir watch options = %+v (watching %v)", g, fw.dirs())
	}
	for p, want := range map[string]bool{
		"objects":      true,
		"objects/ab":   true,
		"logs":         true,
		"logs/refs":    true,
		"hooks":        true,
		"info":         true,
		"refs":         false,
		"refs/heads":   false,
		"refs/remotes": false,
	} {
		if got := g.Ignore(p, true); got != want {
			t.Errorf("git dir Ignore(%q) = %v, want %v", p, got, want)
		}
	}
	if g.Ignore("HEAD", false) || g.Ignore("index", false) || g.Ignore("packed-refs", false) {
		t.Error("HEAD, index and packed-refs must be watched")
	}
}

func TestIgnoredDirsFollowGitignoreAfterRefresh(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	h.watch()
	w := h.a.live.w
	if w.ignoreWorktree("dist", true) {
		t.Fatal("dist is not ignored yet")
	}
	writeFile(t, dir, ".gitignore", "dist/\n")
	writeFile(t, dir, "dist/bundle.js", "x")
	h.a.externalChange(scopeFull)
	h.settle()
	if !w.ignoreWorktree("dist", true) {
		t.Fatal("dist should be ignored after .gitignore changed")
	}
}

func TestWorktreeChangeRefreshesStatusAndKeepsTheGraph(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	fw := h.watch()
	repoLoads, statusLoads := h.countLoads()
	commits, graph, branches := a.repo.Commits, a.repo.Graph, a.repo.Branches
	spins := a.live.spinSeq

	writeFile(t, dir, "a.txt", "changed\n")
	writeFile(t, dir, "new.txt", "new\n")
	fw.source(t, a.repo.Root).send(t, hint("."))
	h.until("the status to show the edit", func() bool { return a.repo.StatusByRel["a.txt"] == "M" })
	h.settle()

	if a.repo.StatusByRel["new.txt"] != "U" {
		t.Fatalf("status = %v", a.repo.StatusByRel)
	}
	if !slicesShareStorage(commits, a.repo.Commits) || !slicesShareStorage(graph, a.repo.Graph) || !slicesShareStorage(branches, a.repo.Branches) {
		t.Fatal("a working tree change re-read the log instead of reusing it")
	}
	if repoLoads.Load() != 0 || statusLoads.Load() != 1 {
		t.Fatalf("full loads = %d, status loads = %d; want 0 and 1", repoLoads.Load(), statusLoads.Load())
	}
	if a.live.spinSeq != spins || a.busyText != "" {
		t.Fatalf("a background refresh showed a spinner (%d new, busy %q)", a.live.spinSeq-spins, a.busyText)
	}
	// The open file follows the change.
	if got := a.codeState.Buffer.String(); got != "changed\n" {
		t.Fatalf("open file shows %q", got)
	}
}

func slicesShareStorage[T any](a, b []T) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

func TestGitDirChangesRefreshEverythingButLockFilesDoNot(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	fw := h.watch()
	repoLoads, statusLoads := h.countLoads()
	src := fw.source(t, filepath.Join(a.repo.Root, ".git"))

	// Lock files and logs are noise: nothing is read. The second send returns
	// only once the first batch has been handled.
	writeFile(t, dir, ".git/index.lock", "")
	writeFile(t, dir, ".git/HEAD.lock", "")
	writeFile(t, dir, ".git/FETCH_HEAD", "x")
	src.send(t, hint("."))
	src.send(t, hint("logs"))
	src.send(t, hint("."))
	h.settle()
	if repoLoads.Load() != 0 || statusLoads.Load() != 0 {
		t.Fatalf("lock files caused a refresh (%d, %d)", repoLoads.Load(), statusLoads.Load())
	}
	_ = os.Remove(filepath.Join(dir, ".git", "index.lock"))
	_ = os.Remove(filepath.Join(dir, ".git", "HEAD.lock"))

	// A commit made elsewhere moves HEAD, a ref and the index.
	mustGit(t, dir, "commit", "--allow-empty", "-m", "made elsewhere")
	src.send(t, hint("refs/heads"))
	h.until("the new commit", func() bool { return a.repo.Commits[0].Subject == "made elsewhere" })
	h.settle()
	if repoLoads.Load() != 1 {
		t.Fatalf("full loads = %d, want 1", repoLoads.Load())
	}

	// A branch created elsewhere, and a checkout, both show.
	mustGit(t, dir, "checkout", "-b", "elsewhere")
	src.send(t, hint("."))
	h.until("the branch", func() bool { return a.repo.Branch == "elsewhere" })
	h.settle()

	// Staging elsewhere changes the index only.
	writeFile(t, dir, "staged.txt", "s\n")
	mustGit(t, dir, "add", "staged.txt")
	src.send(t, hint("."))
	h.until("the staged file", func() bool { return a.repo.StatusByRel["staged.txt"] == "A" })
}

func TestOurOwnOperationsDoNotTriggerARefreshFromTheWatcher(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	fw := h.watch()
	repoLoads, _ := h.countLoads()
	src := fw.source(t, filepath.Join(a.repo.Root, ".git"))

	writeFile(t, dir, "b.txt", "b\n")
	a.runGitOp(0, "Stage All", GitStageAll)
	h.settle()
	if repoLoads.Load() != 1 {
		t.Fatalf("full loads = %d after one operation", repoLoads.Load())
	}
	// The index changed, and the watcher reports it; the operation has read
	// the repository after that change already.
	src.send(t, hint("."))
	src.send(t, hint("."))
	h.settle()
	if repoLoads.Load() != 1 {
		t.Fatalf("the operation's own change was read again (%d full loads)", repoLoads.Load())
	}
}

func TestLinkedWorktreeWatchesBothGitDirs(t *testing.T) {
	mainDir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	mustGit(t, mainDir, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "linked")
	mustGit(t, mainDir, "worktree", "add", wt, "feature")

	h := newHarness(t, wt)
	a := h.a
	fw := h.watch()
	root := a.repo.Root
	gitDir, commonDir, err := gitDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if gitDir == commonDir {
		t.Fatalf("a linked worktree has its own git dir, got %s twice", gitDir)
	}
	if got := a.live.w.sources.Load(); got != 3 {
		t.Fatalf("%d watches started (%v), want worktree, git dir and common dir", got, fw.dirs())
	}
	for _, d := range []string{root, gitDir, commonDir} {
		fw.source(t, d)
	}

	// A branch moved from the main worktree shows up here.
	repoLoads, _ := h.countLoads()
	mustGit(t, mainDir, "commit", "--allow-empty", "-m", "from main")
	fw.source(t, commonDir).send(t, hint("refs/heads"))
	h.until("a full refresh", func() bool { return repoLoads.Load() == 1 })
	h.settle()
	if a.repo.Commits[0].Subject != "from main" {
		t.Fatalf("newest commit = %q", a.repo.Commits[0].Subject)
	}

	// The index of the main worktree is not ours: the common dir reports it
	// (as ".") but nothing this worktree shows has changed.
	writeFile(t, mainDir, "staged-in-main.txt", "x\n")
	mustGit(t, mainDir, "add", "staged-in-main.txt")
	src := fw.source(t, commonDir)
	src.send(t, hint("."))
	src.send(t, hint("."))
	h.settle()
	if repoLoads.Load() != 1 {
		t.Fatalf("another worktree's index caused %d full loads", repoLoads.Load())
	}

	// Our own HEAD and index are in the private git dir.
	mustGit(t, wt, "commit", "--allow-empty", "-m", "from the linked worktree")
	fw.source(t, gitDir).send(t, hint("."))
	h.until("our own commit", func() bool { return a.repo.Commits[0].Subject == "from the linked worktree" })
}

func TestWatchStopsWithTheSession(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	other := newTestRepo(t, map[string]string{"b.txt": "b\n"})
	h := newHarness(t, dir)
	a := h.a
	fw := h.watch()
	w := a.live.w
	old := fw.source(t, a.repo.Root)

	// Opening another repository ends the old session's watch and starts one.
	a.reload(other)
	h.settle()
	if w.ctx.Err() == nil {
		t.Fatal("old watch still running after switching repositories")
	}
	select {
	case <-old.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("old source was not closed")
	}
	if a.live.w == nil || a.live.w.root != a.repo.Root || a.live.w == w {
		t.Fatalf("new watch = %+v for %s", a.live.w, a.repo.Root)
	}
	<-a.live.w.ready
	fw.source(t, a.repo.Root)

	// Closing the window ends it for good.
	cur := a.live.w
	a.endSession()
	if cur.ctx.Err() == nil || a.live.w != nil {
		t.Fatal("watch still running after the session ended")
	}
}

func TestResultsOfAnEndedSessionAreDropped(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	gate := make(chan struct{})
	started := make(chan struct{})
	a.live.loadRepo = func(d string) (*RepoSnapshot, error) {
		close(started)
		<-gate
		snap, err := LoadRepo(d)
		snap.Branch = "stale"
		return snap, err
	}
	a.externalChange(scopeFull)
	<-started
	q := a.live.q
	a.endSession() // the window closes while the load runs
	close(gate)
	q.wait() // the load finishes and publishes
	h.fake.drain()
	if a.repo.Branch == "stale" {
		t.Fatal("a result from an ended session was applied")
	}
}

func TestBackgroundFailureKeepsTheSnapshotQuietly(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	prev := a.repo
	boom := errors.New("boom: cannot read the repository")
	a.live.loadRepo = func(string) (*RepoSnapshot, error) { return nil, boom }
	a.live.loadStatus = func(string) (*RepoSnapshot, error) { return nil, boom }

	a.externalChange(scopeFull)
	h.settle()
	a.externalChange(scopeStatus)
	h.settle()
	a.onFocus()
	h.settle()
	if a.repo != prev || a.repoErr != "" {
		t.Fatalf("a background failure replaced the snapshot (repo=%p err=%q)", a.repo, a.repoErr)
	}
	if hasTextContaining(h.tt, "boom") || hasTextContaining(h.tt, "Refresh failed") || len(a.live.toasts) != 0 {
		t.Fatalf("a background failure was shown: %v", h.tt.Texts())
	}
	if a.busyText != "" {
		t.Fatalf("busy %q", a.busyText)
	}

	// The user's own refresh does tell, and still keeps the snapshot.
	if err := h.tt.Click("Refresh (⌘R)"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.repo != prev || !h.tt.HasText("Refresh failed") || !hasTextContaining(h.tt, "boom") {
		t.Fatalf("manual refresh failure: repo kept=%v texts=%v", a.repo == prev, h.tt.Texts())
	}
	if a.busyText != "" {
		t.Fatalf("spinner left running: %q", a.busyText)
	}
}

func TestOpeningSomethingThatIsNotARepositoryShowsWhy(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	h.a.reload(t.TempDir())
	h.settle()
	if h.a.repo != nil || h.a.repoErr == "" || !h.tt.HasText("No Git Repository Loaded") {
		t.Fatalf("repo=%v err=%q texts=%v", h.a.repo, h.a.repoErr, h.tt.Texts())
	}
}

func TestFocusRefreshesInFullButNotInARush(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	repoLoads, statusLoads := h.countLoads()
	spins := a.live.spinSeq

	a.live.lastFull = time.Now().Add(-time.Hour)
	a.onFocus()
	h.settle()
	if repoLoads.Load() != 1 || statusLoads.Load() != 0 {
		t.Fatalf("focus loaded %d full, %d status", repoLoads.Load(), statusLoads.Load())
	}
	if a.live.spinSeq != spins {
		t.Fatal("focus refresh showed a spinner")
	}
	a.onFocus() // moments later
	h.settle()
	if repoLoads.Load() != 1 {
		t.Fatalf("focus twice in a second loaded %d times", repoLoads.Load())
	}
}

// The one test that runs mygo's real watcher, on the real file system. It
// stands in for running the application, which a test cannot do.
func TestRealWatcherSeesExternalChanges(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	repoLoads, statusLoads := h.countLoads()
	a.live.startWatch = startFileWatch // mygo.WatchFiles itself
	a.stopWatch()
	a.ensureWatch()
	w := a.live.w
	<-w.ready
	if w.sources.Load() != 2 {
		t.Skipf("the file watcher did not start here (%d of 2 watches)", w.sources.Load())
	}
	time.Sleep(300 * time.Millisecond) // let the watches settle in

	writeFile(t, dir, "created.txt", "x\n")
	h.until("the new file in the status", func() bool { return a.repo.StatusByRel["created.txt"] == "U" })

	mustGit(t, dir, "add", "created.txt")
	mustGit(t, dir, "commit", "-m", "committed behind your back")
	h.until("the commit", func() bool { return a.repo.Commits[0].Subject == "committed behind your back" })
	h.until("the clean status", func() bool { return len(a.repo.Staged)+len(a.repo.Unstaged) == 0 })

	// Reading the repository does not touch it, so the watcher falls silent
	// instead of refreshing forever.
	time.Sleep(time.Second)
	h.settle()
	repos, statuses := repoLoads.Load(), statusLoads.Load()
	time.Sleep(2 * time.Second)
	h.settle()
	if repoLoads.Load() != repos || statusLoads.Load() != statuses {
		t.Fatalf("refreshing never stops: %d->%d full, %d->%d status", repos, repoLoads.Load(), statuses, statusLoads.Load())
	}
}
