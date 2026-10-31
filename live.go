package main

import (
	"arbor/perf"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	// focusRefreshGap is how soon after a full refresh the window gaining
	// focus does not start another.
	focusRefreshGap = time.Second
	// errorToastLife is how long an error stays on screen: long enough to
	// read a Git message.
	errorToastLife = 8 * time.Second
)

// liveState is everything one window keeps to run Git in the background and
// to follow the repository as it changes. It belongs to the app, never to the
// package: several windows each have their own. Except where noted it is
// owned by the UI thread; workers get what they need as arguments.
type liveState struct {
	// post runs a function on the UI thread (Window.Update). It is nil
	// without a window, and everything then runs inline.
	post func(func())
	// gen counts the sessions the window has had. Results of work that began
	// in an earlier one (another repository, a closed window) are dropped.
	gen uint64
	q   *gitQueue
	w   *repoWatch
	// loads counts the background loads in flight. For tests.
	loads sync.WaitGroup

	// writes counts the operations that change the repository, queued or
	// running; the refresh button waits while there are any.
	writes  int
	spins   []spinEntry
	spinSeq uint64

	toasts []ui.Toast
	ask    *switchAsk
	// askOpen is the open flag of the stash dialog.
	askOpen bool

	// lastFull is when a full refresh last finished.
	lastFull time.Time

	content contentState

	// Seams for tests; nil uses the real ones.
	loadRepo   func(dir string) (*RepoSnapshot, error)
	loadStatus func(dir string) (*RepoSnapshot, error)
	startWatch watchStarter
}

type spinEntry struct {
	id    uint64
	label string
}

// switchAsk is a checkout that local changes are in the way of, waiting for
// the user to say whether to stash them.
type switchAsk struct {
	branch string
	files  []string
}

// loaders are the functions that read a repository, captured on the UI thread
// for a worker.
type loaders struct {
	repo   func(dir string) (*RepoSnapshot, error)
	status func(dir string) (*RepoSnapshot, error)
}

func (a *app) loaders() loaders {
	l := loaders{repo: LoadRepo, status: LoadWorktreeState}
	if a.live.loadRepo != nil {
		l.repo = a.live.loadRepo
	}
	if a.live.loadStatus != nil {
		l.status = a.live.loadStatus
	}
	return l
}

// queue returns the window's Git queue, making it on first use.
func (a *app) queue() *gitQueue {
	if a.live.q == nil {
		a.live.q = newGitQueue(a.live.post == nil)
	}
	return a.live.q
}

// endSession ends everything running for the repository on screen: queued
// work is dropped, the watchers stop and results still on their way are
// ignored. It is called when another repository is opened and when the window
// closes.
func (a *app) endSession() {
	a.live.gen++
	if q := a.live.q; q != nil {
		q.close()
		a.live.q = nil
	}
	a.stopWatch()
	a.live.writes = 0
	a.live.spins = nil
	a.busyText = ""
	a.live.ask, a.live.askOpen = nil, false
}

// publisher returns a function for a worker to publish results with. What it
// is given runs on the UI thread, unless the session ended meanwhile.
func (a *app) publisher() func(func()) {
	gen, post := a.live.gen, a.live.post
	return func(fn func()) {
		guarded := func() {
			if a.live.gen == gen {
				fn()
			}
		}
		if post == nil {
			guarded()
			return
		}
		post(guarded)
	}
}

// goLoad runs work off the UI thread and then the function it returns on it.
// Without a window both run right here.
func (a *app) goLoad(work func() func()) {
	if a.live.post == nil {
		if apply := work(); apply != nil {
			apply()
		}
		return
	}
	post, gen := a.live.post, a.live.gen
	a.live.loads.Add(1)
	go func() {
		defer a.live.loads.Done()
		apply := work()
		if apply == nil {
			return
		}
		post(func() {
			if a.live.gen == gen {
				apply()
			}
		})
	}()
}

// spinStart shows label in the sidebar's spinner until spinEnd(id).
func (a *app) spinStart(label string) uint64 {
	a.live.spinSeq++
	a.live.spins = append(a.live.spins, spinEntry{a.live.spinSeq, label})
	a.busyText = label
	return a.live.spinSeq
}

func (a *app) spinEnd(id uint64) {
	for i, s := range a.live.spins {
		if s.id == id {
			a.live.spins = append(a.live.spins[:i], a.live.spins[i+1:]...)
			break
		}
	}
	a.busyText = ""
	if n := len(a.live.spins); n > 0 {
		a.busyText = a.live.spins[n-1].label
	}
}

// writeBusy reports whether an operation that changes the repository is
// queued or running.
func (a *app) writeBusy() bool { return a.live.writes > 0 }

// runAsyncGit runs a Git operation in the background, one at a time after
// the ones before it, then reads the repository again. Its error reaches the
// user as a toast.
func (a *app) runAsyncGit(label string, fn func(root string) error) {
	a.runGitOp(perf.OpNone, label, fn)
}

// runGitOp is runAsyncGit for an operation that is measured as op.
func (a *app) runGitOp(op perf.Op, label string, fn func(root string) error) {
	if a.repo == nil {
		return
	}
	root := a.repo.Root
	q, pub, w, ld := a.queue(), a.publisher(), a.live.w, a.loaders()
	span := perf.Begin(op)
	a.live.writes++
	spin := a.spinStart(label)
	q.addWrite(func() {
		err := fn(root)
		span.Mark(writePhase(op))
		var (
			snap *RepoSnapshot
			lerr error
		)
		// When another operation is waiting, it reads the repository
		// after itself.
		reloaded := !q.hasQueuedWrite()
		if reloaded {
			w.recordFingerprint()
			snap, lerr = ld.repo(root)
			span.Mark(perf.PhaseLoadRepo)
			if snap != nil {
				span.SetSize(perf.SizeOf(len(snap.AllFiles)))
			}
		}
		pub(func() { a.finishWrite(label, span, spin, err, reloaded, snap, lerr) })
		if reloaded {
			w.refreshIgnored()
		}
	})
}

func writePhase(op perf.Op) perf.Phase {
	if op == perf.OpSwitchBranch {
		return perf.PhaseGitCheckout
	}
	return perf.PhaseGitWrite
}

func (a *app) finishWrite(label string, span perf.Span, spin uint64, err error, reloaded bool, snap *RepoSnapshot, lerr error) {
	a.live.writes--
	a.spinEnd(spin)
	result := perf.ResultOK
	if err != nil {
		result = perf.ResultError
		a.reportGitError(label, err)
	}
	if reloaded {
		a.applyLoaded(refreshReq{manual: true}, snap, lerr)
	}
	span.Mark(perf.PhaseApply)
	a.paints.Arm(span, result)
}

// checkoutBranch switches to branch, or creates it and switches to it.
func (a *app) checkoutBranch(branch string, create bool) {
	label := "Checkout " + branch
	if create {
		label = "Create branch " + branch
	}
	a.runGitOp(perf.OpSwitchBranch, label, func(root string) error {
		return GitCheckout(root, branch, create)
	})
}

// stashAndSwitch stashes the local changes and then switches to branch. The
// stash is kept; nothing applies it again.
func (a *app) stashAndSwitch(branch string) {
	a.runGitOp(perf.OpSwitchBranch, "Stash & switch to "+branch, func(root string) error {
		return GitStashAndCheckout(root, branch)
	})
}

// reload is reloadRepoAsync: it reads the repository again on behalf of the
// user, or opens the repository at dir when it is not empty. A refresh asked
// for while an operation changes the repository is dropped: the operation
// reads the repository when it is done.
func (a *app) reload(dir string) {
	switching := dir != "" && dir != a.repoDir
	if switching {
		a.endSession()
	} else if a.writeBusy() {
		return
	}
	if dir != "" {
		a.repoDir = dir
	}
	req := refreshReq{scope: scopeFull, dir: a.repoDir, switching: switching, manual: true}
	if !switching {
		req.span = perf.Begin(perf.OpRefresh)
	}
	a.requestRefresh(req)
}

// requestRefresh queues a refresh of the repository.
func (a *app) requestRefresh(req refreshReq) {
	if req.dir == "" {
		req.dir = a.repoDir
	}
	if req.manual {
		req.spins = append(req.spins, a.spinStart("Loading…"))
	}
	pub, w, ld := a.publisher(), a.live.w, a.loaders()
	a.queue().addRefresh(req, func(req refreshReq) { runRefresh(a, pub, w, ld, req) })
}

// externalChange refreshes the repository after something outside Arbor
// changed it, without a spinner and without complaining if that fails.
func (a *app) externalChange(scope refreshScope) {
	if a.repo == nil {
		return
	}
	a.requestRefresh(refreshReq{scope: scope, dir: a.repo.Root, span: perf.Begin(perf.OpExternalChange)})
}

// onFocus is the fallback for changes the watchers did not report.
func (a *app) onFocus() {
	if a.repo == nil || time.Since(a.live.lastFull) < focusRefreshGap {
		return
	}
	a.externalChange(scopeFull)
}

// runRefresh reads the repository on the queue's goroutine and publishes
// what it found. It touches no state of the app but through pub.
func runRefresh(a *app, pub func(func()), w *repoWatch, ld loaders, req refreshReq) {
	span := req.span
	if req.scope == scopeStatus {
		st, err := ld.status(req.dir)
		span.Mark(perf.PhaseGitStatus)
		if st != nil {
			span.SetSize(perf.SizeOf(len(st.AllFiles)))
		}
		pub(func() { a.finishRefresh(req, span, st, err) })
		return
	}
	w.recordFingerprint()
	snap, err := ld.repo(req.dir)
	span.Mark(perf.PhaseLoadRepo)
	if snap != nil {
		span.SetSize(perf.SizeOf(len(snap.AllFiles)))
	}
	pub(func() { a.finishRefresh(req, span, snap, err) })
	w.refreshIgnored()
}

// finishRefresh applies the result of a refresh on the UI thread. snap is a
// whole snapshot for scopeFull and the working tree state for scopeStatus.
func (a *app) finishRefresh(req refreshReq, span perf.Span, snap *RepoSnapshot, err error) {
	for _, id := range req.spins {
		a.spinEnd(id)
	}
	result := perf.ResultOK
	switch {
	case err != nil:
		result = perf.ResultError
		a.applyLoaded(req, nil, err)
	case req.scope == scopeStatus:
		if a.repo != nil && a.repo.Root == snap.Root {
			a.applyRepoSnapshot(mergeWorktreeState(a.repo, snap), nil)
		} else {
			result = perf.ResultCancelled
		}
	default:
		a.applyLoaded(req, snap, nil)
	}
	span.Mark(perf.PhaseApply)
	a.paints.Arm(span, result)
}

// applyLoaded shows a repository that was read in full, or reports why it
// could not be. A failure keeps the last good snapshot on screen; only what
// the user asked for is told about it, and a repository that cannot be opened
// leaves an empty window that says why.
func (a *app) applyLoaded(req refreshReq, snap *RepoSnapshot, err error) {
	if err == nil {
		a.applyRepoSnapshot(snap, nil)
		a.live.lastFull = time.Now()
		a.ensureWatch()
		return
	}
	switch {
	case req.switching || a.repo == nil:
		a.applyRepoSnapshot(nil, err)
	case req.manual:
		a.toastGitError("Refresh", err)
	}
}

// toastGitError tells the user that the operation called label failed, in
// Git's words.
func (a *app) toastGitError(label string, err error) {
	a.live.toasts = append(a.live.toasts, ui.Toast{
		ID:          "git-error:" + label,
		Title:       label + " failed",
		Description: gitMessage(err),
		Type:        "error",
		Timeout:     errorToastLife,
	})
}

// reportGitError tells the user why the operation called label failed. A
// checkout that local changes are in the way of asks whether to stash them
// instead.
func (a *app) reportGitError(label string, err error) {
	var (
		lc *localChangesError
		wb *worktreeBusyError
	)
	switch {
	case errors.As(err, &lc):
		a.live.ask = &switchAsk{branch: lc.Branch, files: lc.Files}
		a.live.askOpen = true
	case errors.As(err, &wb):
		a.live.toasts = append(a.live.toasts, ui.Toast{
			ID:          "git-error:" + label,
			Title:       "Can't switch to " + wb.Branch,
			Description: "It is checked out in another worktree: " + wb.Path,
			Type:        "error",
			Timeout:     errorToastLife,
		})
	default:
		a.toastGitError(label, err)
	}
}

// liveView shows what the background work has to say: error toasts and the
// question about stashing. Call it once per view.
func (a *app) liveView(c *ui.Context) {
	for _, t := range a.live.toasts {
		c.AddToast(t)
	}
	a.live.toasts = nil

	title, msg := "Local changes are in the way", ""
	if ask := a.live.ask; ask != nil {
		title = "Switch to “" + ask.branch + "”?"
		msg = fmt.Sprintf("Switching would overwrite local changes in %s.\n\nStash & Switch puts them in the stash, including untracked files, and then switches. The stash is not applied again automatically.", fileList(ask.files))
	}
	wasOpen := a.live.askOpen
	choice := ui.AlertDialog(c, &a.live.askOpen, title, msg, "Cancel", "Stash & Switch")
	if !wasOpen || a.live.ask == nil {
		return
	}
	switch choice {
	case 1:
		branch := a.live.ask.branch
		a.live.ask = nil
		a.stashAndSwitch(branch)
	case 0, ui.AlertDismissed:
		a.live.ask = nil
	}
}

// fileList names up to three files, and how many more there are.
func fileList(files []string) string {
	switch n := len(files); {
	case n == 0:
		return "your working tree"
	case n <= 3:
		return strings.Join(files, ", ")
	default:
		return fmt.Sprintf("%s and %d more", strings.Join(files[:3], ", "), n-3)
	}
}
