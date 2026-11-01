package main

import (
	"arbor/internal/perfagg"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A stored baseline was measured at some other time, and a machine has slow
// stretches: on one with other work running, the same code takes twice as long
// for tens of seconds at a time. To tell what a change did, the two builds take
// turns on each benchmark, so each pair of runs is under a second apart and
// shares the state of the machine, and every judgement is made on the ratios
// within pairs.

// SchemaVersion is the format of the JSON the commands print. Agents read it, so
// a change of meaning or a removed field bumps it.
const SchemaVersion = 1

// Verdicts on one benchmark in a pair of builds.
const (
	PairFaster = "faster"
	PairSlower = "slower"
	PairFlat   = "flat"
	// PairInconclusive: its noise is above the smallest change worth acting on,
	// and nothing moved by more than the noise.
	PairInconclusive = "inconclusive"
)

// Pair is the judgement on one benchmark. Percentages are of the first build.
type Pair struct {
	Name     string  `json:"name"`
	BaseNs   float64 `json:"baseNs"`
	CurNs    float64 `json:"curNs"`
	DeltaPct float64 `json:"deltaPct"`
	// NoisePct is how uncertain DeltaPct is: half the width of its 95% confidence
	// interval.
	NoisePct float64 `json:"noisePct"`
	Verdict  string  `json:"verdict"`
	Rounds   int     `json:"rounds"`
	// AllocRegression says the second build allocates more than any run of the first.
	AllocRegression bool `json:"allocRegression,omitempty"`
}

// ComparePair judges a's runs against b's, pair by pair, from the median of their
// ratios. A change needs a 95% confidence interval that excludes no change, and
// to reach perfagg.MinEffect.
func ComparePair(name string, a, b Series) Pair {
	n := min(len(a.Ns), len(b.Ns))
	p := Pair{Name: name, BaseNs: Median(a.Ns), CurNs: Median(b.Ns), Rounds: n, Verdict: PairInconclusive}
	if len(a.Allocs) > 0 && len(b.Allocs) > 0 {
		p.AllocRegression = Median(b.Allocs) > maxOf(a.Allocs)
	}
	ratios := make([]float64, 0, n)
	for i := range n {
		if a.Ns[i] > 0 {
			ratios = append(ratios, b.Ns[i]/a.Ns[i])
		}
	}
	slices.Sort(ratios)
	lo, hi, ok := perfagg.MedianCI(ratios)
	if !ok {
		return p
	}
	delta, noise := Median(ratios)-1, (hi-lo)/2
	p.DeltaPct, p.NoisePct = delta*100, noise*100
	moved := lo > 1 || hi < 1
	switch {
	case moved && delta <= -perfagg.MinEffect:
		p.Verdict = PairFaster
	case moved && delta >= perfagg.MinEffect:
		p.Verdict = PairSlower
	case noise >= perfagg.MinEffect:
		p.Verdict = PairInconclusive
	default:
		p.Verdict = PairFlat
	}
	return p
}

// comparePairs judges every benchmark the two sets share, sorted by name.
func comparePairs(a, b map[string]Series) []Pair {
	var out []Pair
	for _, name := range Names(b) {
		if s, ok := a[name]; ok {
			out = append(out, ComparePair(name, s, b[name]))
		}
	}
	return out
}

// binary is a benchmark binary and how to run it.
type binary struct {
	path, dir string
	env       []string
}

// PairRounds is how many pairs of runs say something about a benchmark, and
// minPairRounds the fewest that can give a confidence interval at all.
const (
	PairRounds    = 12
	minPairRounds = 6
)

// runBenchtime is how long one side of a pair runs. Pairs that are close in time
// are worth more than long runs, as the machine changes faster than a run ends.
const runBenchtime = "300ms"

// benchRun says what one run of a binary covers.
type benchRun struct {
	pattern, benchtime string
	// samples is how many times each benchmark is measured; zero means once.
	samples int
	// cpuProfile, if set, is where to write a CPU profile.
	cpuProfile string
}

// benchPackage is the package of the checkout at dir that holds the benchmarks.
// Commits from before internal/app kept them at the root, and a round can
// compare against one of those.
func benchPackage(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "internal", "app")); err == nil {
		return "./internal/app"
	}
	return "."
}

// buildBinary compiles the benchmarks of the checkout at dir; tests replace it.
var buildBinary = func(dir, out string) error {
	cmd := exec.Command("go", "test", "-c", "-o", out, benchPackage(dir))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go test -c: %v\n%s", err, msg)
	}
	return nil
}

// runBinary runs b once and parses what it printed; tests replace it.
var runBinary = func(b binary, r benchRun, progress io.Writer) (Output, error) {
	args := []string{"-test.run", "^$", "-test.bench", r.pattern, "-test.benchmem", "-test.count", strconv.Itoa(max(r.samples, 1)), "-test.timeout", "60m"}
	if r.benchtime != "" {
		args = append(args, "-test.benchtime", r.benchtime)
	}
	if r.cpuProfile != "" {
		args = append(args, "-test.cpuprofile", r.cpuProfile)
	}
	cmd := exec.Command(b.path, args...)
	cmd.Dir = b.dir
	cmd.Env = append(os.Environ(), b.env...)
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out, progress)
	cmd.Stderr = progress
	if err := cmd.Run(); err != nil {
		return Output{}, fmt.Errorf("%s: %w", filepath.Base(b.path), err)
	}
	return ParseBench(&out)
}

// interleave runs a and b in turns on every benchmark r covers, rounds times.
// Rounds spread over minutes, so no slow stretch decides a verdict. Who goes
// first alternates, as the first run after a build pays for cold caches.
func interleave(a, b binary, r benchRun, rounds int, progress io.Writer) (as, bs map[string]Series, err error) {
	if r.benchtime == "" {
		r.benchtime = runBenchtime
	}
	names, err := listBenchmarks(a, r, progress)
	if err != nil {
		return nil, nil, err
	}
	as, bs = map[string]Series{}, map[string]Series{}
	one := func(bin binary, name string, into map[string]Series) error {
		run := r
		run.pattern = exactPattern(name)
		out, err := runBinary(bin, run, progress)
		if err != nil {
			return err
		}
		if s, ok := out.Benchmarks[name]; ok {
			c := into[name]
			c.Ns = append(c.Ns, s.Ns...)
			c.Allocs = append(c.Allocs, s.Allocs...)
			c.Bytes = append(c.Bytes, s.Bytes...)
			into[name] = c
		}
		return nil
	}
	for i := range rounds {
		fmt.Fprintf(progress, "arbor-perf: round %d of %d\n", i+1, rounds)
		first, second, intoFirst, intoSecond := a, b, as, bs
		if i%2 == 1 {
			first, second, intoFirst, intoSecond = b, a, bs, as
		}
		for _, name := range names {
			if err := one(first, name, intoFirst); err != nil {
				return nil, nil, err
			}
			if err := one(second, name, intoSecond); err != nil {
				return nil, nil, err
			}
		}
	}
	return as, bs, nil
}

// measurePairs judges every benchmark r covers. Among this many at 95%, some look
// changed by chance, so whatever looks changed has to show it again in fresh
// rounds, and is judged on those alone.
func measurePairs(a, b binary, r benchRun, rounds int, progress io.Writer) ([]Pair, error) {
	as, bs, err := interleave(a, b, r, rounds, progress)
	if err != nil {
		return nil, err
	}
	pairs := comparePairs(as, bs)
	for i, p := range pairs {
		if p.Verdict != PairFaster && p.Verdict != PairSlower {
			continue
		}
		fmt.Fprintf(progress, "arbor-perf: %s looks %s, measuring it again\n", p.Name, p.Verdict)
		again := r
		again.pattern = exactPattern(p.Name)
		as, bs, err := interleave(a, b, again, rounds, progress)
		if err != nil {
			return nil, err
		}
		q := ComparePair(p.Name, as[p.Name], bs[p.Name])
		if q.Verdict != p.Verdict {
			q.Verdict = PairInconclusive
		}
		q.Rounds += p.Rounds
		pairs[i] = q
	}
	return pairs, nil
}

// listBenchmarks finds the benchmarks r covers by running each of them once.
func listBenchmarks(b binary, r benchRun, progress io.Writer) ([]string, error) {
	r.benchtime = "1x"
	out, err := runBinary(b, r, progress)
	if err != nil {
		return nil, err
	}
	return Names(out.Benchmarks), nil
}

// exactPattern is the -test.bench pattern of one benchmark and nothing else.
func exactPattern(name string) string {
	parts := strings.Split("Benchmark"+name, "/")
	for i, part := range parts {
		parts[i] = "^" + regexp.QuoteMeta(part) + "$"
	}
	return strings.Join(parts, "/")
}

// fixtureEnv makes the benchmark processes share their fixtures, which take
// longer to generate than a benchmark takes to run.
func fixtureEnv() []string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	return []string{"ARBOR_PERF_FIXTURES=" + filepath.Join(dir, "arbor-perf", "fixtures")}
}

// gitOutput runs git in root and trims what it printed; tests replace it.
var gitOutput = func(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// treeDirty reports whether anything but the ledger differs from HEAD. The
// ledger is written by the rounds themselves.
func treeDirty(root string) (bool, error) {
	out, err := gitOutput(root, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasSuffix(line, "perf/ledger.jsonl") {
			return true, nil
		}
	}
	return false, nil
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// compareJSON is what compare -format json prints.
type compareJSON struct {
	Schema  int          `json:"schema"`
	Machine string       `json:"machine"`
	Commit  string       `json:"commit,omitempty"`
	Failed  bool         `json:"failed"`
	Results []resultJSON `json:"results"`
}

type resultJSON struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	BaseNs     float64 `json:"baseNs"`
	CurNs      float64 `json:"curNs"`
	DeltaPct   float64 `json:"deltaPct"`
	NoisePct   float64 `json:"noisePct"`
	AllocsBase float64 `json:"allocsBase"`
	AllocsCur  float64 `json:"allocsCur"`
	Note       string  `json:"note,omitempty"`
	Reruns     int     `json:"reruns,omitempty"`
}

func newCompareJSON(c config, m Machine, rs []Result) compareJSON {
	_, commit := treeVersion(c.root)
	out := compareJSON{Schema: SchemaVersion, Machine: m.Key, Commit: commit, Failed: Failed(rs)}
	for _, r := range rs {
		out.Results = append(out.Results, resultJSON{r.Name, r.Status.String(), r.Base, r.Cur, r.Delta * 100, r.Noise * 100, r.AllocsBase, r.AllocsCur, r.Note, r.Reruns})
	}
	return out
}

// loadAverage reads the one-minute load of the machine, or zero if it cannot.
func loadAverage() float64 {
	text, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
		if err != nil {
			return 0
		}
		text = out
	}
	for _, field := range strings.Fields(string(text)) {
		if f, err := strconv.ParseFloat(field, 64); err == nil {
			return f
		}
	}
	return 0
}

// checkPath is where the last check of a machine is kept.
func checkPath(root string, m Machine) string {
	return filepath.Join(root, ".perf", "check", m.Key+".json")
}

func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Second) }

func pct(f float64) string { return strconv.FormatFloat(f*100, 'f', -1, 64) }
