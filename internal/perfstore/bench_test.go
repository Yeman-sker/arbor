package perfstore

import (
	"arbor/perf"
	"testing"
	"time"
)

// The cost of recording on the UI thread (ARB-5 P2). The sink only copies the
// event into a bounded queue; encoding and the disk belong to the writer.
//
//	go test ./internal/perfstore -run '^$' -bench . -benchmem

var benchEvent = event(perf.OpOpenFile, day1, 27*time.Millisecond, perf.ResultOK, perf.SizeLarge,
	perf.PhaseTime{Phase: perf.PhaseLoadFile, Dur: 20 * time.Millisecond}, perf.PhaseTime{Phase: perf.PhaseApply, Dur: 5 * time.Millisecond})

// BenchmarkSinkEnqueue is the sink handing one event to a queue the writer is
// draining: the path every recorded event takes.
func BenchmarkSinkEnqueue(b *testing.B) {
	s := Open(b.TempDir(), Options{Version: "0.2.0", QueueLen: 1 << 16})
	defer s.Close()
	sink := s.Sink()
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		sink(benchEvent)
		if n++; n == 1<<15 {
			b.StopTimer()
			s.Flush()
			b.StartTimer()
			n = 0
		}
	}
}

// BenchmarkSinkDropped is the sink when the queue is full: it must stay as
// cheap, as a stalled disk may not slow the UI thread.
func BenchmarkSinkDropped(b *testing.B) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	first := true
	s := Open(b.TempDir(), Options{Version: "0.2.0", QueueLen: 4, beforeWrite: func() {
		if first {
			first = false
			entered <- struct{}{}
			<-block
		}
	}})
	sink := s.Sink()
	sink(benchEvent)
	<-entered
	b.ReportAllocs()
	for b.Loop() {
		sink(benchEvent)
	}
	close(block)
	s.Close()
}

// BenchmarkOperation is a whole measured operation as the app does it: Begin,
// one Mark and End, with the store's sink installed.
func BenchmarkOperation(b *testing.B) {
	s := Open(b.TempDir(), Options{Version: "0.2.0", QueueLen: 1 << 16})
	perf.SetSink(s.Sink())
	defer func() { perf.SetSink(nil); s.Close() }()
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		sp := perf.Begin(perf.OpOpenFile)
		sp.SetSize(perf.SizeLarge)
		sp.Mark(perf.PhaseLoadFile)
		sp.End(perf.ResultOK)
		if n++; n == 1<<15 {
			b.StopTimer()
			s.Flush()
			b.StartTimer()
			n = 0
		}
	}
}

// BenchmarkParseLine is reading one line of a log back.
func BenchmarkParseLine(b *testing.B) {
	line := benchEntry().AppendJSON(nil)
	in := interner{}
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		if _, _, ok := parseLine(line, in); !ok {
			b.Fatal("not parsed")
		}
	}
}

func benchEntry() Entry {
	return Entry{T: day1, Op: "switch_branch", Ms: 612, Result: "ok", Size: "large", Version: "0.2.0",
		Phases: []PhaseMs{{"git_checkout", 480}, {"load_repo", 95}, {"apply", 21}}}
}

// BenchmarkEncodeLine is the writer's cost of one line.
func BenchmarkEncodeLine(b *testing.B) {
	e := benchEntry()
	buf := make([]byte, 0, 512)
	b.ReportAllocs()
	for b.Loop() {
		buf = e.AppendJSON(buf[:0])
	}
}
