package main

import (
	"arbor/internal/fixture"
	"arbor/internal/perfstore"
	"arbor/perf"
	"bytes"
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
	{"switch_branch", "choose another branch from the branch picker", scenarioSwitchBranch},
	{"stage_commit", "stage a file, then commit", scenarioStageCommit},
	{"scroll_frames", "scroll the commit graph, counting dropped frames", scenarioScroll},
	{"scroll_long_lines", "scroll long JSON lines in the Files view and the diff, counting dropped frames", scenarioScrollLongLines},
	{"external_change", "change a file behind the app's back", scenarioExternalChange},
	{"idle", "a settled window: frames drawn and CPU used", scenarioIdle},
	{"memory", "resident memory after startup and after use (a baseline, no budget)", scenarioMemory},
	{"new_window", "open a second window", scenarioNewWindow},
	{"diagnostics", "diagnostics on: the real store and Performance window", scenarioDiagnostics},
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
// app reports after it. The app reports a span when the operation's result is
// painted, so a missing one is an error: either the operation failed or its
// instrumentation is gone.
func (a *App) measured(op perf.Op, what string, action func() error) (float64, error) {
	mark := a.Mark()
	if err := action(); err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	r, ok := a.Await(op.String(), mark, 10*time.Second)
	if !ok {
		shown := ""
		if nodes, err := a.Client.Tree(); err == nil {
			shown = "; the window shows: " + Labels(nodes, 40)
		}
		return 0, fmt.Errorf("%s: no %s span followed%s\napp output:\n%s", what, op, shown, a.Output())
	}
	settle(a)
	return r.Ms, nil
}

// waitFor waits until the window shows a node that matches.
func (a *App) waitFor(what string, match func(Node) bool, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for !a.has(match) {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: not shown after %s", what, limit)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// waitGone waits until the window no longer shows a node that matches.
func (a *App) waitGone(what string, match func(Node) bool, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for a.has(match) {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: still shown after %s", what, limit)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
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
		current := "main"
		for i := 0; i < e.Runs; i++ {
			target := fixture.Branch(199)
			if current != "main" {
				target = "main"
			}
			d, err := a.measured(perf.OpSwitchBranch, "switch to "+target, func() error {
				if err := a.clickKey("btn-branch"); err != nil {
					return err
				}
				if _, err := a.node(func(n Node) bool { return strings.HasPrefix(n.KeyString(), "picker-q:") }, 3*time.Second); err != nil {
					return err
				}
				if err := a.Client.Input(map[string]any{"type": "text", "text": target}); err != nil {
					return err
				}
				// Enter, in order after the text: the rows move as the filter applies,
				// so a row's position read here may not be where it ends up.
				return a.Client.Input(map[string]any{"type": "key", "key": "Enter"})
			})
			if err != nil {
				return nil, err
			}
			if err := a.waitFor("branch "+target, func(n Node) bool { return n.KeyString() == "btn-branch" && n.Label == target }, 5*time.Second); err != nil {
				return nil, err
			}
			current = target
			ms = append(ms, d)
		}
		return []Metric{spanMetric("switch_branch", s, perf.OpSwitchBranch, ms, "branch filtered and chosen with Enter until the checkout is painted; main <-> a branch 50 commits behind")}, nil
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
	if err := a.clickKey("seg-git"); err != nil {
		return nil, err
	}
	settle(a)
	unstaged, _, untracked := s.DirtyPaths()
	files := []string{unstaged[0], unstaged[1], unstaged[2], unstaged[3], untracked[0]}
	var stageMs, commitMs []float64
	for i := 0; i < e.Runs && i < len(files); i++ {
		path := files[i]
		if err := a.clickKey("work:" + path); err != nil {
			return nil, err
		}
		settle(a)
		d, err := a.measured(perf.OpStage, "stage "+filepath.Base(path), func() error { return a.clickKey("diff-stage") })
		if err != nil {
			return nil, err
		}
		if err := a.waitFor("the staged file", ByKey("staged:"+path), 5*time.Second); err != nil {
			return nil, err
		}
		stageMs = append(stageMs, d)

		msg := fmt.Sprintf("perf scenario commit %d", i)
		if err := a.clickKey("commit-msg"); err != nil {
			return nil, err
		}
		if err := a.Client.Input(map[string]any{"type": "text", "text": msg}); err != nil {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
		d, err = a.measured(perf.OpCommit, "commit", func() error { return a.clickKey("btn-commit") })
		if err != nil {
			return nil, err
		}
		if err := a.waitGone("the committed file in the changes list", ByKey("staged:"+path), 5*time.Second); err != nil {
			return nil, err
		}
		commitMs = append(commitMs, d)
	}
	return []Metric{
		spanMetric("stage", s, perf.OpStage, stageMs, "Stage clicked until the file is listed as staged"),
		spanMetric("commit", s, perf.OpCommit, commitMs, "Commit clicked until the changes list and graph show it"),
	}, nil
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
		// Collapse the folder that starts open, so the root of the explorer is short
		// enough for a new file at its end to be on screen.
		if err := a.clickKey(filepath.ToSlash(filepath.Dir(filepath.Dir(s.Path(0))))); err != nil {
			return nil, err
		}
		settle(a)
		var wall, refresh []float64
		for i := 0; i < e.Runs; i++ {
			name := fmt.Sprintf("zz_external_%d.txt", i)
			path := filepath.Join(dir, name)
			mark := a.Mark()
			t0 := time.Now()
			if err := os.WriteFile(path, []byte("changed behind the app's back\n"), 0o644); err != nil {
				return nil, err
			}
			seen := false
			for time.Since(t0) < externalChangeWait {
				if a.has(ByKey(name)) {
					seen = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !seen {
				os.Remove(path)
				return []Metric{{Scenario: "external_change", Size: s.Name, Unit: "ms", Budget: perf.OpExternalChange.BudgetMs(), HasBudget: true, Source: "wall", Status: Fail,
					Note: fmt.Sprintf("a new file was not shown within %s", externalChangeWait)}}, nil
			}
			wall = append(wall, float64(time.Since(t0))/float64(time.Millisecond))
			// The app's span starts when its watcher reports the change, so it
			// leaves out the time the file system and the debounce took.
			r, ok := a.Await(perf.OpExternalChange.String(), mark, time.Second)
			if !ok {
				return nil, fmt.Errorf("the file was shown but the app reported no external_change span")
			}
			refresh = append(refresh, r.Ms)
			os.Remove(path)
			settle(a)
			time.Sleep(300 * time.Millisecond) // let the removal's own refresh pass
		}
		return []Metric{
			{Scenario: "external_change", Size: s.Name, Unit: "ms", Samples: wall, Budget: perf.OpExternalChange.BudgetMs(), HasBudget: true, Source: "wall",
				Note: "a file written until the explorer lists it: includes the file system event and the watcher's debounce"},
			{Scenario: "external_change_refresh", Size: s.Name, Unit: "ms", Samples: refresh, Source: "span",
				Note: "the app's own span: watcher event until the refresh is painted"},
		}, nil
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
	return perApp(e, cleanCopy, func(a *App, s fixture.Spec, _ string) ([]Metric, error) {
		var ms []float64
		for range e.Runs {
			d, err := a.measured(perf.OpNewWindow, "File > New Window", func() error {
				return a.Client.Call("menu", map[string]any{"path": []string{"File", "New Window"}}, nil)
			})
			if err != nil {
				return nil, err
			}
			ms = append(ms, d)
		}
		return []Metric{spanMetric("new_window", s, perf.OpNewWindow, ms, "New Window chosen until the new window's first paint")}, nil
	})
}

// scenarioDiagnostics runs the app with diagnostics on in a home of its own: the
// real store writes while the app is used, and the real Performance window opens
// from the menu. It fails when user data reaches the log.
func scenarioDiagnostics(e *Env) ([]Metric, error) {
	s := e.Sizes[0]
	home, err := os.MkdirTemp(e.Tmp, "home-")
	if err != nil {
		return nil, err
	}
	// An app that isn't packaged keeps its data under the name of its binary.
	data := filepath.Join(home, "Library", "Application Support", filepath.Base(e.Bin))
	if err := os.MkdirAll(data, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte(`{"githubAvatars":false,"performanceDiagnostics":true}`), 0o644); err != nil {
		return nil, err
	}
	// With no span log the app's only sink is the one diagnostics installs.
	extraEnv = []string{"HOME=" + home, "ARBOR_PERF_LOG="}
	defer func() { extraEnv = nil }()
	dir, err := e.RepoOf(s, dirtyCopy)
	if err != nil {
		return nil, err
	}
	a, err := Launch(e.Bin, dir)
	if err != nil {
		return nil, err
	}
	defer a.Stop()
	if err := a.clickKey("seg-git"); err != nil {
		return nil, err
	}
	if err := a.Client.Call("menu", map[string]any{"path": []string{"View", "Performance Window"}}, nil); err != nil {
		return nil, fmt.Errorf("the Performance window is not in the menu: %w", err)
	}
	opened := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !opened; time.Sleep(50 * time.Millisecond) {
		var res struct {
			Windows []struct {
				ID    int
				Title string
			}
		}
		if err := a.Client.Call("windows", nil, &res); err != nil {
			return nil, err
		}
		opened = len(res.Windows) >= 2
	}
	if !opened {
		return nil, fmt.Errorf("the Performance window did not open")
	}
	var logs []byte
	logDir := perfstore.LogDir(data)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !bytes.Contains(logs, []byte(`"op":"mode_switch"`)); time.Sleep(100 * time.Millisecond) {
		logs = nil
		files, _ := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			logs = append(logs, b...)
		}
	}
	if !bytes.Contains(logs, []byte(`"op":"mode_switch"`)) {
		return nil, fmt.Errorf("the store wrote no mode_switch to %s: %q", logDir, logs)
	}
	if bytes.Contains(logs, []byte(filepath.Base(dir))) || bytes.Contains(logs, []byte(home)) {
		return nil, fmt.Errorf("a path reached the log: %q", logs)
	}
	return []Metric{{Scenario: "diagnostics", Size: s.Name, Unit: "lines", Samples: []float64{float64(bytes.Count(logs, []byte("\n")))},
		Note: "real store and Performance window, in a home of their own"}}, nil
}
