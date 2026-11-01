package app

import (
	"arbor/internal/git"
	"arbor/perf"
	"time"
)

// Performance instrumentation (ARB-5).
//
// Each user-visible operation is a perf span that begins where the user acts and
// ends at the first paint that contains the new state: the helpers below wrap
// the existing handlers, so a call site changes by one line. Only closed
// enumerations are recorded; no path, name, message or error text reaches an
// event. Spans are inert while no sink is installed (perf.SetSink).
//
// Operations that change the repository (OpStage, OpCommit, OpSwitchBranch,
// OpRefresh, OpExternalChange) and OpCommitCard are instrumented where those
// features live (live.go, commitcard.go).

// spanState is the bookkeeping the helpers keep on the app.
type spanState struct {
	// searchSeen is the explorer query the last build saw, to tell a changed
	// query (a search) from a rebuild.
	searchSeen string

	// pending are the user operations waiting for background loads.
	pending struct{ file, diff, detail loadSpan }
}

// startup holds the startup spans from the start of main until the first paint
// that can carry them. They are process-wide: there is one launch.
var startup struct{ first, ready perf.Span }

// beginStartup starts the startup spans. Call it first in main.
func beginStartup() {
	startup.first = perf.Begin(perf.OpStartupFirstFrame)
	startup.ready = perf.Begin(perf.OpStartupReady)
}

// startupFirstFrame arms the first-frame span for the first paint of the window.
func (a *app) startupFirstFrame() {
	if !startup.first.Active() {
		return
	}
	startup.first.SetSize(a.repoSize())
	a.paints.Arm(startup.first, perf.ResultOK)
	startup.first = perf.Span{}
}

// startupReady arms the ready span once the first repository snapshot is
// applied, so ready is the first paint that shows the repository (or why there
// is none). It is deferred by applyRepoSnapshot and does nothing after that.
func (a *app) startupReady(err error) {
	if !startup.ready.Active() {
		return
	}
	startup.ready.SetSize(a.repoSize())
	result := perf.ResultOK
	if err != nil {
		result = perf.ResultError
	}
	a.paints.Arm(startup.ready, result)
	startup.ready = perf.Span{}
}

// repoSize buckets the open repository by file count.
func (a *app) repoSize() perf.Size {
	if a.repo == nil {
		return perf.SizeUnknown
	}
	return perf.SizeOf(len(a.repo.AllFiles))
}

// beginOp starts a span for op, sized by the open repository.
func (a *app) beginOp(op perf.Op) perf.Span {
	sp := perf.Begin(op)
	sp.SetSize(a.repoSize())
	return sp
}

// switchMode is how the user changes between the Files and Git views.
func (a *app) switchMode(mode int) {
	if a.activeMode == mode {
		return
	}
	sp := a.beginOp(perf.OpModeSwitch)
	a.activeMode = mode
	a.contentSeq++
	a.paints.Arm(sp, perf.ResultOK)
}

// loadSpan is a user operation waiting for content that loads in the
// background (content.go). The span begins at the click and is armed where the
// loaded content is published, so it ends at the paint that shows it. One is
// pending per kind of load, and seq is the load that will publish it.
type loadSpan struct {
	seq   uint64
	span  perf.Span
	began time.Time
}

// staleSpan is how long a span may wait for a load that was replaced without
// the user clicking again; after that it is dropped rather than reported, as a
// duration that long would not be what anyone waited for.
const staleSpan = 5 * time.Second

// start waits for the load numbered seq. A span still waiting for an earlier
// load was replaced by this click: it ends as cancelled.
func (l *loadSpan) start(a *app, span perf.Span, seq uint64) {
	if l.span.Active() && time.Since(l.began) < staleSpan {
		a.paints.Arm(l.span, perf.ResultCancelled)
	}
	*l = loadSpan{}
	if span.Active() {
		*l = loadSpan{seq: seq, span: span, began: time.Now()}
	}
}

// drop forgets the span: the click started no load, so there is nothing to wait for.
func (l *loadSpan) drop() { *l = loadSpan{} }

// publish arms the span where load seq put its content on screen. When a later
// load than the one the click started is the one published, the click's was
// replaced and its span is cancelled. phase names what the wait was mostly for.
func (l *loadSpan) publish(a *app, seq uint64, phase perf.Phase, result perf.Result) {
	if !l.span.Active() {
		return
	}
	if seq > l.seq {
		result = perf.ResultCancelled
	}
	l.span.Mark(phase)
	a.paints.Arm(l.span, result)
	*l = loadSpan{}
}

// openFile is how the user opens a file of the explorer. The text loads in the
// background: the span ends at the paint after it is shown.
func (a *app) openFile(rel string) {
	c := &a.live.content
	before := c.fileSeq
	a.pending.file.start(a, a.beginOp(perf.OpOpenFile), before+1)
	a.selectExplorerFile(rel)
	if c.fileSeq == before {
		a.pending.file.drop() // a folder, or no repository: no load began
	}
}

// clickWorkingDiff is how the user opens the diff of a changed file.
func (a *app) clickWorkingDiff(st git.FileStatus) {
	c := &a.live.content
	before := c.diffSeq
	a.pending.diff.start(a, a.beginOp(perf.OpOpenDiff), before+1)
	a.selectWorkingFile(st)
	if c.diffSeq == before {
		a.pending.diff.drop()
	}
}

// clickCommitDiff is how the user opens the diff of a file of the expanded commit.
func (a *app) clickCommitDiff(fIdx int) {
	c := &a.live.content
	before := c.diffSeq
	a.pending.diff.start(a, a.beginOp(perf.OpOpenDiff), before+1)
	a.selectCommitFile(fIdx)
	if c.diffSeq == before {
		a.pending.diff.drop()
	}
}

// clickCommit is how the user clicks a commit of the graph; only expanding one
// is measured. The span ends at the paint after the commit's files are shown.
func (a *app) clickCommit(idx int) {
	c := &a.live.content
	before := c.detailSeq
	expanding := a.repo != nil && idx >= 0 && idx < len(a.repo.Commits) && a.expandedCommit != a.repo.Commits[idx].Hash
	if expanding {
		a.pending.detail.start(a, a.beginOp(perf.OpExpandCommit), before+1)
	}
	a.toggleCommit(idx)
	if expanding && c.detailSeq == before {
		a.pending.detail.drop()
	}
}

// trackSearch starts a search span in the first build that sees a new explorer
// query, so the span covers that build, the filtering and the paint but not the
// keystroke's delivery.
func (a *app) trackSearch() {
	if a.explorerQuery == a.searchSeen {
		return
	}
	a.searchSeen = a.explorerQuery
	a.paints.Arm(a.beginOp(perf.OpSearch), perf.ResultOK)
}
