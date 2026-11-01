package perfreport

import (
	"arbor/internal/perfagg"
	"arbor/internal/perfstore"
	"arbor/perf"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func e(op string, t time.Time, ms float64) perfstore.Entry {
	return perfstore.Entry{T: t, Op: op, Ms: ms, Result: "ok", Size: "large", Version: "0.2.0",
		Phases: []perfstore.PhaseMs{{Name: "git_status", Ms: ms * 0.7}, {Name: "apply", Ms: ms * 0.3}}}
}

func model() *perfagg.Model {
	m := perfagg.NewModel(time.UTC)
	for i := range 100 { // frequent and over its 300 ms budget
		m.Add(e("commit", now.Add(-time.Duration(i)*time.Hour), 400))
	}
	for i := range 30 { // rare and far over
		m.Add(e("external_change", now.Add(-time.Duration(i)*time.Hour), 1500))
	}
	for i := range 50 { // within budget
		m.Add(e("open_file", now.Add(-time.Duration(i)*time.Hour), 20))
	}
	for i := range 5 { // too few to judge, though far over
		m.Add(e("switch_branch", now.Add(-time.Duration(i)*time.Hour), 9000))
	}
	return m
}

func TestEveryOperationHasATarget(t *testing.T) {
	for _, op := range perf.Ops() {
		if tg := Targets[op.String()]; tg.Code == "" || len(tg.Benchmarks) == 0 {
			t.Errorf("%s has no code or benchmark mapping", op)
		}
	}
}

func TestRankingIsFrequencyTimesBudgetExcess(t *testing.T) {
	r := Build(model(), 7, now, 0)
	var got []string
	for _, o := range r.Ops {
		got = append(got, o.Op)
	}
	// external_change: 30 × 500 = 15000; commit: 100 × 100 = 10000.
	want := "external_change commit open_file switch_branch"
	if strings.Join(got, " ") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
	if r.Ops[0].Dominant != "git_status" || !r.Ops[3].Thin || r.Ops[2].Score != 0 {
		t.Fatalf("ops = %+v", r.Ops)
	}
}

func TestReportIsDeterministicAndReadable(t *testing.T) {
	a, b := Build(model(), 7, now, 2), Build(model(), 7, now, 2)
	ja, _ := a.JSON()
	jb, _ := b.JSON()
	if string(ja) != string(jb) || a.Markdown() != b.Markdown() {
		t.Fatal("same data gave different reports")
	}
	md := a.Markdown()
	for _, want := range []string{"## external_change", "internal/app/live.go, internal/app/watch.go", "git_status", "switch_branch (thin)", "2 unreadable lines"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	empty := Build(perfagg.NewModel(time.UTC), 7, now, 0)
	if !strings.Contains(empty.Markdown(), "No data") {
		t.Fatal(empty.Markdown())
	}
}

func TestTrendComparesTheLastTwoVersions(t *testing.T) {
	m := perfagg.NewModel(time.UTC)
	add := func(version string, ms float64, ago time.Duration, n int) {
		for i := range n {
			x := e("search", now.Add(-ago+time.Duration(i)*time.Minute), ms)
			x.Version = version
			m.Add(x)
		}
	}
	add("0.1.0", 40, 90*24*time.Hour, 20)
	add("0.2.0", 20, 2*24*time.Hour, 20)
	add("0.3.0", 10, 24*time.Hour, 20)
	add("0.4.0", 99, time.Hour, 2) // too few to count
	o := Build(m, 7, now, 0).Ops[0]
	if o.Trend == nil || *o.Trend != -0.5 || o.TrendFrom != "0.2.0" || o.TrendTo != "0.3.0" {
		t.Fatalf("trend = %v %s to %s", o.Trend, o.TrendFrom, o.TrendTo)
	}
	if !strings.Contains(Build(m, 7, now, 0).Markdown(), "-50% (0.2.0 to 0.3.0)") {
		t.Fatal(Build(m, 7, now, 0).Markdown())
	}
}

func TestExportRoundTripAndCap(t *testing.T) {
	dir := t.TempDir()
	st := perfstore.Open(dir, perfstore.Options{Version: "0.2.0"})
	perf.SetSink(st.Sink())
	for range 3 {
		s := perf.Begin(perf.OpCommit)
		s.Mark(perf.PhaseGitWrite)
		s.End(perf.ResultOK)
	}
	perf.SetSink(nil)
	st.Close()

	x, err := BuildExport(dir, "darwin", "apple-m4", time.Now())
	if err != nil || x.Events != 3 || x.Truncated != 0 {
		t.Fatalf("export = %+v, %v", x, err)
	}
	data, err := x.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	for k := range top {
		switch k {
		case "format", "generated", "os", "cpu", "events", "truncated", "dropped", "entries":
		default:
			t.Errorf("unexpected export field %q", k)
		}
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	if bad, err := Load(path, func(e perfstore.Entry) {
		n++
		if e.Op != "commit" || len(e.Phases) != 1 {
			t.Errorf("entry = %+v", e)
		}
	}); err != nil || bad != 0 || n != 3 {
		t.Fatalf("load: n=%d bad=%d err=%v", n, bad, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "none"), func(perfstore.Entry) {}); err == nil {
		t.Fatal("missing input gave no error")
	}
	other := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(other, []byte(`{"format":"other"}`), 0o644)
	if _, err := Load(other, func(perfstore.Entry) {}); err == nil {
		t.Fatal("foreign file accepted")
	}
}
