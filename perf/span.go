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

// endHeld finishes a span whose paint was held: the duration stops at armed, when
// the work was done, and an operation that went well counts as unseen.
func (s *Span) endHeld(r Result, armed time.Time) {
	if s.ev.Op == OpNone {
		return
	}
	if r == ResultOK {
		r = ResultUnseen
	}
	s.ev.Result = r
	s.ev.Dur = armed.Sub(s.ev.Start)
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
	armed  time.Time
}

// PaintHeld is how long after Arm a paint means the window was not painting
// rather than slow. An occluded or minimized window holds its frames, so what a
// span waits for there is the user coming back, not the app. A settled frame
// takes milliseconds. A real stall this long ends as unseen too, which is the
// price of telling them apart without asking the window.
const PaintHeld = 2 * time.Second

// Arm schedules s to end at the next paint. Call it where the new state is published.
func (t *PaintTracker) Arm(s Span, r Result) {
	if s.Active() {
		t.pending = append(t.pending, armedSpan{s, r, time.Now()})
	}
}

// Pending reports whether a span is waiting for a paint.
func (t *PaintTracker) Pending() bool { return len(t.pending) > 0 }

// Painted ends every armed span, charging the wait for the paint to PhasePaint so
// that the phases account for the whole duration. It is safe to call repeatedly.
func (t *PaintTracker) Painted() {
	for i := range t.pending {
		p := &t.pending[i]
		if time.Since(p.armed) > PaintHeld {
			p.span.endHeld(p.result, p.armed)
			continue
		}
		p.span.Mark(PhasePaint)
		p.span.End(p.result)
	}
	t.pending = t.pending[:0]
}
