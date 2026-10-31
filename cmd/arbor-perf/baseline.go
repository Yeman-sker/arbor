package main

import (
	"arbor/internal/machine"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// BaselineVersion is the file format. A baseline also pins the fixture version
// it was recorded on: numbers from other fixtures are not comparable.
const BaselineVersion = 1

// Machine identifies the class of machine a baseline belongs to: see package machine.
type Machine = machine.Machine

// MachineKey builds the baseline file name from the CPU model and OS.
func MachineKey(cpu, goos, goarch string) string { return machine.Key(cpu, goos, goarch) }

// DetectMachine reads the CPU model of the machine it runs on.
func DetectMachine() Machine { return machine.Detect() }

// Baseline is what .perf/baselines/<machine>.json holds.
type Baseline struct {
	Version        int               `json:"version"`
	FixtureVersion int               `json:"fixtureVersion"`
	Machine        Machine           `json:"machine"`
	Go             string            `json:"go"`
	RecordedAt     string            `json:"recordedAt"`
	Benchmarks     map[string]Series `json:"benchmarks"`
}

// baselinePath is where the baseline of a machine lives, outside version control.
func baselinePath(root string, m Machine) string {
	return filepath.Join(root, ".perf", "baselines", m.Key+".json")
}

// LoadBaseline reads a baseline; a missing file is os.ErrNotExist.
func LoadBaseline(path string) (*Baseline, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bl Baseline
	if err := json.Unmarshal(b, &bl); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if bl.Version != BaselineVersion {
		return nil, fmt.Errorf("%s: baseline format %d, this tool reads %d", path, bl.Version, BaselineVersion)
	}
	return &bl, nil
}

// SaveBaseline writes the baseline atomically.
func SaveBaseline(path string, bl *Baseline) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".baseline-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func newBaseline(m Machine, fixtureVersion int, benchmarks map[string]Series, now time.Time) *Baseline {
	return &Baseline{
		Version: BaselineVersion, FixtureVersion: fixtureVersion, Machine: m,
		Go: runtime.Version(), RecordedAt: now.UTC().Format(time.RFC3339), Benchmarks: benchmarks,
	}
}

var errNoModule = errors.New("no go.mod found; run inside the arbor repository or pass -root")

// findRoot walks up from dir to the directory holding go.mod.
func findRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoModule
		}
		dir = parent
	}
}
