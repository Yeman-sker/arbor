package perf

import (
	"strings"
	"testing"
)

func TestDisabledSpanIsInert(t *testing.T) {
	SetSink(nil)
	s := Begin(OpOpenFile)
	s.Mark(PhaseLoadFile)
	s.End(ResultOK)
	if s.Active() {
		t.Fatal("span active while recording is off")
	}
	var tr PaintTracker
	tr.Arm(Begin(OpOpenFile), ResultOK)
	if tr.Pending() {
		t.Fatal("inactive span must not wait for a paint")
	}
}

func TestSpanReportsPhasesOnce(t *testing.T) {
	var got []Event
	SetSink(func(e Event) { got = append(got, e) })
	defer SetSink(nil)

	s := Begin(OpSwitchBranch)
	s.SetSize(SizeOf(20000))
	s.Mark(PhaseGitCheckout)
	s.Mark(PhaseLoadRepo)
	s.End(ResultError)
	s.End(ResultOK)

	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	e := got[0]
	if e.Op != OpSwitchBranch || e.Result != ResultError || e.Size != SizeLarge || e.NPhases != 2 {
		t.Fatalf("event = %+v", e)
	}
	if e.Phases[0].Phase != PhaseGitCheckout || e.Phases[1].Phase != PhaseLoadRepo {
		t.Fatalf("phases = %+v", e.Phases)
	}
}

func TestExtraPhasesAreDropped(t *testing.T) {
	var got Event
	SetSink(func(e Event) { got = e })
	defer SetSink(nil)
	s := Begin(OpRefresh)
	for range maxPhases + 3 {
		s.Mark(PhaseApply)
	}
	s.End(ResultOK)
	if got.NPhases != maxPhases {
		t.Fatalf("phases = %d", got.NPhases)
	}
}

func TestPaintTrackerEndsAtPaint(t *testing.T) {
	n := 0
	SetSink(func(Event) { n++ })
	defer SetSink(nil)
	var tr PaintTracker
	tr.Arm(Begin(OpModeSwitch), ResultOK)
	if n != 0 || !tr.Pending() {
		t.Fatal("span ended before the paint")
	}
	tr.Painted()
	tr.Painted()
	if n != 1 || tr.Pending() {
		t.Fatalf("events = %d pending = %v", n, tr.Pending())
	}
}

func TestCatalogIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range Ops() {
		name := o.String()
		if name == "" || name == "unknown" || seen[name] {
			t.Errorf("op %d has bad name %q", o, name)
		}
		seen[name] = true
		if o.BudgetMs() <= 0 {
			t.Errorf("%s has no budget", name)
		}
	}
	for p := PhaseNone + 1; p < phaseCount; p++ {
		if p.String() == "" || strings.Contains(p.String(), " ") {
			t.Errorf("phase %d has bad name %q", p, p.String())
		}
	}
}

func BenchmarkDisabled(b *testing.B) {
	SetSink(nil)
	b.ReportAllocs()
	for b.Loop() {
		s := Begin(OpOpenFile)
		s.Mark(PhaseLoadFile)
		s.End(ResultOK)
	}
}

func BenchmarkEnabled(b *testing.B) {
	SetSink(func(Event) {})
	defer SetSink(nil)
	b.ReportAllocs()
	for b.Loop() {
		s := Begin(OpOpenFile)
		s.End(ResultOK)
	}
}
