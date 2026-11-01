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

// climbModel has three versions getting faster, on days far apart: time plays
// no part in the summary. Runs vary around each median, as real ones do.
func climbModel() *Model {
	m := NewModel(utc)
	for _, v := range []struct {
		version string
		ms      float64
		month   time.Month
	}{{"0.1.0", 400, 1}, {"0.2.0", 200, 6}, {"0.3.0", 100, 12}} {
		for s := range 30 {
			t := time.Date(2026, v.month, 1, 0, s, 0, 0, utc)
			ms := v.ms * (0.95 + 0.1*float64(s)/29)
			m.Add(entry("open_file", t, ms, "ok", "small", v.version, perfstore.PhaseMs{Name: "load_file", Ms: ms}))
		}
	}
	return m
}

func TestSummarizeIsAPointPerVersion(t *testing.T) {
	m := climbModel()
	s := m.Summarize("open_file", Filter{Size: "small"})
	if len(s.Points) != 3 || s.Points[0].Version != "0.1.0" || s.Points[2].Version != "0.3.0" {
		t.Fatalf("points = %+v", s.Points)
	}
	if s.Points[1].N != 30 || s.Points[1].Thin {
		t.Fatalf("0.2.0 = %+v", s.Points[1])
	}
	if s.Version != "0.3.0" || s.BaseVersion != "0.1.0" || s.Verdict != VerdictFaster || !s.HasChange || s.Change > -0.7 || s.Change < -0.8 {
		t.Fatalf("summary = %+v", s)
	}
	if s.N != 30 || len(s.Phases) != 1 || len(s.Slowest) == 0 || s.Slowest[0].Version != "0.3.0" {
		t.Fatalf("the detail is not of the latest version: %+v", s)
	}
	last := m.Summarize("open_file", Filter{Size: "small", Last: 2})
	if len(last.Points) != 2 || last.BaseVersion != "0.2.0" || last.Verdict != VerdictFaster {
		t.Fatalf("last 2 = %+v", last)
	}
	if none := m.Summarize("open_file", Filter{Size: "large"}); none.N != 0 || none.Version != "" || none.HasChange || len(none.Points) != 3 || none.Points[0].N != 0 {
		t.Fatalf("other size = %+v", none)
	}
}

func TestSummaryOnlyComparesVersionsWithEnoughSamples(t *testing.T) {
	m := NewModel(utc)
	for i := range 19 { // too few to judge
		m.Add(entry("search", at(2, 0).Add(time.Duration(i)*time.Minute), 40, "ok", "small", "1"))
	}
	for i := range 25 {
		m.Add(entry("search", at(3, 0).Add(time.Duration(i)*time.Minute), 20, "ok", "small", "2"))
	}
	for i := range 2 { // the latest, but too few to lead with
		m.Add(entry("search", at(4, i), 90, "ok", "small", "3"))
	}
	s := m.Summarize("search", Filter{})
	if !s.Points[0].Thin || s.Points[1].Thin || !s.Points[2].Thin {
		t.Fatalf("points = %+v", s.Points)
	}
	if s.HasChange || s.Verdict != VerdictThin || s.Current != 20 || s.CurrentFrom != "2" || s.Version != "3" {
		t.Fatalf("summary = %+v", s)
	}
	thin := NewModel(utc)
	thin.Add(entry("search", at(2, 1), 7, "ok", "small", "1"))
	ts := thin.Summarize("search", Filter{})
	if ts.Current != 7 || ts.CurrentFrom != "1" || ts.HasChange {
		t.Fatalf("thin = %+v", ts)
	}
	if empty := NewModel(nil).Summarize("search", Filter{}); empty.N != 0 || len(empty.Points) != 0 || empty.Current != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

// A newer version that only ran on small repositories must not read as faster
// than an older one that ran on large ones.
func TestMixedSizesGetNoVerdict(t *testing.T) {
	m := NewModel(utc)
	for i := range 30 {
		m.Add(entry("search", at(2, 0).Add(time.Duration(i)*time.Minute), 400, "ok", "large", "1"))
		m.Add(entry("search", at(3, 0).Add(time.Duration(i)*time.Minute), 20, "ok", "small", "2"))
	}
	if s := m.Summarize("search", Filter{}); s.Verdict != VerdictMixed || s.HasChange {
		t.Fatalf("summary = %+v", s)
	}
	if v, _ := m.CompareVersions("search", "large", "1", "2"); v != VerdictThin {
		t.Fatalf("one size with no data on one side = %s", v)
	}
}

func TestMedianCI(t *testing.T) {
	seq := func(n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = float64(i + 1)
		}
		return out
	}
	if _, _, ok := MedianCI(seq(5)); ok {
		t.Fatal("5 values cannot give a 95% interval")
	}
	// From published tables of order statistics for the median.
	for _, c := range []struct {
		n      int
		lo, hi float64
	}{{6, 1, 6}, {9, 2, 8}, {20, 6, 15}, {100, 40, 61}} {
		lo, hi, ok := MedianCI(seq(c.n))
		if !ok || lo != c.lo || hi != c.hi {
			t.Errorf("n=%d: [%v, %v], want [%v, %v]", c.n, lo, hi, c.lo, c.hi)
		}
	}
	if lo, hi, ok := MedianCI(seq(10000)); !ok || lo > 4905 || lo < 4895 || hi < 5095 || hi > 5106 {
		t.Errorf("n=10000: [%v, %v]", lo, hi)
	}
}

func TestJudgeNeedsARealDifference(t *testing.T) {
	spread := func(mid, width float64) []float64 {
		out := make([]float64, 40)
		for i := range out {
			out[i] = mid - width + 2*width*float64(i)/39
		}
		return out
	}
	if v, c := Judge(spread(100, 5), spread(50, 5)); v != VerdictFaster || c > -0.45 {
		t.Fatalf("%s %v", v, c)
	}
	if v, _ := Judge(spread(50, 5), spread(100, 5)); v != VerdictSlower {
		t.Fatal(v)
	}
	if v, _ := Judge(spread(100, 90), spread(80, 90)); v != VerdictFlat {
		t.Fatalf("wide runs: %s", v)
	}
	// Precise runs, but too small a change to act on.
	if v, _ := Judge(spread(100, 1), spread(98, 1)); v != VerdictFlat {
		t.Fatalf("small change: %s", v)
	}
	if v, _ := Judge(spread(100, 1)[:19], spread(10, 1)); v != VerdictThin {
		t.Fatal(v)
	}
}

func TestUnexplainedAndUnseenAreCounted(t *testing.T) {
	m := NewModel(utc)
	m.Add(entry("x", at(2, 0), 320000, "ok", "small", "1", perfstore.PhaseMs{Name: "git_status", Ms: 30}))
	m.Add(entry("x", at(2, 1), 100, "ok", "small", "1", perfstore.PhaseMs{Name: "git_status", Ms: 90}))
	m.Add(entry("x", at(2, 2), 4, "ok", "small", "1"))
	m.Add(entry("x", at(2, 3), 60, "ok", "small", "1"))
	m.Add(entry("x", at(2, 4), 60, perfstore.ResultUnseen, "small", "1"))
	st := m.Stat(Query{Op: "x", R: AllTime}, 0)
	if st.N != 4 || st.Unseen != 1 || st.Total() != 5 || st.Unexplained != 2 {
		t.Fatalf("stat = %+v", st)
	}
}
