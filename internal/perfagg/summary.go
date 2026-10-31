package perfagg

import (
	"arbor/internal/perfstore"
	"sort"
	"time"
)

// DayPoint is one day of a chart: the median and the 95th percentile of the
// operation that day.
type DayPoint struct {
	// Day is the start of the day in the model's location.
	Day time.Time
	// N is how many samples the day has. A day with fewer than MinDaySamples is
	// a Gap: it is not drawn, and the line is broken there.
	N        int
	P50, P95 float64
	Gap      bool
}

// VersionMark is where a new app version first shows in the data.
type VersionMark struct {
	Version string
	At      time.Time
}

// Filter says which part of the data a summary covers.
type Filter struct {
	// Size is small, medium or large; "" for all.
	Size string
	// Days is the length of the range in calendar days, ending with the day of
	// End (the present).
	Days int
	End  time.Time
}

// Summary is everything a card of the Performance window shows about one
// operation.
type Summary struct {
	Op string
	// From is the start of the first day of the range, To the start of the day
	// after the last.
	From, To time.Time

	// N counts the ok samples of the range, Errors and Cancelled the others.
	N, Errors, Cancelled int
	P50, P95, P99, Max   float64

	// Current is the median of the latest days (CurrentDays of them) when they
	// have enough samples, else of the whole range: the number a card leads with.
	Current     float64
	CurrentN    int
	CurrentDays int
	// Baseline is the median of the first days of the range. Change is how
	// Current moved from it, as a fraction (-0.5 is half as slow), and is only
	// there (HasChange) when both sides have MinDaySamples samples.
	Baseline  float64
	Change    float64
	HasChange bool

	Days    []DayPoint
	Marks   []VersionMark
	Phases  []PhaseStat
	Slowest []perfstore.Entry
}

// edgeDays is how many days at each end of a range the change is measured on.
func edgeDays(days int) int { return max(1, days/6) }

// Summarize computes the card of op for f.
func (m *Model) Summarize(op string, f Filter) Summary {
	days := max(f.Days, 1)
	r := m.Last(days, f.End)
	q := Query{Op: op, Size: f.Size, R: r}
	st := m.Stat(q, 10)
	s := Summary{
		Op: op, From: m.DayStart(r.From), To: m.DayStart(r.To + 1),
		N: st.N, Errors: st.Errors, Cancelled: st.Cancelled,
		P50: st.P50, P95: st.P95, P99: st.P99, Max: st.Max,
		Phases: st.Phases, Slowest: st.Slowest,
	}
	for d := r.From; d <= r.To; d++ {
		dq := q
		dq.R = Range{d, d}
		vs := m.Values(dq)
		p := DayPoint{Day: m.DayStart(d), N: len(vs)}
		if len(vs) >= MinDaySamples {
			p.P50, p.P95 = Percentile(vs, 50), Percentile(vs, 95)
		} else {
			p.Gap = true
		}
		s.Days = append(s.Days, p)
	}

	edge := edgeDays(days)
	cur := m.Values(Query{Op: op, Size: f.Size, R: Range{r.To - int32(edge) + 1, r.To}})
	base := m.Values(Query{Op: op, Size: f.Size, R: Range{r.From, r.From + int32(edge) - 1}})
	switch {
	case len(cur) >= MinDaySamples:
		s.Current, s.CurrentN, s.CurrentDays = Percentile(cur, 50), len(cur), edge
	case st.N > 0:
		s.Current, s.CurrentN, s.CurrentDays = st.P50, st.N, days
	}
	if len(base) >= MinDaySamples {
		s.Baseline = Percentile(base, 50)
		if len(cur) >= MinDaySamples && s.Baseline > 0 {
			s.Change = (s.Current - s.Baseline) / s.Baseline
			s.HasChange = true
		}
	}
	s.Marks = m.marks(s.From, s.To)
	return s
}

// marks are the versions that first appeared inside [from, to), not counting
// the oldest version of the data, which has nothing before it.
func (m *Model) marks(from, to time.Time) []VersionMark {
	vs := m.Versions()
	var out []VersionMark
	for i, v := range vs {
		if i == 0 {
			continue
		}
		if !v.First.Before(from) && v.First.Before(to) {
			out = append(out, VersionMark{v.Version, v.First})
		}
	}
	return out
}

// Trend compares the median of the days up to end with the same number of days
// before them.
type Trend struct {
	Now, Before          Stat
	P50Change, P95Change float64
	// Reliable is set when both periods have MinDaySamples samples.
	Reliable bool
}

// TrendOf compares the period of days ending with the day of end against the
// period of the same length before it.
func (m *Model) TrendOf(q Query, days int, end time.Time) Trend {
	days = max(days, 1)
	endDay := m.Day(end)
	q.R = Range{endDay - int32(days) + 1, endDay}
	now := m.Stat(q, 0)
	q.R = Range{endDay - int32(2*days) + 1, endDay - int32(days)}
	before := m.Stat(q, 0)
	t := Trend{Now: now, Before: before}
	if now.N >= MinDaySamples && before.N >= MinDaySamples {
		t.Reliable = true
		if before.P50 > 0 {
			t.P50Change = (now.P50 - before.P50) / before.P50
		}
		if before.P95 > 0 {
			t.P95Change = (now.P95 - before.P95) / before.P95
		}
	}
	return t
}

// SortSlowest orders entries slowest first, oldest first among equals.
func SortSlowest(es []perfstore.Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Ms != es[j].Ms {
			return es[i].Ms > es[j].Ms
		}
		return es[i].T.Before(es[j].T)
	})
}
