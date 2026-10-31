package main

import (
	"arbor/internal/fixture"
	"arbor/perf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Scenario is one real-app measurement. Budgets come from the perf catalog so
// the app and the runner cannot disagree; the few that are not an operation of
// the catalog are defined here.
type Scenario struct {
	Name string
	Doc  string
	Run  func(*Env) ([]Metric, error)
}

const (
	// frameMs is the frame budget at 60 Hz. A frame that takes longer is dropped.
	frameMs = 1000.0 / 60
	// droppedFramesBudget is the share of frames allowed to be dropped, in percent.
	droppedFramesBudget = 1.0
	// idleWindow is how long the idle scenario watches a settled window.
	idleWindow = 5 * time.Second
	// idleCPUBudget is the share of one core a settled app may use, in percent.
	idleCPUBudget = 1.0
	// externalChangeWait is how long the external change scenario waits for the UI.
	externalChangeWait = 3 * time.Second
	// windowH is the height of the window's content, for telling what is on screen.
	windowH = 820
)

var scenarios = []Scenario{
	{"startup", "first frame and ready, on a cold start of the process", scenarioStartup},
	{"mode_switch", "Files <-> Git by clicking the mode switcher", scenarioModeSwitch},
	{"open_file", "click files in the explorer", scenarioOpenFile},
	{"open_diff", "click changed files, then the files of a commit", scenarioOpenDiff},
	{"expand_commit", "click commits in the graph", scenarioExpandCommit},
	{"search", "type into and clear the file search", scenarioSearch},
	{"switch_branch", "choose another branch from the branch menu", scenarioSwitchBranch},
	{"stage_commit", "stage a file, then commit", scenarioStageCommit},
	{"scroll_frames", "scroll the commit graph, counting dropped frames", scenarioScroll},
	{"external_change", "change a file behind the app's back", scenarioExternalChange},
	{"idle", "a settled window: frames drawn and CPU used", scenarioIdle},
	{"memory", "resident memory after startup and after use (a baseline, no budget)", scenarioMemory},
	{"new_window", "open a second window", scenarioNewWindow},
}

// ---------------------------------------------------------------------------
// Helpers

// settle waits for the window to stop asking for frames, as far as it will.
func settle(a *App) { a.Client.Idle(800 * time.Millisecond) }

// node reads the tree until a node matches.
func (a *App) node(match func(Node) bool, wait time.Duration) (Node, error) {
	deadline := time.Now().Add(wait)
	for {
		nodes, err := a.Client.Tree()
		if err != nil {
			return Node{}, err
		}
		if n, ok := Find(nodes, match); ok {
			return n, nil
		}
		if time.Now().After(deadline) {
			return Node{}, fmt.Errorf("element not found; the window shows: %s", Labels(nodes, 25))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// clickKey clicks the element with the key, as a pointer does.
func (a *App) clickKey(key string) error {
	n, err := a.node(ByKey(key), 5*time.Second)
	if err != nil {
		return fmt.Errorf("key %q: %w", key, err)
	}
	return a.Client.Click(n)
}

// measured performs action and returns the duration of the first span of op the
// app reports after it.
func (a *App) measured(op perf.Op, what string, action func() error) (float64, error) {
	mark := a.Mark()
	if err := action(); err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	r, ok := a.Await(op.String(), mark, 3*time.Second)
	if !ok {
		return 0, fmt.Errorf("%s: no %s span followed", what, op)
	}
	settle(a)
	return r.Ms, nil
}

// timed measures an operation the app may or may not report as a span: the span
// when it does, else the wall-clock time from the action until done is true.
func (a *App) timed(op perf.Op, what string, action func() error, done func() bool, limit time.Duration) (ms float64, source string, err error) {
	mark := a.Mark()
	t0 := time.Now()
	if err := action(); err != nil {
		return 0, "", fmt.Errorf("%s: %w", what, err)
	}
	for time.Since(t0) < limit {
		if r, ok := a.Await(op.String(), mark, 0); ok {
			settle(a)
			return r.Ms, "span", nil
		}
		if done() {
			wall := float64(time.Since(t0)) / float64(time.Millisecond)
			// The span, if there is one, lands with the paint that shows the result.
			if r, ok := a.Await(op.String(), mark, 300*time.Millisecond); ok {
				settle(a)
				return r.Ms, "span", nil
			}
			settle(a)
			return wall, "wall", nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	shown := ""
	if nodes, err := a.Client.Tree(); err == nil {
		shown = "; the window shows: " + Labels(nodes, 40)
	}
	return 0, "", fmt.Errorf("%s: not done after %s%s\napp output:\n%s", what, limit, shown, a.Output())
}

// has reports whether the window shows a node that matches.
func (a *App) has(match func(Node) bool) bool {
	nodes, err := a.Client.Tree()
	if err != nil {
		return false
	}
	_, ok := Find(nodes, match)
	return ok
}

func launchOn(e *Env, s fixture.Spec, kind repoKind) (*App, string, error) {
	dir, err := e.RepoOf(s, kind)
	if err != nil {
		return nil, "", err
	}
	a, err := Launch(e.Bin, dir)
	if err != nil {
		return nil, "", err
	}
	if _, ok := a.Await(perf.OpStartupReady.String(), 0, 15*time.Second); !ok {
		a.Stop()
		return nil, "", fmt.Errorf("the app never reported startup_ready\n%s", a.Output())
	}
	settle(a)
	return a, dir, nil
}

// perApp runs fn on a freshly launched app for each fixture size.
func perApp(e *Env, kind repoKind, fn func(a *App, s fixture.Spec, dir string) ([]Metric, error)) ([]Metric, error) {
	var out []Metric
	for _, s := range e.Sizes {
		a, dir, err := launchOn(e, s, kind)
		if err != nil {
			return out, fmt.Errorf("%s: %w", s.Name, err)
		}
		ms, err := fn(a, s, dir)
		a.Stop()
		out = append(out, ms...)
		if err != nil {
			return out, fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return out, nil
}

func spanMetric(scenario string, s fixture.Spec, op perf.Op, ms []float64, note string) Metric {
	return Metric{Scenario: scenario, Size: s.Name, Unit: "ms", Samples: ms, Budget: op.BudgetMs(), HasBudget: true, Source: "span", Note: note}
}

// timedMetric is a metric that may be a span or a wall-clock time; its source
// says which, and a wall-clock one says what it includes.
func timedMetric(scenario string, s fixture.Spec, op perf.Op, ms []float64, sources []string, wallNote string) Metric {
	m := spanMetric(scenario, s, op, ms, "")
	m.Source = sources[0]
	for _, src := range sources {
		if src != m.Source {
			m.Source = "mixed"
		}
	}
	if m.Source != "span" {
		m.Note = wallNote
	}
	return m
}

// ---------------------------------------------------------------------------
// Scenarios

func scenarioStartup(e *Env) ([]Metric, error) {
	var out []Metric
	for _, s := range e.Sizes {
		dir, err := e.RepoOf(s, dirtyRepo)
		if err != nil {
			return out, err
		}
		var first, ready []float64
		for i := 0; i < e.Runs; i++ {
			a, err := Launch(e.Bin, dir)
			if err != nil {
				return out, err
			}
			f, ok1 := a.Await(perf.OpStartupFirstFrame.String(), 0, 15*time.Second)
			r, ok2 := a.Await(perf.OpStartupReady.String(), 0, 15*time.Second)
			a.Stop()
			if !ok1 || !ok2 {
				return out, fmt.Errorf("%s: startup spans missing (first frame %v, ready %v)", s.Name, ok1, ok2)
			}
			first, ready = append(first, f.Ms), append(ready, r.Ms)
		}
		note := "from the start of main(), cold process; excludes exec and runtime init"
		out = append(out,
			spanMetric("startup_first_frame", s, perf.OpStartupFirstFrame, first, note),
			spanMetric("startup_ready", s, perf.OpStartupReady, ready, note))
	}
	return out, nil
}

func scenarioModeSwitch(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		var ms []float64
		for i := 0; i < e.Runs; i++ {
			for _, key := range []string{"seg-git", "seg-files"} {
				d, err := a.measured(perf.OpModeSwitch, "click "+key, func() error { return a.clickKey(key) })
				if err != nil {
					return nil, err
				}
				ms = append(ms, d)
			}
		}
		return []Metric{spanMetric("mode_switch", s, perf.OpModeSwitch, ms, "Files<->Git, both directions")}, nil
	})
}

func scenarioOpenFile(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		if err := a.clickKey(filepath.ToSlash(filepath.Dir(s.Path(s.Per)))); err != nil { // open a folder: not measured
			return nil, err
		}
		settle(a)
		var ms []float64
		for i := 0; i < e.Runs; i++ {
			key := s.Path(s.Per + 2 + i)
			d, err := a.measured(perf.OpOpenFile, "click "+filepath.Base(key), func() error { return a.clickKey(key) })
			if err != nil {
				return nil, err
			}
			ms = append(ms, d)
		}
		return []Metric{spanMetric("open_file", s, perf.OpOpenFile, ms, "")}, nil
	})
}

func scenarioOpenDiff(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		if err := a.clickKey("seg-git"); err != nil {
			return nil, err
		}
		settle(a)
		unstaged, staged, _ := s.DirtyPaths()
		// Rows on screen, none of them the file shown first.
		keys := []string{"work:" + unstaged[1], "work:" + unstaged[2], "work:" + unstaged[3], "staged:" + staged[0], "staged:" + staged[1]}
		var working []float64
		for i := 0; i < e.Runs; i++ {
			key := keys[i%len(keys)]
			d, err := a.measured(perf.OpOpenDiff, "click "+key, func() error { return a.clickKey(key) })
			if err != nil {
				return nil, err
			}
			working = append(working, d)
		}

		// Expand a commit with enough files, then open each of its first files.
		var files []string
		for tried := 0; len(files) < 3; tried++ {
			nodes, err := a.Client.Tree()
			if err != nil {
				return nil, err
			}
			var commits []Node
			for _, n := range nodes {
				if n.Role == "listItem" && strings.HasPrefix(n.KeyString(), "commit:") && Visible(n, 1280, windowH) {
					commits = append(commits, n)
				}
			}
			if tried >= len(commits) || tried >= 4 {
				return nil, fmt.Errorf("none of the first commits shows 3 files")
			}
			commit := commits[tried]
			if _, err := a.measured(perf.OpExpandCommit, "expand a commit", func() error { return a.Client.Click(commit) }); err != nil {
				return nil, err
			}
			prefix := "cf:" + strings.TrimPrefix(commit.KeyString(), "commit:") + ":"
			if nodes, err = a.Client.Tree(); err != nil {
				return nil, err
			}
			files = files[:0]
			for _, n := range nodes {
				if strings.HasPrefix(n.KeyString(), prefix) && Visible(n, 1280, windowH) {
					files = append(files, n.KeyString())
				}
			}
			if len(files) < 3 { // collapse it again and try the next commit
				if err := a.Client.Click(commit); err != nil {
					return nil, err
				}
				settle(a)
			}
		}
		var inCommit []float64
		for i := 0; i < e.Runs; i++ {
			key := files[(i+1)%len(files)] // the first file is already open
			d, err := a.measured(perf.OpOpenDiff, "click a commit file", func() error { return a.clickKey(key) })
			if err != nil {
				return nil, err
			}
			inCommit = append(inCommit, d)
		}
		return []Metric{
			spanMetric("open_diff", s, perf.OpOpenDiff, working, "changed files of the working tree"),
			spanMetric("open_diff_commit", s, perf.OpOpenDiff, inCommit, "files of an expanded commit"),
		}, nil
	})
}

func scenarioExpandCommit(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		if err := a.clickKey("seg-git"); err != nil {
			return nil, err
		}
		settle(a)
		var ms []float64
		clicked := map[string]bool{}
		for i := 0; i < e.Runs; i++ {
			nodes, err := a.Client.Tree()
			if err != nil {
				return nil, err
			}
			target, ok := Find(nodes, func(n Node) bool {
				return n.Role == "listItem" && strings.HasPrefix(n.KeyString(), "commit:") && !clicked[n.KeyString()] && Visible(n, 1280, windowH)
			})
			if !ok {
				return nil, fmt.Errorf("no commit left to click; the window shows: %s", Labels(nodes, 30))
			}
			clicked[target.KeyString()] = true
			d, err := a.measured(perf.OpExpandCommit, "click a commit", func() error { return a.Client.Click(target) })
			if err != nil {
				return nil, err
			}
			ms = append(ms, d)
		}
		return []Metric{spanMetric("expand_commit", s, perf.OpExpandCommit, ms, "")}, nil
	})
}

func scenarioSearch(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		if err := a.clickKey("search-files"); err != nil {
			return nil, err
		}
		var ms []float64
		for _, ch := range "f01" {
			d, err := a.measured(perf.OpSearch, fmt.Sprintf("type %q", ch), func() error {
				return a.Client.Input(map[string]any{"type": "text", "text": string(ch)})
			})
			if err != nil {
				return nil, err
			}
			ms = append(ms, d)
		}
		for range "f01" {
			d, err := a.measured(perf.OpSearch, "Backspace", func() error {
				return a.Client.Input(map[string]any{"type": "key", "key": "Backspace"})
			})
			if err != nil {
				return nil, err
			}
			ms = append(ms, d)
		}
		return []Metric{spanMetric("search", s, perf.OpSearch, ms, "each keystroke of a 3-letter query, typed then erased")}, nil
	})
}

func scenarioSwitchBranch(e *Env) ([]Metric, error) {
	return perApp(e, cleanCopy, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		var ms []float64
		var sources []string
		current := "main"
		for i := 0; i < e.Runs; i++ {
			target := fixture.Branch(199)
			if current != "main" {
				target = "main"
			}
			d, src, err := a.timed(perf.OpSwitchBranch, "switch to "+target, func() error {
				if err := a.clickKey("btn-branch"); err != nil {
					return err
				}
				item, err := a.node(func(n Node) bool { return n.Role == "menuItemRadio" && n.Label == target }, 3*time.Second)
				if err != nil {
					return err
				}
				return a.Client.Click(item)
			}, func() bool {
				return a.has(func(n Node) bool { return n.KeyString() == "btn-branch" && n.Label == target })
			}, 10*time.Second)
			if err != nil {
				return nil, err
			}
			current = target
			ms, sources = append(ms, d), append(sources, src)
		}
		return []Metric{timedMetric("switch_branch", s, perf.OpSwitchBranch, ms, sources,
			"wall clock, menu item clicked until the branch name shows (no switch_branch span yet)")}, nil
	})
}

// repoLog summarizes the last commits and the status of a repository, for
// diagnosing a scenario that failed.
func repoLog(dir string) string {
	var b strings.Builder
	for _, args := range [][]string{{"log", "--stat", "--format=%h %s", "-6"}, {"status", "--short"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		b.Write(out)
	}
	return b.String()
}

func scenarioStageCommit(e *Env) ([]Metric, error) {
	return perApp(e, dirtyCopy, func(a *App, s fixture.Spec, dir string) ([]Metric, error) {
		ms, err := stageCommit(e, a, s)
		if err != nil {
			err = fmt.Errorf("%w\nrepository:\n%s", err, repoLog(dir))
		}
		return ms, err
	})
}

func stageCommit(e *Env, a *App, s fixture.Spec) ([]Metric, error) {
	{
		if err := a.clickKey("seg-git"); err != nil {
			return nil, err
		}
		settle(a)
		unstaged, _, untracked := s.DirtyPaths()
		files := []string{unstaged[0], unstaged[1], unstaged[2], unstaged[3], untracked[0]}
		var stageMs, commitMs []float64
		var stageSrc, commitSrc []string
		for i := 0; i < e.Runs && i < len(files); i++ {
			path := files[i]
			if err := a.clickKey("work:" + path); err != nil {
				return nil, err
			}
			settle(a)
			d, src, err := a.timed(perf.OpStage, "stage "+filepath.Base(path), func() error { return a.clickKey("diff-stage") },
				func() bool { return a.has(ByKey("staged:" + path)) }, 10*time.Second)
			if err != nil {
				return nil, err
			}
			stageMs, stageSrc = append(stageMs, d), append(stageSrc, src)

			msg := fmt.Sprintf("perf scenario commit %d", i)
			if err := a.clickKey("commit-msg"); err != nil {
				return nil, err
			}
			if err := a.Client.Input(map[string]any{"type": "text", "text": msg}); err != nil {
				return nil, err
			}
			time.Sleep(100 * time.Millisecond)
			d, src, err = a.timed(perf.OpCommit, "commit", func() error { return a.clickKey("btn-commit") },
				func() bool { return !a.has(ByKey("staged:" + path)) }, 10*time.Second)
			if err != nil {
				return nil, err
			}
			commitMs, commitSrc = append(commitMs, d), append(commitSrc, src)
		}
		return []Metric{
			timedMetric("stage", s, perf.OpStage, stageMs, stageSrc, "wall clock, Stage clicked until the file is listed as staged (no stage span yet)"),
			timedMetric("commit", s, perf.OpCommit, commitMs, commitSrc, "wall clock, Commit clicked until the committed file leaves the changes list (no commit span yet)"),
		}, nil
	}
}

// scrollGraph wheels over the commit graph down and back up and returns the
// frames the engine logged meanwhile.
func scrollGraph(a *App) ([]Frame, error) {
	graph, err := a.node(func(n Node) bool { return n.Role == "list" && n.Bounds.X == 0 && n.Bounds.Y > 300 }, 3*time.Second)
	if err != nil {
		return nil, err
	}
	x, y := graph.Bounds.Center()
	firstCommit := func() string {
		nodes, _ := a.Client.Tree()
		n, _ := Find(nodes, func(n Node) bool { return n.Role == "listItem" && strings.HasPrefix(n.KeyString(), "commit:") })
		return n.KeyString()
	}
	before := firstCommit()
	t0 := time.Now()
	for i, dy := range []float64{50, -50} {
		for j := 0; j < 120; j++ {
			if err := a.Client.Wheel(x, y, dy); err != nil {
				return nil, err
			}
		}
		if i == 0 {
			settle(a)
			if firstCommit() == before {
				return nil, fmt.Errorf("120 wheel events did not scroll the commit graph")
			}
		}
	}
	settle(a)
	return a.FramesSince(t0), nil
}

func scenarioScroll(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		if err := a.clickKey("seg-git"); err != nil {
			return nil, err
		}
		settle(a)
		var pct, total, worst []float64
		for i := 0; i < e.Runs; i++ {
			frames, err := scrollGraph(a)
			if err != nil {
				return nil, err
			}
			if len(frames) == 0 {
				return nil, fmt.Errorf("scrolling drew no frames; is the window visible?")
			}
			dropped, max := 0, 0.0
			for _, f := range frames {
				if f.Ms > frameMs {
					dropped++
				}
				if f.Ms > max {
					max = f.Ms
				}
			}
			pct = append(pct, 100*float64(dropped)/float64(len(frames)))
			total, worst = append(total, float64(len(frames))), append(worst, max)
		}
		note := fmt.Sprintf("a frame over %.1f ms is dropped; 240 wheel events per run, frames logged by MYGO_FRAME_STATS", frameMs)
		return []Metric{
			{Scenario: "dropped_frames", Size: s.Name, Unit: "%", Samples: pct, Budget: droppedFramesBudget, HasBudget: true, Source: "frames", Note: note},
			{Scenario: "scroll_frame_max", Size: s.Name, Unit: "ms", Samples: worst, Source: "frames", Note: "slowest frame while scrolling"},
			{Scenario: "scroll_frame_count", Size: s.Name, Unit: "", Samples: total, Source: "frames", Note: "frames drawn per run"},
		}, nil
	})
}

func scenarioExternalChange(e *Env) ([]Metric, error) {
	return perApp(e, dirtyCopy, func(a *App, s fixture.Spec, dir string) ([]Metric, error) {
		var ms []float64
		for i := 0; i < e.Runs; i++ {
			name := fmt.Sprintf("zz_external_%d.txt", i)
			mark := a.Mark()
			t0 := time.Now()
			if err := os.WriteFile(filepath.Join(dir, name), []byte("changed behind the app's back\n"), 0o644); err != nil {
				return nil, err
			}
			seen := false
			for time.Since(t0) < externalChangeWait {
				if a.has(ByKey(name)) {
					seen = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !seen {
				m := Metric{Scenario: "external_change", Size: s.Name, Unit: "ms", Budget: perf.OpExternalChange.BudgetMs(), HasBudget: true, Source: "wall", Status: Fail,
					Note: fmt.Sprintf("a new file was not shown within %s: the app has no file watcher yet (ARB-4)", externalChangeWait)}
				os.Remove(filepath.Join(dir, name))
				return []Metric{m}, nil
			}
			wall := float64(time.Since(t0)) / float64(time.Millisecond)
			if r, ok := a.Await(perf.OpExternalChange.String(), mark, 500*time.Millisecond); ok {
				ms = append(ms, r.Ms)
			} else {
				ms = append(ms, wall)
			}
			os.Remove(filepath.Join(dir, name))
		}
		m := spanMetric("external_change", s, perf.OpExternalChange, ms, "")
		m.Source = "wall"
		m.Note = "wall clock, file written until it is listed in the explorer"
		return []Metric{m}, nil
	})
}

func scenarioIdle(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		windows := min(e.Runs, 3)
		var frames, cpu []float64
		for i := 0; i < windows; i++ {
			settle(a)
			time.Sleep(time.Second)
			_, c0, err := a.Usage()
			if err != nil {
				return nil, err
			}
			t0 := time.Now()
			time.Sleep(idleWindow)
			_, c1, err := a.Usage()
			if err != nil {
				return nil, err
			}
			frames = append(frames, float64(len(a.FramesSince(t0))))
			cpu = append(cpu, 100*float64(c1-c0)/float64(time.Since(t0)))
		}
		note := fmt.Sprintf("%d windows of %s on a settled window", windows, idleWindow)
		return []Metric{
			{Scenario: "idle_frames", Size: s.Name, Unit: "", Samples: frames, Budget: 0, HasBudget: true, Source: "frames", Note: note},
			{Scenario: "idle_cpu", Size: s.Name, Unit: "%", Samples: cpu, Budget: idleCPUBudget, HasBudget: true, Source: "ps", Note: "share of one core; ps resolution is 10 ms"},
		}, nil
	})
}

func scenarioMemory(e *Env) ([]Metric, error) {
	return perApp(e, dirtyRepo, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		sample := func() (float64, error) { // the median of three, as the resident set moves with the collector
			var v []float64
			for i := 0; i < 3; i++ {
				rss, _, err := a.Usage()
				if err != nil {
					return 0, err
				}
				v = append(v, rss)
				time.Sleep(700 * time.Millisecond)
			}
			sort.Float64s(v)
			return v[1], nil
		}
		rss0, err := sample()
		if err != nil {
			return nil, err
		}
		// Use the app a little: both views, a few files, diffs and commits.
		for i := 0; i < 3; i++ {
			for _, key := range []string{"seg-git", "seg-files"} {
				if err := a.clickKey(key); err != nil {
					return nil, err
				}
				settle(a)
			}
		}
		if err := a.clickKey("seg-git"); err != nil {
			return nil, err
		}
		settle(a)
		for i := 0; i < 5; i++ {
			nodes, err := a.Client.Tree()
			if err != nil {
				return nil, err
			}
			if n, ok := Find(nodes, func(n Node) bool {
				return n.Role == "listItem" && strings.HasPrefix(n.KeyString(), "commit:") && Visible(n, 1280, windowH)
			}); ok {
				a.Client.Click(n)
				settle(a)
			}
		}
		rss1, err := sample()
		if err != nil {
			return nil, err
		}
		return []Metric{
			{Scenario: "memory_startup", Size: s.Name, Unit: "MB", Samples: []float64{rss0}, Source: "ps", Note: "resident set after startup, settled"},
			{Scenario: "memory_after_use", Size: s.Name, Unit: "MB", Samples: []float64{rss1}, Source: "ps", Note: "after mode switches and expanding commits"},
		}, nil
	})
}

func scenarioNewWindow(e *Env) ([]Metric, error) {
	return []Metric{Skipped("new_window", "", fmt.Sprintf("not implementable until ARB-1 (multi-window): the app has one window and no way to open another; budget %.0f ms", perf.OpNewWindow.BudgetMs()))}, nil
}
