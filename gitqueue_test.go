package main

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestQueueRunsOneJobAtATimeInOrder(t *testing.T) {
	q := newGitQueue(false)
	var (
		mu      sync.Mutex
		order   []string
		running atomic.Int32
		maxSeen atomic.Int32
	)
	record := func(s string) {
		n := running.Add(1)
		if n > maxSeen.Load() {
			maxSeen.Store(n)
		}
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
		running.Add(-1)
	}
	gate := make(chan struct{})
	started := make(chan struct{})
	q.addWrite(func() {
		close(started)
		<-gate
		record("write1")
	})
	<-started // write1 is running; the rest has to wait for it
	q.addWrite(func() { record("write2") })
	if q.addRefresh(refreshReq{scope: scopeStatus}, func(r refreshReq) { record(fmt.Sprintf("refresh scope=%d", r.scope)) }) {
		t.Fatal("first refresh should be queued, not joined")
	}
	if !q.addRefresh(refreshReq{scope: scopeFull}, func(refreshReq) { t.Error("a joined refresh must not run on its own") }) {
		t.Fatal("second refresh should join the first")
	}
	if !q.addRefresh(refreshReq{scope: scopeStatus}, func(refreshReq) { t.Error("a joined refresh must not run on its own") }) {
		t.Fatal("third refresh should join the first")
	}
	q.addWrite(func() { record("write3") })
	if !q.hasQueuedWrite() {
		t.Fatal("expected queued writes")
	}

	close(gate)
	q.wait()

	want := []string{"write1", "write2", fmt.Sprintf("refresh scope=%d", scopeFull), "write3"}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if maxSeen.Load() != 1 {
		t.Fatalf("%d jobs ran at once", maxSeen.Load())
	}
}

func TestQueueMergesRefreshRequests(t *testing.T) {
	spin1, spin2 := uint64(1), uint64(2)
	r := refreshReq{scope: scopeStatus, dir: "/a", spins: []uint64{spin1}}
	r = r.merge(refreshReq{scope: scopeFull, manual: true, spins: []uint64{spin2}})
	r = r.merge(refreshReq{scope: scopeStatus})
	if r.scope != scopeFull || !r.manual || r.dir != "/a" || !slices.Equal(r.spins, []uint64{spin1, spin2}) {
		t.Fatalf("merged = %+v", r)
	}
}

func TestQueueCloseDropsWaitingJobs(t *testing.T) {
	q := newGitQueue(false)
	gate := make(chan struct{})
	started := make(chan struct{})
	var ran atomic.Int32
	q.addWrite(func() { close(started); <-gate; ran.Add(1) })
	<-started
	q.addWrite(func() { ran.Add(10) })
	q.close()
	q.addWrite(func() { ran.Add(100) }) // refused
	close(gate)
	q.wait()
	if ran.Load() != 1 {
		t.Fatalf("ran = %d, want only the running job (1)", ran.Load())
	}
}

func TestQueueInlineKeepsOrderWhenJobsSubmitJobs(t *testing.T) {
	q := newGitQueue(true)
	var order []string
	q.addWrite(func() {
		order = append(order, "outer-start")
		q.addWrite(func() { order = append(order, "inner") })
		order = append(order, "outer-end")
	})
	if !slices.Equal(order, []string{"outer-start", "outer-end", "inner"}) {
		t.Fatalf("order = %v", order)
	}
}

// The user's operations run one after the other, a refresh asked for meanwhile
// waits, and the refresh button and ⌘R do nothing until the operations are done.
func TestRefreshDisabledAndOrderedWhileWriteRuns(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "1\n"})
	h := newHarness(t, dir)
	a := h.a

	var loads atomic.Int32
	a.live.loadRepo = func(d string) (*RepoSnapshot, error) {
		loads.Add(1)
		return LoadRepo(d)
	}

	var (
		mu    sync.Mutex
		order []string
	)
	step := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}
	gate := make(chan struct{})
	started := make(chan struct{})
	a.runAsyncGit("Slow", func(string) error {
		close(started)
		<-gate
		step("slow")
		return nil
	})
	<-started
	a.runAsyncGit("Fast", func(string) error { step("fast"); return nil })

	if !a.writeBusy() {
		t.Fatal("writeBusy = false while operations run")
	}
	h.tt.Frame()

	// The refresh button is disabled; clicking it and pressing ⌘R do nothing.
	before := loads.Load()
	if err := h.tt.Click("Refresh (⌘R)"); err != nil {
		t.Fatal(err)
	}
	h.tt.Key(ui.Cmd, ui.KeyR)
	h.tt.Frame()
	if a.live.q == nil {
		t.Fatal("no queue")
	}
	if a.live.q.hasQueuedRefresh() {
		t.Fatal("a refresh was queued while an operation was running")
	}
	if loads.Load() != before {
		t.Fatalf("repository read %d times while the operation ran", loads.Load()-before)
	}

	// Changes seen by the watcher meanwhile wait their turn and join up.
	for range 5 {
		a.externalChange(scopeStatus)
	}
	a.externalChange(scopeFull)
	if n := a.live.q.queuedRefreshes(); n != 1 {
		t.Fatalf("%d refreshes queued, want 1", n)
	}

	close(gate)
	h.settle()

	mu.Lock()
	got := slices.Clone(order)
	mu.Unlock()
	if !slices.Equal(got, []string{"slow", "fast"}) {
		t.Fatalf("operations ran in order %v", got)
	}
	// "slow" left reading the repository to "fast"; then the one joined refresh.
	if n := loads.Load() - before; n != 2 {
		t.Fatalf("repository read %d times, want 2 (after the last operation, then the refresh)", n)
	}
	if a.writeBusy() || a.busyText != "" {
		t.Fatalf("still busy: writes=%d busy=%q", a.live.writes, a.busyText)
	}

	// Refresh works again.
	before = loads.Load()
	if err := h.tt.Click("Refresh (⌘R)"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if loads.Load() != before+1 {
		t.Fatalf("refresh click read the repository %d times", loads.Load()-before)
	}
}

func (q *gitQueue) queuedRefreshes() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, j := range q.jobs {
		if !j.write {
			n++
		}
	}
	return n
}

func (q *gitQueue) hasQueuedRefresh() bool { return q.queuedRefreshes() > 0 }
