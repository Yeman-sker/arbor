package perfstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The ledger is the memory of the climb: one line for every round an agent tried,
// kept or not, and for what it learned about the measuring. It lives in the
// repository (LedgerPath), so the next agent starts from it, and is copied to
// RoundsFile in BenchDir for the Performance window. It holds benchmark numbers
// and an agent's own notes, and nothing of anyone's usage.

// LedgerPath is the ledger, relative to the repository root.
const LedgerPath = "perf/ledger.jsonl"

// RoundsFile is the window's copy of the ledger, inside BenchDir.
const RoundsFile = "rounds.jsonl"

// Kinds of ledger lines.
const (
	// RoundTry is a change judged by arbor-perf climb try.
	RoundTry = "round"
	// RoundMeasurement says the operation's numbers measured something else
	// until now, so field data from before T does not count.
	RoundMeasurement = "measurement"
	// RoundClassified says why the rounds before it failed, ending a stall.
	RoundClassified = "classified"
)

// Verdicts of a round.
const (
	VerdictKeep         = "keep"
	VerdictRevert       = "revert"
	VerdictInconclusive = "inconclusive"
)

// Round is one line of the ledger.
type Round struct {
	T    time.Time `json:"t"`
	Kind string    `json:"kind"`
	Op   string    `json:"op"`
	// Note is the hypothesis of a round, or what was found.
	Note string `json:"note,omitempty"`
	// Commit was measured against Base, on Machine.
	Commit  string `json:"commit,omitempty"`
	Base    string `json:"base,omitempty"`
	Machine string `json:"machine,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	// Reason is a stable name for why: see cmd/arbor-perf/climb.go.
	Reason string `json:"reason,omitempty"`
	// Target is what the round aimed at, and Holdout how the held-out fixture
	// went: faster, flat, slower or inconclusive.
	Target      []RoundBench `json:"target,omitempty"`
	Holdout     string       `json:"holdout,omitempty"`
	Regressions []string     `json:"regressions,omitempty"`
}

// RoundBench is one benchmark of a round.
type RoundBench struct {
	Name     string  `json:"name"`
	BaseNs   float64 `json:"baseNs"`
	CurNs    float64 `json:"curNs"`
	DeltaPct float64 `json:"deltaPct"`
	NoisePct float64 `json:"noisePct"`
	Verdict  string  `json:"verdict"`
}

// AppendRound adds r to the file at path, creating it.
func AppendRound(path string, r Round) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ReadRounds reads the file at path, oldest first. A file that is not there is
// empty, and a garbled line is skipped.
func ReadRounds(path string) ([]Round, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Round
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Round
		if json.Unmarshal([]byte(line), &r) == nil && r.Kind != "" && r.Op != "" {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}
