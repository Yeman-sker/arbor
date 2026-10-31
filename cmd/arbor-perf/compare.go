package main

import (
	"fmt"
	"math"
	"sort"
)

// Options are the rules of the ratchet. They are constants of the process, not
// knobs for a single comparison: loosening one is a reviewed change.
type Options struct {
	// Threshold is the relative slowdown that counts as a regression.
	Threshold float64
	// NoiseK is how many robust standard deviations of run-to-run noise a change
	// must exceed. Noise is the larger of the baseline's and the new runs'.
	NoiseK float64
	// MinImprove is the smallest relative speed-up that tightens the baseline.
	MinImprove float64
	// MinRuns is the fewest runs of a benchmark that can be compared.
	MinRuns int
}

// DefaultOptions: a regression is more than 10% slower and beyond two sigma of
// the measured noise. Allocation counts compare exactly: any median above every
// baseline run is a regression. (Benchmarks that start processes allocate a few
// more or fewer objects from run to run; the baseline's own spread is the only
// allowance, and a benchmark whose baseline runs agree has none.)
var DefaultOptions = Options{Threshold: 0.10, NoiseK: 2, MinImprove: 0.03, MinRuns: 5}

// Status is the verdict on one benchmark.
type Status int

const (
	StatusOK Status = iota
	StatusImproved
	StatusRegression
	StatusAllocRegression
	StatusNew
	StatusMissing
	StatusTooFewRuns
)

func (s Status) String() string {
	return [...]string{"ok", "improved", "REGRESSION", "ALLOC REGRESSION", "new", "MISSING", "TOO FEW RUNS"}[s]
}

// Failed reports whether the status fails a comparison.
func (s Status) Failed() bool {
	return s == StatusRegression || s == StatusAllocRegression || s == StatusMissing || s == StatusTooFewRuns
}

// Result is the comparison of one benchmark with its baseline.
type Result struct {
	Name       string
	Status     Status
	Base, Cur  float64 // median ns/op
	Delta      float64 // (Cur-Base)/Base
	Noise      float64 // k*sigma relative to Base: a change smaller than this is noise
	AllocsBase float64
	AllocsCur  float64
	BytesBase  float64
	BytesCur   float64
	Note       string

	// Tighten says which parts of the baseline the new runs improve.
	TightenTime, TightenAllocs bool
}

// Median of xs; 0 for none.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// Sigma is the robust standard deviation of xs: 1.4826 times the median
// absolute deviation, which one stray run barely moves.
func Sigma(xs []float64) float64 {
	m := Median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - m)
	}
	return 1.4826 * Median(dev)
}

// Compare judges the current runs against the baseline. Benchmarks only in the
// baseline are Missing (a deleted benchmark must not hide a regression) unless
// partial is set, for a run that was limited to some benchmarks.
func Compare(base, cur map[string]Series, partial bool, o Options) []Result {
	seen := map[string]bool{}
	var out []Result
	for _, name := range Names(cur) {
		seen[name] = true
		c := cur[name]
		b, ok := base[name]
		if !ok {
			out = append(out, Result{Name: name, Status: StatusNew, Cur: Median(c.Ns), AllocsCur: Median(c.Allocs), BytesCur: Median(c.Bytes)})
			continue
		}
		out = append(out, compareOne(name, b, c, o))
	}
	if !partial {
		for _, name := range Names(base) {
			if !seen[name] {
				out = append(out, Result{Name: name, Status: StatusMissing, Base: Median(base[name].Ns), Note: "in the baseline but not in this run"})
			}
		}
	}
	return out
}

func compareOne(name string, b, c Series, o Options) Result {
	r := Result{
		Name: name, Base: Median(b.Ns), Cur: Median(c.Ns),
		AllocsBase: Median(b.Allocs), AllocsCur: Median(c.Allocs),
		BytesBase: Median(b.Bytes), BytesCur: Median(c.Bytes),
	}
	if len(c.Ns) < o.MinRuns {
		r.Status = StatusTooFewRuns
		r.Note = fmt.Sprintf("%d runs, need %d", len(c.Ns), o.MinRuns)
		return r
	}
	if r.Base > 0 {
		r.Delta = (r.Cur - r.Base) / r.Base
	}
	noiseAbs := o.NoiseK * math.Max(Sigma(b.Ns), Sigma(c.Ns))
	if r.Base > 0 {
		r.Noise = noiseAbs / r.Base
	}
	diff := r.Cur - r.Base
	hasAllocs := len(b.Allocs) > 0 && len(c.Allocs) > 0

	switch {
	case hasAllocs && r.AllocsCur > maxOf(b.Allocs):
		r.Status = StatusAllocRegression
		r.Note = fmt.Sprintf("allocs/op %v -> %v", r.AllocsBase, r.AllocsCur)
	case r.Delta > o.Threshold && diff > noiseAbs:
		r.Status = StatusRegression
	default:
		r.Status = StatusOK
		switch {
		case r.Delta > o.Threshold:
			r.Note = "slower, but within noise"
		case r.Delta > 0:
			r.Note = fmt.Sprintf("within %.0f%%", o.Threshold*100)
		}
	}
	if r.Status.Failed() {
		return r
	}
	if -diff > noiseAbs && -r.Delta >= o.MinImprove {
		r.TightenTime = true
	}
	if hasAllocs && r.AllocsCur < minOf(b.Allocs) {
		r.TightenAllocs = true
	}
	if r.TightenTime || r.TightenAllocs {
		r.Status = StatusImproved
		r.Note = ""
	}
	return r
}

func maxOf(xs []float64) float64 {
	m := math.Inf(-1)
	for _, x := range xs {
		m = math.Max(m, x)
	}
	return m
}

func minOf(xs []float64) float64 {
	m := math.Inf(1)
	for _, x := range xs {
		m = math.Min(m, x)
	}
	return m
}

// Failed reports whether any result fails the comparison.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Status.Failed() {
			return true
		}
	}
	return false
}

// Tighten returns the baseline with every improvement locked in and new
// benchmarks added. It never loosens: regressions and unchanged benchmarks keep
// their recorded runs. The input is not modified.
func Tighten(base, cur map[string]Series, rs []Result) (updated map[string]Series, changed bool) {
	updated = make(map[string]Series, len(base))
	for k, v := range base {
		updated[k] = v
	}
	for _, r := range rs {
		c := cur[r.Name]
		switch {
		case r.Status == StatusNew:
			updated[r.Name] = c
			changed = true
		case r.Status == StatusImproved:
			s := updated[r.Name]
			if r.TightenTime {
				s.Ns = c.Ns
				changed = true
			}
			if r.TightenAllocs {
				s.Allocs, s.Bytes = c.Allocs, c.Bytes
				changed = true
			}
			updated[r.Name] = s
		}
	}
	return updated, changed
}
