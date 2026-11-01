package app

import (
	"arbor/perf"
	"sync"
)

// refreshScope says how much of the repository a refresh reads again.
type refreshScope uint8

const (
	// scopeStatus reads the status and the file list only: what a change in
	// the working tree can alter. The commit graph is reused.
	scopeStatus refreshScope = iota + 1
	// scopeFull reads everything.
	scopeFull
)

// refreshReq is a request to read the repository again. Requests that wait
// in the queue together are merged into one.
type refreshReq struct {
	scope refreshScope
	// dir is the directory to read; it is the repository root unless the user
	// opened another repository.
	dir string
	// switching is set when dir is a repository the user just opened, so that
	// failing to read it is shown as an empty window and not kept quiet.
	switching bool
	// manual is a refresh the user asked for: it shows a spinner and tells
	// the user when it fails. Others are silent.
	manual bool
	// spins are the spinner entries to clear when the refresh is done.
	spins []uint64
	span  perf.Span
}

// merge folds o, a later request, into r.
func (r refreshReq) merge(o refreshReq) refreshReq {
	r.scope = max(r.scope, o.scope)
	if o.dir != "" {
		r.dir = o.dir
	}
	r.switching = r.switching || o.switching
	r.spins = append(r.spins[:len(r.spins):len(r.spins)], o.spins...)
	if o.manual {
		// The user is waiting from their click, not from the first
		// background event.
		r.manual = true
		r.span = o.span
	}
	return r
}

// gitJob is a unit of work for the queue.
type gitJob struct {
	write bool
	// req is the merged request of a refresh job. It cannot change once the
	// job has started.
	req refreshReq
	run func(j *gitJob)
}

// gitQueue runs Git work one job at a time, in the order it was submitted:
// the operations that change the repository and the refreshes that read it.
// It never has more than one refresh waiting: a request that comes while
// one waits joins it, because that refresh starts after the request and so
// sees what it is about.
//
// With inline set (no window, as in tests of the models) jobs run on the
// goroutine that submitted them, still one after the other.
type gitQueue struct {
	mu       sync.Mutex
	idle     *sync.Cond
	jobs     []*gitJob
	draining bool
	closed   bool
	inline   bool
}

func newGitQueue(inline bool) *gitQueue {
	q := &gitQueue{inline: inline}
	q.idle = sync.NewCond(&q.mu)
	return q
}

// addWrite queues an operation that changes the repository.
func (q *gitQueue) addWrite(run func()) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.jobs = append(q.jobs, &gitJob{write: true, run: func(*gitJob) { run() }})
	q.kick()
}

// addRefresh queues a refresh, or joins the one already waiting, and reports
// whether it joined. run is called with the merged request when the job
// starts.
func (q *gitQueue) addRefresh(req refreshReq, run func(refreshReq)) (joined bool) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	for _, j := range q.jobs {
		if !j.write {
			j.req = j.req.merge(req)
			q.mu.Unlock()
			return true
		}
	}
	q.jobs = append(q.jobs, &gitJob{req: req, run: func(j *gitJob) { run(j.req) }})
	q.kick()
	return false
}

// kick starts draining; q.mu is held and released.
func (q *gitQueue) kick() {
	if q.draining {
		q.mu.Unlock()
		return
	}
	q.draining = true
	q.mu.Unlock()
	if q.inline {
		q.drain()
		return
	}
	go q.drain()
}

func (q *gitQueue) drain() {
	for {
		q.mu.Lock()
		if q.closed || len(q.jobs) == 0 {
			q.draining = false
			q.idle.Broadcast()
			q.mu.Unlock()
			return
		}
		j := q.jobs[0]
		q.jobs[0] = nil
		q.jobs = q.jobs[1:]
		q.mu.Unlock()
		j.run(j)
	}
}

// hasQueuedWrite reports whether an operation that changes the repository
// waits behind the running job. The running one then leaves reading the
// repository to it.
func (q *gitQueue) hasQueuedWrite() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.write {
			return true
		}
	}
	return false
}

// close drops the jobs that wait and refuses new ones. The job running goes
// on to its end.
func (q *gitQueue) close() {
	q.mu.Lock()
	q.closed = true
	clear(q.jobs)
	q.jobs = nil
	q.mu.Unlock()
}

// wait blocks until nothing is running or waiting. For tests.
func (q *gitQueue) wait() {
	q.mu.Lock()
	for q.draining {
		q.idle.Wait()
	}
	q.mu.Unlock()
}
