package main

import (
	"arbor/perf"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// fakeWindow stands in for a native window: Update queues for the test, which
// plays the UI thread, and Close is the user closing it. Like mygo's, Update
// runs what it is given whether the window is there or not.
type fakeWindow struct {
	host      *fakeHost
	spec      windowSpec
	view      func(c *ui.Context)
	tt        *ui.Tester
	title     string
	shown     int
	destroyed bool
	onClosed  []func()
	onFocus   []func()
}

func (w *fakeWindow) Update(fn func())  { w.host.ui.post(fn) }
func (w *fakeWindow) Invalidate()       {}
func (w *fakeWindow) Show()             { w.shown++ }
func (w *fakeWindow) SetTitle(s string) { w.title = s }
func (w *fakeWindow) IsDestroyed() bool { return w.destroyed }
func (w *fakeWindow) OnFocus(fn func()) (off func()) {
	w.onFocus = append(w.onFocus, fn)
	return func() {}
}
func (w *fakeWindow) OnClosed(fn func()) (off func()) {
	w.onClosed = append(w.onClosed, fn)
	return func() {}
}

// Close is the window being closed, by the user or by Close().
func (w *fakeWindow) Close() {
	if w.destroyed {
		return
	}
	w.destroyed = true
	for _, fn := range w.onClosed {
		fn()
	}
}

// tester is the window's content, built the first time it is asked for.
func (w *fakeWindow) tester() *ui.Tester {
	if w.tt == nil {
		w.tt = ui.NewTester(w.view, 1280, 800)
	}
	return w.tt
}

// fakeHost creates fakeWindows.
type fakeHost struct {
	ui      fakeUI
	windows []*fakeWindow
	// onNew, if set, is called as each window is created.
	onNew func()
}

func (h *fakeHost) NewWindow(spec windowSpec, view func(c *ui.Context)) hostWindow {
	w := &fakeWindow{host: h, spec: spec, view: view}
	h.windows = append(h.windows, w)
	if h.onNew != nil {
		h.onNew()
	}
	return w
}

// open returns the windows that are still open.
func (h *fakeHost) open() []*fakeWindow {
	var out []*fakeWindow
	for _, w := range h.windows {
		if !w.destroyed {
			out = append(out, w)
		}
	}
	return out
}

// regHarness is a registry of fake windows on temporary repositories, with a
// session file in a temporary directory: nothing touches the user's.
type regHarness struct {
	t     *testing.T
	reg   *windowRegistry
	host  *fakeHost
	store *sessionStore
	path  string
	// picks are the folders the dialog will answer, in order; "" cancels.
	picks  []string
	picked int
}

func noWatch(context.Context, string, mygo.FileWatchOptions) (hintSource, error) {
	return nil, errors.New("watching is off in this test")
}

func newRegHarness(t *testing.T) *regHarness {
	t.Helper()
	h := &regHarness{t: t, host: &fakeHost{}, path: filepath.Join(t.TempDir(), "session.json")}
	h.store = newSessionStore(h.path)
	h.reg = newWindowRegistry(h.host, h.store)
	h.reg.newApp = func() *app {
		a := newBareApp("")
		a.live.startWatch = noWatch
		a.cards.settings = newSettingsStore("")
		return a
	}
	h.reg.pick = func(_ hostWindow, done func(string)) {
		dir := ""
		if h.picked < len(h.picks) {
			dir = h.picks[h.picked]
		}
		h.picked++
		done(dir)
	}
	t.Cleanup(func() {
		for _, m := range h.reg.Windows() {
			m.app.endSession()
		}
		h.store.Flush()
	})
	return h
}

// settle lets every window's background work and the UI thread run to
// completion, building frames between.
func (h *regHarness) settle() {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		for _, m := range h.reg.Windows() {
			if q := m.app.live.q; q != nil {
				q.wait()
			}
			m.app.live.loads.Wait()
		}
		n := h.host.ui.drain()
		for _, w := range h.host.open() {
			if w.tt != nil {
				w.tt.Frame()
			}
		}
		if n == 0 && h.quiet() {
			return
		}
	}
	h.t.Fatal("background work did not settle")
}

func (h *regHarness) quiet() bool {
	h.host.ui.mu.Lock()
	defer h.host.ui.mu.Unlock()
	return len(h.host.ui.fns) == 0
}

// win returns the fake window of slot.
func (h *regHarness) win(slot int) *fakeWindow {
	h.t.Helper()
	for _, w := range h.host.open() {
		if w.spec.Slot == slot {
			return w
		}
	}
	h.t.Fatalf("no open window in slot %d", slot)
	return nil
}

func (h *regHarness) app(slot int) *app {
	h.t.Helper()
	m := h.managed(slot)
	return m.app
}

func (h *regHarness) managed(slot int) *managedWindow {
	h.t.Helper()
	for _, m := range h.reg.Windows() {
		if m.slot == slot {
			return m
		}
	}
	h.t.Fatalf("no window in slot %d: %v", slot, h.slots())
	return nil
}

func (h *regHarness) slots() []int {
	var out []int
	for _, m := range h.reg.Windows() {
		out = append(out, m.slot)
	}
	return out
}

// repo makes a repository with one file on branch main.
func repoWith(t *testing.T, file string) string {
	t.Helper()
	dir := newTestRepo(t, map[string]string{file: file + "\n"})
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestSlotKeys(t *testing.T) {
	for slot, want := range map[int]string{
		0: "arbor-main", 1: "arbor-main", 2: "arbor-window-2", 3: "arbor-window-3", 12: "arbor-window-12",
	} {
		if got := slotKey(slot); got != want {
			t.Errorf("slotKey(%d) = %q, want %q", slot, got, want)
		}
	}
}

func TestLowestFreeSlot(t *testing.T) {
	taken := func(set ...int) func(int) bool {
		return func(s int) bool { return slices.Contains(set, s) }
	}
	for _, tc := range []struct {
		taken []int
		want  int
	}{
		{nil, 1}, {[]int{1}, 2}, {[]int{1, 2, 3}, 4}, {[]int{1, 3}, 2}, {[]int{2, 3}, 1}, {[]int{1, 2, 4, 5}, 3},
	} {
		if got := lowestFreeSlot(taken(tc.taken...)); got != tc.want {
			t.Errorf("taken %v: slot %d, want %d", tc.taken, got, tc.want)
		}
	}
}

func TestSlotsAreReusedAfterClose(t *testing.T) {
	h := newRegHarness(t)
	for i := 0; i < 3; i++ {
		h.reg.NewWindow()
	}
	if got := h.slots(); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("slots = %v", got)
	}
	var keys []string
	for _, w := range h.host.windows {
		keys = append(keys, w.spec.StateKey)
	}
	if want := []string{"arbor-main", "arbor-window-2", "arbor-window-3"}; !slices.Equal(keys, want) {
		t.Fatalf("state keys = %v, want %v", keys, want)
	}

	h.win(2).Close()
	if got := h.slots(); !slices.Equal(got, []int{1, 3}) {
		t.Fatalf("after closing 2: %v", got)
	}
	h.reg.NewWindow()
	if got := h.slots(); !slices.Equal(got, []int{1, 3, 2}) {
		t.Fatalf("the next window did not take the free slot: %v", got)
	}
	if last := h.host.windows[len(h.host.windows)-1]; last.spec.Slot != 2 || last.spec.StateKey != "arbor-window-2" {
		t.Fatalf("new window = %+v: its place and size are slot 2's", last.spec)
	}

	// The first slot too.
	h.win(1).Close()
	h.reg.NewWindow()
	if last := h.host.windows[len(h.host.windows)-1]; last.spec.Slot != 1 || last.spec.StateKey != "arbor-main" {
		t.Fatalf("new window = %+v, want slot 1", last.spec)
	}
	// A slot that a restore names is its own, even when it is not the lowest.
	h.reg.open(7, "", false)
	if got := h.host.windows[len(h.host.windows)-1].spec.StateKey; got != "arbor-window-7" {
		t.Fatalf("slot 7 state key = %q", got)
	}
}

func TestCascadeOrigin(t *testing.T) {
	work := mygo.Rectangle{X: 0, Y: 30, Width: 1920, Height: 1050}
	if x, y := cascadeOrigin(1, work, 1280, 820); x != 0 || y != 0 {
		t.Fatalf("slot 1 = (%d, %d): the first window is centered by the window system", x, y)
	}
	x2, y2 := cascadeOrigin(2, work, 1280, 820)
	x3, y3 := cascadeOrigin(3, work, 1280, 820)
	if x2 != (1920-1280)/2+cascadeStep || y2 != 30+(1050-820)/2+cascadeStep {
		t.Fatalf("slot 2 = (%d, %d)", x2, y2)
	}
	if x3-x2 != cascadeStep || y3-y2 != cascadeStep {
		t.Fatalf("slot 3 is (%d, %d) from slot 2, want a step of %d", x3-x2, y3-y2, cascadeStep)
	}
	// Start over after a few; a small display does not push a window off it.
	if x, y := cascadeOrigin(2+cascadeWrap, work, 1280, 820); x != x2 || y != y2 {
		t.Fatalf("wrapped slot = (%d, %d), want (%d, %d)", x, y, x2, y2)
	}
	if x, y := cascadeOrigin(2, mygo.Rectangle{Width: 800, Height: 600}, 1280, 820); x != cascadeStep || y != cascadeStep {
		t.Fatalf("on a small display: (%d, %d)", x, y)
	}
	if x, y := cascadeOrigin(2, mygo.Rectangle{}, 1280, 820); x != 0 || y != 0 {
		t.Fatalf("with no display known: (%d, %d)", x, y)
	}
}

func TestOpenWindowFocusesTheWindowThatShowsTheRepository(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	b := repoWith(t, "b.txt")

	first := h.reg.OpenWindow(a)
	h.settle()
	if first == nil || first.app.repo == nil || first.app.repo.Root != a {
		t.Fatalf("window did not load %s: %+v", a, first)
	}
	other := h.reg.OpenWindow(b)
	h.settle()
	if len(h.host.open()) != 2 || other.slot != 2 {
		t.Fatalf("open windows = %d, slots %v", len(h.host.open()), h.slots())
	}

	// The same repository again, however it is spelled: the window comes to
	// the front instead of another.
	link := filepath.Join(t.TempDir(), "link-to-a")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(a, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{a, a + "/", link, sub, filepath.Join(a, ".", "sub", "..")} {
		before := h.win(first.slot).shown
		if got := h.reg.OpenWindow(spelling); got != first {
			t.Fatalf("OpenWindow(%q) made another window (slot %d)", spelling, got.slot)
		}
		if h.win(first.slot).shown != before+1 {
			t.Fatalf("OpenWindow(%q) did not bring the window to the front", spelling)
		}
	}
	h.settle()
	if len(h.host.open()) != 2 {
		t.Fatalf("%d windows after asking for the same repository again", len(h.host.open()))
	}
}

func TestLinkedWorktreesAreSeparateRepositories(t *testing.T) {
	h := newRegHarness(t)
	repo := repoWith(t, "a.txt")
	mustGit(t, repo, "branch", "wt-branch")
	wt := filepath.Join(t.TempDir(), "linked")
	mustGit(t, repo, "worktree", "add", "-q", wt, "wt-branch")
	realWt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}

	h.reg.OpenWindow(repo)
	h.reg.OpenWindow(realWt)
	h.settle()
	if len(h.host.open()) != 2 {
		t.Fatalf("%d windows: a linked worktree is a window of its own", len(h.host.open()))
	}
	if h.app(1).repo.Root != repo || h.app(2).repo.Root != realWt {
		t.Fatalf("roots: %s, %s", h.app(1).repo.Root, h.app(2).repo.Root)
	}
	if h.app(2).repo.Branch != "wt-branch" {
		t.Fatalf("the worktree window shows branch %q", h.app(2).repo.Branch)
	}
	// Asking for either again finds its own.
	if got := h.reg.OpenWindow(realWt); got.slot != 2 {
		t.Fatalf("worktree asked for again opened slot %d", got.slot)
	}
	if got := h.reg.OpenWindow(filepath.Join(repo, ".")); got.slot != 1 {
		t.Fatalf("main working tree asked for again opened slot %d", got.slot)
	}
}

func TestOpenHereReplacesTheRepositoryOrFocusesTheOtherWindow(t *testing.T) {
	h := newRegHarness(t)
	a, b, c := repoWith(t, "a.txt"), repoWith(t, "b.txt"), repoWith(t, "c.txt")
	h.reg.OpenWindow(a)
	h.reg.OpenWindow(b)
	h.settle()
	first := h.app(1)

	// A repository no window shows: this window shows it.
	h.reg.OpenHere(first, c)
	if first.repo != nil || first.loading != c {
		t.Fatalf("not opening: repo=%v loading=%q", first.repo, first.loading)
	}
	h.settle()
	if first.repo == nil || first.repo.Root != c || first.loading != "" || len(h.host.open()) != 2 {
		t.Fatalf("root=%v loading=%q windows=%d", first.repo, first.loading, len(h.host.open()))
	}
	if first.selectedFile != "c.txt" {
		t.Fatalf("nothing of the old repository stays: file %q", first.selectedFile)
	}

	// One that another window shows: that window comes forward, nothing else.
	shown := h.win(2).shown
	h.reg.OpenHere(first, b)
	h.settle()
	if h.win(2).shown != shown+1 || first.repo.Root != c {
		t.Fatalf("shown %d→%d, first window shows %s", shown, h.win(2).shown, first.repo.Root)
	}
	// And the one it shows already stays as it is.
	h.reg.OpenHere(first, c)
	if first.loading != "" || first.repo == nil {
		t.Fatal("reopening the repository on screen started a load")
	}
}

func TestEveryRepositoryOpenedBecomesRecentInOrder(t *testing.T) {
	h := newRegHarness(t)
	a, b, c := repoWith(t, "a.txt"), repoWith(t, "b.txt"), repoWith(t, "c.txt")
	for _, dir := range []string{a, b, c} {
		h.reg.OpenWindow(dir)
		h.settle()
	}
	if got := h.reg.Recents(); !slices.Equal(got, []string{c, b, a}) {
		t.Fatalf("recents = %v", got)
	}
	// Showing another in a window, and then this one again, makes each the
	// latest in turn.
	d := repoWith(t, "d.txt")
	h.reg.OpenHere(h.app(1), d)
	h.settle()
	if got := h.reg.Recents(); !slices.Equal(got, []string{d, c, b, a}) {
		t.Fatalf("recents after opening d = %v", got)
	}
	h.reg.OpenHere(h.app(1), a)
	h.settle()
	if got := h.reg.Recents(); !slices.Equal(got, []string{a, d, c, b}) {
		t.Fatalf("recents after reopening a = %v", got)
	}
	// A window that stays on its repository does not reorder anything.
	h.reg.OpenHere(h.app(3), c)
	h.settle()
	if got := h.reg.Recents(); !slices.Equal(got, []string{a, d, c, b}) {
		t.Fatalf("recents after asking for what is on screen = %v", got)
	}
}

func TestWindowTitleFollowsTheRepository(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	if got := h.win(1).title; got != "Arbor" {
		t.Fatalf("title while opening = %q", got)
	}
	h.settle()
	if want := filepath.Base(a) + " — Arbor"; h.win(1).title != want {
		t.Fatalf("title = %q, want %q", h.win(1).title, want)
	}
}

// The window shows that it is opening, and the read does not hold anything up.
func TestAWindowNeverWaitsForTheRepository(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	h.reg.newApp = func() *app {
		app := newBareApp("")
		app.live.startWatch = noWatch
		app.live.loadRepo = func(dir string) (*RepoSnapshot, error) {
			started <- struct{}{}
			<-release
			return LoadRepo(dir)
		}
		return app
	}

	m := h.reg.OpenWindow(a)
	if m == nil || m.app.repo != nil {
		t.Fatalf("OpenWindow returned a loaded repository, or no window: %+v", m)
	}
	<-started // the load is under way, in the background
	tt := h.win(1).tester()
	tt.Frame()
	name := filepath.Base(a)
	if !tt.HasText("Opening " + name + "…") {
		t.Fatalf("no loading state: %v", tt.Texts())
	}
	if !tt.HasText("Loading…") {
		t.Fatalf("no spinner label in the sidebar: %v", tt.Texts())
	}
	if tt.HasText("No Git Repository Loaded") || tt.HasText("Open Repository…") {
		t.Fatalf("a window that is loading offers the empty state: %v", tt.Texts())
	}
	// The window answers while it waits: a click on a mode, a frame.
	if err := tt.Click("Git"); err != nil {
		t.Fatal(err)
	}
	if m.app.activeMode != modeGit {
		t.Fatal("the window did not respond while loading")
	}

	close(release)
	h.settle()
	if m.app.repo == nil || m.app.loading != "" || tt.HasText("Opening "+name+"…") {
		t.Fatalf("after the load: repo=%v loading=%q texts=%v", m.app.repo, m.app.loading, tt.Texts())
	}
	if !tt.HasText("a.txt") {
		t.Fatalf("the repository is not shown: %v", tt.Texts())
	}
}

func TestAFolderThatIsNotARepositoryShowsWhyAndIsNotRemembered(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	plain := t.TempDir()
	h.reg.OpenWindow(a)
	h.reg.OpenWindow(plain)
	h.settle()
	m := h.managed(2)
	if m.app.repo != nil || m.app.repoErr == "" || m.app.loading != "" {
		t.Fatalf("repo=%v err=%q loading=%q", m.app.repo, m.app.repoErr, m.app.loading)
	}
	tt := h.win(2).tester()
	tt.Frame()
	if !tt.HasText("No Git Repository Loaded") || !tt.HasText(m.app.repoErr) {
		t.Fatalf("the error is not shown: %v", tt.Texts())
	}
	if got := h.reg.Recents(); !slices.Equal(got, []string{a}) {
		t.Fatalf("recents = %v: the folder never opened", got)
	}
	// The session keeps the window that works, not the one that did not.
	if got := h.store.Get().Windows; len(got) != 1 || got[0].Slot != 1 || got[0].Repo != a {
		t.Fatalf("session windows = %+v", got)
	}
}

func TestARecentThatIsGoneIsForgotten(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	h.settle()
	gone := filepath.Join(t.TempDir(), "deleted-repo")
	h.store.Update(func(s *session) { s.addRecent(gone) })
	if !slices.Contains(h.reg.Recents(), gone) {
		t.Fatal("setup")
	}
	h.reg.OpenHere(h.app(1), gone)
	// Whether it is there is asked off the UI thread, so the answer comes a
	// moment after the load's.
	deadline := time.Now().Add(5 * time.Second)
	for slices.Contains(h.reg.Recents(), gone) && time.Now().Before(deadline) {
		h.settle()
		time.Sleep(2 * time.Millisecond)
	}
	if slices.Contains(h.reg.Recents(), gone) {
		t.Fatalf("recents still list a directory that is not there: %v", h.reg.Recents())
	}
	if h.app(1).repoErr == "" {
		t.Fatal("no error shown for the missing folder")
	}
}

func TestNothingOpensTheDialogOnLaunch(t *testing.T) {
	h := newRegHarness(t)
	h.reg.Restore("", "/")
	h.settle()
	if h.picked != 0 {
		t.Fatalf("the folder dialog was asked for %d times at launch", h.picked)
	}
	if len(h.host.open()) != 1 {
		t.Fatalf("%d windows at launch with nothing to restore, want an empty one", len(h.host.open()))
	}
	a := h.app(1)
	if a.repo != nil || a.loading != "" || a.repoErr != "" {
		t.Fatalf("the empty window is not empty: repo=%v loading=%q err=%q", a.repo, a.loading, a.repoErr)
	}
}

func TestRestoreOpensTheLastSessionInItsSlots(t *testing.T) {
	a, b, c := repoWith(t, "a.txt"), repoWith(t, "b.txt"), repoWith(t, "c.txt")
	gone := filepath.Join(t.TempDir(), "deleted")
	path := filepath.Join(t.TempDir(), "session.json")
	seed := newSessionStore("")
	seed.path = path
	seed.Update(func(s *session) {
		s.Windows = []sessionWindow{{3, b}, {1, a}, {2, gone}, {5, c}}
		s.Recents = []string{b, a, gone}
	})
	seed.Flush()

	h := newRegHarness(t)
	h.store = newSessionStore(path)
	h.reg.store = h.store
	if got := h.store.Get(); len(got.Windows) != 3 || slices.Contains(got.Recents, gone) {
		t.Fatalf("a directory that is gone stays in the session: %+v", got)
	}
	h.reg.Restore("", "/somewhere/else")
	h.settle()
	if got := h.slots(); !slices.Equal(got, []int{1, 3, 5}) {
		t.Fatalf("restored slots = %v", got)
	}
	for slot, want := range map[int]string{1: a, 3: b, 5: c} {
		if got := h.app(slot).repo; got == nil || got.Root != want {
			t.Fatalf("slot %d shows %v, want %s", slot, got, want)
		}
		if got := h.win(slot).spec.StateKey; got != slotKey(slot) {
			t.Fatalf("slot %d state key = %q", slot, got)
		}
	}
	if h.picked != 0 {
		t.Fatal("the dialog opened")
	}
}

func TestRestoreWithACommandLineRepositoryOpensOnlyThat(t *testing.T) {
	a, b := repoWith(t, "a.txt"), repoWith(t, "b.txt")
	h := newRegHarness(t)
	h.store.Update(func(s *session) { s.Windows = []sessionWindow{{1, a}, {2, b}} })
	h.reg.Restore(b, "/")
	h.settle()
	if got := h.slots(); !slices.Equal(got, []int{1}) {
		t.Fatalf("slots = %v: a repository named on the command line is all that opens", got)
	}
	if h.app(1).repo.Root != b {
		t.Fatalf("shows %s", h.app(1).repo.Root)
	}
	// A relative one is made absolute before the session keeps it.
	if got := h.store.Get().Windows; len(got) != 1 || !filepath.IsAbs(got[0].Repo) {
		t.Fatalf("session = %+v", got)
	}
}

func TestRestoreFallsBackToTheRepositoryTheProcessStartedIn(t *testing.T) {
	a := repoWith(t, "a.txt")
	sub := filepath.Join(a, "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newRegHarness(t)
	h.reg.Restore("", sub)
	h.settle()
	if len(h.host.open()) != 1 || h.app(1).repo == nil || h.app(1).repo.Root != a {
		t.Fatalf("windows=%d repo=%v", len(h.host.open()), h.app(1).repo)
	}
}

func TestClosingWindowsUpdatesTheSessionButQuittingDoesNot(t *testing.T) {
	h := newRegHarness(t)
	a, b, c := repoWith(t, "a.txt"), repoWith(t, "b.txt"), repoWith(t, "c.txt")
	for _, dir := range []string{a, b, c} {
		h.reg.OpenWindow(dir)
	}
	h.settle()
	want := []sessionWindow{{1, a}, {2, b}, {3, c}}
	if got := h.store.Get().Windows; !slices.Equal(got, want) {
		t.Fatalf("session windows = %+v", got)
	}

	h.win(2).Close()
	h.settle()
	if got := h.store.Get().Windows; !slices.Equal(got, []sessionWindow{{1, a}, {3, c}}) {
		t.Fatalf("after closing slot 2: %+v", got)
	}
	// The recents keep what was open.
	if got := h.reg.Recents(); !slices.Contains(got, b) {
		t.Fatalf("closing a window lost its repository from the recents: %v", got)
	}

	// Quitting closes the windows, and they are still the session.
	h.reg.BeginQuit()
	h.win(1).Close()
	h.win(3).Close()
	h.settle()
	h.reg.Flush()
	if got := readSession(h.path).Windows; !slices.Equal(got, []sessionWindow{{1, a}, {3, c}}) {
		t.Fatalf("the session on disk after quitting = %+v", got)
	}
}

func TestRestoreLeavesTheSessionAloneUntilAllTheWindowsAreBack(t *testing.T) {
	a, b, c := repoWith(t, "a.txt"), repoWith(t, "b.txt"), repoWith(t, "c.txt")
	path := filepath.Join(t.TempDir(), "session.json")
	seed := newSessionStore(path)
	seed.Update(func(s *session) { s.Windows = []sessionWindow{{1, a}, {2, b}, {3, c}} })
	seed.Flush()

	h := newRegHarness(t)
	h.store = newSessionStore(path)
	h.reg.store = h.store
	var seen []int
	h.host.onNew = func() { seen = append(seen, len(h.store.Get().Windows)) }
	h.reg.Restore("", "/")
	h.settle()
	if !slices.Equal(seen, []int{3, 3, 3}) {
		t.Fatalf("the session had %v windows as each came back: a crash half way would lose the others", seen)
	}
	h.store.Flush()
	if got := readSession(path).Windows; len(got) != 3 {
		t.Fatalf("session on disk = %+v", got)
	}
}

func TestClosingTheLastWindowWhereThatQuitsKeepsItInTheSession(t *testing.T) {
	h := newRegHarness(t)
	h.reg.quitsWithLastWindow = true
	a, b := repoWith(t, "a.txt"), repoWith(t, "b.txt")
	h.reg.OpenWindow(a)
	h.reg.OpenWindow(b)
	h.settle()
	h.win(2).Close()
	h.settle()
	if got := h.store.Get().Windows; !slices.Equal(got, []sessionWindow{{1, a}}) {
		t.Fatalf("after closing one of two: %+v", got)
	}
	h.win(1).Close() // the application quits with it
	h.settle()
	h.reg.Flush()
	if got := readSession(h.path).Windows; !slices.Equal(got, []sessionWindow{{1, a}}) {
		t.Fatalf("the last window is gone from the session: %+v", got)
	}

	// Where the application stays in the Dock, closing it empties the session.
	h2 := newRegHarness(t)
	h2.reg.OpenWindow(a)
	h2.settle()
	h2.win(1).Close()
	h2.settle()
	if got := h2.store.Get().Windows; len(got) != 0 {
		t.Fatalf("session windows = %+v", got)
	}
}

func TestRefreshInAWindowWithNothingOpenDoesNothing(t *testing.T) {
	h := newRegHarness(t)
	h.reg.NewWindow()
	app, tt := h.app(1), h.win(1).tester()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyR)
	if err := tt.Click("Refresh (⌘R)"); err != nil {
		t.Fatal(err)
	}
	app.reload("")
	h.settle()
	if app.repo != nil || app.repoErr != "" || app.loading != "" || app.busyText != "" {
		t.Fatalf("a refresh with nothing open did something: err=%q loading=%q busy=%q", app.repoErr, app.loading, app.busyText)
	}
}

func TestActivatedOpensAWindowOnlyWhenThereIsNone(t *testing.T) {
	h := newRegHarness(t)
	h.reg.Activated(false)
	if len(h.host.windows) != 0 {
		t.Fatal("activating the application before it opened its windows opened one")
	}
	h.reg.Restore("", "/")
	h.reg.Activated(true)
	if len(h.host.open()) != 1 {
		t.Fatal("activating with a visible window opened another")
	}
	// All minimized: bring one back instead of another.
	shown := h.win(1).shown
	h.reg.Activated(false)
	if len(h.host.open()) != 1 || h.win(1).shown != shown+1 {
		t.Fatalf("windows=%d shown %d→%d", len(h.host.open()), shown, h.win(1).shown)
	}
	// With no window at all, the Dock icon opens one.
	h.win(1).Close()
	h.reg.Activated(false)
	if len(h.host.open()) != 1 {
		t.Fatalf("%d windows after activating with none", len(h.host.open()))
	}
	// Not while quitting.
	h.reg.BeginQuit()
	h.win(1).Close()
	h.reg.Activated(false)
	if len(h.host.open()) != 0 {
		t.Fatal("a window opened while quitting")
	}
}

func TestNewWindowSpanEndsAtTheFirstPaintOfTheLoadingState(t *testing.T) {
	var got []perf.Event
	var mu sync.Mutex
	perf.SetSink(func(e perf.Event) { mu.Lock(); got = append(got, e); mu.Unlock() })
	defer perf.SetSink(nil)

	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	tt := h.win(1).tester() // builds the first frame: the repository is not loaded
	tt.Frame()
	mu.Lock()
	events := slices.Clone(got)
	mu.Unlock()
	var spans []perf.Event
	for _, e := range events {
		if e.Op == perf.OpNewWindow {
			spans = append(spans, e)
		}
	}
	if len(spans) != 1 || spans[0].Result != perf.ResultOK {
		t.Fatalf("new_window events = %+v", spans)
	}
	if h.app(1).repo != nil || !tt.HasText("Opening "+filepath.Base(a)+"…") {
		t.Fatal("the first paint was not the loading state")
	}
	// The windows of a launch are not measured as new windows.
	h.reg.Restore(a, "/")
	h.settle()
	h.win(2).tester().Frame()
	mu.Lock()
	n := 0
	for _, e := range got {
		if e.Op == perf.OpNewWindow {
			n++
		}
	}
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d new_window events, want 1", n)
	}
}

func TestEmptyWindowListsRecentsAndOpensOne(t *testing.T) {
	h := newRegHarness(t)
	a, b := repoWith(t, "a.txt"), repoWith(t, "b.txt")
	h.store.Update(func(s *session) { s.Recents = []string{a, b} })
	h.reg.NewWindow()
	tt := h.win(1).tester()
	tt.Frame()
	for _, want := range []string{"Open a repository", "Open Repository…", "RECENT", filepath.Base(a), filepath.Base(b)} {
		if !tt.HasText(want) {
			t.Fatalf("empty window lacks %q: %v", want, tt.Texts())
		}
	}
	if h.picked != 0 {
		t.Fatal("the dialog opened by itself")
	}
	if err := tt.Click(filepath.Base(b)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if got := h.app(1).repo; got == nil || got.Root != b {
		t.Fatalf("clicking a recent shows %v, want %s", got, b)
	}
	if len(h.host.open()) != 1 {
		t.Fatalf("a recent opened %d windows", len(h.host.open()))
	}

	// Without recents the empty state says nothing of them.
	h2 := newRegHarness(t)
	h2.reg.NewWindow()
	tt2 := h2.win(1).tester()
	tt2.Frame()
	if tt2.HasText("RECENT") || !tt2.HasText("Open a repository") {
		t.Fatalf("empty window without recents: %v", tt2.Texts())
	}
}

func TestEmptyWindowButtonAsksForAFolder(t *testing.T) {
	h := newRegHarness(t)
	a := repoWith(t, "a.txt")
	h.picks = []string{"", a}
	h.reg.NewWindow()
	tt := h.win(1).tester()
	tt.Frame()
	if err := tt.Click("Open Repository…"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.picked != 1 || h.app(1).repo != nil {
		t.Fatalf("a cancelled dialog changed something: asked %d, repo %v", h.picked, h.app(1).repo)
	}
	if err := tt.Click("Open Repository…"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.app(1).repo == nil || h.app(1).repo.Root != a {
		t.Fatalf("the folder picked is not shown: %v", h.app(1).repo)
	}
}

func TestRepositoryMenuListsRecentsAndTheOpenActions(t *testing.T) {
	h := newRegHarness(t)
	a, b, c := repoWith(t, "a.txt"), repoWith(t, "b.txt"), repoWith(t, "c.txt")
	h.reg.OpenWindow(a)
	h.reg.OpenWindow(b)
	h.settle()
	h.store.Update(func(s *session) { s.addRecent(c) })
	app, tt := h.app(1), h.win(1).tester()
	tt.Frame()

	if err := tt.Click(filepath.Base(a)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.repoPick.open {
		t.Fatal("the repository button did not open the menu")
	}
	var labels []string
	for _, i := range app.repoPick.matches {
		labels = append(labels, app.repoPick.rows[i].Label)
	}
	// This window's repository on top, then the recent ones.
	if want := []string{filepath.Base(a), filepath.Base(c), filepath.Base(b)}; !slices.Equal(labels, want) {
		t.Fatalf("rows = %v, want %v", labels, want)
	}
	rows := app.repoPick.rows
	if !rows[0].Current || rows[1].Tag != "" || rows[2].Tag != "open" {
		t.Fatalf("rows = %+v: the repository on show is marked, and one that another window shows is tagged", rows)
	}
	if !tt.HasText("Open…") || !tt.HasText("Open in New Window…") || !tt.HasText("open") {
		t.Fatalf("menu entries missing: %v", tt.Texts())
	}

	// Choosing a recent shows it in this window.
	if err := tt.Click(filepath.Base(c)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if app.repo == nil || app.repo.Root != c || len(h.host.open()) != 2 {
		t.Fatalf("repo=%v windows=%d", app.repo, len(h.host.open()))
	}

	// Choosing the one another window shows brings that window forward.
	shown := h.win(2).shown
	if err := tt.Click(filepath.Base(app.repo.Root)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(filepath.Base(b)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.win(2).shown != shown+1 || app.repo.Root != c {
		t.Fatalf("window 2 shown %d→%d, this one shows %s", shown, h.win(2).shown, app.repo.Root)
	}

	// Open… asks for a folder and shows it here; Open in New Window… in
	// another window.
	d, e := repoWith(t, "d.txt"), repoWith(t, "e.txt")
	h.picks = []string{d, e}
	if err := tt.Click(filepath.Base(app.repo.Root)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Open…"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if app.repo.Root != d || len(h.host.open()) != 2 {
		t.Fatalf("Open… shows %v in %d windows", app.repo.Root, len(h.host.open()))
	}
	if err := tt.Click(filepath.Base(d)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Open in New Window…"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if len(h.host.open()) != 3 || app.repo.Root != d || h.app(3).repo.Root != e {
		t.Fatalf("Open in New Window…: windows=%d this=%s new=%v", len(h.host.open()), app.repo.Root, h.app(3).repo)
	}
}

func TestRepositoryMenuShiftOpensARecentInANewWindow(t *testing.T) {
	h := newRegHarness(t)
	a, b := repoWith(t, "a.txt"), repoWith(t, "b.txt")
	h.reg.OpenWindow(a)
	h.settle()
	h.store.Update(func(s *session) { s.addRecent(b) })
	app, tt := h.app(1), h.win(1).tester()
	tt.Frame()
	if err := tt.Click(filepath.Base(a)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type(filepath.Base(b))
	tt.Frame()
	tt.Key(ui.Shift, ui.KeyEnter)
	h.settle()
	if len(h.host.open()) != 2 || app.repo.Root != a || h.app(2).repo == nil || h.app(2).repo.Root != b {
		t.Fatalf("windows=%d this=%s", len(h.host.open()), app.repo.Root)
	}
}

func TestRepositoryMenuWithoutARegistryShowsTheRepository(t *testing.T) {
	a, tt := branchesApp(t, 2)
	if err := tt.Click(filepath.Base(a.repo.Root)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.repoPick.open || !tt.HasText("Search repositories…") {
		t.Fatalf("menu: %v", tt.Texts())
	}
	if tt.HasText("Open…") {
		t.Fatal("actions of a registry shown without one")
	}
}

func TestWorktreeBusyToastOffersAWindowForTheWorktree(t *testing.T) {
	h := newRegHarness(t)
	repo := repoWith(t, "a.txt")
	mustGit(t, repo, "branch", "busy")
	wt := filepath.Join(t.TempDir(), "other-worktree")
	mustGit(t, repo, "worktree", "add", "-q", wt, "busy")
	realWt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	h.reg.OpenWindow(repo)
	h.settle()
	app, tt := h.app(1), h.win(1).tester()
	tt.Frame()

	app.checkoutBranch("busy", false)
	h.settle()
	if !hasTextContaining(tt, realWt) || !tt.HasText("Open in New Window") {
		t.Fatalf("the toast has no action: %v", tt.Texts())
	}
	if len(h.host.open()) != 1 {
		t.Fatal("the window opened by itself")
	}
	if err := tt.Click("Open in New Window"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if len(h.host.open()) != 2 {
		t.Fatalf("%d windows after the action", len(h.host.open()))
	}
	if wtApp := h.app(2); wtApp.repo == nil || wtApp.repo.Root != realWt || wtApp.repo.Branch != "busy" {
		t.Fatalf("the new window shows %v", wtApp.repo)
	}
	if app.repo.Branch != "main" {
		t.Fatalf("branch = %q", app.repo.Branch)
	}

	// Asking again brings that window forward instead of making another.
	shown := h.win(2).shown
	app.checkoutBranch("busy", false)
	h.settle()
	if err := tt.Click("Open in New Window"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if len(h.host.open()) != 2 || h.win(2).shown != shown+1 {
		t.Fatalf("windows=%d shown %d→%d", len(h.host.open()), shown, h.win(2).shown)
	}
}

func TestWorktreeBusyToastWithoutARegistryHasNoAction(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	mustGit(t, dir, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, dir, "worktree", "add", "-q", wt, "feature")
	h := newHarness(t, dir)
	h.a.checkoutBranch("feature", false)
	h.settle()
	if hasTextContaining(h.tt, "Open in New Window") {
		t.Fatalf("an action that has nowhere to go: %v", h.tt.Texts())
	}
}

func TestFileAndDockMenus(t *testing.T) {
	h := newRegHarness(t)
	items := h.reg.fileMenuItems()
	byID := map[string]*mygo.MenuItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if it := byID["file.newWindow"]; it == nil || it.Label != "New Window" || it.Accelerator != "CmdOrCtrl+N" {
		t.Fatalf("New Window item = %+v", it)
	}
	if byID["file.open"] == nil || byID["file.openNewWindow"] == nil {
		t.Fatalf("File menu items = %v", items)
	}
	var hasClose bool
	for _, it := range items {
		hasClose = hasClose || it.Role == mygo.RoleClose
	}
	if !hasClose {
		t.Fatal("the File menu lost Close Window")
	}

	byID["file.newWindow"].Click(byID["file.newWindow"], nil)
	if len(h.host.open()) != 1 {
		t.Fatalf("New Window made %d windows", len(h.host.open()))
	}

	// The menu bar keeps the standard menus and the settings switch.
	menu := newAppMenu(newSettingsStore(""), nil, items...)
	if menu.ItemByID("view.githubAvatars") == nil || menu.ItemByID("file.newWindow") == nil {
		t.Fatal("the menu lost an item")
	}
	top := map[mygo.MenuRole]bool{}
	for _, it := range menu.Items() {
		top[it.Role] = true
	}
	for _, r := range []mygo.MenuRole{mygo.RoleAppMenu, mygo.RoleFileMenu, mygo.RoleEditMenu, mygo.RoleViewMenu, mygo.RoleWindowMenu} {
		if !top[r] {
			t.Errorf("the menu bar lost %s", r)
		}
	}

	// The Dock menu opens a window, and lists the latest recents.
	var recents []string
	for i := 0; i < 8; i++ {
		recents = append(recents, filepath.Join(t.TempDir(), "r"+string(rune('a'+i))))
	}
	h.store.Update(func(s *session) {
		for _, r := range slices.Backward(recents) {
			s.addRecent(r)
		}
	})
	dock := h.reg.dockMenu()
	var labels []string
	for _, it := range dock.Items() {
		if it.Type != mygo.MenuItemSeparator {
			labels = append(labels, it.Label)
		}
	}
	if len(labels) != 1+dockRecents || labels[0] != "New Window" || labels[1] != "ra" {
		t.Fatalf("Dock menu = %v", labels)
	}
	dock.ItemByID("dock.newWindow").Click(nil, nil)
	if len(h.host.open()) != 2 {
		t.Fatalf("the Dock's New Window made %d windows", len(h.host.open()))
	}
}

func TestMenusAreRebuiltWhenWindowsAndRecentsChange(t *testing.T) {
	h := newRegHarness(t)
	changes := 0
	h.reg.changed = func() { changes++ }
	a := repoWith(t, "a.txt")
	h.reg.OpenWindow(a)
	opened := changes
	if opened == 0 {
		t.Fatal("opening a window did not say so")
	}
	h.settle()
	if changes == opened {
		t.Fatal("a repository that loaded did not say so")
	}
	before := changes
	h.win(1).Close()
	if changes == before {
		t.Fatal("closing a window did not say so")
	}
}

func TestRegistryAllowsNoWindowWithADestroyedHost(t *testing.T) {
	h := newRegHarness(t)
	h.reg.host = destroyedHost{}
	if m := h.reg.NewWindow(); m != nil || len(h.reg.Windows()) != 0 {
		t.Fatalf("a window that could not be created was kept: %+v", m)
	}
}

type destroyedHost struct{}

func (destroyedHost) NewWindow(windowSpec, func(*ui.Context)) hostWindow {
	return &fakeWindow{host: &fakeHost{}, destroyed: true}
}

func TestRepoKeyAndSameRepo(t *testing.T) {
	a := repoWith(t, "a.txt")
	sub := filepath.Join(a, "x", "y")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repoKey(sub); got != a {
		t.Fatalf("repoKey(sub) = %q, want %q", got, a)
	}
	plain := t.TempDir()
	real, _ := filepath.EvalSymlinks(plain)
	if got := repoKey(plain); got != real {
		t.Fatalf("a folder in no repository is its own key: %q, want %q", got, real)
	}
	if repoKey("") != "" || sameRepo("", a) || sameRepo(a, "") {
		t.Fatal("an empty path names no repository")
	}
	if !sameRepo(a, sub) || sameRepo(a, plain) {
		t.Fatal("sameRepo")
	}
	if root, ok := inRepo(sub); !ok || root != a {
		t.Fatalf("inRepo(sub) = %q, %v", root, ok)
	}
	if _, ok := inRepo(plain); ok {
		t.Fatal("a folder in no repository is in one")
	}
}

func TestRepoNameAndTildePath(t *testing.T) {
	if repoName("/a/b/arbor") != "arbor" || repoName("/") != "/" {
		t.Fatalf("repoName: %q, %q", repoName("/a/b/arbor"), repoName("/"))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	if got := tildePath(filepath.Join(home, "Developer", "x")); got != "~"+string(filepath.Separator)+filepath.Join("Developer", "x") {
		t.Fatalf("tildePath = %q", got)
	}
	if tildePath(home) != "~" || tildePath("/usr/local") != "/usr/local" {
		t.Fatalf("tildePath home = %q", tildePath(home))
	}
	if strings.HasPrefix(tildePath(home+"x"), "~") {
		t.Fatal("a sibling of the home directory is not in it")
	}
}
