package main

import (
	"arbor/internal/fixture"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"arbor/perf"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func series(ns ...float64) Series { return Series{Ns: ns} }

func TestComparePairNeedsAnIntervalAndEffect(t *testing.T) {
	base := series(100, 102, 98, 101, 99, 100, 102, 98)
	if p := ComparePair("x", base, series(80, 82, 78, 81, 79, 80, 82, 78)); p.Verdict != PairFaster || p.DeltaPct > -15 {
		t.Fatalf("faster = %+v", p)
	}
	if p := ComparePair("x", base, series(125, 128, 122, 126, 124, 125, 128, 122)); p.Verdict != PairSlower {
		t.Fatalf("slower = %+v", p)
	}
	// The same build: small differences both ways.
	if p := ComparePair("x", base, series(101, 101, 99, 100, 100, 99, 103, 97)); p.Verdict != PairFlat {
		t.Fatalf("same = %+v", p)
	}
	// A consistent 2% is real, and too small to act on.
	if p := ComparePair("x", base, series(98, 100, 96, 99, 97, 98, 100, 96)); p.Verdict != PairFlat {
		t.Fatalf("2%% = %+v", p)
	}
	// Runs all over the place say nothing.
	if p := ComparePair("x", base, series(60, 150, 70, 160, 90, 170, 55, 140)); p.Verdict != PairInconclusive {
		t.Fatalf("noisy = %+v", p)
	}
	// Five pairs cannot give a 95% interval.
	if p := ComparePair("x", series(100, 100, 100, 100, 100), series(70, 70, 70, 70, 70)); p.Verdict != PairInconclusive {
		t.Fatalf("too few = %+v", p)
	}
	// At the usual twelve pairs the interval tolerates a stray pair, and not several.
	base12 := series(100, 102, 98, 101, 99, 100, 102, 98, 101, 99, 100, 102)
	if p := ComparePair("x", base12, series(80, 82, 120, 81, 79, 80, 82, 78, 81, 79, 80, 82)); p.Verdict != PairFaster {
		t.Fatalf("one stray = %+v", p)
	}
	if p := ComparePair("x", base12, series(80, 110, 105, 81, 112, 80, 108, 78, 115, 79, 104, 82)); p.Verdict == PairFaster {
		t.Fatalf("many strays = %+v", p)
	}
	withAllocs := func(a float64) Series {
		return Series{Ns: []float64{100, 100, 100, 100, 100}, Allocs: []float64{a, a, a, a, a}}
	}
	if p := ComparePair("x", withAllocs(10), withAllocs(11)); !p.AllocRegression {
		t.Fatalf("allocs = %+v", p)
	}
}

// fakeRuns answers runs of the builds a and b from ns, which is told the build,
// the benchmark's pattern and how many runs of it came before.
func fakeRuns(t *testing.T, ns func(build, pattern string, nth int) float64) *[]string {
	t.Helper()
	old := runBinary
	t.Cleanup(func() { runBinary = old })
	var order []string
	counts := map[string]int{}
	runBinary = func(b binary, r benchRun, _ io.Writer) (Output, error) {
		if r.benchtime == "1x" {
			return ParseBench(strings.NewReader("BenchmarkA/x-8 1 1 ns/op\nBenchmarkA/y-8 1 1 ns/op\n"))
		}
		order = append(order, b.path+" "+r.pattern)
		key := b.path + r.pattern
		v := ns(b.path, r.pattern, counts[key])
		counts[key]++
		name := strings.NewReplacer("^", "", "$", "", "Benchmark", "").Replace(r.pattern)
		return ParseBench(strings.NewReader(fmt.Sprintf("Benchmark%s-8 10 %f ns/op\n", name, v)))
	}
	return &order
}

func TestTurnsAreTakenOnEachBenchmark(t *testing.T) {
	order := fakeRuns(t, func(string, string, int) float64 { return 100 })
	if _, _, err := interleave(binary{path: "a"}, binary{path: "b"}, benchRun{pattern: "."}, 2, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a ^BenchmarkA$/^x$", "b ^BenchmarkA$/^x$", "a ^BenchmarkA$/^y$", "b ^BenchmarkA$/^y$",
		"b ^BenchmarkA$/^x$", "a ^BenchmarkA$/^x$", "b ^BenchmarkA$/^y$", "a ^BenchmarkA$/^y$",
	}
	if !slices.Equal(*order, want) {
		t.Fatalf("order = %v", *order)
	}
}

func TestAChangeHasToShowTwice(t *testing.T) {
	const rounds = 8
	fakeRuns(t, func(build, pattern string, nth int) float64 {
		if build != "b" {
			return 100
		}
		switch {
		case strings.Contains(pattern, "x"): // slower for good
			return 130
		case nth < rounds: // only by chance
			return 130
		}
		return 100
	})
	pairs, err := measurePairs(binary{path: "a"}, binary{path: "b"}, benchRun{pattern: "."}, rounds, io.Discard)
	if err != nil || len(pairs) != 2 {
		t.Fatalf("pairs = %+v, %v", pairs, err)
	}
	if pairs[0].Verdict != PairSlower || pairs[0].Rounds != 2*rounds {
		t.Fatalf("real = %+v", pairs[0])
	}
	if pairs[1].Verdict != PairInconclusive {
		t.Fatalf("by chance = %+v", pairs[1])
	}
}

func TestDecideFollowsTheRulesOfTheClimb(t *testing.T) {
	p := func(v string, delta float64) []Pair { return []Pair{{Name: "A/x", Verdict: v, DeltaPct: delta}} }
	cases := []struct {
		name                            string
		target, holdout, guards, others []Pair
		verdict, reason                 string
	}{
		{"both improved", p(PairFaster, -20), p(PairFaster, -18), p(PairFlat, 0), nil, perfstore.VerdictKeep, ReasonImproved},
		{"only the visible ones", p(PairFaster, -20), p(PairFlat, 1), nil, nil, perfstore.VerdictRevert, ReasonOverfit},
		{"held-out worse", p(PairFaster, -20), p(PairSlower, 8), nil, nil, perfstore.VerdictRevert, ReasonHoldoutWorse},
		{"another held-out worse", p(PairFaster, -20), p(PairFaster, -9), nil, p(PairSlower, 15), perfstore.VerdictRevert, ReasonHoldoutWorse},
		{"guard regressed", p(PairFaster, -20), p(PairFaster, -18), p(PairSlower, 15), nil, perfstore.VerdictRevert, ReasonRegression},
		{"guard a little slower holds", p(PairFaster, -20), p(PairFaster, -18), p(PairSlower, 6), nil, perfstore.VerdictKeep, ReasonImproved},
		{"target slower", p(PairSlower, 6), p(PairFlat, 0), nil, nil, perfstore.VerdictRevert, ReasonRegression},
		{"no effect", p(PairFlat, -1), p(PairFlat, 0), nil, nil, perfstore.VerdictRevert, ReasonNoEffect},
		{"too noisy to say", p(PairInconclusive, -3), p(PairFlat, 0), nil, nil, perfstore.VerdictInconclusive, ReasonNoisy},
		{"held-out too noisy", p(PairFaster, -20), p(PairInconclusive, -3), nil, nil, perfstore.VerdictInconclusive, ReasonHoldoutNoisy},
		{"no held-out", p(PairFaster, -20), nil, nil, nil, perfstore.VerdictInconclusive, ReasonNoHoldout},
		{"no target", nil, nil, nil, nil, perfstore.VerdictInconclusive, ReasonNoTarget},
	}
	for _, c := range cases {
		d, _ := decide(c.target, c.holdout, c.guards, c.others, DefaultOptions)
		if d.verdict != c.verdict || d.reason != c.reason {
			t.Errorf("%s: %s %s, want %s %s", c.name, d.verdict, d.reason, c.verdict, c.reason)
		}
	}
	alloc := []Pair{{Name: "G/x", Verdict: PairFlat, AllocRegression: true}}
	if d, regs := decide(p(PairFaster, -20), p(PairFaster, -18), alloc, nil, DefaultOptions); d.verdict != perfstore.VerdictRevert || len(regs) != 1 || regs[0] != "G/x allocates more" {
		t.Errorf("alloc guard: %+v %v", d, regs)
	}
}

func goodCheck(now time.Time) *CheckResult {
	return &CheckResult{OK: true, Fixture: fixture.Version, T: now.Format(time.RFC3339)}
}

func opReport(op string, n int, p50, p95, budget, score float64, flags ...string) perfreport.OpReport {
	return perfreport.OpReport{Op: op, Count: n, P50: p50, P95: p95, Budget: budget, Score: score, Flags: flags,
		Thin: n < perfreport.MinSamples, Target: perfreport.Targets[op]}
}

func TestARegressionSaysHowMuch(t *testing.T) {
	target := []Pair{{Name: "A/x", Verdict: PairFaster, DeltaPct: -20}}
	guards := []Pair{{Name: "B/x", Verdict: PairSlower, DeltaPct: 24.4}, {Name: "C/x", Verdict: PairFlat, AllocRegression: true}}
	_, got := decide(target, nil, guards, nil, DefaultOptions)
	if !slices.Equal(got, []string{"B/x +24%", "C/x allocates more"}) {
		t.Fatalf("regressions = %v", got)
	}
}

func TestDecideNext(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	report := func(ops ...perfreport.OpReport) *perfreport.Report { return &perfreport.Report{Ops: ops} }
	base := func() nextInput { return nextInput{Report: report(), Check: goodCheck(now), Now: now} }

	in := base()
	in.Check = nil
	if s := decideNext(in); s.Action != ActionValidate || s.Reason != "no_check" {
		t.Fatalf("no check = %+v", s)
	}
	in.Check = goodCheck(now)
	in.Check.OK, in.Check.Problems = false, []string{"X: an unchanged build shows faster"}
	if s := decideNext(in); s.Action != ActionValidate || s.Reason != "check_failed" || len(s.Problems) != 1 {
		t.Fatalf("failed check = %+v", s)
	}
	in.Check = goodCheck(now)
	in.Check.Fixture = fixture.Version + 1
	if s := decideNext(in); s.Reason != "fixture_changed" {
		t.Fatalf("other fixture = %+v", s)
	}
	in.Check = goodCheck(now.Add(-40 * 24 * time.Hour))
	if s := decideNext(in); s.Reason != "check_stale" {
		t.Fatalf("old check = %+v", s)
	}

	// A number that cannot be explained comes before anything over budget, and
	// the few samples that show it are enough.
	in = base()
	bad := opReport("external_change", 15, 320564, 633651, 1000, 9e6, perfreport.FlagThin, perfreport.FlagUnexplained)
	bad.Unexplained = 9
	in.Report = report(opReport("open_file", 100, 200, 400, 100, 3e4), bad)
	if s := decideNext(in); s.Action != ActionMeasure || s.Op != "external_change" || s.Evidence.Unexplained != 9 || len(s.Commands) != 1 {
		t.Fatalf("unexplained = %+v", s)
	}

	// Otherwise the worst trustworthy operation, with what is needed to act.
	in = base()
	thin := opReport("commit", 3, 900, 900, 300, 1800, perfreport.FlagThin)
	in.Report = report(opReport("open_file", 100, 200, 400, 100, 3e4), thin)
	in.Check.Unresolvable = []string{"OpenFile/large", "Frame/git/small"}
	in.Rounds = []perfstore.Round{{Kind: perfstore.RoundTry, Op: "open_file", Verdict: perfstore.VerdictRevert, Reason: ReasonNoEffect, Note: "cache the tree"}, {Kind: perfstore.RoundTry, Op: "search", Verdict: perfstore.VerdictKeep}}
	s := decideNext(in)
	if s.Action != ActionOptimize || s.Op != "open_file" || s.Evidence.Source != "field" || s.Target == nil || len(s.Rules) == 0 {
		t.Fatalf("optimize = %+v", s)
	}
	if len(s.PastAttempts) != 1 || s.PastAttempts[0].Note != "cache the tree" {
		t.Fatalf("past = %+v", s.PastAttempts)
	}
	if len(s.Unresolvable) != 1 || s.Unresolvable[0] != "OpenFile/large" {
		t.Fatalf("unresolvable = %+v", s.Unresolvable)
	}

	// Scenarios stand in when real use is thin, and partial coverage is said.
	in = base()
	in.E2E = []e2eMetric{
		{Scenario: "commit", Size: "large", Samples: []float64{1}, Budget: 300, HasBudget: true, Status: "FAIL", Median: 338},
		{Scenario: "external_change", Size: "large", Samples: []float64{1}, Budget: 1000, HasBudget: true, Status: "FAIL", Median: 1700},
		{Scenario: "dropped_frames", Budget: 1, HasBudget: true, Status: "FAIL", Median: 9},
		{Scenario: "open_file", Budget: 100, HasBudget: true, Status: "PASS", Median: 9},
	}
	s = decideNext(in)
	if s.Action != ActionOptimize || s.Op != "external_change" || s.Evidence.Source != "e2e" || !strings.Contains(s.Detail, "e2e -only external_change") {
		t.Fatalf("e2e = %+v", s)
	}

	// Three failures in a row: stop changing things until someone looks. A
	// classification, or a keep, ends the stall.
	in = base()
	in.Report = report(opReport("open_file", 100, 200, 400, 100, 3e4))
	fail := perfstore.Round{Kind: perfstore.RoundTry, Op: "open_file", Verdict: perfstore.VerdictRevert}
	in.Rounds = []perfstore.Round{fail, fail, fail}
	if s := decideNext(in); s.Action != ActionClassify || len(s.PastAttempts) != 3 {
		t.Fatalf("stalled = %+v", s)
	}
	in.Rounds = append(in.Rounds, perfstore.Round{Kind: perfstore.RoundClassified, Op: "open_file", Note: "the benchmark does not cover it"})
	if s := decideNext(in); s.Action != ActionOptimize {
		t.Fatalf("after classifying = %+v", s)
	}
	in.Rounds = []perfstore.Round{fail, fail, {Kind: perfstore.RoundTry, Op: "open_file", Verdict: perfstore.VerdictKeep}, fail, fail}
	if s := decideNext(in); s.Action != ActionOptimize {
		t.Fatalf("a keep resets = %+v", s)
	}

	// Nothing over budget only means stop when enough was seen.
	in = base()
	if s := decideNext(in); s.Action != ActionCollect {
		t.Fatalf("no evidence = %+v", s)
	}
	var ops []perfreport.OpReport
	for _, op := range perf.Ops() {
		ops = append(ops, opReport(op.String(), 100, 1, 2, 300, 0))
	}
	in.Report = report(ops...)
	if s := decideNext(in); s.Action != ActionStop {
		t.Fatalf("all well = %+v", s)
	}
}

func TestJudgeCheck(t *testing.T) {
	aa := []Pair{{Name: "A", Verdict: PairFlat, NoisePct: 2}, {Name: "B", Verdict: PairSlower, DeltaPct: 8, NoisePct: 3}, {Name: "C", Verdict: PairFlat, NoisePct: 2}, {Name: "D", Verdict: PairInconclusive, NoisePct: 12}}
	slow := []Pair{{Name: "A", Verdict: PairSlower, DeltaPct: 20}, {Name: "B", Verdict: PairSlower, DeltaPct: 20}, {Name: "C", Verdict: PairFlat, DeltaPct: 1}, {Name: "D", Verdict: PairInconclusive}}
	r := judgeCheck(aa, slow)
	if r.OK || len(r.Problems) != 2 || !strings.Contains(r.Problems[0], "B: an unchanged build") || !strings.Contains(r.Problems[1], "C: a build 30% slower") {
		t.Fatalf("check = %+v", r)
	}
	if len(r.Unresolvable) != 1 || r.Unresolvable[0] != "D" {
		t.Fatalf("unresolvable = %v", r.Unresolvable)
	}
	if ok := judgeCheck(aa[:1], slow[:1]); !ok.OK || len(ok.Problems) != 0 {
		t.Fatalf("clean = %+v", ok)
	}
	if none := judgeCheck(nil, nil); none.OK {
		t.Fatal("a check of nothing passed")
	}
}

func TestBenchPatternAndHelpers(t *testing.T) {
	if got := benchPattern([]string{"OpenFile"}); got != "^Benchmark(OpenFile)$" {
		t.Fatal(got)
	}
	if got := benchPattern([]string{"Frame/search"}); got != "^BenchmarkFrame$/^search$" {
		t.Fatal(got)
	}
	if got := benchPattern([]string{"Frame/files", "Frame/git"}); got != "^Benchmark(Frame)$" {
		t.Fatal(got)
	}
	tgt := perfreport.Target{Benchmarks: []string{"Frame/git", "OpenFile"}}
	if !matchesTarget("Frame/git/small", tgt) || !matchesTarget("OpenFile/holdout", tgt) || matchesTarget("FrameEntrance/git/small", tgt) || matchesTarget("Frame/files/small", tgt) {
		t.Fatal("matchesTarget")
	}
	if !isHoldout("OpenFile/holdout") || isHoldout("OpenFile/large") {
		t.Fatal("isHoldout")
	}
	if got := pprofCoverage("File: x\nDuration: 5.01s, Total samples = 1.20s (23.95%)\nShowing"); got != 23.95 {
		t.Fatal(got)
	}
	if got := pprofCoverage("nothing"); got != 0 {
		t.Fatal(got)
	}
}

// fakeTools makes try run on canned benchmark results.
func fakeTools(t *testing.T, root string, results func(cur bool) string, dirty string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	oldBuild, oldRun, oldGit, oldPP := buildBinary, runBinary, gitOutput, runPProf
	t.Cleanup(func() { buildBinary, runBinary, gitOutput, runPProf = oldBuild, oldRun, oldGit, oldPP })
	buildBinary = func(string, string) error { return nil }
	runBinary = func(b binary, r benchRun, _ io.Writer) (Output, error) {
		text := strings.Repeat(results(strings.HasSuffix(b.path, "cur.test")), max(r.samples, 1))
		return ParseBench(strings.NewReader(text))
	}
	gitOutput = func(_ string, args ...string) (string, error) {
		switch {
		case args[0] == "status":
			return dirty, nil
		case args[0] == "rev-parse" && args[len(args)-1] == "HEAD":
			return "bbbbbbb", nil
		case args[0] == "rev-parse":
			return "aaaaaaa", nil
		}
		return "", nil
	}
	runPProf = func(...string) (string, error) { return "Duration: 1s, Total samples = 1s (100%)\nflat\n", nil }
	if err := saveJSONFile(checkPath(root, DetectMachine()), goodCheck(climbNow())); err != nil {
		t.Fatal(err)
	}
}

func benchText(lines map[string]float64) string {
	var b strings.Builder
	for name, ns := range lines {
		fmt.Fprintf(&b, "Benchmark%s-8 100 %f ns/op 64 B/op 10 allocs/op\n", name, ns)
	}
	return b.String()
}

func runClimb(t *testing.T, root string, args ...string) (int, map[string]any, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append(append([]string{"climb"}, args...), "-root", root), strings.NewReader(""), &out, &errb)
	var v map[string]any
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatalf("not JSON: %v\n%s\n%s", err, out.String(), errb.String())
		}
	}
	return code, v, errb.String()
}

func TestClimbTryKeepsARealImprovementAndWritesTheLedger(t *testing.T) {
	root := t.TempDir()
	fakeTools(t, root, func(cur bool) string {
		ns := 1000.0
		if cur {
			ns = 700
		}
		return benchText(map[string]float64{"OpenFile/large": ns, "OpenFile/holdout": ns, "LoadRepo/large": 500, "LoadRepo/holdout": 500})
	}, "")
	if err := SaveBaseline(baselinePath(root, DetectMachine()), newBaseline(DetectMachine(), fixture.Version, map[string]Series{
		"OpenFile/large": series(1000, 1000, 1000, 1000, 1000), "LoadRepo/large": series(2000, 2000, 2000, 2000, 2000),
	}, time.Now())); err != nil {
		t.Fatal(err)
	}
	code, res, _ := runClimb(t, root, "try", "-op", "open_file", "-hypothesis", "read less of the tree")
	if code != 0 || res["verdict"] != perfstore.VerdictKeep || res["reason"] != ReasonImproved || res["holdout"] != PairFaster {
		t.Fatalf("code=%d %v", code, res)
	}
	if res["baselineUpdated"] != true || res["profileDiff"] == nil {
		t.Fatalf("res = %v", res)
	}
	// Numbers of the held-out fixture never reach the agent.
	data, _ := json.Marshal(res)
	if strings.Contains(string(data), "/holdout") {
		t.Fatalf("held-out numbers leaked: %s", data)
	}
	rounds, err := perfstore.ReadRounds(filepath.Join(root, perfstore.LedgerPath))
	if err != nil || len(rounds) != 1 {
		t.Fatalf("ledger = %+v, %v", rounds, err)
	}
	r := rounds[0]
	if r.Kind != perfstore.RoundTry || r.Op != "open_file" || r.Note != "read less of the tree" || r.Commit != "bbbbbbb" || r.Base != "aaaaaaa" || r.Verdict != perfstore.VerdictKeep || len(r.Target) != 1 || strings.Contains(fmt.Sprint(r), "holdout/") {
		t.Fatalf("round = %+v", r)
	}
	// The aim is tightened, and what this round did not prove is left alone.
	bl, err := LoadBaseline(baselinePath(root, DetectMachine()))
	if err != nil || bl.Benchmarks["OpenFile/large"].Ns[0] != 700 || bl.Benchmarks["LoadRepo/large"].Ns[0] != 2000 {
		t.Fatalf("baseline = %+v %v", bl, err)
	}
	if _, ok := bl.Benchmarks["OpenFile/holdout"]; ok {
		t.Fatal("the held-out fixture got into the baseline")
	}
	// The ledger answers, and the file it reads is in the repository.
	code, res, _ = runClimb(t, root, "log", "-op", "open_file")
	if code != 0 || len(res["rounds"].([]any)) != 1 {
		t.Fatalf("log = %d %v", code, res)
	}
}

func TestClimbTryRevertsAFittedChange(t *testing.T) {
	root := t.TempDir()
	fakeTools(t, root, func(cur bool) string {
		ns := 1000.0
		if cur {
			ns = 700
		}
		return benchText(map[string]float64{"OpenFile/large": ns, "OpenFile/holdout": 1000})
	}, "")
	code, res, _ := runClimb(t, root, "try", "-op", "open_file", "-hypothesis", "special-case the fixture")
	if code != 1 || res["verdict"] != perfstore.VerdictRevert || res["reason"] != ReasonOverfit || !strings.Contains(res["next"].(string), "Revert") {
		t.Fatalf("code=%d %v", code, res)
	}
}

func TestClimbTryRefuses(t *testing.T) {
	root := t.TempDir()
	fakeTools(t, root, func(bool) string { return "" }, "")
	for _, c := range []struct {
		reason string
		args   []string
	}{
		{"unknown_op", []string{"try", "-op", "nope", "-hypothesis", "x"}},
		{"no_hypothesis", []string{"try", "-op", "open_file"}},
		{"too_few_rounds", []string{"try", "-op", "open_file", "-hypothesis", "x", "-rounds", "2"}},
	} {
		if code, res, _ := runClimb(t, root, c.args...); code != 2 || res["verdict"] != "refused" || res["reason"] != c.reason {
			t.Errorf("%s: %d %v", c.reason, code, res)
		}
	}
	dirtyRoot := t.TempDir()
	fakeTools(t, dirtyRoot, func(bool) string { return "" }, " M main.go")
	if code, res, _ := runClimb(t, dirtyRoot, "try", "-op", "open_file", "-hypothesis", "x"); code != 2 || res["reason"] != "dirty_tree" {
		t.Errorf("dirty: %d %v", code, res)
	}
	// The ledger of the last round does not make a tree dirty.
	ledgerRoot := t.TempDir()
	fakeTools(t, ledgerRoot, func(bool) string { return "" }, " M perf/ledger.jsonl")
	if dirty, err := treeDirty(ledgerRoot); dirty || err != nil {
		t.Errorf("ledger counted as dirty: %v %v", dirty, err)
	}
	// Without a check the numbers are not to be believed yet.
	unchecked := t.TempDir()
	fakeTools(t, unchecked, func(bool) string { return "" }, "")
	os.Remove(checkPath(unchecked, DetectMachine()))
	if code, res, _ := runClimb(t, unchecked, "try", "-op", "open_file", "-hypothesis", "x"); code != 2 || res["reason"] != "no_check" {
		t.Errorf("unchecked: %d %v", code, res)
	}
}

func TestMeasurementFixDropsOlderFieldData(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	logs := t.TempDir()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old := climbNow
	climbNow = func() time.Time { return now }
	t.Cleanup(func() { climbNow = old })

	var b bytes.Buffer
	for i := range 10 {
		e := perfstore.Entry{T: now.Add(-time.Hour + time.Duration(i)*time.Second), Op: "external_change", Ms: 320000, Result: "ok", Size: "small", Version: "dev",
			Phases: []perfstore.PhaseMs{{Name: "git_status", Ms: 30}}}
		b.Write(e.AppendJSON(nil))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(logs, perfstore.LogName(now)), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveJSONFile(checkPath(root, DetectMachine()), goodCheck(now)); err != nil {
		t.Fatal(err)
	}
	if code, res, _ := runClimb(t, root, "next", "-input", logs); code != 0 || res["action"] != ActionMeasure || res["op"] != "external_change" {
		t.Fatalf("before = %d %v", code, res)
	}
	if code, res, _ := runClimb(t, root, "log", "-op", "external_change", "-measurement", "spans waited for a window out of sight"); code != 0 || res["kind"] != perfstore.RoundMeasurement {
		t.Fatalf("log = %d %v", code, res)
	}
	if code, res, _ := runClimb(t, root, "next", "-input", logs); code != 0 || res["action"] == ActionMeasure {
		t.Fatalf("after = %d %v", code, res)
	}
	if code, _, stderr := runClimb(t, root, "log", "-op", "nope", "-measurement", "x"); code != 2 || !strings.Contains(stderr, "not an operation") {
		t.Fatalf("bad op = %d %s", code, stderr)
	}
}

func TestEveryOperationHasABenchmarkTarget(t *testing.T) {
	for _, op := range perf.Ops() {
		tg, ok := perfreport.Targets[op.String()]
		if !ok || len(tg.Benchmarks) == 0 {
			t.Errorf("%s has no benchmark", op)
		}
	}
}

func TestCompareJSON(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)
	t.Setenv("HOME", t.TempDir())
	text := benchText(map[string]float64{"A/x": 100})
	text = strings.Repeat(text, 5)
	p := filepath.Join(root, "in.txt")
	os.WriteFile(p, []byte(text), 0o644)
	var out, errb bytes.Buffer
	if code := run([]string{"record", "-input", p, "-root", root}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	out.Reset()
	if code := run([]string{"compare", "-format", "json", "-input", p, "-root", root}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var v compareJSON
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if v.Schema != SchemaVersion || v.Failed || len(v.Results) != 1 || v.Results[0].Name != "A/x" || v.Results[0].Status != "ok" {
		t.Fatalf("%+v", v)
	}
}
