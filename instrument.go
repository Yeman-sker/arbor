package main

import "arbor/perf"

// Performance instrumentation (ARB-5).
//
// Each user-visible operation is a perf span that begins where the user acts and
// ends at the first paint that contains the new state: the helpers below wrap
// the existing handlers, so a call site changes by one line. Only closed
// enumerations are recorded; no path, name, message or error text reaches an
// event. Spans are inert while no sink is installed (perf.SetSink).
//
// Git writes (OpStage, OpCommit, OpSwitchBranch, OpRefresh, OpExternalChange)
// and OpCommitCard are instrumented where those features live.

// spanState is the bookkeeping the helpers keep on the app.
type spanState struct {
	// searchSeen is the explorer query the last build saw, to tell a changed
	// query (a search) from a rebuild.
	searchSeen string
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

// openFile is how the user opens a file of the explorer.
func (a *app) openFile(rel string) {
	sp := a.beginOp(perf.OpOpenFile)
	a.selectExplorerFile(rel)
	a.paints.Arm(sp, perf.ResultOK)
}

// openWorkingDiff is how the user opens the diff of a changed file.
func (a *app) openWorkingDiff(st FileStatus) {
	sp := a.beginOp(perf.OpOpenDiff)
	a.selectWorkingFile(st)
	a.paints.Arm(sp, perf.ResultOK)
}

// openCommitDiff is how the user opens the diff of a file of the expanded commit.
func (a *app) openCommitDiff(fIdx int) {
	sp := a.beginOp(perf.OpOpenDiff)
	a.selectCommitFile(fIdx)
	a.paints.Arm(sp, perf.ResultOK)
}

// expandCommit is how the user clicks a commit; only expanding one is measured.
func (a *app) expandCommit(idx int) {
	var sp perf.Span
	if a.repo != nil && idx >= 0 && idx < len(a.repo.Commits) && a.expandedCommit != a.repo.Commits[idx].Hash {
		sp = a.beginOp(perf.OpExpandCommit)
	}
	a.toggleCommit(idx)
	a.paints.Arm(sp, perf.ResultOK)
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
