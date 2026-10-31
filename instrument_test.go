package main

import (
	"arbor/internal/fixture"
	"arbor/perf"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// collect installs a sink for the test and returns the events it receives.
func collect(t *testing.T) *[]perf.Event {
	t.Helper()
	var got []perf.Event
	perf.SetSink(func(e perf.Event) { got = append(got, e) })
	t.Cleanup(func() { perf.SetSink(nil) })
	return &got
}

// instrumented opens the dirty small fixture in a tester, with the sink on.
func instrumented(t *testing.T) (*app, *ui.Tester, *[]perf.Event) {
	t.Helper()
	dir := benchRepo(t, fixture.Small)
	a := newApp(dir)
	if a.repo == nil {
		t.Fatalf("repo not loaded: %s", a.repoErr)
	}
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Advance(2 * time.Second)
	got := collect(t)
	return a, tt, got
}

func wantOne(t *testing.T, got *[]perf.Event, op perf.Op) perf.Event {
	t.Helper()
	if len(*got) != 1 {
		t.Fatalf("events = %+v, want exactly one %s", *got, op)
	}
	e := (*got)[0]
	*got = (*got)[:0]
	if e.Op != op || e.Result != perf.ResultOK || e.Size != perf.SizeSmall || e.Dur <= 0 {
		t.Fatalf("event = %+v, want %s ok small with a duration", e, op)
	}
	return e
}

// TestEverySpanEndsAtPaint runs each instrumented operation outside a frame:
// it is armed and waiting, nothing is reported until the view paints, and the
// next frame reports it once.
func TestEverySpanEndsAtPaint(t *testing.T) {
	a, tt, got := instrumented(t)
	unstaged := a.repo.Unstaged[1]

	cases := []struct {
		name string
		op   perf.Op
		do   func()
	}{
		{"mode switch", perf.OpModeSwitch, func() { a.switchMode(modeGit) }},
		{"open file", perf.OpOpenFile, func() { a.openFile(fixture.Small.Path(10)) }},
		{"open working diff", perf.OpOpenDiff, func() { a.openWorkingDiff(unstaged) }},
		{"expand commit", perf.OpExpandCommit, func() { a.expandCommit(2) }},
		{"open commit diff", perf.OpOpenDiff, func() { a.openCommitDiff(0) }},
		{"search", perf.OpSearch, func() { a.explorerQuery = "f001"; a.trackSearch() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.do()
			if !a.paints.Pending() || len(*got) != 0 {
				t.Fatalf("span not waiting for the paint: pending=%v events=%+v", a.paints.Pending(), *got)
			}
			tt.Frame()
			wantOne(t, got, c.op)
			if a.paints.Pending() {
				t.Fatal("span still pending after the paint")
			}
			tt.Frame() // a second paint reports nothing new
			if len(*got) != 0 {
				t.Fatalf("repainting reported again: %+v", *got)
			}
		})
	}
}

func TestCollapsingACommitIsNotMeasured(t *testing.T) {
	a, tt, got := instrumented(t)
	a.expandCommit(1)
	tt.Frame()
	wantOne(t, got, perf.OpExpandCommit)
	a.expandCommit(1) // collapses
	tt.Frame()
	if len(*got) != 0 {
		t.Fatalf("collapse produced %+v", *got)
	}
}

// TestSpansFromRealInput drives the app through the tester's pointer and
// keyboard: each interaction's own frames paint the span, with no extra Frame.
func TestSpansFromRealInput(t *testing.T) {
	a, tt, got := instrumented(t)
	s := fixture.Small
	expect := func(what string, op perf.Op) {
		t.Helper()
		if a.paints.Pending() {
			t.Fatalf("%s: span still waiting after the interaction's frames", what)
		}
		wantOne(t, got, op)
	}

	if err := tt.Click("Git"); err != nil {
		t.Fatal(err)
	}
	expect("click Git tab", perf.OpModeSwitch)
	tt.Key(ui.Cmd, ui.Key1)
	expect("cmd+1", perf.OpModeSwitch)
	tt.Key(ui.Cmd, ui.Key2)
	expect("cmd+2", perf.OpModeSwitch)
	tt.Key(ui.Cmd, ui.Key2)
	if len(*got) != 0 {
		t.Fatalf("switching to the current mode reported %+v", *got)
	}

	// Git mode: a changed file, a commit, a file of that commit.
	if err := tt.Click(filepath.Base(s.Path(41))); err != nil {
		t.Fatal(err)
	}
	expect("open working diff", perf.OpOpenDiff)
	if err := tt.Click(a.repo.Commits[3].Subject); err != nil {
		t.Fatal(err)
	}
	expect("expand commit", perf.OpExpandCommit)
	if len(a.commitFiles) < 2 {
		t.Fatalf("commit has %d files", len(a.commitFiles))
	}
	if err := tt.Click(filepath.Base(a.commitFiles[1].Path)); err != nil {
		t.Fatal(err)
	}
	expect("open commit diff", perf.OpOpenDiff)

	// Files mode: open a folder (not measured), then a file.
	if err := tt.Click("Files"); err != nil {
		t.Fatal(err)
	}
	expect("click Files tab", perf.OpModeSwitch)
	if err := tt.Click("mod01"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("opening a folder reported %+v", *got)
	}
	if err := tt.Click(filepath.Base(s.Path(10))); err != nil {
		t.Fatal(err)
	}
	expect("open file", perf.OpOpenFile)

	// Search: typing changes the query; every change ends at a paint.
	if err := tt.Click("Search files…"); err != nil {
		t.Fatal(err)
	}
	tt.Type("f0001")
	if a.explorerQuery != "f0001" {
		t.Fatalf("query = %q", a.explorerQuery)
	}
	if a.paints.Pending() || len(*got) == 0 {
		t.Fatalf("search: pending=%v events=%d", a.paints.Pending(), len(*got))
	}
	for _, e := range *got {
		if e.Op != perf.OpSearch || e.Result != perf.ResultOK || e.Size != perf.SizeSmall {
			t.Fatalf("search event = %+v", e)
		}
	}
}

func TestStartupSpansEndAtFirstPaint(t *testing.T) {
	got := collect(t)
	t.Cleanup(func() { startup.first, startup.ready = perf.Span{}, perf.Span{} })

	beginStartup()
	a := newApp(benchRepo(t, fixture.Small)) // applies the first snapshot: ready is armed
	a.startupFirstFrame()                    // the window is about to be created
	if len(*got) != 0 || !a.paints.Pending() {
		t.Fatalf("startup spans ended before any paint: %+v", *got)
	}
	ui.NewTester(a.view, 1280, 800) // the first frame
	if len(*got) != 2 || a.paints.Pending() {
		t.Fatalf("events = %+v pending = %v", *got, a.paints.Pending())
	}
	seen := map[perf.Op]perf.Event{}
	for _, e := range *got {
		seen[e.Op] = e
	}
	for _, op := range []perf.Op{perf.OpStartupFirstFrame, perf.OpStartupReady} {
		if e, ok := seen[op]; !ok || e.Result != perf.ResultOK || e.Size != perf.SizeSmall || e.Dur <= 0 {
			t.Errorf("%s event = %+v", op, e)
		}
	}
	// Later snapshots and windows are not startup.
	*got = (*got)[:0]
	a.applyRepoSnapshot(a.repo, nil)
	ui.NewTester(a.view, 800, 600)
	if len(*got) != 0 {
		t.Fatalf("a later apply reported %+v", *got)
	}
}

func TestStartupReadyReportsAnError(t *testing.T) {
	got := collect(t)
	t.Cleanup(func() { startup.first, startup.ready = perf.Span{}, perf.Span{} })

	beginStartup()
	a := newApp(t.TempDir()) // not a repository
	a.startupFirstFrame()
	ui.NewTester(a.view, 800, 600)
	var ready *perf.Event
	for i := range *got {
		if (*got)[i].Op == perf.OpStartupReady {
			ready = &(*got)[i]
		}
	}
	if ready == nil || ready.Result != perf.ResultError || ready.Size != perf.SizeUnknown {
		t.Fatalf("events = %+v", *got)
	}
}
