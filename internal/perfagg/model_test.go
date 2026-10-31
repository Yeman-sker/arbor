package perfagg

import (
	"arbor/internal/perfstore"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestPercentileIsNearestRank(t *testing.T) {
	// The worked example of the nearest-rank method: eleven values.
	v := []float64{3, 6, 7, 8, 8, 9, 10, 13, 15, 16, 20}
	for _, c := range []struct{ p, want float64 }{
		{0, 3}, {1, 3}, {9, 3}, {10, 6}, {25, 7}, {50, 9}, {75, 15}, {90, 16}, {91, 20}, {99, 20}, {100, 20}, {150, 20}, {-5, 3},
	} {
		if got := Percentile(v, c.p); got != c.want {
			t.Errorf("P%v = %v, want %v", c.p, got, c.want)
		}
	}
	// Five values: ranks 1, 2, 3 (the median is the middle one), 5.
	f := []float64{15, 20, 35, 40, 50}
	for _, c := range []struct{ p, want float64 }{{30, 20}, {40, 20}, {50, 35}, {95, 50}, {100, 50}} {
		if got := Percentile(f, c.p); got != c.want {
			t.Errorf("P%v of five = %v, want %v", c.p, got, c.want)
		}
	}
	// 1..100: the p-th percentile is p.
	hundred := make([]float64, 100)
	for i := range hundred {
		hundred[i] = float64(i + 1)
	}
	for _, p := range []float64{1, 5, 50, 90, 95, 99, 100} {
		if got := Percentile(hundred, p); got != p {
			t.Errorf("P%v of 1..100 = %v", p, got)
		}
	}
	// Float noise must not move a rank: 99.9 percent of 1000 values is rank 999.
	thousand := make([]float64, 1000)
	for i := range thousand {
		thousand[i] = float64(i + 1)
	}
	if got := Percentile(thousand, 99.9); got != 999 {
		t.Errorf("P99.9 of 1..1000 = %v, want 999", got)
	}
	if got := Percentile(thousand, 95); got != 950 {
		t.Errorf("P95 of 1..1000 = %v, want 950", got)
	}
	// Edge cases.
	if Percentile(nil, 50) != 0 || Percentile([]float64{7}, 99) != 7 || Percentile([]float64{7}, 0) != 7 {
		t.Error("empty and single-value cases wrong")
	}
	// The result is always a value of the data.
	for n := 1; n < 40; n++ {
		xs := make([]float64, n)
		for i := range xs {
			xs[i] = float64(i)*1.5 + 0.25
		}
		for p := 0.0; p <= 100; p += 3.7 {
			got := Percentile(xs, p)
			found := false
			for _, x := range xs {
				found = found || x == got
			}
			if !found {
				t.Fatalf("P%v of %d values = %v, not one of them", p, n, got)
			}
		}
	}
}

var utc = time.UTC

func at(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, utc) }

func entry(op string, t time.Time, ms float64, result, size, version string, phases ...perfstore.PhaseMs) perfstore.Entry {
	return perfstore.Entry{T: t, Op: op, Ms: ms, Result: result, Size: size, Version: version, Phases: phases}
}

func TestStatCountsOnlyOKDurations(t *testing.T) {
	m := NewModel(utc)
	for i := 1; i <= 10; i++ {
		m.Add(entry("open_file", at(5, i), float64(i*10), "ok", "small", "1"))
	}
	m.Add(entry("open_file", at(5, 11), 9999, "error", "small", "1"))
	m.Add(entry("open_file", at(5, 12), 8888, "cancelled", "small", "1"))
	m.Add(entry("open_file", at(5, 13), 7777, "cancelled", "small", "1"))
	st := m.Stat(Query{Op: "open_file", R: AllTime}, 3)
	if st.N != 10 || st.Errors != 1 || st.Cancelled != 2 || st.Total() != 13 {
		t.Fatalf("counts = %+v", st)
	}
	// Ten values 10..100: the median is rank 5, p95 and p99 are rank 10.
	if st.P50 != 50 || st.P95 != 100 || st.P99 != 100 || st.Max != 100 || st.Mean != 55 {
		t.Fatalf("stat = %+v", st)
	}
	if len(st.Slowest) != 3 || st.Slowest[0].Ms != 100 || st.Slowest[2].Ms != 80 {
		t.Fatalf("slowest = %+v", st.Slowest)
	}
	if got := m.Stat(Query{Op: "nope", R: AllTime}, 0); got.Total() != 0 {
		t.Fatalf("unknown op has %+v", got)
	}
	if m.Events() != 13 || m.Count("open_file") != 13 || fmt.Sprint(m.Ops()) != "[open_file]" {
		t.Fatalf("events=%d count=%d ops=%v", m.Events(), m.Count("open_file"), m.Ops())
	}
}

func TestStatFiltersBySizeVersionAndDays(t *testing.T) {
	m := NewModel(utc)
	for d := 1; d <= 10; d++ {
		m.Add(entry("commit", at(d, 9), float64(100*d), "ok", "small", "1.0"))
		m.Add(entry("commit", at(d, 10), float64(1000*d), "ok", "large", "2.0"))
	}
	all := m.Stat(Query{Op: "commit", R: AllTime}, 0)
	small := m.Stat(Query{Op: "commit", Size: "small", R: AllTime}, 0)
	v2 := m.Stat(Query{Op: "commit", Version: "2.0", R: AllTime}, 0)
	if all.N != 20 || small.N != 10 || v2.N != 10 || small.Max != 1000 || v2.Max != 10000 {
		t.Fatalf("all=%+v small=%+v v2=%+v", all.N, small, v2)
	}
	days := m.Stat(Query{Op: "commit", Size: "small", R: Range{m.Day(at(3, 0)), m.Day(at(5, 0))}}, 0)
	if days.N != 3 || days.P50 != 400 {
		t.Fatalf("days 3-5 = %+v", days)
	}
	by := m.ByVersion(Query{Op: "commit", R: AllTime})
	if len(by) != 2 || by[0].Key != "1.0" || by[1].Key != "2.0" || by[0].N != 10 {
		t.Fatalf("by version = %+v", by)
	}
	bs := m.BySize(Query{Op: "commit", R: AllTime})
	if len(bs) != 2 || bs[0].Key != "small" || bs[1].Key != "large" {
		t.Fatalf("by size = %+v", bs)
	}
	if got := fmt.Sprint(m.Sizes()); got != "[small large]" {
		t.Fatalf("sizes = %s", got)
	}
}

func TestPhasesAndTheirShare(t *testing.T) {
	m := NewModel(utc)
	for i := 0; i < 10; i++ {
		m.Add(entry("switch_branch", at(5, i), 100, "ok", "large", "1",
			perfstore.PhaseMs{Name: "git_checkout", Ms: 70}, perfstore.PhaseMs{Name: "load_repo", Ms: 20},
			perfstore.PhaseMs{Name: "apply", Ms: 5}, perfstore.PhaseMs{Name: "apply", Ms: 5}))
	}
	st := m.Stat(Query{Op: "switch_branch", R: AllTime}, 1)
	if len(st.Phases) != 3 || st.Phases[0].Phase != "git_checkout" || st.Phases[0].P50 != 70 || st.Phases[2].Phase != "apply" || st.Phases[2].P50 != 10 {
		t.Fatalf("phases = %+v", st.Phases)
	}
	if math.Abs(st.Phases[0].Share-0.7) > 1e-9 || math.Abs(st.Phases[1].Share-0.2) > 1e-9 {
		t.Fatalf("shares = %+v", st.Phases)
	}
	if len(st.Slowest) != 1 || len(st.Slowest[0].Phases) != 4 {
		t.Fatalf("slowest keeps its phases: %+v", st.Slowest)
	}
}

// A week of data with a day too thin to draw, a new version, and a speed-up.
func climbModel() (*Model, time.Time) {
	m := NewModel(utc)
	now := at(31, 12)
	for back := 29; back >= 0; back-- {
		day := now.AddDate(0, 0, -back)
		version := "0.1.0"
		ms := 100.0
		if back < 15 {
			version = "0.2.0"
			ms = 50
		}
		n := 20
		if back == 10 {
			n = 3 // too few samples: a gap
		}
		for i := 0; i < n; i++ {
			m.Add(entry("open_file", day.Add(time.Duration(i)*time.Minute), ms+float64(i), "ok", "large", version))
		}
	}
	return m, now
}

func TestSummarizeGapsChangeAndMarks(t *testing.T) {
	m, now := climbModel()
	s := m.Summarize("open_file", Filter{Days: 30, End: now})
	if len(s.Days) != 30 || !s.From.Equal(at(2, 0)) || !s.To.Equal(at(1, 0).AddDate(0, 1, 0)) {
		t.Fatalf("range: %d days from %v to %v", len(s.Days), s.From, s.To)
	}
	gaps := 0
	for i, d := range s.Days {
		if d.Gap {
			gaps++
			if d.N != 3 || i != 19 {
				t.Errorf("gap at index %d with %d samples", i, d.N)
			}
			if d.P50 != 0 || d.P95 != 0 {
				t.Errorf("a gap carries numbers: %+v", d)
			}
		} else if d.N != 20 {
			t.Errorf("day %d has %d samples", i, d.N)
		}
	}
	if gaps != 1 {
		t.Fatalf("%d gaps, want 1", gaps)
	}
	// The first five days are 100..119 ms (median of 20 values: rank 10 = 109),
	// the last five 50..69 (rank 10 = 59).
	if s.Baseline != 109 || s.Current != 59 || s.CurrentDays != 5 || !s.HasChange {
		t.Fatalf("baseline=%v current=%v days=%d change=%v", s.Baseline, s.Current, s.CurrentDays, s.HasChange)
	}
	if want := (59.0 - 109.0) / 109.0; math.Abs(s.Change-want) > 1e-9 {
		t.Fatalf("change = %v, want %v", s.Change, want)
	}
	if len(s.Marks) != 1 || s.Marks[0].Version != "0.2.0" || !s.Marks[0].At.Equal(at(17, 12)) {
		t.Fatalf("marks = %+v", s.Marks)
	}
	if s.N != 29*20+3 || s.P50 == 0 || s.P95 < s.P50 || s.Max != 119 {
		t.Fatalf("totals: N=%d p50=%v p95=%v max=%v", s.N, s.P50, s.P95, s.Max)
	}
	if len(s.Slowest) == 0 || s.Slowest[0].Ms != 119 {
		t.Fatalf("slowest = %+v", s.Slowest)
	}
	// A narrower range keeps no mark that is outside it, and the first version has none.
	w := m.Summarize("open_file", Filter{Days: 7, End: now})
	if len(w.Days) != 7 || len(w.Marks) != 0 {
		t.Fatalf("7 days: %d days, marks %+v", len(w.Days), w.Marks)
	}
	// Sizes filter.
	if none := m.Summarize("open_file", Filter{Days: 30, End: now, Size: "small"}); none.N != 0 || none.HasChange || none.Current != 0 {
		t.Fatalf("small = %+v", none)
	}
}

func TestSummaryChangeNeedsEnoughSamplesOnBothSides(t *testing.T) {
	m := NewModel(utc)
	now := at(31, 12)
	for i := 0; i < 4; i++ { // four samples at the start: too few
		m.Add(entry("search", at(2, i), 40, "ok", "small", "1"))
	}
	for d := 20; d <= 31; d++ {
		for i := 0; i < 6; i++ {
			m.Add(entry("search", at(d, i), 20, "ok", "small", "1"))
		}
	}
	s := m.Summarize("search", Filter{Days: 30, End: now})
	if s.HasChange || s.Baseline != 0 || s.Current != 20 {
		t.Fatalf("summary = baseline %v current %v change %v has=%v", s.Baseline, s.Current, s.Change, s.HasChange)
	}
	// Too thin everywhere: the headline falls back to the whole range, or to nothing.
	thin := NewModel(utc)
	for d := 28; d <= 31; d++ {
		thin.Add(entry("search", at(d, 1), float64(d), "ok", "small", "1"))
	}
	ts := thin.Summarize("search", Filter{Days: 30, End: now})
	if ts.CurrentDays != 30 || ts.Current != 29 || ts.HasChange {
		t.Fatalf("thin: current=%v over %d days has=%v", ts.Current, ts.CurrentDays, ts.HasChange)
	}
	for _, d := range ts.Days {
		if !d.Gap {
			t.Fatalf("a day with one sample is drawn: %+v", d)
		}
	}
	empty := NewModel(nil).Summarize("search", Filter{Days: 30, End: now})
	if empty.N != 0 || len(empty.Days) != 30 || empty.Current != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestDaysAreCalendarDaysOfTheLocation(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*3600)
	m := NewModel(tokyo)
	// 20:00 UTC on the 30th is 05:00 on the 31st in Tokyo.
	e := entry("stage", time.Date(2026, 10, 30, 20, 0, 0, 0, utc), 5, "ok", "small", "1")
	m.Add(e)
	if got := m.DayStart(m.Day(e.T)); !got.Equal(time.Date(2026, 10, 31, 0, 0, 0, 0, tokyo)) {
		t.Fatalf("day start = %v", got)
	}
	if m.Day(e.T) == NewModel(utc).Day(e.T) {
		t.Fatal("the same instant is the same day in both zones")
	}
	if m.DayStart(m.Day(e.T)+1).Sub(m.DayStart(m.Day(e.T))) != 24*time.Hour {
		t.Fatal("a day is not 24 hours")
	}
}

func TestTrendComparesThePeriodWithTheOneBefore(t *testing.T) {
	m := NewModel(utc)
	now := at(31, 12)
	for back := 0; back < 14; back++ {
		ms := 200.0
		if back < 7 {
			ms = 100
		}
		for i := 0; i < 3; i++ {
			m.Add(entry("commit", now.AddDate(0, 0, -back).Add(time.Duration(i)*time.Minute), ms, "ok", "large", "1"))
		}
	}
	tr := m.TrendOf(Query{Op: "commit"}, 7, now)
	if !tr.Reliable || tr.Now.N != 21 || tr.Before.N != 21 || math.Abs(tr.P50Change-(-0.5)) > 1e-9 {
		t.Fatalf("trend = %+v", tr)
	}
	thin := m.TrendOf(Query{Op: "commit"}, 1, now) // three samples a day: too few
	if thin.Reliable {
		t.Fatalf("a trend of 3 against 3 samples is called reliable: %+v", thin)
	}
}

func TestSlowestKeepsTheTopOfEachDayAndIsBounded(t *testing.T) {
	m := NewModel(utc)
	for i := 0; i < 500; i++ {
		m.Add(entry("open_diff", at(5, 0).Add(time.Duration(i)*time.Second), float64((i*37)%500), "ok", "small", "1"))
	}
	st := m.Stat(Query{Op: "open_diff", R: AllTime}, 100)
	if len(st.Slowest) != SlowestPerDay {
		t.Fatalf("kept %d slowest of one day, want %d", len(st.Slowest), SlowestPerDay)
	}
	for i := 1; i < len(st.Slowest); i++ {
		if st.Slowest[i].Ms > st.Slowest[i-1].Ms {
			t.Fatalf("slowest not in order: %+v", st.Slowest)
		}
	}
	if st.Slowest[0].Ms != 499 {
		t.Fatalf("slowest = %v", st.Slowest[0].Ms)
	}
}
