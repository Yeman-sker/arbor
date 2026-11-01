package app

import (
	"arbor/internal/git"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// waitPosted waits until background work has handed n functions to the UI
// thread, which it does a moment after it is let go.
func (h *regHarness) waitPosted(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.host.ui.mu.Lock()
		got := len(h.host.ui.fns)
		h.host.ui.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("fewer than %d functions reached the UI thread", n)
}

func TestLateResultsAfterTheWindowClosedAreDropped(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	h.settle()
	app := h.app(1)
	before := app.repo

	// A load that the window closes under.
	release := make(chan struct{})
	applied := false
	app.goLoad(func() func() {
		<-release
		return func() { applied = true }
	})
	// A refresh in flight: its snapshot is not what the window shows.
	loading := make(chan struct{})
	app.live.loadRepo = func(dir string) (*git.RepoSnapshot, error) {
		close(loading)
		<-release
		late := *before
		late.Branch = "late"
		return &late, nil
	}
	app.reload("")
	q := app.live.q
	<-loading
	// A hover card's job that has started, and one the window never starts.
	statRelease := make(chan struct{})
	var statMu sync.Mutex
	var statHashes []string
	started := make(chan struct{}, 1)
	app.cards.statLoad = func(root, hash string) (commitStat, error) {
		statMu.Lock()
		statHashes = append(statHashes, hash)
		statMu.Unlock()
		started <- struct{}{}
		<-statRelease
		return commitStat{Files: 3}, nil
	}
	app.cards.stat(app.live.post, a, "1234567")
	<-started
	statKey := a + "\x00" + "1234567"
	// And results that wait on the UI thread already.
	var viaOnUI, viaLater bool
	app.onUI(func() { viaOnUI = true })
	app.later(func() { viaLater = true })

	h.win(1).Close()
	if !app.closed || app.live.q != nil || app.live.w != nil {
		t.Fatalf("closed=%v queue=%v watch=%v: the window left things running", app.closed, app.live.q, app.live.w)
	}
	if len(h.reg.Windows()) != 0 {
		t.Fatal("the registry still has the window")
	}
	// Starting work in a closed window does nothing, and nothing runs for it.
	app.cards.stat(app.live.post, a, "7654321")
	app.reload("")
	app.externalChange(scopeStatus)
	app.onFocus()
	app.runGitOp(0, "Late", func(string) error { t.Error("a Git operation ran for a closed window"); return nil })
	app.goLoad(func() func() { t.Error("a load ran for a closed window"); return nil })
	if app.live.q != nil {
		t.Fatal("a closed window made a queue")
	}

	close(release)
	close(statRelease)
	app.live.loads.Wait()
	q.wait()
	// The load's result, the refresh's, the card's, and the one onUI posted:
	// the UI thread runs them all, as mygo does whether the window is there
	// or not.
	h.waitPosted(4)
	h.host.ui.drain()
	if applied || viaOnUI || viaLater {
		t.Fatalf("late work ran: load=%v onUI=%v later=%v", applied, viaOnUI, viaLater)
	}
	if len(app.queued) != 0 {
		t.Fatalf("%d queued actions are left", len(app.queued))
	}
	if app.repo != before || app.repo.Branch != "main" {
		t.Fatalf("the snapshot that arrived after closing was shown: %+v", app.repo)
	}
	if e := app.cards.stats[statKey]; e.state != statLoading {
		t.Fatalf("a card's size arrived in a closed window: %+v", e)
	}
	statMu.Lock()
	hashes := slices.Clone(statHashes)
	statMu.Unlock()
	if !slices.Equal(hashes, []string{"1234567"}) {
		t.Fatalf("the card jobs that ran: %v; the one asked for after closing must not", hashes)
	}
	// A second close is harmless.
	app.windowClosed()
}

func TestClosingTheWindowWhileItOpensTheRepository(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	release := make(chan struct{})
	started := make(chan struct{})
	h.reg.newApp = func() *app {
		app := newBareApp("")
		app.live.startWatch = noWatch
		app.live.loadRepo = func(dir string) (*git.RepoSnapshot, error) {
			close(started)
			<-release
			return git.LoadRepo(dir)
		}
		return app
	}
	h.reg.OpenWindow(a)
	app := h.app(1)
	q := app.live.q
	<-started
	h.win(1).Close()
	close(release)
	q.wait()
	h.waitPosted(1)
	h.host.ui.drain()
	if app.repo != nil {
		t.Fatalf("a repository was shown in a closed window: %+v", app.repo)
	}
	// It did not become a recent repository, nor a session window.
	if got := h.reg.Recents(); len(got) != 0 {
		t.Fatalf("recents = %v", got)
	}
	if got := h.store.Get().Windows; len(got) != 0 {
		t.Fatalf("session windows = %+v", got)
	}
}

func TestClosingTheWindowStopsItsWatchers(t *testing.T) {
	h := newRegHarness(t)
	var mu sync.Mutex
	var ctxs []context.Context
	h.reg.newApp = func() *app {
		app := newBareApp("")
		app.live.startWatch = func(ctx context.Context, dir string, opts mygo.FileWatchOptions) (hintSource, error) {
			mu.Lock()
			ctxs = append(ctxs, ctx)
			mu.Unlock()
			return nil, errors.New("no real watcher in this test")
		}
		return app
	}
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	h.settle()
	app := h.app(1)
	w := app.live.w
	if w == nil {
		t.Fatal("the window is not watching its repository")
	}
	<-w.ready
	mu.Lock()
	n := len(ctxs)
	mu.Unlock()
	if n == 0 {
		t.Fatal("no watch was started")
	}
	h.win(1).Close()
	mu.Lock()
	defer mu.Unlock()
	for i, ctx := range ctxs {
		if ctx.Err() == nil {
			t.Fatalf("watch %d of the closed window still runs", i)
		}
	}
	if w.ctx.Err() == nil || app.live.w != nil {
		t.Fatal("the watch was not stopped")
	}
}

func TestClosingTheWindowStopsTheGitQueue(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	h.settle()
	app := h.app(1)

	release := make(chan struct{})
	ran := make(chan string, 4)
	q := app.queue()
	q.addWrite(func() { ran <- "first"; <-release })
	q.addWrite(func() { ran <- "queued behind it" })
	<-ran
	gen := app.live.gen
	h.win(1).Close()
	if app.live.gen == gen {
		t.Fatal("the session did not end")
	}
	close(release)
	q.wait()
	select {
	case got := <-ran:
		t.Fatalf("%q ran after the window closed", got)
	default:
	}
}

func TestACardJobStartedAfterCloseDoesNotRun(t *testing.T) {
	var cc commitCards
	var mu sync.Mutex
	var posts int
	post := func(fn func()) {
		mu.Lock()
		posts++
		mu.Unlock()
		fn()
	}
	ran := make(chan struct{}, 1)
	cc.statLoad = func(root, hash string) (commitStat, error) {
		ran <- struct{}{}
		return commitStat{}, nil
	}
	cc.stop()
	cc.stat(post, "/r", "abcdef1")
	cc.githubRepo(post, "/r")
	time.Sleep(30 * time.Millisecond)
	select {
	case <-ran:
		t.Fatal("a job ran for a stopped window")
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 0 {
		t.Fatalf("%d results were posted", posts)
	}
}

// Two windows are two apps: what one does shows nothing in the other, and no
// state sits in the package for them to share. Run with -race.
func TestTwoWindowsShareNoState(t *testing.T) {
	dirA := newTestRepo(t, map[string]string{"alpha.txt": "alpha\n", "both.txt": "x\n"})
	dirB := newTestRepo(t, map[string]string{"beta.txt": "beta\n", "both.txt": "y\n"})
	mustGit(t, dirB, "checkout", "-q", "-b", "only-in-b")
	writeFile(t, dirA, "alpha.txt", "alpha changed\n")
	writeFile(t, dirB, "beta.txt", "beta changed\n")

	ha, hb := newHarness(t, dirA), newHarness(t, dirB)
	if ha.a.codeState == hb.a.codeState || ha.a.diffState == hb.a.diffState {
		t.Fatal("the apps share their editors")
	}

	// settle without Fatal, which is for the test's goroutine.
	settle := func(h *harness) bool {
		for i := 0; i < 200; i++ {
			if q := h.a.live.q; q != nil {
				q.wait()
			}
			h.a.live.loads.Wait()
			n := h.fake.drain()
			h.tt.Frame()
			if n == 0 && h.quiet() {
				return true
			}
		}
		return false
	}
	var wg sync.WaitGroup
	drive := func(h *harness, msg, file string) {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			h.tt.Key(ui.Cmd, ui.Key2)
			h.a.reload("")
			if !settle(h) {
				t.Errorf("%s did not settle", msg)
				return
			}
			h.tt.Key(ui.Cmd, ui.Key1)
			h.a.commitMsg = msg
			h.a.selectExplorerFile(file)
			if !settle(h) {
				t.Errorf("%s did not settle", msg)
				return
			}
		}
	}
	wg.Add(2)
	go drive(ha, "typed in A", "alpha.txt")
	go drive(hb, "typed in B", "beta.txt")
	wg.Wait()

	if ha.a.repo.Root == hb.a.repo.Root || ha.a.repo.Branch != "main" || hb.a.repo.Branch != "only-in-b" {
		t.Fatalf("A shows %s on %s, B shows %s on %s", ha.a.repo.Root, ha.a.repo.Branch, hb.a.repo.Root, hb.a.repo.Branch)
	}
	if ha.a.commitMsg != "typed in A" || hb.a.commitMsg != "typed in B" {
		t.Fatalf("drafts: %q, %q", ha.a.commitMsg, hb.a.commitMsg)
	}
	if ha.a.selectedFile != "alpha.txt" || hb.a.selectedFile != "beta.txt" ||
		ha.a.codeState.Buffer.String() != "alpha changed\n" || hb.a.codeState.Buffer.String() != "beta changed\n" {
		t.Fatalf("files: %q %q, buffers %q %q", ha.a.selectedFile, hb.a.selectedFile, ha.a.codeState.Buffer.String(), hb.a.codeState.Buffer.String())
	}
	for _, f := range ha.a.repo.AllFiles {
		if f == "beta.txt" {
			t.Fatal("A lists a file of B")
		}
	}
	if !hasTextContaining(ha.tt, "alpha") || hasTextContaining(ha.tt, "beta.txt") {
		t.Fatalf("A shows %v", ha.tt.Texts())
	}
	if !hasTextContaining(hb.tt, "beta") || hasTextContaining(hb.tt, "alpha.txt") {
		t.Fatalf("B shows %v", hb.tt.Texts())
	}
	// Closing one leaves the other going.
	ha.a.windowClosed()
	hb.a.reload("")
	if !settle(hb) || hb.a.repo == nil || hb.a.repo.Branch != "only-in-b" {
		t.Fatal("window B stopped with window A")
	}
}
