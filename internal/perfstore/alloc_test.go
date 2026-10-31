package perfstore

import (
	"arbor/perf"
	"testing"
)

// Recording an operation on the UI thread allocates nothing: with the writer
// held, so that its own work is not counted, Begin to End costs no heap.
func TestRecordingDoesNotAllocate(t *testing.T) {
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	first := true
	s := Open(t.TempDir(), Options{Version: "1", QueueLen: 1 << 14, beforeWrite: func() {
		if first {
			first = false
			entered <- struct{}{}
			<-gate
		}
	}})
	perf.SetSink(s.Sink())
	t.Cleanup(func() { perf.SetSink(nil); close(gate); s.Close() })
	sp := perf.Begin(perf.OpOpenFile)
	sp.End(perf.ResultOK)
	<-entered // the writer is held inside its first write
	allocs := testing.AllocsPerRun(2000, func() {
		sp := perf.Begin(perf.OpOpenFile)
		sp.SetSize(perf.SizeLarge)
		sp.Mark(perf.PhaseLoadFile)
		sp.Mark(perf.PhaseApply)
		sp.End(perf.ResultOK)
	})
	if allocs != 0 {
		t.Fatalf("recording an operation allocates %v times", allocs)
	}
	if s.Stats().Dropped != 0 {
		t.Fatalf("the queue overflowed during the measurement: %+v", s.Stats())
	}
}
