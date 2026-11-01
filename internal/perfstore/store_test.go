package perfstore

import (
	"arbor/perf"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func event(op perf.Op, start time.Time, dur time.Duration, r perf.Result, size perf.Size, phases ...perf.PhaseTime) perf.Event {
	e := perf.Event{Op: op, Result: r, Size: size, Start: start, Dur: dur}
	copy(e.Phases[:], phases)
	e.NPhases = uint8(len(phases))
	return e
}

var day1 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func readAll(t *testing.T, dir string) ([]Entry, ReadStats) {
	t.Helper()
	var got []Entry
	st, err := ReadDir(dir, func(e Entry) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	return got, st
}

func TestLineFormat(t *testing.T) {
	e := Entry{
		T: day1, Op: "switch_branch", Ms: 612, Result: "ok", Size: "large", Version: "0.2.0",
		Phases: []PhaseMs{{"git_checkout", 480}, {"load_repo", 95}, {"apply", 21}},
	}
	want := `{"t":"2026-10-31T12:00:00Z","op":"switch_branch","ms":612,"phases":{"git_checkout":480,"load_repo":95,"apply":21},"result":"ok","size":"large","version":"0.2.0"}`
	if got := string(e.AppendJSON(nil)); got != want {
		t.Fatalf("line =\n%s\nwant\n%s", got, want)
	}
	back, _, ok := ParseLine([]byte(want))
	if !ok || back.Op != "switch_branch" || back.Ms != 612 || back.Size != "large" || back.Version != "0.2.0" || !back.T.Equal(day1) {
		t.Fatalf("parsed = %+v ok=%v", back, ok)
	}
	if len(back.Phases) != 3 || back.Phases[0] != (PhaseMs{"git_checkout", 480}) {
		t.Fatalf("phases = %+v", back.Phases)
	}
}

func TestLineRoundsToMicrosecondsAndMergesRepeatedPhases(t *testing.T) {
	e := Entry{T: day1.Add(900 * time.Millisecond), Op: "open_file", Ms: 12.34567891, Result: "ok", Size: "small", Version: "1",
		Phases: []PhaseMs{{"apply", 1}, {"load_file", 2}, {"apply", 3.5}}}
	line := string(e.AppendJSON(nil))
	if !strings.Contains(line, `"ms":12.346`) || !strings.Contains(line, `"apply":4.5`) || strings.Count(line, `"apply"`) != 1 {
		t.Fatalf("line = %s", line)
	}
	if !strings.Contains(line, `"t":"2026-10-31T12:00:00Z"`) {
		t.Fatalf("time not truncated to the second: %s", line)
	}
}

// The encoder only ever writes safe tokens, whatever it is handed.
func TestEncoderWritesOnlySafeTokens(t *testing.T) {
	const sentinel = "/Users/someone/secret-repo \"quoted\"\nnewline"
	e := Entry{T: day1, Op: sentinel, Ms: 1, Result: sentinel, Size: sentinel, Version: sentinel, Phases: []PhaseMs{{sentinel, 1}}}
	line := string(e.AppendJSON(nil))
	for _, bad := range []string{"Users", "secret", "quoted", "newline", "/", " "} {
		if strings.Contains(line, bad) {
			t.Fatalf("line leaks %q: %s", bad, line)
		}
	}
	if _, _, ok := ParseLine([]byte(line)); !ok {
		t.Fatalf("the cleaned line does not parse: %s", line)
	}
	if SafeVersion("") != "dev" || SafeVersion("0.1.0-rc.1+build5") != "0.1.0-rc.1+build5" || SafeVersion("a b") != "_" {
		t.Fatalf("SafeVersion wrong")
	}
}

func TestParseToleratesBadLines(t *testing.T) {
	good := `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":5,"result":"ok","size":"small","version":"1"}`
	for name, line := range map[string]string{
		"empty":         ``,
		"cut off":       good[:40],
		"not json":      `hello`,
		"array":         `[1,2]`,
		"no op":         `{"t":"2026-10-31T12:00:00Z","ms":5}`,
		"no ms":         `{"t":"2026-10-31T12:00:00Z","op":"open_file"}`,
		"negative ms":   `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":-3}`,
		"absurd ms":     `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":1e30}`,
		"bad time":      `{"t":"yesterday","op":"open_file","ms":3}`,
		"ancient time":  `{"t":"1970-01-01T00:00:00Z","op":"open_file","ms":3}`,
		"string ms":     `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":"3"}`,
		"binary":        "\x00\x01\xff\xfe",
		"unterminated":  `{"t":"2026-10-31T12:00:00Z","op":"open_file`,
		"bad phase":     `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":3,"phases":{"a":"x"}}`,
		"object ms":     `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":{}}`,
		"trailing junk": `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":3,"phases":{`,
	} {
		if _, _, ok := ParseLine([]byte(line)); ok {
			t.Errorf("%s: parsed as an event", name)
		}
	}
	// Unknown keys, any order, escapes and extra whitespace are fine.
	for name, line := range map[string]string{
		"reordered":    `{"version":"1","size":"small","result":"ok","ms":5,"op":"open_file","t":"2026-10-31T12:00:00Z"}`,
		"extra keys":   `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":5,"future":{"a":[1,2,{"b":"}"}]},"x":null,"y":true}`,
		"spaces":       ` { "t" : "2026-10-31T12:00:00Z" , "op" : "open_file" , "ms" : 5 } `,
		"escaped":      `{"t":"2026-10-31T12:00:00Z","op":"open_file","ms":5}`,
		"fractional s": `{"t":"2026-10-31T12:00:00.250Z","op":"open_file","ms":5}`,
	} {
		e, _, ok := ParseLine([]byte(line))
		if !ok || e.Op != "open_file" || e.Ms != 5 {
			t.Errorf("%s: %+v ok=%v", name, e, ok)
		}
	}
	// What a file says is cleaned to safe tokens, never trusted.
	e, _, ok := ParseLine([]byte(`{"t":"2026-10-31T12:00:00Z","op":"/etc/passwd","ms":5,"result":"a b","version":"x/y"}`))
	if !ok || e.Op != "_" || e.Result != "_" || e.Version != "_" {
		t.Errorf("unsafe strings not cleaned: %+v", e)
	}
	// A line of lost events.
	_, d, ok := ParseLine([]byte(`{"t":"2026-10-31T12:00:00Z","dropped":7,"version":"1"}`))
	if !ok || d != 7 {
		t.Errorf("dropped line = %d ok=%v", d, ok)
	}
}

func TestReadSkipsCorruptLinesAndCountsThem(t *testing.T) {
	dir := t.TempDir()
	good := Entry{T: day1, Op: "open_file", Ms: 5, Result: "ok", Size: "small", Version: "1"}
	var b bytes.Buffer
	b.Write(good.AppendJSON(nil))
	b.WriteString("\n{garbage\n\n")
	b.Write(good.AppendJSON(nil))
	b.WriteString("\n")
	b.WriteString(`{"t":"2026-10-31T12:00:00Z","dropped":3}` + "\n")
	b.Write(good.AppendJSON(nil)[:30]) // a write cut off by a crash, no newline
	if err := os.WriteFile(filepath.Join(dir, LogName(day1)), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, st := readAll(t, dir)
	if len(got) != 2 || st.Events != 2 || st.Corrupt != 2 || st.Dropped != 3 || st.Files != 1 {
		t.Fatalf("got %d events, stats %+v", len(got), st)
	}
	// A missing directory is an empty log.
	if got, st := readAll(t, filepath.Join(dir, "missing")); len(got) != 0 || st.Files != 0 {
		t.Fatalf("missing dir: %v %+v", got, st)
	}
}

func TestStoreWritesRotatesAndReadsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "perf")
	s := Open(dir, Options{Version: "0.2.0"})
	sink := s.Sink()
	d2 := day1.Add(24 * time.Hour)
	sink(event(perf.OpOpenFile, day1, 27*time.Millisecond, perf.ResultOK, perf.SizeSmall, perf.PhaseTime{Phase: perf.PhaseLoadFile, Dur: 20 * time.Millisecond}))
	sink(event(perf.OpCommit, day1.Add(time.Minute), 400*time.Millisecond, perf.ResultError, perf.SizeLarge))
	sink(event(perf.OpSwitchBranch, d2, 600*time.Millisecond, perf.ResultOK, perf.SizeMedium,
		perf.PhaseTime{Phase: perf.PhaseGitCheckout, Dur: 480 * time.Millisecond}, perf.PhaseTime{Phase: perf.PhaseLoadRepo, Dur: 95 * time.Millisecond}))
	s.Flush()

	files, err := LogFiles(dir)
	if err != nil || len(files) != 2 || filepath.Base(files[0]) != "perf-2026-10-31.jsonl" || filepath.Base(files[1]) != "perf-2026-11-01.jsonl" {
		t.Fatalf("files = %v err=%v", files, err)
	}
	got, st := readAll(t, dir)
	if len(got) != 3 || st.Corrupt != 0 {
		t.Fatalf("read %d events, %+v", len(got), st)
	}
	if got[0].Op != "open_file" || got[0].Version != "0.2.0" || got[0].Ms != 27 || got[0].Phases[0].Name != "load_file" || got[0].Phases[0].Ms != 20 {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Result != "error" || got[1].Size != "large" || got[2].Op != "switch_branch" || got[2].Phases[1].Name != "load_repo" {
		t.Errorf("events = %+v", got)
	}
	if fi, _ := os.Stat(files[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	s.Close()
	s.Close() // twice is fine
	if st := s.Stats(); st.Written != 3 || st.Dropped != 0 || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}
	// After Close the sink is a no-op.
	sink(event(perf.OpOpenFile, day1, time.Millisecond, perf.ResultOK, perf.SizeSmall))
	s.Flush()
}

func TestCloseWritesWhatIsQueued(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, Options{Version: "1"})
	for i := 0; i < 1000; i++ {
		s.Sink()(event(perf.OpSearch, day1.Add(time.Duration(i)*time.Second), time.Millisecond, perf.ResultOK, perf.SizeSmall))
	}
	s.Close()
	got, _ := readAll(t, dir)
	if len(got)+int(s.Stats().Dropped) != 1000 || len(got) == 0 {
		t.Fatalf("read %d, dropped %d", len(got), s.Stats().Dropped)
	}
}

// A full queue drops events instead of blocking the caller, and the log says how many.
func TestFullQueueDropsAndCounts(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	first := true
	s := Open(dir, Options{Version: "1", QueueLen: 8, beforeWrite: func() {
		if first { // hold the writer inside its first write while the queue fills
			first = false
			entered <- struct{}{}
			<-gate
		}
	}})
	sink := s.Sink()
	sink(event(perf.OpSearch, day1, time.Millisecond, perf.ResultOK, perf.SizeSmall))
	<-entered
	const n = 100
	start := time.Now()
	for i := 0; i < n; i++ {
		sink(event(perf.OpSearch, day1, time.Millisecond, perf.ResultOK, perf.SizeSmall))
	}
	if time.Since(start) > time.Second {
		t.Fatalf("recording %d events into a full queue took %v: the sink must not block", n, time.Since(start))
	}
	close(gate)
	s.Close()
	st := s.Stats()
	if st.Dropped != n-8 || st.Written != 1+8 {
		t.Fatalf("stats = %+v, want 92 dropped and 9 written", st)
	}
	got, rs := readAll(t, dir)
	if len(got) != 9 || rs.Dropped != n-8 {
		t.Fatalf("read %d events and %d dropped from the log, want 9 and 92", len(got), rs.Dropped)
	}
}

func TestStoreSurvivesAnUnwritableDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(filepath.Join(blocker, "perf"), Options{Version: "1"}) // its parent is a file
	for i := 0; i < 50; i++ {
		s.Sink()(event(perf.OpOpenFile, day1, time.Millisecond, perf.ResultOK, perf.SizeSmall))
	}
	s.Flush()
	s.Close()
	if st := s.Stats(); st.Written != 0 || st.Failed == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestStoreIsSafeForConcurrentSinksAndFlushes(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, Options{Version: "1"})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s.Sink()(event(perf.OpStage, day1, time.Millisecond, perf.ResultOK, perf.SizeSmall))
				if i%50 == 0 {
					s.Flush()
				}
			}
		}()
	}
	wg.Wait()
	s.Close()
	got, st := readAll(t, dir)
	if st.Corrupt != 0 || len(got) != int(s.Stats().Written) {
		t.Fatalf("read %d (corrupt %d), written %d", len(got), st.Corrupt, s.Stats().Written)
	}
}

func writeLog(t *testing.T, dir string, day time.Time, size int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, LogName(day))
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	files, _ := LogFiles(dir)
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "perf-"), ".jsonl"))
	}
	return out
}

func TestRetentionByAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	for _, back := range []int{0, 1, 29, 30, 31, 45, 100} {
		writeLog(t, dir, now.AddDate(0, 0, -back), 10)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("not a log"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed := Trim(dir, now, 30*24*time.Hour, 1<<30, "2026-10-31")
	got := names(t, dir)
	want := []string{"2026-10-01", "2026-10-02", "2026-10-30", "2026-10-31"}
	if fmt.Sprint(got) != fmt.Sprint(want) || removed != 3 {
		t.Fatalf("kept %v (removed %d), want %v", got, removed, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal("trim touched a file that is not a log")
	}
}

func TestRetentionBySizeRemovesTheOldestFirstAndKeepsToday(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	for back := 5; back >= 0; back-- {
		writeLog(t, dir, now.AddDate(0, 0, -back), 100)
	}
	// 600 bytes against a cap of 350: the three oldest go.
	Trim(dir, now, 30*24*time.Hour, 350, "2026-10-31")
	want := []string{"2026-10-29", "2026-10-30", "2026-10-31"}
	if got := names(t, dir); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	// A cap smaller than one day still leaves the day being written.
	Trim(dir, now, 30*24*time.Hour, 10, "2026-10-31")
	if got := names(t, dir); fmt.Sprint(got) != "[2026-10-31]" {
		t.Fatalf("kept %v, want only today", got)
	}
}

func TestStoreAppliesRetentionWhenItOpens(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	writeLog(t, dir, now.AddDate(0, 0, -400), 10)
	writeLog(t, dir, now.AddDate(0, 0, -2), 10)
	s := Open(dir, Options{Version: "1", Now: func() time.Time { return now }})
	s.Flush()
	s.Close()
	if got := names(t, dir); fmt.Sprint(got) != "[2026-10-29]" {
		t.Fatalf("kept %v", got)
	}
}

func TestDeleteRemovesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "perf")
	s := Open(dir, Options{Version: "1"})
	s.Sink()(event(perf.OpOpenFile, day1, time.Millisecond, perf.ResultOK, perf.SizeSmall))
	s.Close()
	if info := Info(dir); info.Files != 1 || info.Bytes == 0 {
		t.Fatalf("info = %+v", info)
	}
	if err := Delete(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory is still there: %v", err)
	}
	if err := Delete(dir); err != nil {
		t.Fatalf("deleting what is gone: %v", err)
	}
	if err := Delete(""); err == nil {
		t.Fatal("deleting the empty path must fail")
	}
}

func TestBenchHistoryIsAppendOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "perf-bench")
	if rows, bad, err := ReadBench(dir); rows != nil || bad != 0 || err != nil {
		t.Fatalf("missing history = %v %d %v", rows, bad, err)
	}
	r1 := BenchRow{T: day1, Kind: BenchRecord, Name: "OpenFile/large", Ns: 4.9e6, Runs: 5, Allocs: 147, Version: "0.1.0", Commit: "abc1234", Machine: "apple-m4-darwin-arm64", Fixture: 1, Status: "recorded"}
	if err := AppendBench(dir, []BenchRow{r1}); err != nil {
		t.Fatal(err)
	}
	r2 := r1
	r2.T = day1.Add(24 * time.Hour)
	r2.Ns = 3.1e6
	r2.Version = ""
	r2.Name = "Open\"File\n/large"
	if err := AppendBench(dir, []BenchRow{r2}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(dir, BenchFile), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("not json\n")
	f.Close()
	rows, bad, err := ReadBench(dir)
	if err != nil || len(rows) != 2 || bad != 1 {
		t.Fatalf("rows = %v bad=%d err=%v", rows, bad, err)
	}
	if rows[0].Name != "OpenFile/large" || rows[0].Ns != 4.9e6 || !rows[0].T.Equal(day1) || rows[0].Commit != "abc1234" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Version != "dev" || strings.ContainsAny(rows[1].Name, "\"\n") {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestUserDataDirHonoursTheOverride(t *testing.T) {
	t.Setenv(UserDataDirEnv, "/somewhere")
	if UserDataDir() != "/somewhere" || LogDir("/somewhere") != "/somewhere/perf" || BenchDir("/somewhere") != "/somewhere/perf-bench" {
		t.Fatal("directory names wrong")
	}
	// The logs and the history never share a directory, so deleting one keeps the other.
	if strings.HasPrefix(BenchDir("/x"), LogDir("/x")+"/") {
		t.Fatal("the benchmark history is inside the diagnostics logs")
	}
}
