package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Unix(1_700_000_000, 0)

const sampleOutput = `goos: darwin
goarch: arm64
pkg: arbor
cpu: Apple M4
BenchmarkLoadRepo/small-10         	      30	  43700000 ns/op	  819704 B/op	    3977 allocs/op
BenchmarkLoadRepo/small-10         	      30	  43900000 ns/op	  819700 B/op	    3977 allocs/op
BenchmarkComputeGraph/large-10     	    8000	    147972 ns/op	  134520 B/op	    1914 allocs/op
BenchmarkNoMem-10                  	    1000	      1500 ns/op
some log line from a benchmark
--- BENCH: BenchmarkFoo-10
    foo_test.go:12: not a result
PASS
ok  	arbor	11.590s
`

func TestParseBench(t *testing.T) {
	out, err := ParseBench(strings.NewReader(sampleOutput))
	if err != nil {
		t.Fatal(err)
	}
	if out.GOOS != "darwin" || out.GOARCH != "arm64" || out.CPU != "Apple M4" {
		t.Errorf("meta = %+v", out)
	}
	lr := out.Benchmarks["LoadRepo/small"]
	if len(lr.Ns) != 2 || lr.Ns[1] != 43900000 || lr.Allocs[0] != 3977 || lr.Bytes[0] != 819704 {
		t.Errorf("LoadRepo/small = %+v", lr)
	}
	if _, ok := out.Benchmarks["ComputeGraph/large"]; !ok {
		t.Errorf("suffix -10 not stripped: %v", Names(out.Benchmarks))
	}
	if nm := out.Benchmarks["NoMem"]; len(nm.Ns) != 1 || len(nm.Allocs) != 0 {
		t.Errorf("NoMem = %+v", nm)
	}
	if len(out.Benchmarks) != 3 {
		t.Errorf("benchmarks = %v", Names(out.Benchmarks))
	}
}

func TestMedianAndSigma(t *testing.T) {
	if got := Median([]float64{5, 1, 3}); got != 3 {
		t.Errorf("median = %v", got)
	}
	if got := Median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("even median = %v", got)
	}
	if got := Sigma([]float64{10, 10, 10, 10, 10}); got != 0 {
		t.Errorf("sigma of constants = %v", got)
	}
	// One stray run barely moves the robust spread.
	if got := Sigma([]float64{100, 101, 99, 100, 5000}); got > 2 {
		t.Errorf("sigma = %v, an outlier must not count", got)
	}
}

func s(ns []float64, bytes, allocs float64) Series {
	out := Series{Ns: ns}
	for range ns {
		out.Bytes = append(out.Bytes, bytes)
		out.Allocs = append(out.Allocs, allocs)
	}
	return out
}

func one(t *testing.T, base, cur Series) Result {
	t.Helper()
	rs := Compare(map[string]Series{"B": base}, map[string]Series{"B": cur}, false, DefaultOptions)
	if len(rs) != 1 {
		t.Fatalf("results = %+v", rs)
	}
	return rs[0]
}

var steady = []float64{100, 101, 99, 100, 100}

func TestRegressionBeyondThresholdAndNoise(t *testing.T) {
	r := one(t, s(steady, 10, 5), s([]float64{120, 121, 119, 120, 120}, 10, 5))
	if r.Status != StatusRegression || !r.Status.Failed() {
		t.Fatalf("status = %v (%+v)", r.Status, r)
	}
	if r.Delta < 0.19 || r.Delta > 0.21 {
		t.Errorf("delta = %v", r.Delta)
	}
}

func TestSlowerWithinNoiseIsNotARegression(t *testing.T) {
	noisy := []float64{100, 135, 85, 120, 105}
	r := one(t, s(noisy, 10, 5), s([]float64{118, 150, 100, 135, 120}, 10, 5))
	if r.Status != StatusOK {
		t.Fatalf("status = %v, want ok: a 15%% shift inside the noise (%+v)", r.Status, r)
	}
	if r.Note == "" {
		t.Error("expected a note that the change is within noise")
	}
}

func TestSlowerWithinThresholdIsNotARegression(t *testing.T) {
	r := one(t, s(steady, 10, 5), s([]float64{108, 108, 108, 108, 108}, 10, 5))
	if r.Status != StatusOK {
		t.Fatalf("status = %v, want ok: 8%% is under the 10%% threshold", r.Status)
	}
}

func TestImprovementTightensTheBaseline(t *testing.T) {
	base := map[string]Series{"B": s(steady, 10, 5), "Other": s(steady, 1, 1)}
	faster := s([]float64{80, 81, 79, 80, 80}, 10, 5)
	cur := map[string]Series{"B": faster, "Other": s(steady, 1, 1)}
	rs := Compare(base, cur, false, DefaultOptions)
	if Failed(rs) {
		t.Fatalf("improvement failed: %+v", rs)
	}
	var b Result
	for _, r := range rs {
		if r.Name == "B" {
			b = r
		}
	}
	if b.Status != StatusImproved || !b.TightenTime {
		t.Fatalf("B = %+v", b)
	}
	updated, changed := Tighten(base, cur, rs)
	if !changed || Median(updated["B"].Ns) != 80 {
		t.Fatalf("baseline not tightened: %+v", updated["B"])
	}
	if Median(updated["Other"].Ns) != 100 {
		t.Fatalf("an unchanged benchmark was rewritten: %+v", updated["Other"])
	}
	if Median(base["B"].Ns) != 100 {
		t.Fatal("Tighten modified its input")
	}
}

func TestTightenNeverLoosens(t *testing.T) {
	base := map[string]Series{"B": s(steady, 10, 5)}
	cur := map[string]Series{"B": s([]float64{150, 150, 150, 150, 150}, 10, 5)}
	rs := Compare(base, cur, false, DefaultOptions)
	updated, changed := Tighten(base, cur, rs)
	if changed || Median(updated["B"].Ns) != 100 {
		t.Fatalf("a regression changed the baseline: %+v", updated["B"])
	}
}

func TestSmallSpeedupBelowMinimumDoesNotTighten(t *testing.T) {
	r := one(t, s(steady, 10, 5), s([]float64{99, 99, 99, 99, 99}, 10, 5))
	if r.Status != StatusOK || r.TightenTime {
		t.Fatalf("1%% speed-up tightened: %+v", r)
	}
}

func TestAllocationCountsCompareExactly(t *testing.T) {
	// Same speed, one more allocation.
	r := one(t, s(steady, 10, 5), s(steady, 10, 6))
	if r.Status != StatusAllocRegression || !r.Status.Failed() {
		t.Fatalf("status = %v, want ALLOC REGRESSION", r.Status)
	}
	// Faster but with an extra allocation is still a failure, and tightens nothing.
	base := map[string]Series{"B": s(steady, 10, 5)}
	cur := map[string]Series{"B": s([]float64{70, 70, 70, 70, 70}, 10, 6)}
	rs := Compare(base, cur, false, DefaultOptions)
	if !Failed(rs) {
		t.Fatal("extra allocation passed")
	}
	if _, changed := Tighten(base, cur, rs); changed {
		t.Fatal("a failing comparison tightened the baseline")
	}
	// Fewer allocations tighten the allocation baseline even at the same speed.
	r = one(t, s(steady, 10, 5), s(steady, 8, 4))
	if r.Status != StatusImproved || !r.TightenAllocs || r.TightenTime {
		t.Fatalf("fewer allocs: %+v", r)
	}
	base = map[string]Series{"B": s(steady, 10, 5)}
	cur = map[string]Series{"B": s(steady, 8, 4)}
	updated, _ := Tighten(base, cur, Compare(base, cur, false, DefaultOptions))
	if Median(updated["B"].Allocs) != 4 || Median(updated["B"].Ns) != 100 {
		t.Fatalf("updated = %+v", updated["B"])
	}
}

// Benchmarks that start processes allocate a few objects more or fewer from run
// to run: the baseline's own spread is the allowance, and nothing more.
func TestAllocationSpreadOfTheBaselineIsTheOnlyAllowance(t *testing.T) {
	base := Series{Ns: steady, Allocs: []float64{4288, 4288, 4290, 4288, 4288}}
	within := Series{Ns: steady, Allocs: []float64{4290, 4290, 4290, 4290, 4290}}
	if r := one(t, base, within); r.Status.Failed() {
		t.Fatalf("a median equal to the baseline's highest run failed: %+v", r)
	}
	beyond := Series{Ns: steady, Allocs: []float64{4291, 4291, 4291, 4291, 4291}}
	if r := one(t, base, beyond); r.Status != StatusAllocRegression {
		t.Fatalf("one allocation beyond the baseline's spread: %+v", r)
	}
	fewer := Series{Ns: steady, Allocs: []float64{4200, 4200, 4200, 4200, 4200}}
	if r := one(t, base, fewer); !r.TightenAllocs {
		t.Fatalf("fewer allocations did not tighten: %+v", r)
	}
}

func TestNewAndMissingBenchmarks(t *testing.T) {
	base := map[string]Series{"Old": s(steady, 1, 1), "Both": s(steady, 1, 1)}
	cur := map[string]Series{"New": s(steady, 1, 1), "Both": s(steady, 1, 1)}
	rs := Compare(base, cur, false, DefaultOptions)
	got := map[string]Status{}
	for _, r := range rs {
		got[r.Name] = r.Status
	}
	if got["New"] != StatusNew || got["Old"] != StatusMissing || got["Both"] != StatusOK {
		t.Fatalf("statuses = %v", got)
	}
	if !Failed(rs) {
		t.Fatal("a deleted benchmark must fail the comparison")
	}
	// A run limited to some benchmarks does not report the rest missing.
	if Failed(Compare(base, map[string]Series{"Both": s(steady, 1, 1)}, true, DefaultOptions)) {
		t.Fatal("partial run reported missing benchmarks")
	}
	updated, changed := Tighten(base, cur, rs)
	if !changed || len(updated["New"].Ns) != 5 || len(updated["Old"].Ns) != 5 {
		t.Fatalf("updated = %v", Names(updated))
	}
}

// A slow stretch of a shared machine slows the first run; the rerun is the
// evidence. A real slowdown shows again.
func TestConfirmClearsASlowStretchAndKeepsARealRegression(t *testing.T) {
	base := map[string]Series{"Stretch/x": s(steady, 10, 5), "Real/x": s(steady, 10, 5), "Fine/x": s(steady, 10, 5)}
	slow := s([]float64{150, 150, 150, 150, 150}, 10, 5)
	cur := map[string]Series{"Stretch/x": slow, "Real/x": slow, "Fine/x": s(steady, 10, 5)}
	rs := Compare(base, cur, false, DefaultOptions)
	if !Failed(rs) {
		t.Fatal("the slow runs were not flagged to begin with")
	}

	var asked [][]string
	rerun := func(names []string) (map[string]Series, error) {
		asked = append(asked, names)
		return map[string]Series{
			"Real/x":    s([]float64{148, 149, 150, 151, 152}, 10, 5),
			"Stretch/x": s(steady, 10, 5), // the machine is quiet again
		}, nil
	}
	final, judged, err := Confirm(base, cur, rs, rerun, 2, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Result{}
	for _, r := range final {
		got[r.Name] = r
	}
	if got["Stretch/x"].Status.Failed() || got["Stretch/x"].Note == "" {
		t.Errorf("a slowdown that did not reproduce failed: %+v", got["Stretch/x"])
	}
	if got["Real/x"].Status != StatusRegression || got["Real/x"].Reruns != 2 {
		t.Errorf("a slowdown that reproduced was cleared: %+v", got["Real/x"])
	}
	if got["Fine/x"].Reruns != 0 {
		t.Errorf("a benchmark that was fine was run again: %+v", got["Fine/x"])
	}
	if len(asked) != 2 || len(asked[1]) != 1 || asked[1][0] != "Real/x" {
		t.Errorf("rounds asked for %v: only what still regressed should be run again", asked)
	}
	if Median(judged["Stretch/x"].Ns) != 100 || Median(cur["Stretch/x"].Ns) != 150 {
		t.Errorf("judged runs %v, inputs must not change", judged["Stretch/x"].Ns)
	}
	if !Failed(final) {
		t.Error("the real regression must still fail the comparison")
	}
}

// A rerun that finds nothing (a wrong pattern) must not pass as "not reproduced".
func TestConfirmFailsLoudlyWhenTheRerunProducesNothing(t *testing.T) {
	base := map[string]Series{"A": s(steady, 10, 5)}
	cur := map[string]Series{"A": s([]float64{150, 150, 150, 150, 150}, 10, 5)}
	rs := Compare(base, cur, false, DefaultOptions)
	_, _, err := Confirm(base, cur, rs, func([]string) (map[string]Series, error) { return nil, nil }, 2, DefaultOptions)
	if err == nil {
		t.Fatal("an empty rerun was accepted")
	}
}

func TestConfirmDoesNotRerunAllocationRegressions(t *testing.T) {
	base := map[string]Series{"A": s(steady, 10, 5)}
	cur := map[string]Series{"A": s(steady, 10, 6)}
	rs := Compare(base, cur, false, DefaultOptions)
	called := false
	final, _, err := Confirm(base, cur, rs, func([]string) (map[string]Series, error) { called = true; return nil, nil }, 2, DefaultOptions)
	if err != nil || called || final[0].Status != StatusAllocRegression {
		t.Fatalf("called=%v err=%v final=%+v", called, err, final)
	}
}

func TestNoisyFlagsABaselineRecordedOnABusyMachine(t *testing.T) {
	m := map[string]Series{
		"Quiet": s([]float64{100, 101, 99, 100, 102}, 1, 1),
		"Busy":  s([]float64{108, 92, 74, 66, 65, 64, 61}, 1, 1), // a machine that calmed down
	}
	if got := Noisy(m, NoisyLimit); len(got) != 1 || got[0] != "Busy" {
		t.Fatalf("noisy = %v", got)
	}
}

func TestFamiliesNamesEachParentOnce(t *testing.T) {
	got := families([]string{"Frame/search/large", "GitStatus/small", "Frame/git/small", "NewApp"})
	if strings.Join(got, ",") != "Frame,GitStatus,NewApp" {
		t.Fatalf("families = %v", got)
	}
}

func TestTooFewRunsCannotBeCompared(t *testing.T) {
	r := one(t, s(steady, 1, 1), s([]float64{100, 100, 100}, 1, 1))
	if r.Status != StatusTooFewRuns || !r.Status.Failed() {
		t.Fatalf("status = %v", r.Status)
	}
}

func TestMachineKeyHasNoHostOrUser(t *testing.T) {
	if got := MachineKey("Apple M4", "darwin", "arm64"); got != "apple-m4-darwin-arm64" {
		t.Errorf("key = %q", got)
	}
	if got := MachineKey("Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz", "linux", "amd64"); got != "intel-r-core-tm-i7-9750h-cpu-2-60ghz-linux-amd64" {
		t.Errorf("key = %q", got)
	}
	if got := MachineKey("", "linux", "arm64"); got != "unknown-cpu-linux-arm64" {
		t.Errorf("key = %q", got)
	}
	host, _ := os.Hostname()
	if host != "" && strings.Contains(DetectMachine().Key, strings.ToLower(host)) {
		t.Errorf("machine key %q contains the host name", DetectMachine().Key)
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".perf", "baselines", "x.json")
	in := newBaseline(Machine{Key: "x", CPU: "cpu", GOOS: "darwin", Arch: "arm64"}, 1, map[string]Series{"B": s(steady, 10, 5)}, testNow)
	if err := SaveBaseline(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Machine.Key != "x" || Median(out.Benchmarks["B"].Allocs) != 5 {
		t.Fatalf("loaded = %+v", out)
	}
	if _, err := LoadBaseline(filepath.Join(t.TempDir(), "none.json")); !os.IsNotExist(err) {
		t.Fatalf("missing file error = %v", err)
	}
}

// TestRecordCompareRoundTrip drives the commands with canned go test output.
func TestRecordCompareRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchLines := func(ns float64, allocs int) string {
		var b strings.Builder
		b.WriteString("goos: x\ngoarch: y\ncpu: z\n")
		for range 5 {
			b.WriteString("BenchmarkThing-8  100  " + strconv.FormatFloat(ns, 'f', -1, 64) + " ns/op  64 B/op  " + strconv.Itoa(allocs) + " allocs/op\n")
		}
		return b.String() + "PASS\n"
	}
	write := func(name, text string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	runCmd := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(append(args, "-root", root), strings.NewReader(""), &out, &errb)
		return code, out.String() + errb.String()
	}

	if code, out := runCmd("compare", "-input", write("a.txt", benchLines(1000, 10))); code != 2 || !strings.Contains(out, "no baseline") {
		t.Fatalf("compare without baseline = %d\n%s", code, out)
	}
	if code, out := runCmd("record", "-input", write("a.txt", benchLines(1000, 10))); code != 0 {
		t.Fatalf("record = %d\n%s", code, out)
	}
	if code, out := runCmd("compare", "-input", write("same.txt", benchLines(1005, 10))); code != 0 {
		t.Fatalf("same = %d\n%s", code, out)
	}
	if code, out := runCmd("compare", "-input", write("slow.txt", benchLines(1500, 10))); code != 1 || !strings.Contains(out, "REGRESSION") {
		t.Fatalf("slow = %d\n%s", code, out)
	}
	if code, out := runCmd("compare", "-input", write("alloc.txt", benchLines(1000, 11))); code != 1 || !strings.Contains(out, "ALLOC REGRESSION") {
		t.Fatalf("alloc = %d\n%s", code, out)
	}
	// An improvement passes and tightens, so the old speed is now a regression.
	if code, out := runCmd("compare", "-input", write("fast.txt", benchLines(500, 10))); code != 0 || !strings.Contains(out, "tightened") {
		t.Fatalf("fast = %d\n%s", code, out)
	}
	if code, out := runCmd("compare", "-input", write("old.txt", benchLines(1000, 10))); code != 1 {
		t.Fatalf("old speed after tightening = %d\n%s", code, out)
	}
	if code, _ := runCmd("record", "-count", "3", "-input", write("a.txt", benchLines(1000, 10))); code != 2 {
		t.Fatalf("record with -count 3 = %d, want a usage error", code)
	}
}
