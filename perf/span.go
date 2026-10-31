package perf

import (
	"sync/atomic"
	"time"
)

const maxPhases = 6

// PhaseTime is the time spent in one phase of an operation.
type PhaseTime struct {
	Phase Phase
	Dur   time.Duration
}

// Event is one finished operation. It holds no free-form text.
type Event struct {
	Op      Op
	Result  Result
	Size    Size
	Start   time.Time
	Dur     time.Duration
	NPhases uint8
	Phases  [maxPhases]PhaseTime
}

// Sink receives finished events. It runs on the caller's goroutine, including
// the UI thread, so it must only hand the event off.
type Sink func(Event)

var sink atomic.Pointer[Sink]

// SetSink installs the receiver of events; nil turns recording off.
func SetSink(s Sink) {
	if s == nil {
		sink.Store(nil)
		return
	}
	sink.Store(&s)
}

// Enabled reports whether events are being recorded.
func Enabled() bool { return sink.Load() != nil }

// Span measures one operation. Its zero value does nothing, which is what Begin
// returns while recording is off. A span belongs to one goroutine at a time.
type Span struct {
	ev   Event
	last time.Time
}

// Begin starts measuring op.
func Begin(op Op) Span {
	if sink.Load() == nil {
		return Span{}
	}
	now := time.Now()
	return Span{ev: Event{Op: op, Start: now}, last: now}
}

// Active reports whether the span will produce an event.
func (s *Span) Active() bool { return s.ev.Op != OpNone }

// SetSize records the repository size bucket.
func (s *Span) SetSize(size Size) { s.ev.Size = size }

// Mark closes a phase: the time since Begin or the previous Mark is charged to p.
// Phases past the fixed capacity still count toward the total.
func (s *Span) Mark(p Phase) {
	if s.ev.Op == OpNone {
		return
	}
	now := time.Now()
	if s.ev.NPhases < maxPhases {
		s.ev.Phases[s.ev.NPhases] = PhaseTime{p, now.Sub(s.last)}
		s.ev.NPhases++
	}
	s.last = now
}

// End finishes the span and hands the event to the sink. Prefer PaintTracker
// when the operation changes what is on screen.
func (s *Span) End(r Result) {
	if s.ev.Op == OpNone {
		return
	}
	s.ev.Result = r
	s.ev.Dur = time.Since(s.ev.Start)
	if p := sink.Load(); p != nil {
		(*p)(s.ev)
	}
	s.ev.Op = OpNone
}

// PaintTracker ends spans at the first paint that contains their new state, so
// a recorded duration is what the user waited for, excluding GPU present.
// It is owned by the UI thread.
type PaintTracker struct {
	pending []armedSpan
}

type armedSpan struct {
	span   Span
	result Result
}

// Arm schedules s to end at the next paint. Call it where the new state is published.
func (t *PaintTracker) Arm(s Span, r Result) {
	if s.Active() {
		t.pending = append(t.pending, armedSpan{s, r})
	}
}

// Pending reports whether a span is waiting for a paint.
func (t *PaintTracker) Pending() bool { return len(t.pending) > 0 }

// Painted ends every armed span. It is safe to call repeatedly.
func (t *PaintTracker) Painted() {
	for i := range t.pending {
		t.pending[i].span.End(t.pending[i].result)
	}
	t.pending = t.pending[:0]
}
