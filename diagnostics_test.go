package main

import (
	"arbor/internal/machine"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"arbor/perf"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakePrompts answers the dialogs at once.
type fakePrompts struct {
	enable bool
	off    diagOff
	save   string
	asked  int
}

func (f *fakePrompts) ConfirmEnable(done func(bool))        { f.asked++; done(f.enable) }
func (f *fakePrompts) ConfirmDisable(done func(diagOff))    { f.asked++; done(f.off) }
func (f *fakePrompts) SavePath(_ string, done func(string)) { f.asked++; done(f.save) }

func newDiag(t *testing.T) (*diagnostics, *fakePrompts, string) {
	t.Helper()
	dir := t.TempDir()
	fp := &fakePrompts{enable: true, off: diagOffKeep}
	d := newDiagnostics(newSettingsStore(filepath.Join(dir, "settings.json")), dir, "0.2.0", fp)
	t.Cleanup(func() { d.Close(); perf.SetSink(nil) })
	return d, fp, dir
}

// quietWindow is a window nothing is published to.
type quietWindow struct{ fakeWindow }

func (w *quietWindow) Update(func()) {}

func logBytes(t *testing.T, dir string) []byte {
	t.Helper()
	var all []byte
	_ = filepath.Walk(perfstore.LogDir(dir), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(path)
			all = append(all, data...)
		}
		return nil
	})
	return all
}

func recordOne(op perf.Op) {
	s := perf.Begin(op)
	s.End(perf.ResultOK)
}

func TestDiagnosticsAreOffUntilAllowed(t *testing.T) {
	d, fp, dir := newDiag(t)
	d.Start()
	if perf.Enabled() || d.Enabled() || d.OpenWindow() != nil {
		t.Fatal("diagnostics on without being allowed")
	}
	if len(d.menuItems()) != 1 {
		t.Fatalf("the window and export are offered while off: %d items", len(d.menuItems()))
	}
	recordOne(perf.OpCommit)
	if len(logBytes(t, dir)) != 0 {
		t.Fatal("recorded while off")
	}
	fp.enable = false
	d.Toggle()
	if d.Enabled() || perf.Enabled() {
		t.Fatal("cancelling the dialog turned it on")
	}
	if _, err := os.Stat(perfstore.LogDir(dir)); err == nil {
		t.Fatal("a log directory exists though nothing was allowed")
	}
}

func TestDiagnosticsRecordThenKeepOrDelete(t *testing.T) {
	d, fp, dir := newDiag(t)
	d.Toggle()
	if !d.Enabled() || !perf.Enabled() || fp.asked != 1 {
		t.Fatalf("enabled=%v sink=%v asked=%d", d.Enabled(), perf.Enabled(), fp.asked)
	}
	if len(d.menuItems()) != 3 {
		t.Fatalf("menu items = %d", len(d.menuItems()))
	}
	recordOne(perf.OpCommit)

	fp.off = diagOffCancel
	d.Toggle()
	if !d.Enabled() || !perf.Enabled() {
		t.Fatal("cancelling the dialog turned it off")
	}

	fp.off = diagOffKeep
	d.Toggle()
	if d.Enabled() || perf.Enabled() {
		t.Fatal("still on")
	}
	if !bytes.Contains(logBytes(t, dir), []byte(`"op":"commit"`)) {
		t.Fatalf("log = %q", logBytes(t, dir))
	}
	recordOne(perf.OpStage)
	if bytes.Contains(logBytes(t, dir), []byte(`"op":"stage"`)) {
		t.Fatal("recorded after turning off")
	}

	// The setting survives a restart, and recording resumes with it.
	d.Toggle()
	d2 := newDiagnostics(newSettingsStore(filepath.Join(dir, "settings.json")), dir, "0.2.0", fp)
	perf.SetSink(nil)
	d.uninstall()
	d2.Start()
	if !perf.Enabled() {
		t.Fatal("not resumed at launch")
	}
	d2.Close()
	d.install()

	fp.off = diagOffDelete
	d.Toggle()
	if _, err := os.Stat(perfstore.LogDir(dir)); !os.IsNotExist(err) {
		t.Fatalf("delete left the data: %v", err)
	}
}

// TestPrivacySentinel drives real flows in a repository whose every name holds a
// sentinel, including failing Git operations, and looks for it in what is written.
func TestPrivacySentinel(t *testing.T) {
	const sentinel = "ZZSENTINELZZ"
	d, _, dir := newDiag(t)
	d.Toggle()

	repo := newTestRepo(t, map[string]string{sentinel + ".txt": "first\n"})
	mustGit(t, repo, "config", "user.name", sentinel)
	mustGit(t, repo, "config", "user.email", sentinel+"@example.com")
	mustGit(t, repo, "checkout", "-q", "-b", "feature/"+sentinel)
	writeFile(t, repo, sentinel+".txt", "second "+sentinel+"\n")
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-q", "-m", "subject "+sentinel+"\n\nbody "+sentinel)
	mustGit(t, repo, "checkout", "-q", "-")
	writeFile(t, repo, sentinel+".txt", "dirty "+sentinel+"\n")

	a := newApp(repo)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	a.switchMode(modeGit)
	tt.Frame()
	a.checkoutBranch("feature/"+sentinel, false) // refused: local changes conflict
	tt.Frame()
	a.checkoutBranch("missing/"+sentinel, false) // fails: no such branch
	tt.Frame()
	a.reload("")
	tt.Frame()
	a.switchMode(modeFiles)
	tt.Frame()

	if !perf.Enabled() {
		t.Fatal("sink is gone")
	}
	d.store.Flush()
	log := logBytes(t, dir)
	if !bytes.Contains(log, []byte(`"switch_branch"`)) || !bytes.Contains(log, []byte(`"result":"error"`)) {
		t.Fatalf("the flows were not recorded, so this proves nothing: %s", log)
	}
	if bytes.Contains(log, []byte(sentinel)) {
		t.Fatalf("user data in the log: %s", log)
	}
	data, x, err := d.export()
	if err != nil || x.Events == 0 {
		t.Fatalf("export: %+v, %v", x, err)
	}
	if bytes.Contains(data, []byte(sentinel)) {
		t.Fatalf("user data in the export: %s", data)
	}
	if host, _ := os.Hostname(); host != "" && len(host) > 3 && bytes.Contains(data, []byte(`"`+host+`"`)) {
		t.Fatal("hostname in the export")
	}
}

func seedPerfLog(t *testing.T, dir string, now time.Time) {
	t.Helper()
	logs := perfstore.LogDir(dir)
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	day := func(i int) time.Time { return now.AddDate(0, 0, -i) }
	for i := 29; i >= 0; i-- {
		version := "0.1.0"
		if i < 20 {
			version = "0.2.0"
		}
		if i < 10 {
			version = "0.3.0"
		}
		ops := []struct {
			op   string
			base float64
		}{{"open_file", 160}, {"commit", 520}, {"switch_branch", 700}, {"mode_switch", 3}, {"expand_commit", 90}, {"external_change", 900}}
		for _, o := range ops {
			if o.op == "expand_commit" && version == "0.2.0" {
				continue // a gap
			}
			for s := range 12 {
				e := perfstore.Entry{T: day(i).Add(time.Duration(s) * time.Minute), Op: o.op,
					Ms: (o.base*0.4 + o.base*0.6*float64(i)/29) * (1 + float64(s)/10), Result: "ok", Size: "large", Version: version,
					Phases: []perfstore.PhaseMs{{Name: "git_status", Ms: o.base * 0.3}, {Name: "apply", Ms: o.base * 0.1}}}
				b.Write(e.AppendJSON(nil))
				b.WriteByte('\n')
			}
		}
	}
	if err := os.WriteFile(filepath.Join(logs, perfstore.LogName(now)), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func perfTester(t *testing.T, dir string, now time.Time) (*perfView, *ui.Tester) {
	t.Helper()
	p := newPerfView(dir)
	p.now = func() time.Time { return now }
	p.exportReport = func() ([]byte, *perfreport.Export, error) {
		x, err := perfreport.BuildExport(perfstore.LogDir(dir), "darwin/arm64", "Apple M4", now)
		if err != nil {
			return nil, nil, err
		}
		data, err := x.Marshal()
		return data, x, err
	}
	p.reload()
	tt := ui.NewTester(p.view, 1120, 780)
	tt.Frame()
	return p, tt
}

func TestPerformanceWindowCards(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	seedPerfLog(t, dir, now)
	p, tt := perfTester(t, dir, now)

	if len(p.cards) != len(perf.Ops()) {
		t.Fatalf("%d cards for %d operations", len(p.cards), len(perf.Ops()))
	}
	for _, want := range []string{"open_file", "commit", "new_window", "No data"} {
		if !hasTextContaining(tt, want) {
			t.Errorf("no %q in %v", want, tt.Texts())
		}
	}
	var expand *trendCard
	for i := range p.cards {
		if p.cards[i].Key == "expand_commit" {
			expand = &p.cards[i]
		}
	}
	pts := expand.Points
	if len(pts) != 3 || pts[0].Label != "0.1.0" || pts[0].Gap || !pts[1].Gap || pts[2].Label != "0.3.0" || pts[2].Gap {
		t.Fatalf("points = %+v", pts)
	}
	if !expand.HasChange || expand.Change >= 0 || expand.Base != "0.1.0" || expand.Build != "0.3.0" {
		t.Fatalf("change=%v %v from %s to %s", expand.HasChange, expand.Change, expand.Base, expand.Build)
	}
	for _, want := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		if !tt.HasText(want) {
			t.Errorf("no %q along the axis in %v", want, tt.Texts())
		}
	}
	if !hasTextContaining(tt, "since 0.1.0") {
		t.Fatalf("the change does not say from where: %v", tt.Texts())
	}
	if !hasTextContaining(tt, "budget") {
		t.Fatal("no budget standing on any card")
	}

	// The pointer over a chart says what is there.
	r, ok := tt.Find("open_file")
	if !ok {
		t.Fatal("no card")
	}
	tt.Move(r.X+200, r.Y+90)
	tt.Frame()
	if !hasTextContaining(tt, "p95") {
		t.Fatalf("hover shows nothing: %v", tt.Texts())
	}

	// A card opens its detail: phases and slowest samples.
	tt.ClickAt(r.X+200, r.Y+6)
	tt.Frame()
	if p.detail != "open_file" || !tt.HasText("On 0.3.0") || !tt.HasText("Phases") || !tt.HasText("Slowest samples") || !tt.HasText("git_status") {
		t.Fatalf("detail=%q %v", p.detail, tt.Texts())
	}
	if err := tt.Click("All operations"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.detail != "" {
		t.Fatal("did not go back")
	}

	// Filters rebuild the cards.
	if err := tt.Click("Last 5"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.last != 5 || len(p.cards[0].Points) != 3 {
		t.Fatalf("last=%d points=%d", p.last, len(p.cards[0].Points))
	}
	if err := tt.Click("Small"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.total() != 0 {
		t.Fatalf("small has %d samples", p.total())
	}
	if !hasTextContaining(tt, "No operations recorded") {
		t.Fatalf("no empty note: %v", tt.Texts())
	}
}

func TestPerformanceWindowClimb(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	key := machine.Detect().Key
	ledger := filepath.Join(perfstore.BenchDir(dir), perfstore.RoundsFile)
	for _, r := range []perfstore.Round{
		{T: now.AddDate(0, 0, -3), Kind: perfstore.RoundTry, Op: "open_file", Note: "read less of the tree", Commit: "bbbbbbb", Base: "aaaaaaa", Machine: key, Verdict: perfstore.VerdictKeep, Reason: "train_and_holdout_improved",
			Target: []perfstore.RoundBench{{Name: "OpenFile/large", BaseNs: 4e7, CurNs: 3e7}, {Name: "OpenFile/small", BaseNs: 2e6, CurNs: 2e6}}},
		{T: now.AddDate(0, 0, -2), Kind: perfstore.RoundTry, Op: "open_file", Note: "cache everything", Commit: "ccccccc", Base: "bbbbbbb", Machine: key, Verdict: perfstore.VerdictRevert, Reason: "train_improved_holdout_flat",
			Target: []perfstore.RoundBench{{Name: "OpenFile/large", BaseNs: 3e7, CurNs: 1e7}}},
		{T: now.AddDate(0, 0, -1), Kind: perfstore.RoundTry, Op: "open_file", Note: "skip the sort", Commit: "ddddddd", Base: "bbbbbbb", Machine: key, Verdict: perfstore.VerdictKeep, Reason: "train_and_holdout_improved",
			Target: []perfstore.RoundBench{{Name: "OpenFile/large", BaseNs: 3e7, CurNs: 2e7}}},
		{T: now, Kind: perfstore.RoundTry, Op: "search", Commit: "eeeeeee", Base: "ddddddd", Machine: "elsewhere", Verdict: perfstore.VerdictKeep,
			Target: []perfstore.RoundBench{{Name: "Frame/search/large", BaseNs: 3e7, CurNs: 2e7}}},
		{T: now, Kind: perfstore.RoundMeasurement, Op: "external_change", Note: "spans waited for a window out of sight"},
	} {
		if err := perfstore.AppendRound(ledger, r); err != nil {
			t.Fatal(err)
		}
	}
	p, tt := perfTester(t, dir, now)
	if err := tt.Click("Climb"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var large *trendCard
	for i := range p.cards {
		if p.cards[i].Key == "OpenFile/large" {
			large = &p.cards[i]
		}
		if p.cards[i].Key == "Frame/search/large" {
			t.Fatal("a round of another machine class is not comparable")
		}
	}
	if large == nil || len(large.Points) != 3 || large.Current != 20 || large.Base != "aaaaaaa" || large.Build != "ddddddd" || large.Change != -0.5 {
		t.Fatalf("card = %+v", large)
	}
	if !strings.HasPrefix(p.info, "2 kept of 4") {
		t.Fatalf("info = %q", p.info)
	}
	// What was thrown away is on show too, with why.
	for _, want := range []string{"Rounds", "cache everything", "revert · train improved holdout flat", "spans waited for a window out of sight"} {
		if !hasTextContaining(tt, want) {
			t.Errorf("no %q in %v", want, tt.Texts())
		}
	}
	if err := tt.Click("Small"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(p.cards) != 1 || p.cards[0].Key != "OpenFile/small" {
		t.Fatalf("small = %+v", p.cards)
	}

	empty, emptyTester := perfTester(t, t.TempDir(), now)
	if err := emptyTester.Click("Climb"); err != nil {
		t.Fatal(err)
	}
	emptyTester.Frame()
	if len(empty.cards) != 0 || !hasTextContaining(emptyTester, "climb next") {
		t.Fatalf("no hint: %v", emptyTester.Texts())
	}
}

func TestBuildTicksFitAndKeepTheNewest(t *testing.T) {
	var points []trendPoint
	for i := range 40 {
		points = append(points, trendPoint{Label: fmt.Sprintf("0.%d.0", i)})
	}
	ticks := buildTicks(points, 270)
	if len(ticks) > 6 || len(ticks) < 3 || ticks[len(ticks)-1].Label != "0.39.0" {
		t.Fatalf("ticks = %+v", ticks)
	}
	if ticks[0].At >= ticks[1].At || ticks[len(ticks)-1].At != slotX(39, 40) {
		t.Fatalf("ticks out of order: %+v", ticks)
	}
	if one := buildTicks(points[:1], 270); len(one) != 1 || one[0].At != 0.5 {
		t.Fatalf("one = %+v", one)
	}
	if none := buildTicks(nil, 270); len(none) != 0 {
		t.Fatalf("none = %+v", none)
	}
}

func TestCommitVersion(t *testing.T) {
	const rev = "5e6b2964bf35aaaa"
	if got := commitVersion([]debug.BuildSetting{{Key: "vcs.revision", Value: rev}, {Key: "vcs.modified", Value: "false"}}); got != "5e6b296" {
		t.Fatal(got)
	}
	got := commitVersion([]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}, {Key: "vcs.revision", Value: rev}})
	if got != "5e6b296+dirty" || perfstore.SafeVersion(got) != got {
		t.Fatalf("%q is not kept as %q", got, perfstore.SafeVersion(got))
	}
	if got := commitVersion(nil); got != "" {
		t.Fatal(got)
	}
	if got := buildVersion("0.3.0"); got != "0.3.0" {
		t.Fatal(got)
	}
}

func TestExportPreviewThenSave(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	seedPerfLog(t, dir, now)
	p, tt := perfTester(t, dir, now)
	out := filepath.Join(t.TempDir(), "report.json")
	asked := 0
	p.saveExport = func(name string, done func(string)) {
		asked++
		if name != "arbor-performance-2026-10-31.json" {
			t.Errorf("name = %q", name)
		}
		done(out)
	}
	p.beginExport()
	tt.Frame()
	if !p.previewOpen || !hasTextContaining(tt, "operations,") || !hasTextContaining(tt, "arbor-perf-export/1") {
		t.Fatalf("preview: %v", tt.Texts())
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("saved before the user asked")
	}
	if err := tt.Click("Save…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if asked != 1 || p.previewOpen {
		t.Fatalf("asked=%d open=%v", asked, p.previewOpen)
	}
	x, err := perfreport.ReadExport(out)
	if err != nil || x.Events == 0 {
		t.Fatalf("saved: %+v, %v", x, err)
	}
}

func TestPerformanceWindowLifecycle(t *testing.T) {
	d, _, _ := newDiag(t)
	d.Toggle()
	var w *quietWindow
	d.newWindow = func(func(*ui.Context)) hostWindow {
		w = &quietWindow{}
		return w
	}
	p := d.OpenWindow()
	if p == nil || d.OpenWindow() != p {
		t.Fatal("not one window")
	}
	w.Close()
	if d.window != nil || !p.closed {
		t.Fatal("window not forgotten")
	}
	p2 := d.OpenWindow()
	fp := d.prompts.(*fakePrompts)
	fp.off = diagOffKeep
	d.Toggle()
	if !p2.closed || d.window != nil {
		t.Fatalf("window left open after turning off: %v", p2.closed)
	}
}
