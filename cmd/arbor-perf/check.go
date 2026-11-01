package main

import (
	"arbor/internal/fixture"
	"arbor/internal/perfagg"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// check proves the measuring before anything is believed from it. Two runs of
// one build must show no change, which measures the noise and catches false
// wins, and a build slowed on purpose must show the change, which catches a
// benchmark that cannot see what it claims to measure.

// checkSlowdown is the known change, comfortably above perfagg.MinEffect.
const checkSlowdown = 0.3

// CheckResult is what check prints and keeps in .perf/check.
type CheckResult struct {
	Schema       int     `json:"schema"`
	Machine      string  `json:"machine"`
	Commit       string  `json:"commit,omitempty"`
	Dirty        bool    `json:"dirty"`
	Fixture      int     `json:"fixtureVersion"`
	T            string  `json:"t"`
	Rounds       int     `json:"rounds"`
	MinEffectPct float64 `json:"minEffectPct"`
	// LoadAvg is the machine's one-minute load as the check began, the first thing
	// to look at when the noise is high. Zero if it cannot be read.
	LoadAvg float64 `json:"loadAvg,omitempty"`
	// OK says the measuring can be trusted on the benchmarks that are not listed
	// in Unresolvable, Problems says what went wrong otherwise.
	OK       bool     `json:"ok"`
	Problems []string `json:"problems,omitempty"`
	// Unresolvable benchmarks vary more than MinEffect by chance. A try that
	// aims at one needs more rounds to say anything.
	Unresolvable []string     `json:"unresolvable,omitempty"`
	Benchmarks   []CheckBench `json:"benchmarks"`
	// Partial says only some benchmarks ran, so this says nothing of the rest.
	Partial bool `json:"partial,omitempty"`
}

// CheckBench is the check of one benchmark.
type CheckBench struct {
	Name         string  `json:"name"`
	NoisePct     float64 `json:"noisePct"`
	AADeltaPct   float64 `json:"aaDeltaPct"`
	AAVerdict    string  `json:"aaVerdict"`
	InjectedPct  float64 `json:"injectedPct"`
	MeasuredPct  float64 `json:"measuredPct"`
	SlowVerdict  string  `json:"slowVerdict"`
	Resolvable   bool    `json:"resolvable"`
	FalseChange  bool    `json:"falseChange,omitempty"`
	MissedChange bool    `json:"missedChange,omitempty"`
}

// judgeCheck turns the two experiments into a result.
func judgeCheck(aa, slow []Pair) CheckResult {
	r := CheckResult{Schema: SchemaVersion, MinEffectPct: perfagg.MinEffect * 100, Fixture: fixture.Version, OK: true}
	bySlow := map[string]Pair{}
	for _, p := range slow {
		bySlow[p.Name] = p
	}
	for _, p := range aa {
		s := bySlow[p.Name]
		b := CheckBench{Name: p.Name, NoisePct: p.NoisePct, AADeltaPct: p.DeltaPct, AAVerdict: p.Verdict,
			InjectedPct: checkSlowdown * 100, MeasuredPct: s.DeltaPct, SlowVerdict: s.Verdict,
			Resolvable: p.NoisePct < perfagg.MinEffect*100 && p.Verdict != PairInconclusive}
		b.FalseChange = p.Verdict == PairFaster || p.Verdict == PairSlower
		b.MissedChange = b.Resolvable && s.Verdict != PairSlower
		if b.FalseChange {
			r.OK = false
			r.Problems = append(r.Problems, fmt.Sprintf("%s: an unchanged build shows %s (%+.1f%%, noise %.1f%%)", p.Name, p.Verdict, p.DeltaPct, p.NoisePct))
		}
		if b.MissedChange {
			r.OK = false
			r.Problems = append(r.Problems, fmt.Sprintf("%s: a build %.0f%% slower shows %s (%+.1f%%)", p.Name, checkSlowdown*100, s.Verdict, s.DeltaPct))
		}
		if !b.Resolvable {
			r.Unresolvable = append(r.Unresolvable, p.Name)
		}
		r.Benchmarks = append(r.Benchmarks, b)
	}
	if len(aa) == 0 {
		r.OK = false
		r.Problems = append(r.Problems, "no benchmarks ran")
	}
	return r
}

func check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", "", "repository root")
	bench := fs.String("bench", ".", "benchmarks to check (default all)")
	benchtime := fs.String("benchtime", "", "passed to the benchmarks")
	rounds := fs.Int("rounds", PairRounds, "pairs of runs in each experiment")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	if *rounds < minPairRounds {
		fmt.Fprintf(stderr, "arbor-perf: -rounds %d: at least %d are needed\n", *rounds, minPairRounds)
		return 2
	}
	if _, err := regexp.Compile(*bench); err != nil {
		fmt.Fprintf(stderr, "arbor-perf: -bench: %v\n", err)
		return 2
	}
	r, err := runCheck(root, *bench, *benchtime, *rounds, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	if err := writeJSON(stdout, r); err != nil {
		return 2
	}
	if !r.OK {
		return 1
	}
	return 0
}

func runCheck(root, bench, benchtime string, rounds int, progress io.Writer) (CheckResult, error) {
	m := DetectMachine()
	load := loadAverage()
	tmp, err := os.MkdirTemp("", "arbor-check-")
	if err != nil {
		return CheckResult{}, err
	}
	defer os.RemoveAll(tmp)
	exe := filepath.Join(tmp, "bench.test")
	fmt.Fprintln(progress, "arbor-perf: building the benchmarks...")
	if err := buildBinary(root, exe); err != nil {
		return CheckResult{}, err
	}
	env := append(fixtureEnv(), "ARBOR_PERF_HOLDOUT=1")
	plain := binary{exe, root, env}
	run := benchRun{pattern: bench, benchtime: benchtime}

	fmt.Fprintln(progress, "arbor-perf: an unchanged build against itself...")
	aa, err := measurePairs(plain, plain, run, rounds, progress)
	if err != nil {
		return CheckResult{}, err
	}
	fmt.Fprintf(progress, "arbor-perf: against itself slowed by %s%%...\n", pct(checkSlowdown))
	slowed := binary{exe, root, append(append([]string(nil), env...), "ARBOR_PERF_SLOWDOWN="+fmt.Sprint(checkSlowdown))}
	slow, err := measurePairs(plain, slowed, run, rounds, progress)
	if err != nil {
		return CheckResult{}, err
	}
	r := judgeCheck(aa, slow)
	r.Machine, r.Rounds, r.T = m.Key, rounds, nowUTC().Format("2006-01-02T15:04:05Z")
	r.LoadAvg = load
	r.Partial = bench != "."
	_, r.Commit = treeVersion(root)
	r.Dirty, _ = treeDirty(root)
	if !r.Partial {
		if err := saveJSONFile(checkPath(root, m), r); err != nil {
			return r, err
		}
	}
	return r, nil
}

func resolveRoot(root string) (string, error) {
	if root != "" {
		return root, nil
	}
	return findRoot(".")
}
