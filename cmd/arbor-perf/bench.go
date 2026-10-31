package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Series holds the per-run results of one benchmark: one entry per run of
// `go test -bench -count N`, in the order they ran.
type Series struct {
	Ns     []float64 `json:"ns"`
	Bytes  []float64 `json:"bytes,omitempty"`
	Allocs []float64 `json:"allocs,omitempty"`
}

// Output is a parsed `go test -bench -benchmem` run.
type Output struct {
	GOOS, GOARCH, CPU string
	Benchmarks        map[string]Series
}

var procSuffix = regexp.MustCompile(`-\d+$`)

// ParseBench reads the text `go test -bench` prints. Lines that are not
// benchmark results (PASS, ok, logs) are ignored, and the -GOMAXPROCS suffix is
// dropped from names so results compare across core counts.
func ParseBench(r io.Reader) (Output, error) {
	out := Output{Benchmarks: map[string]Series{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "goos:"):
			out.GOOS = strings.TrimSpace(strings.TrimPrefix(line, "goos:"))
		case strings.HasPrefix(line, "goarch:"):
			out.GOARCH = strings.TrimSpace(strings.TrimPrefix(line, "goarch:"))
		case strings.HasPrefix(line, "cpu:"):
			out.CPU = strings.TrimSpace(strings.TrimPrefix(line, "cpu:"))
		case strings.HasPrefix(line, "Benchmark"):
			name, ns, bytes, allocs, ok := parseBenchLine(line)
			if !ok {
				continue
			}
			s := out.Benchmarks[name]
			s.Ns = append(s.Ns, ns)
			if allocs >= 0 {
				s.Bytes = append(s.Bytes, bytes)
				s.Allocs = append(s.Allocs, allocs)
			}
			out.Benchmarks[name] = s
		}
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	for name, s := range out.Benchmarks {
		if len(s.Allocs) != 0 && len(s.Allocs) != len(s.Ns) {
			return out, fmt.Errorf("%s: some runs have allocation counts and some do not (use -benchmem)", name)
		}
	}
	return out, nil
}

// parseBenchLine reads "BenchmarkX/y-10  100  1234 ns/op  56 B/op  7 allocs/op".
// allocs is -1 when the line has no allocation columns.
func parseBenchLine(line string) (name string, ns, bytes, allocs float64, ok bool) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return "", 0, 0, 0, false
	}
	if _, err := strconv.Atoi(f[1]); err != nil {
		return "", 0, 0, 0, false
	}
	name = procSuffix.ReplaceAllString(strings.TrimPrefix(f[0], "Benchmark"), "")
	allocs = -1
	seenNs := false
	for i := 2; i+1 < len(f); i += 2 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return "", 0, 0, 0, false
		}
		switch f[i+1] {
		case "ns/op":
			ns, seenNs = v, true
		case "B/op":
			bytes = v
		case "allocs/op":
			allocs = v
		}
	}
	return name, ns, bytes, allocs, seenNs
}

// Names lists the benchmark names of a result set in sorted order.
func Names(m map[string]Series) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
