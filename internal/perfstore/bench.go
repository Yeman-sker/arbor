package perfstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The benchmark history is the append-only record of every run of
// `arbor-perf record` and `arbor-perf compare`: one dated, versioned row per
// benchmark and run. It is what the Benchmarks source of the Performance window
// charts. The per-machine baseline in .perf/ stays what the ratchet judges
// against; the history only remembers.

// BenchFile is the history file inside BenchDir.
const BenchFile = "history.jsonl"

// Kinds of rows.
const (
	BenchRecord  = "record"
	BenchCompare = "compare"
)

// BenchRow is one benchmark of one run.
type BenchRow struct {
	T    time.Time `json:"t"`
	Kind string    `json:"kind"`
	// Name is the benchmark as go test names it, without "Benchmark" and the
	// -GOMAXPROCS suffix: "OpenFile/large".
	Name string `json:"name"`
	// Ns is the median ns/op of the runs; MinNs and MaxNs bound them.
	Ns     float64 `json:"ns"`
	MinNs  float64 `json:"minNs,omitempty"`
	MaxNs  float64 `json:"maxNs,omitempty"`
	Runs   int     `json:"runs,omitempty"`
	Allocs float64 `json:"allocs,omitempty"`
	Bytes  float64 `json:"bytes,omitempty"`
	// Version is the app version of the tree measured (mygo.json), Commit its
	// short Git commit when known, Machine the machine class and Fixture the
	// fixture version the numbers belong to.
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Machine string `json:"machine"`
	Fixture int    `json:"fixture,omitempty"`
	// Status is the verdict of a compare ("ok", "improved", "REGRESSION"...);
	// "recorded" for a record.
	Status string `json:"status,omitempty"`
}

// AppendBench adds rows to the history in dir, creating it. The rows go in one
// write, so a reader never sees half of a run.
func AppendBench(dir string, rows []BenchRow) error {
	if len(rows) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf []byte
	for _, r := range rows {
		r.T = r.T.UTC().Truncate(time.Second)
		r.Name = cleanBenchText(r.Name)
		r.Version = SafeVersion(r.Version)
		r.Commit = cleanBenchText(r.Commit)
		r.Machine = cleanBenchText(r.Machine)
		r.Status = cleanBenchText(r.Status)
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf = append(append(buf, b...), '\n')
	}
	f, err := os.OpenFile(filepath.Join(dir, BenchFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(buf)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// cleanBenchText keeps a benchmark name, machine key or status to printable
// ASCII without quotes, so that a row is always a readable line.
func cleanBenchText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ReadBench reads the history in dir, oldest row first as written. A history
// that is not there is empty, and a row that is garbled is skipped and counted.
func ReadBench(dir string) (rows []BenchRow, corrupt int, err error) {
	f, err := os.Open(filepath.Join(dir, BenchFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r BenchRow
		if json.Unmarshal([]byte(line), &r) != nil || r.Name == "" || r.T.Before(minTime) ||
			math.IsNaN(r.Ns) || math.IsInf(r.Ns, 0) || r.Ns < 0 {
			corrupt++
			continue
		}
		rows = append(rows, r)
	}
	return rows, corrupt, sc.Err()
}
