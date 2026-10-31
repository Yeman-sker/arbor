// Package perfagg turns the log of Arbor's measured operations into the numbers
// that are read: percentiles by day, by version and by size, the phases of an
// operation and its slowest samples. It is pure: it reads entries and returns
// values, so the Performance window, the export and the agent report all
// count the same way and one set of tests covers them.
//
// Percentiles are nearest rank (see Percentile). Only operations that ended ok
// have durations that mean something; errors and cancellations are counted.
package perfagg

import (
	"arbor/internal/perfstore"
	"math"
	"sort"
	"time"
)

// MinDaySamples is how many samples a day (or a side of a comparison) needs
// before a number is drawn or compared: fewer is a gap, not a point.
const MinDaySamples = 5

// SlowestPerDay is how many of the slowest samples a day keeps.
const SlowestPerDay = 10

// Percentile is the p-th percentile of sorted by the nearest-rank method: the
// smallest value that at least p percent of the values are not above, the
// value of rank ceil(p/100*n). It is always one of the values; p <= 0 gives the
// smallest. An empty slice gives 0.
func Percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	// The epsilon absorbs the error of p*n/100 (99.9*1000/100 is 999.0000000000001).
	rank := int(math.Ceil(p*float64(n)/100 - 1e-9))
	rank = max(1, min(n, rank))
	return sorted[rank-1]
}

type bucket struct {
	size, version     string
	ms                []float32
	phases            map[string][]float32
	slow              []perfstore.Entry // the slowest ok samples, slowest first
	errors, cancelled int
}

// opData holds the samples of one operation by day.
type opData struct {
	days map[int32][]*bucket
	n    int
}

// Model is the log, grouped for queries. Build it with Add, in any order; it
// is not safe for concurrent use. A day is a calendar day in the location the
// model was made with.
type Model struct {
	loc      *time.Location
	ops      map[string]*opData
	versions map[string]time.Time // first sample of each version
	sizes    map[string]bool
	first    time.Time
	last     time.Time
	events   int

	// Dropped and Corrupt are what the reader found wrong with the log: events
	// the writer lost to a full queue and lines that could not be read.
	Dropped uint64
	Corrupt int
}

// NewModel makes an empty model whose days are those of loc (UTC for nil).
func NewModel(loc *time.Location) *Model {
	if loc == nil {
		loc = time.UTC
	}
	return &Model{loc: loc, ops: map[string]*opData{}, versions: map[string]time.Time{}, sizes: map[string]bool{}}
}

// Loc is the location the days are counted in.
func (m *Model) Loc() *time.Location { return m.loc }

// Day is the index of the calendar day of t: days since 1970-01-01, counted in
// the model's location.
func (m *Model) Day(t time.Time) int32 {
	y, mo, d := t.In(m.loc).Date()
	return int32(time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// DayStart is the start of day index d in the model's location.
func (m *Model) DayStart(d int32) time.Time {
	u := time.Unix(int64(d)*86400, 0).UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, m.loc)
}

// Add puts one event into the model.
func (m *Model) Add(e perfstore.Entry) {
	m.events++
	if m.first.IsZero() || e.T.Before(m.first) {
		m.first = e.T
	}
	if e.T.After(m.last) {
		m.last = e.T
	}
	if t, ok := m.versions[e.Version]; !ok || e.T.Before(t) {
		m.versions[e.Version] = e.T
	}
	m.sizes[e.Size] = true
	od := m.ops[e.Op]
	if od == nil {
		od = &opData{days: map[int32][]*bucket{}}
		m.ops[e.Op] = od
	}
	od.n++
	day := m.Day(e.T)
	var b *bucket
	for _, c := range od.days[day] {
		if c.size == e.Size && c.version == e.Version {
			b = c
			break
		}
	}
	if b == nil {
		b = &bucket{size: e.Size, version: e.Version}
		od.days[day] = append(od.days[day], b)
	}
	switch e.Result {
	case perfstore.ResultOK:
		b.ms = append(b.ms, float32(e.Ms))
		if len(e.Phases) > 0 {
			if b.phases == nil {
				b.phases = map[string][]float32{}
			}
			seen := map[string]bool{}
			for _, p := range e.Phases {
				if seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				sum := 0.0
				for _, q := range e.Phases {
					if q.Name == p.Name {
						sum += q.Ms
					}
				}
				b.phases[p.Name] = append(b.phases[p.Name], float32(sum))
			}
		}
		b.keepSlow(e)
	case perfstore.ResultError:
		b.errors++
	case perfstore.ResultCancelled:
		b.cancelled++
	}
}

// keepSlow remembers e when it is among the slowest of the bucket.
func (b *bucket) keepSlow(e perfstore.Entry) {
	if len(b.slow) >= SlowestPerDay && e.Ms <= b.slow[len(b.slow)-1].Ms {
		return
	}
	e.Phases = append([]perfstore.PhaseMs(nil), e.Phases...)
	i := sort.Search(len(b.slow), func(i int) bool { return b.slow[i].Ms < e.Ms })
	b.slow = append(b.slow, perfstore.Entry{})
	copy(b.slow[i+1:], b.slow[i:])
	b.slow[i] = e
	if len(b.slow) > SlowestPerDay {
		b.slow = b.slow[:SlowestPerDay]
	}
}

// Events is how many events the model holds, of every result.
func (m *Model) Events() int { return m.events }

// Bounds are the times of the first and the last event.
func (m *Model) Bounds() (first, last time.Time, ok bool) {
	return m.first, m.last, m.events > 0
}

// Ops lists the operations with events, sorted by name.
func (m *Model) Ops() []string {
	out := make([]string, 0, len(m.ops))
	for op := range m.ops {
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

// Count is how many events (of any result) an operation has.
func (m *Model) Count(op string) int {
	if od := m.ops[op]; od != nil {
		return od.n
	}
	return 0
}

// VersionInfo is an app version seen in the log and when it was first seen.
type VersionInfo struct {
	Version string
	First   time.Time
}

// Versions lists the versions in the order they first appeared.
func (m *Model) Versions() []VersionInfo {
	out := make([]VersionInfo, 0, len(m.versions))
	for v, t := range m.versions {
		out = append(out, VersionInfo{v, t})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].First.Equal(out[j].First) {
			return out[i].First.Before(out[j].First)
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Sizes lists the size buckets with events, smallest first.
func (m *Model) Sizes() []string {
	order := map[string]int{"small": 0, "medium": 1, "large": 2}
	var out []string
	for s := range m.sizes {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, iok := order[out[i]]
		oj, jok := order[out[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		}
		return out[i] < out[j]
	})
	return out
}

// Range is a stretch of days, by index, both ends included.
type Range struct{ From, To int32 }

// AllTime is every day.
var AllTime = Range{math.MinInt32, math.MaxInt32}

// Last is the n days that end with the day of end.
func (m *Model) Last(n int, end time.Time) Range {
	d := m.Day(end)
	return Range{d - int32(max(n, 1)) + 1, d}
}

// Query selects samples of one operation. An empty Size or Version matches any.
type Query struct {
	Op      string
	Size    string
	Version string
	R       Range
}

// PhaseStat is the time one phase took, over the samples that had it.
type PhaseStat struct {
	Phase    string
	N        int
	P50, P95 float64
	// Share is the phase's part of the sum of the phases' medians, 0 to 1.
	Share float64
}

// Stat is what a query found.
type Stat struct {
	// N counts the samples that ended ok, and they alone give the durations.
	N         int
	Errors    int
	Cancelled int
	P50, P95  float64
	P99       float64
	Max, Mean float64
	// Phases are sorted by median, longest first. Slowest are the slowest
	// samples, slowest first, at most Limit of them.
	Phases  []PhaseStat
	Slowest []perfstore.Entry
}

// Total is every sample of the query, whatever its result.
func (s Stat) Total() int { return s.N + s.Errors + s.Cancelled }

func (m *Model) collect(q Query, fn func(b *bucket)) {
	od := m.ops[q.Op]
	if od == nil {
		return
	}
	span := int64(q.R.To) - int64(q.R.From) + 1
	if span <= int64(len(od.days)) {
		for d := q.R.From; d <= q.R.To; d++ {
			for _, b := range od.days[d] {
				if (q.Size == "" || b.size == q.Size) && (q.Version == "" || b.version == q.Version) {
					fn(b)
				}
			}
		}
		return
	}
	for d, bs := range od.days {
		if d < q.R.From || d > q.R.To {
			continue
		}
		for _, b := range bs {
			if (q.Size == "" || b.size == q.Size) && (q.Version == "" || b.version == q.Version) {
				fn(b)
			}
		}
	}
}

// Values returns the durations of the ok samples of q, sorted.
func (m *Model) Values(q Query) []float64 {
	var vs []float64
	m.collect(q, func(b *bucket) {
		for _, v := range b.ms {
			vs = append(vs, float64(v))
		}
	})
	sort.Float64s(vs)
	return vs
}

// Stat computes the numbers of q. limit caps the slowest samples it returns.
func (m *Model) Stat(q Query, limit int) Stat {
	var st Stat
	var vs []float64
	phases := map[string][]float64{}
	var slow []perfstore.Entry
	sum := 0.0
	m.collect(q, func(b *bucket) {
		st.Errors += b.errors
		st.Cancelled += b.cancelled
		for _, v := range b.ms {
			vs = append(vs, float64(v))
			sum += float64(v)
		}
		for name, pv := range b.phases {
			for _, v := range pv {
				phases[name] = append(phases[name], float64(v))
			}
		}
		slow = append(slow, b.slow...)
	})
	st.N = len(vs)
	if st.N > 0 {
		sort.Float64s(vs)
		st.P50, st.P95, st.P99 = Percentile(vs, 50), Percentile(vs, 95), Percentile(vs, 99)
		st.Max, st.Mean = vs[len(vs)-1], sum/float64(len(vs))
	}
	var share float64
	for name, pv := range phases {
		sort.Float64s(pv)
		ps := PhaseStat{Phase: name, N: len(pv), P50: Percentile(pv, 50), P95: Percentile(pv, 95)}
		share += ps.P50
		st.Phases = append(st.Phases, ps)
	}
	for i := range st.Phases {
		if share > 0 {
			st.Phases[i].Share = st.Phases[i].P50 / share
		}
	}
	sort.Slice(st.Phases, func(i, j int) bool {
		if st.Phases[i].P50 != st.Phases[j].P50 {
			return st.Phases[i].P50 > st.Phases[j].P50
		}
		return st.Phases[i].Phase < st.Phases[j].Phase
	})
	sort.SliceStable(slow, func(i, j int) bool {
		if slow[i].Ms != slow[j].Ms {
			return slow[i].Ms > slow[j].Ms
		}
		return slow[i].T.Before(slow[j].T)
	})
	if limit >= 0 && len(slow) > limit {
		slow = slow[:limit]
	}
	st.Slowest = slow
	return st
}

// Group is a Stat for one value of a dimension.
type Group struct {
	Key string
	Stat
}

// ByVersion splits q (whose Version it ignores) by app version, in the order
// the versions first appeared.
func (m *Model) ByVersion(q Query) []Group {
	var out []Group
	for _, v := range m.Versions() {
		q.Version = v.Version
		if st := m.Stat(q, 0); st.Total() > 0 {
			out = append(out, Group{v.Version, st})
		}
	}
	return out
}

// BySize splits q (whose Size it ignores) by size bucket, smallest first.
func (m *Model) BySize(q Query) []Group {
	var out []Group
	for _, s := range m.Sizes() {
		q.Size = s
		if st := m.Stat(q, 0); st.Total() > 0 {
			out = append(out, Group{s, st})
		}
	}
	return out
}
