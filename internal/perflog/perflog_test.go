package perflog

import (
	"arbor/perf"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesEventsAsLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	stop, err := Install(path)
	if err != nil {
		t.Fatal(err)
	}
	defer perf.SetSink(nil)

	sp := perf.Begin(perf.OpSwitchBranch)
	sp.SetSize(perf.SizeLarge)
	sp.Mark(perf.PhaseGitCheckout)
	sp.Mark(perf.PhaseLoadRepo)
	sp.End(perf.ResultError)
	sp2 := perf.Begin(perf.OpSearch)
	sp2.End(perf.ResultOK)
	stop()
	stop() // idempotent

	got := ReadFile(path)
	if len(got) != 2 {
		t.Fatalf("records = %+v", got)
	}
	r := got[0]
	if r.Op != "switch_branch" || r.Result != "error" || r.Size != "large" || r.StartNs == 0 || r.Ms < 0 {
		t.Errorf("record = %+v", r)
	}
	if len(r.Phases) != 2 || r.Phases[0].Phase != "git_checkout" || r.Phases[1].Phase != "load_repo" {
		t.Errorf("phases = %+v", r.Phases)
	}
	if got[1].Op != "search" || got[1].Result != "ok" {
		t.Errorf("second = %+v", got[1])
	}
}

// TestRecordHasOnlyClosedFields guards the privacy contract: a record is a fixed
// set of names, a size bucket and numbers.
func TestRecordHasOnlyClosedFields(t *testing.T) {
	b, err := json.Marshal(Record{Phases: []Phase{{}}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(b, &fields)
	allowed := map[string]bool{"op": true, "result": true, "size": true, "startNs": true, "ms": true, "phases": true}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("unexpected field %q", k)
		}
	}
}

func TestReadSkipsPartialLines(t *testing.T) {
	got := Read(strings.NewReader("{\"op\":\"open_file\",\"result\":\"ok\"}\n{\"op\":\"sea"))
	if len(got) != 1 || got[0].Op != "open_file" {
		t.Fatalf("records = %+v", got)
	}
}
