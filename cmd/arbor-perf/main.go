// Command arbor-perf records and compares Arbor's Go benchmarks: the ratchet of
// ARB-5. Baselines live per machine class in .perf/baselines, outside git.
//
//	go run ./cmd/arbor-perf record    # run the benchmarks and store them as the baseline
//	go run ./cmd/arbor-perf compare   # run them again and fail on a regression
//
// Every benchmark runs at least five times and is judged by its median. A
// regression is a median more than 10% slower AND beyond the measured run-to-run
// noise; allocation counts compare exactly. An improvement tightens the stored
// baseline, so it can only get better.
package main

import (
	"arbor/internal/fixture"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// runBenchmarks runs the benchmarks and returns what go test printed; tests
// replace it.
var runBenchmarks = func(root, bench, benchtime string, count int, progress io.Writer) ([]byte, error) {
	args := []string{"test", "-run", "^$", "-bench", bench, "-benchmem", "-count", strconv.Itoa(count), "-timeout", "60m"}
	if benchtime != "" {
		args = append(args, "-benchtime", benchtime)
	}
	args = append(args, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out, progress)
	cmd.Stderr = progress
	err := cmd.Run()
	return out.Bytes(), err
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

const usage = `usage: arbor-perf <record|compare> [flags]

  record    run the benchmarks and store the result as this machine's baseline
  compare   run the benchmarks again, report changes, fail on a regression and
            tighten the baseline on improvements

flags:
  -root dir        repository root (default: found from the working directory)
  -bench regexp    benchmarks to run (default all); a partial run never reports
                   the others as missing
  -count n         runs per benchmark, at least 5 (default 5)
  -benchtime t     passed to go test -benchtime
  -input file      read go test -bench output from file ("-" for stdin) instead of running it
  -no-update       compare only: leave the baseline untouched
`

type config struct {
	root, bench, benchtime, input string
	count                         int
	noUpdate                      bool
}

func parseFlags(name string, args []string, stderr io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.root, "root", "", "")
	fs.StringVar(&c.bench, "bench", ".", "")
	fs.StringVar(&c.benchtime, "benchtime", "", "")
	fs.StringVar(&c.input, "input", "", "")
	fs.IntVar(&c.count, "count", DefaultOptions.MinRuns, "")
	fs.BoolVar(&c.noUpdate, "no-update", false, "")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.count < DefaultOptions.MinRuns {
		return c, fmt.Errorf("-count %d: at least %d runs are needed to tell a change from noise", c.count, DefaultOptions.MinRuns)
	}
	if c.root == "" {
		root, err := findRoot(".")
		if err != nil {
			return c, err
		}
		c.root = root
	}
	return c, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	if cmd != "record" && cmd != "compare" {
		fmt.Fprintf(stderr, "arbor-perf: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	c, err := parseFlags(cmd, args[1:], stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		}
		return 2
	}
	if cmd == "record" {
		return record(c, stdin, stdout, stderr)
	}
	return compare(c, stdin, stdout, stderr)
}

// benchOutput gets the parsed benchmark results, running them or reading -input.
func benchOutput(c config, stdin io.Reader, stderr io.Writer) (Output, error) {
	var raw []byte
	var err error
	switch c.input {
	case "":
		fmt.Fprintf(stderr, "arbor-perf: running benchmarks (%d runs each)...\n", c.count)
		raw, err = runBenchmarks(c.root, c.bench, c.benchtime, c.count, stderr)
		if err != nil {
			return Output{}, fmt.Errorf("go test: %w", err)
		}
	case "-":
		raw, err = io.ReadAll(stdin)
	default:
		raw, err = os.ReadFile(c.input)
	}
	if err != nil {
		return Output{}, err
	}
	out, err := ParseBench(bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	if len(out.Benchmarks) == 0 {
		return out, errors.New("no benchmark results found")
	}
	return out, nil
}

func record(c config, stdin io.Reader, stdout, stderr io.Writer) int {
	m := DetectMachine()
	out, err := benchOutput(c, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	for name, s := range out.Benchmarks {
		if len(s.Ns) < DefaultOptions.MinRuns {
			fmt.Fprintf(stderr, "arbor-perf: %s ran %d times, need %d\n", name, len(s.Ns), DefaultOptions.MinRuns)
			return 2
		}
	}
	path := baselinePath(c.root, m)
	benchmarks := out.Benchmarks
	if c.bench != "." {
		// A partial record updates only what it ran.
		if old, err := LoadBaseline(path); err == nil && old.FixtureVersion == fixture.Version {
			benchmarks = map[string]Series{}
			for k, v := range old.Benchmarks {
				benchmarks[k] = v
			}
			for k, v := range out.Benchmarks {
				benchmarks[k] = v
			}
		}
	}
	if err := SaveBaseline(path, newBaseline(m, fixture.Version, benchmarks, time.Now())); err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "recorded %d benchmarks for %s\n  %s\n", len(out.Benchmarks), m.Key, path)
	if noisy := Noisy(out.Benchmarks, NoisyLimit); len(noisy) > 0 {
		fmt.Fprintf(stdout, "warning: %d of the benchmarks varied by more than %.0f%% between runs (%s): the machine was probably busy;\n"+
			"comparisons against a noisy baseline are unreliable, so record it again when the machine is quiet\n",
			len(noisy), NoisyLimit*100, strings.Join(noisy, ", "))
	}
	return 0
}

func compare(c config, stdin io.Reader, stdout, stderr io.Writer) int {
	m := DetectMachine()
	path := baselinePath(c.root, m)
	base, err := LoadBaseline(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "arbor-perf: no baseline for this machine (%s)\nrecord one with: go run ./cmd/arbor-perf record\n", m.Key)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	if base.FixtureVersion != fixture.Version {
		fmt.Fprintf(stderr, "arbor-perf: the baseline was recorded on fixture version %d, the code has %d; re-record it\n", base.FixtureVersion, fixture.Version)
		return 2
	}
	out, err := benchOutput(c, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	results := Compare(base.Benchmarks, out.Benchmarks, c.bench != ".", DefaultOptions)
	if c.input == "" && hasStatus(results, StatusRegression) {
		// Only a slowdown that shows again is a regression: see Confirm.
		rerun := func(names []string) (map[string]Series, error) {
			// go test matches the whole name of a top-level benchmark, with its prefix.
			pattern := "^Benchmark(" + strings.Join(families(names), "|") + ")$"
			fmt.Fprintf(stderr, "arbor-perf: re-running %s to confirm a slowdown...\n", pattern)
			raw, err := runBenchmarks(c.root, pattern, c.benchtime, c.count, stderr)
			if err != nil {
				return nil, fmt.Errorf("go test: %w", err)
			}
			again, err := ParseBench(bytes.NewReader(raw))
			return again.Benchmarks, err
		}
		var err error
		if results, out.Benchmarks, err = Confirm(base.Benchmarks, out.Benchmarks, results, rerun, confirmRounds, DefaultOptions); err != nil {
			fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
			return 2
		}
	}
	printReport(stdout, m, results)

	failed := Failed(results)
	if !c.noUpdate && !failed {
		if updated, changed := Tighten(base.Benchmarks, out.Benchmarks, results); changed {
			if err := SaveBaseline(path, newBaseline(m, fixture.Version, updated, time.Now())); err != nil {
				fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
				return 2
			}
			fmt.Fprintf(stdout, "baseline tightened: %s\n", path)
		}
	}
	if failed {
		fmt.Fprintln(stdout, "FAIL: the baseline was left as it was")
		return 1
	}
	return 0
}

// confirmRounds is how many times a suspected slowdown is run again.
const confirmRounds = 2

func hasStatus(rs []Result, s Status) bool {
	for _, r := range rs {
		if r.Status == s {
			return true
		}
	}
	return false
}

// families names the top-level benchmarks (before the first slash) of names,
// each once: go test selects sub-benchmarks by their parent.
func families(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		top, _, _ := strings.Cut(n, "/")
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
		}
	}
	sort.Strings(out)
	return out
}

func printReport(w io.Writer, m Machine, rs []Result) {
	fmt.Fprintf(w, "machine %s\n\n", m.Key)
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BENCHMARK\tBASELINE\tNOW\tCHANGE\tNOISE\tALLOCS\tVERDICT")
	counts := map[Status]int{}
	for _, r := range rs {
		counts[r.Status]++
		switch r.Status {
		case StatusNew:
			fmt.Fprintf(tw, "%s\t-\t%s\t\t\t%s\tnew (added to the baseline)\n", r.Name, fmtDur(r.Cur), fmtAllocs(r.AllocsCur, r.AllocsCur))
		case StatusMissing:
			fmt.Fprintf(tw, "%s\t%s\t-\t\t\t\tMISSING: %s\n", r.Name, fmtDur(r.Base), r.Note)
		default:
			verdict := r.Status.String()
			if r.Note != "" {
				verdict += " (" + r.Note + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%+.1f%%\t±%.1f%%\t%s\t%s\n", r.Name, fmtDur(r.Base), fmtDur(r.Cur),
				r.Delta*100, r.Noise*100, fmtAllocs(r.AllocsBase, r.AllocsCur), verdict)
		}
	}
	tw.Flush()
	var parts []string
	for _, s := range []Status{StatusOK, StatusImproved, StatusNew, StatusRegression, StatusAllocRegression, StatusMissing, StatusTooFewRuns} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], strings.ToLower(s.String())))
		}
	}
	fmt.Fprintf(w, "\n%d benchmarks: %s\n", len(rs), strings.Join(parts, ", "))
}

func fmtAllocs(base, cur float64) string {
	if base == cur {
		return strconv.FormatFloat(cur, 'f', -1, 64)
	}
	return fmt.Sprintf("%s -> %s", strconv.FormatFloat(base, 'f', -1, 64), strconv.FormatFloat(cur, 'f', -1, 64))
}

func fmtDur(ns float64) string {
	switch {
	case ns >= 1e9:
		return fmt.Sprintf("%.2fs", ns/1e9)
	case ns >= 1e6:
		return fmt.Sprintf("%.2fms", ns/1e6)
	case ns >= 1e3:
		return fmt.Sprintf("%.2fµs", ns/1e3)
	}
	return fmt.Sprintf("%.0fns", ns)
}
