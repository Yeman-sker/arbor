package main

import (
	"arbor/internal/fixture"
	"arbor/perf"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The long-line scroll scenario (ARB-12): scrolling text whose lines run to
// kilobytes, as minified or generated JSON does, in the Files view and in the diff.
// The commit graph scrolls smoothly whatever the editor does, so scroll_frames
// cannot see it.
//
// Only lines not highlighted yet are slow, so each run opens the file afresh, as
// someone does who opens it to read, and scrolls down from the top.

const (
	// longLinesFile is the file the scenario scrolls: 2 MB of JSON lines of about
	// 7 KB, the length that stopped the window for 300 to 575 ms a frame. The diff
	// shows its first 512 KB, about 70 lines.
	longLinesFile = longLinesDir + "/long-lines.json"
	longLinesKeys = 120
	longLinesN    = 300
	// longLinesOther is another changed file, opened between the runs.
	longLinesOther = longLinesDir + "/other.txt"
	// longLinesDir holds both, first in the explorer so that its rows are on
	// screen however many files the fixture has.
	longLinesDir = "0-long-lines"
	// longLinesEvents is how many wheel events a run sends, every longLinesPace
	// as a touchpad does: sent at once they would be taken in a few frames.
	longLinesEvents = 120
	longLinesPace   = 8 * time.Millisecond
	longLinesDY     = 24
	// lostRedrawWait is how long an opened file may take to show before the
	// scenario counts it as waiting for a frame: it opens in milliseconds.
	lostRedrawWait = 3 * time.Second
)

// longJSON makes n lines of JSON objects of keys keys each, every changeEvery-th
// line changed when changeEvery is not 0.
func longJSON(n, keys, changeEvery int) string {
	r := rand.New(rand.NewSource(1))
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("{")
		for k := 0; k < keys; k++ {
			v := r.Intn(99999)
			if changeEvery > 0 && i%changeEvery == 0 && k%10 == 0 {
				v = -v
			}
			fmt.Fprintf(&b, `"key_%d_%d":{"id":%d,"name":"value %d","tags":["a","b"]},`, i, k, k, v)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// longLinesRepo is a private copy of the fixture with longLinesFile and
// longLinesOther committed and then changed, so that both show in the Files view
// and have a diff.
func longLinesRepo(e *Env, s fixture.Spec) (string, error) {
	dir, err := e.RepoOf(s, dirtyCopy)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(filepath.Join(dir, longLinesDir), 0o755); err != nil {
		return "", err
	}
	write := func(name, text string) error { return os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644) }
	if err := write(longLinesFile, longJSON(longLinesN, longLinesKeys, 0)); err != nil {
		return "", err
	}
	if err := write(longLinesOther, "one\ntwo\n"); err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"add", longLinesFile, longLinesOther},
		{"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "-m", "add long lines"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := write(longLinesOther, "one\nthree\n"); err != nil {
		return "", err
	}
	return dir, write(longLinesFile, longJSON(longLinesN, longLinesKeys, 3))
}

// paceWheel sends wheel events down at (x, y), longLinesPace apart, and returns the
// frames logged meanwhile.
func paceWheel(a *App, x, y float64) ([]Frame, error) {
	t0 := time.Now()
	for i := 0; i < longLinesEvents; i++ {
		if err := a.Client.Wheel(x, y, longLinesDY); err != nil {
			return nil, err
		}
		time.Sleep(longLinesPace)
	}
	settle(a)
	return a.FramesSince(t0), nil
}

// clickSteady clicks the element with the key once it stays where it is: rows
// slide in as a view or folder opens, and a click read from a frame of that lands
// on another row.
func (a *App) clickSteady(key string) error {
	deadline := time.Now().Add(5 * time.Second)
	n, err := a.node(ByKey(key), 5*time.Second)
	for err == nil {
		time.Sleep(50 * time.Millisecond)
		var again Node
		if again, err = a.node(ByKey(key), time.Second); err != nil {
			break
		}
		if again.Bounds == n.Bounds || time.Now().After(deadline) {
			return a.Client.Click(again)
		}
		n = again
	}
	return fmt.Errorf("key %q: %w", key, err)
}

// openSteady opens a file by clicking its row, and waits for the span of op that
// says it is on screen. When none follows within lostRedrawWait, the pointer
// moves by a pixel, which draws a frame, and lost reports whether the span came
// then: the content was there, waiting for a frame nothing asked for.
func (a *App) openSteady(op perf.Op, key string) (lost bool, err error) {
	mark := a.Mark()
	if err := a.clickSteady(key); err != nil {
		return false, err
	}
	if _, ok := a.Await(op.String(), mark, lostRedrawWait); ok {
		settle(a)
		return false, nil
	}
	n, err := a.node(ByKey(key), time.Second)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", key, err)
	}
	x, y := n.Bounds.Center()
	if err := a.Client.Input(map[string]any{"type": "pointerMove", "x": x + 1, "y": y}); err != nil {
		return false, err
	}
	if _, ok := a.Await(op.String(), mark, 5*time.Second); !ok {
		return false, fmt.Errorf("open %s: no %s span followed, even after the pointer moved\napp output:\n%s", key, op, a.Output())
	}
	settle(a)
	return true, nil
}

func scenarioScrollLongLines(e *Env) ([]Metric, error) {
	var out []Metric
	for _, s := range e.Sizes {
		dir, err := longLinesRepo(e, s)
		if err != nil {
			return out, err
		}
		a, err := Launch(e.Bin, dir)
		if err != nil {
			return out, err
		}
		ms, err := scrollLongLines(e, a, s)
		a.Stop()
		out = append(out, ms...)
		if err != nil {
			return out, fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return out, nil
}

func scrollLongLines(e *Env, a *App, s fixture.Spec) ([]Metric, error) {
	if _, ok := a.Await(perf.OpStartupReady.String(), 0, 15*time.Second); !ok {
		return nil, fmt.Errorf("the app never reported startup_ready\n%s", a.Output())
	}
	settle(a)
	views := []struct {
		name   string
		op     perf.Op
		prefix string // of the keys of the rows that open the files
		enter  func() error
	}{
		{"files", perf.OpOpenFile, "", func() error {
			if a.has(ByKey(longLinesOther)) {
				return nil
			}
			return a.clickKey(longLinesDir)
		}},
		{"diff", perf.OpOpenDiff, "work:", func() error { return a.clickKey("seg-git") }},
	}
	var out []Metric
	for _, v := range views {
		if err := v.enter(); err != nil {
			return nil, fmt.Errorf("enter the %s view: %w", v.name, err)
		}
		settle(a)
		var pct, worst []float64
		lost := 0
		for i := 0; i < e.Runs; i++ {
			for _, key := range []string{longLinesOther, longLinesFile} {
				l, err := a.openSteady(v.op, v.prefix+key)
				if err != nil {
					return nil, err
				}
				if l {
					lost++
				}
			}
			// The editor of the Files view, or the new side of the diff: the
			// rightmost editor right of the sidebar.
			ed, err := a.node(func(n Node) bool { return n.Role == "textField" && n.Bounds.X > 600 && n.Bounds.Height > 200 }, time.Second)
			if err != nil {
				ed, err = a.node(func(n Node) bool { return n.Role == "textField" && n.Bounds.X > 200 && n.Bounds.Height > 200 }, 5*time.Second)
			}
			if err != nil {
				return nil, fmt.Errorf("%s view: %w", v.name, err)
			}
			x, y := ed.Bounds.Center()
			frames, err := paceWheel(a, x, y)
			if err != nil {
				return nil, err
			}
			if len(frames) == 0 {
				return nil, fmt.Errorf("scrolling the %s view drew no frames; is the window visible?", v.name)
			}
			dropped, slowest := 0, 0.0
			for _, f := range frames {
				if f.Ms > frameMs {
					dropped++
				}
				slowest = max(slowest, f.Ms)
			}
			pct = append(pct, 100*float64(dropped)/float64(len(frames)))
			worst = append(worst, slowest)
		}
		note := fmt.Sprintf("%s view, ~7 KB JSON lines opened afresh; %d wheel events %s apart per run; a frame over %.1f ms is dropped", v.name, longLinesEvents, longLinesPace, frameMs)
		out = append(out,
			Metric{Scenario: "long_lines_dropped_" + v.name, Size: s.Name, Unit: "%", Samples: pct, Budget: droppedFramesBudget, HasBudget: true, Source: "frames", Note: note},
			Metric{Scenario: "long_lines_frame_max_" + v.name, Size: s.Name, Unit: "ms", Samples: worst, Source: "frames", Note: "slowest frame while scrolling the " + v.name + " view"},
			Metric{Scenario: "lost_redraws_" + v.name, Size: s.Name, Unit: "", Samples: []float64{float64(lost)}, Budget: 0, HasBudget: true, Source: "span",
				Note: fmt.Sprintf("of %d opens after scrolling, those that showed only once the pointer moved: loaded, but no frame asked for", 2*e.Runs)})
	}
	return out, nil
}
