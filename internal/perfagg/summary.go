package perfagg

import (
	"arbor/internal/perfstore"
	"sort"
	"time"
)

// VersionPoint is one version on a chart: how an operation did on that build.
type VersionPoint struct {
	Version string
	// First is when the version first appeared in the log.
	First time.Time
	// N is how many ok samples it has. With none there is nothing to draw, and
	// with fewer than MinSamples (Thin) the numbers are not evidence yet.
	N        int
	P50, P95 float64
	Thin     bool
}

// Filter says which part of the data a summary covers.
type Filter struct {
	// Size is small, medium or large; "" for all.
	Size string
	// Last is how many of the latest versions to cover; 0 for all of them.
	Last int
}

// Summary is everything a card of the Performance window shows about one
// operation: a point per version, and the latest version with samples in detail.
type Summary struct {
	Op     string
	Points []VersionPoint

	// Version is the latest version with samples, and the fields below
	// describe it. Slowest and Phases exist to explain the number on the card.
	Version              string
	N, Errors, Cancelled int
	Unseen               int
	P50, P95, P99, Max   float64
	Phases               []PhaseStat
	Slowest              []perfstore.Entry

	// Current is the median of the latest version with at least MinSamples
	// samples, else of the latest with any. Verdict compares it with the first
	// version with at least MinSamples, BaseVersion, and Change is the move as a
	// fraction (-0.5 is half as slow), set when the verdict is faster or slower.
	Current     float64
	CurrentFrom string
	BaseVersion string
	Verdict     string
	Change      float64
	HasChange   bool
}

// Summarize computes the card of op for f.
func (m *Model) Summarize(op string, f Filter) Summary {
	versions := m.Versions()
	if f.Last > 0 && len(versions) > f.Last {
		versions = versions[len(versions)-f.Last:]
	}
	s := Summary{Op: op, Verdict: VerdictThin}
	var firstGood, lastGood *VersionPoint
	for _, v := range versions {
		st := m.Stat(Query{Op: op, Size: f.Size, Version: v.Version, R: AllTime}, 0)
		s.Points = append(s.Points, VersionPoint{
			Version: v.Version, First: v.First, N: st.N, P50: st.P50, P95: st.P95, Thin: st.N < MinSamples,
		})
	}
	for i := range s.Points {
		p := &s.Points[i]
		if p.N == 0 {
			continue
		}
		s.Version = p.Version
		if !p.Thin {
			if firstGood == nil {
				firstGood = p
			}
			lastGood = p
		}
	}
	if s.Version == "" {
		return s
	}
	st := m.Stat(Query{Op: op, Size: f.Size, Version: s.Version, R: AllTime}, 10)
	s.N, s.Errors, s.Cancelled, s.Unseen = st.N, st.Errors, st.Cancelled, st.Unseen
	s.P50, s.P95, s.P99, s.Max = st.P50, st.P95, st.P99, st.Max
	s.Phases, s.Slowest = st.Phases, st.Slowest
	s.Current, s.CurrentFrom = st.P50, s.Version
	if lastGood != nil {
		s.Current, s.CurrentFrom = lastGood.P50, lastGood.Version
	}
	if firstGood != nil && firstGood != lastGood {
		s.BaseVersion = firstGood.Version
		s.Verdict, s.Change = m.CompareVersions(op, f.Size, firstGood.Version, lastGood.Version)
		s.HasChange = s.Verdict == VerdictFaster || s.Verdict == VerdictSlower
	}
	return s
}

// CompareVersions judges op on version later against version earlier. Over all
// sizes it only speaks when both ran on the same one size: with a mix, a version
// can look faster for having opened smaller repositories.
func (m *Model) CompareVersions(op, size, earlier, later string) (string, float64) {
	if size == "" {
		a := m.BySize(Query{Op: op, Version: earlier, R: AllTime})
		b := m.BySize(Query{Op: op, Version: later, R: AllTime})
		if len(a) != 1 || len(b) != 1 || a[0].Key != b[0].Key {
			return VerdictMixed, 0
		}
	}
	q := Query{Op: op, Size: size, R: AllTime}
	q.Version = earlier
	before := m.Values(q)
	q.Version = later
	return Judge(before, m.Values(q))
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
