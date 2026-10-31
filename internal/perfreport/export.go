// Package perfreport turns the performance logs into what people and agents
// read: the export a user may choose to send, and the ranked report.
//
// An export holds log lines exactly as they were recorded, which only name
// catalog operations, phases, results, size buckets and app versions.
package perfreport

import (
	"arbor/internal/perfstore"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ExportFormat identifies the file format.
const ExportFormat = "arbor-perf-export/1"

// MaxExportEntries caps an export; the most recent operations are kept.
const MaxExportEntries = 50000

// Export is a performance export file.
type Export struct {
	Format    string    `json:"format"`
	Generated time.Time `json:"generated"`
	// OS and CPU are the machine class. There is no hostname or user name.
	OS  string `json:"os"`
	CPU string `json:"cpu"`
	// Events counts the operations in the logs, Truncated how many of the oldest
	// did not fit, and Dropped how many the recorder could not keep up with.
	Events    int   `json:"events"`
	Truncated int   `json:"truncated"`
	Dropped   int64 `json:"dropped"`
	// Entries are log lines.
	Entries []json.RawMessage `json:"entries"`
}

// BuildExport reads the logs in dir.
func BuildExport(dir, osName, cpu string, now time.Time) (*Export, error) {
	var entries []perfstore.Entry
	st, err := perfstore.ReadDir(dir, func(e perfstore.Entry) { entries = append(entries, e) })
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].T.Before(entries[j].T) })
	x := &Export{
		Format: ExportFormat, Generated: now.UTC().Truncate(time.Second),
		OS: osName, CPU: cpu, Events: len(entries), Dropped: int64(st.Dropped),
	}
	if len(entries) > MaxExportEntries {
		x.Truncated = len(entries) - MaxExportEntries
		entries = entries[x.Truncated:]
	}
	x.Entries = make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		x.Entries = append(x.Entries, e.AppendJSON(nil))
	}
	return x, nil
}

// Marshal is the file's content.
func (x *Export) Marshal() ([]byte, error) {
	return json.MarshalIndent(x, "", " ")
}

// ReadExport reads a file made by Marshal.
func ReadExport(path string) (*Export, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var x Export
	if err := json.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if x.Format != ExportFormat {
		return nil, fmt.Errorf("%s: not a performance export (format %q)", filepath.Base(path), x.Format)
	}
	return &x, nil
}

// Visit gives the entries of the export to visit, and how many lines it skipped.
func (x *Export) Visit(visit func(perfstore.Entry)) (bad int) {
	for _, raw := range x.Entries {
		e, _, ok := perfstore.ParseLine(raw)
		if !ok {
			bad++
			continue
		}
		visit(e)
	}
	return bad
}

// Load reads operations from path, a log directory or an export file.
func Load(path string, visit func(perfstore.Entry)) (bad int, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("no performance data at %s", path)
		}
		return 0, err
	}
	if info.IsDir() {
		st, err := perfstore.ReadDir(path, visit)
		return int(st.Corrupt), err
	}
	x, err := ReadExport(path)
	if err != nil {
		return 0, err
	}
	return x.Visit(visit), nil
}
