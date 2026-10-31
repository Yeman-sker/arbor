package main

import (
	"arbor/internal/perfagg"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"encoding/json"
	"flag"
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

// parseSince reads a period such as 7d, 30d or 48h as whole days.
func parseSince(s string) (int, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			return n, nil
		}
	}
	if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return max(int((dur+23*time.Hour)/(24*time.Hour)), 1), nil
	}
	return 0, fmt.Errorf("-since %q: use a period such as 7d or 48h", s)
}

// report writes the ranked report of the logs, or of an export a user sent.
func report(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "7d", "period, such as 7d or 48h")
	input := fs.String("input", "", "a log directory or an export file (default: this computer's logs)")
	format := fs.String("format", "md", "md or json, written to standard output")
	out := fs.String("out", "", "also write report.md and report.json to this directory")
	nowFlag := fs.String("now", "", "the end of the period, RFC 3339 (default: now)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	days, err := parseSince(*since)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	if *format != "md" && *format != "json" {
		fmt.Fprintf(stderr, "arbor-perf: -format %q: use md or json\n", *format)
		return 2
	}
	now := time.Now().UTC()
	if *nowFlag != "" {
		if now, err = time.Parse(time.RFC3339, *nowFlag); err != nil {
			fmt.Fprintf(stderr, "arbor-perf: -now: %v\n", err)
			return 2
		}
	}
	path := *input
	if path == "" {
		path = perfstore.LogDir(perfstore.UserDataDir())
	}
	m := perfagg.NewModel(time.UTC)
	bad, err := perfreport.Load(path, m.Add)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	rep := perfreport.Build(m, days, now, bad)
	js, err := rep.JSON()
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return 2
	}
	md := rep.Markdown()
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
			return 2
		}
		for name, data := range map[string][]byte{"report.md": []byte(md), "report.json": append(js, '\n')} {
			if err := os.WriteFile(filepath.Join(*out, name), data, 0o644); err != nil {
				fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
				return 2
			}
		}
	}
	if *format == "json" {
		fmt.Fprintf(stdout, "%s\n", js)
	} else {
		fmt.Fprint(stdout, md)
	}
	return 0
}

// benchRows are the results of a run as rows of the history the app's
// Performance window draws.
func benchRows(c config, m Machine, kind string, benchmarks map[string]Series, status map[string]string, now time.Time) []perfstore.BenchRow {
	version, commit := treeVersion(c.root)
	names := make([]string, 0, len(benchmarks))
	for name := range benchmarks {
		names = append(names, name)
	}
	slices.Sort(names)
	rows := make([]perfstore.BenchRow, 0, len(names))
	for _, name := range names {
		s := benchmarks[name]
		r := perfstore.BenchRow{T: now.UTC().Truncate(time.Second), Kind: kind, Name: name, Ns: Median(s.Ns), Runs: len(s.Ns),
			Version: version, Commit: commit, Machine: m.Key, Status: status[name]}
		if len(s.Ns) > 0 {
			r.MinNs, r.MaxNs = slices.Min(s.Ns), slices.Max(s.Ns)
		}
		if len(s.Allocs) > 0 {
			r.Allocs = Median(s.Allocs)
		}
		if len(s.Bytes) > 0 {
			r.Bytes = Median(s.Bytes)
		}
		rows = append(rows, r)
	}
	return rows
}

// appendHistory adds a run to the history. It is only for real runs (no -input)
// and its failure is not the run's: the baseline is what the ratchet uses.
func appendHistory(c config, m Machine, kind string, benchmarks map[string]Series, status map[string]string, stderr io.Writer) {
	if c.input != "" {
		return
	}
	userData := perfstore.UserDataDir()
	if userData == "" {
		return
	}
	if err := perfstore.AppendBench(perfstore.BenchDir(userData), benchRows(c, m, kind, benchmarks, status, time.Now())); err != nil {
		fmt.Fprintf(stderr, "arbor-perf: could not add to the history: %v\n", err)
	}
}

var cleanVersion = regexp.MustCompile(`^[0-9A-Za-z.+-]+$`)

// treeVersion is the app version (mygo.json) and short commit of the tree.
func treeVersion(root string) (version, commit string) {
	if data, err := os.ReadFile(filepath.Join(root, "mygo.json")); err == nil {
		var meta struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &meta) == nil && cleanVersion.MatchString(meta.Version) {
			version = meta.Version
		}
	}
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	return version, commit
}
