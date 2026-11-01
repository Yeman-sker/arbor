package app

import (
	"arbor/internal/git"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// settleFade is a little more than the hold and the fade.
const settleFade = scrollFadeHold + scrollFadeOut + 100*time.Millisecond

// startFade opens a window and lets its first frames pass, which a scroller
// does not count as scrolling.
func startFade(view func(*ui.Context), w, h int) *ui.Tester {
	tt := ui.NewTester(view, w, h)
	tt.Advance(scrollFadeQuiet + 50*time.Millisecond)
	return tt
}

// fadeList is a window holding one fading list of plain rows.
type fadeList struct {
	fade   scrollFade
	list   ui.ListState
	rows   int
	epoch  uint64
	builds int
}

func (h *fadeList) view(c *ui.Context) {
	h.builds++
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		h.fade.List(c, t, h.epoch, &h.list, h.rows, func(i int) {
			ui.Text(c, fmt.Sprintf("row %03d", i)).Height(24)
		})
	})
}

// fadeEditor is a window holding one fading editor of short lines.
type fadeEditor struct {
	fade   scrollFade
	state  *editor.State
	epoch  uint64
	builds int
}

func newFadeEditor(lines int) *fadeEditor {
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	return &fadeEditor{state: &editor.State{
		Buffer:          editor.NewBuffer(sb.String()),
		ReadOnly:        true,
		NoScrollPastEnd: true,
	}}
}

func (h *fadeEditor) view(c *ui.Context) {
	h.builds++
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		h.fade.Editor(c, t, h.epoch, h.state).Grow(1).FillWidth()
	})
}

// thumbPixels reports the span of rows, from y0, of the right edge of img
// (w wide) that differ from the blank middle of the window beside them: the
// scroll bar's thumb, where it is drawn.
func thumbPixels(img *image.RGBA, w, y0 int) (top, bottom, n int) {
	top, bottom = -1, -1
	for y := y0; y < img.Bounds().Dy(); y++ {
		ref := img.RGBAAt(w/2, y)
		found := false
		for x := w - 10; x < w; x++ {
			px := img.RGBAAt(x, y)
			d := abs8(px.R, ref.R) + abs8(px.G, ref.G) + abs8(px.B, ref.B)
			if d > 6 {
				found = true
				n++
			}
		}
		if found {
			if top < 0 {
				top = y
			}
			bottom = y
		}
	}
	return top, bottom, n
}

func abs8(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestScrollFadeThumbFollowsScrolling(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%v", dark), func(t *testing.T) {
			h := &fadeList{rows: 200}
			tt := startFade(h.view, 300, 400)
			tt.SetDark(dark)
			tt.Move(100, 200)
			if _, _, n := thumbPixels(tt.Image(), 300, 0); n != 0 || h.fade.alpha != 0 {
				t.Fatalf("thumb drawn before any scrolling: %d pixels, alpha %v", n, h.fade.alpha)
			}

			tt.Scroll(100, 200, 0, 300)
			if _, _, n := thumbPixels(tt.Image(), 300, 0); n == 0 || h.fade.alpha != 1 {
				t.Fatalf("thumb not visible after scrolling: %d pixels, alpha %v", n, h.fade.alpha)
			}

			tt.Advance(scrollFadeHold - 100*time.Millisecond)
			if _, _, n := thumbPixels(tt.Image(), 300, 0); n == 0 || h.fade.alpha != 1 {
				t.Fatalf("thumb gone during the hold: %d pixels, alpha %v", n, h.fade.alpha)
			}

			tt.Advance(100*time.Millisecond + scrollFadeOut/2)
			if _, _, n := thumbPixels(tt.Image(), 300, 0); n == 0 || h.fade.alpha <= 0 || h.fade.alpha >= 1 {
				t.Fatalf("thumb not half faded: %d pixels, alpha %v", n, h.fade.alpha)
			}

			tt.Advance(scrollFadeOut)
			if _, _, n := thumbPixels(tt.Image(), 300, 0); n != 0 || h.fade.alpha != 0 {
				t.Fatalf("thumb still visible after the fade: %d pixels, alpha %v", n, h.fade.alpha)
			}

			// Hovering it without scrolling brings nothing back.
			tt.Move(120, 220)
			tt.Move(296, 220)
			tt.Advance(time.Second)
			if _, _, n := thumbPixels(tt.Image(), 300, 0); n != 0 || h.fade.alpha != 0 {
				t.Fatalf("hover brought the thumb back: %d pixels, alpha %v", n, h.fade.alpha)
			}
		})
	}
}

func TestScrollFadeKeepsThumbWhileScrollingContinues(t *testing.T) {
	h := &fadeList{rows: 400}
	tt := startFade(h.view, 300, 400)
	tt.Move(100, 200)
	for i := 0; i < 6; i++ {
		tt.Scroll(100, 200, 0, 200)
		tt.Advance(600 * time.Millisecond)
		if h.fade.alpha != 1 {
			t.Fatalf("step %d: thumb faded 600ms after a scroll, alpha %v", i, h.fade.alpha)
		}
	}
	// The wheel's glide moves the list over several frames after the event.
	tt.Wheel(100, 200, 0, 240)
	tt.Advance(100 * time.Millisecond)
	if h.fade.alpha != 1 {
		t.Fatalf("thumb not visible during a wheel glide, alpha %v", h.fade.alpha)
	}
	tt.Advance(settleFade)
	if h.fade.alpha != 0 {
		t.Fatalf("thumb still visible after scrolling stopped, alpha %v", h.fade.alpha)
	}
}

func TestScrollFadeWithReducedMotionHidesWithoutFading(t *testing.T) {
	h := &fadeList{rows: 200}
	tt := startFade(h.view, 300, 400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Move(100, 200)
	tt.Scroll(100, 200, 0, 300)
	tt.Advance(scrollFadeHold - 50*time.Millisecond)
	if h.fade.alpha != 1 {
		t.Fatalf("thumb hidden during the hold, alpha %v", h.fade.alpha)
	}
	tt.Advance(100 * time.Millisecond)
	if h.fade.alpha != 0 || tt.FramePending() {
		t.Fatalf("thumb did not go at once after the hold: alpha %v, pending %v", h.fade.alpha, tt.FramePending())
	}
}

func TestScrollFadeAsksForNoFramesWhenIdle(t *testing.T) {
	h := &fadeList{rows: 200}
	tt := startFade(h.view, 300, 400)
	tt.Move(100, 200)
	if tt.FramePending() {
		t.Fatal("frame pending before any scrolling")
	}
	built := h.builds
	tt.Advance(10 * time.Second)
	if h.builds != built {
		t.Fatalf("%d frames built while idle", h.builds-built)
	}

	tt.Scroll(100, 200, 0, 300)
	tt.Advance(scrollFadeHold + scrollFadeOut/2)
	if !tt.FramePending() {
		t.Fatal("no frame pending in the middle of the fade")
	}
	tt.Advance(scrollFadeOut)
	if tt.FramePending() {
		t.Fatal("frame pending after the thumb faded out")
	}
	built = h.builds
	tt.Advance(10 * time.Second)
	if h.builds != built || tt.FramePending() {
		t.Fatalf("%d frames built after the thumb faded out", h.builds-built)
	}

	// Frames the view builds anyway ask for none of their own.
	tt.Frame()
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("frame pending after the view built with the thumb hidden")
	}
}

func TestScrollFadeContentSwitchDoesNotRevealThumb(t *testing.T) {
	h := &fadeList{rows: 200}
	tt := startFade(h.view, 300, 400)
	tt.Move(100, 200)
	tt.Scroll(100, 200, 0, 600)
	tt.Advance(settleFade)
	if h.fade.alpha != 0 {
		t.Fatalf("thumb still visible, alpha %v", h.fade.alpha)
	}

	// The content is replaced by one that shows from elsewhere.
	h.epoch++
	h.rows = 120
	h.list.ScrollTo(80, ui.Start)
	tt.Frame()
	for i := 0; i < 40; i++ {
		tt.Advance(16 * time.Millisecond)
		if _, _, n := thumbPixels(tt.Image(), 300, 0); n != 0 || h.fade.alpha != 0 {
			t.Fatalf("frame %d after the switch drew the thumb: %d pixels, alpha %v", i, n, h.fade.alpha)
		}
	}

	// The same jump with no switch is scrolling, as is the user's.
	h.list.ScrollTo(10, ui.Start)
	tt.Frame()
	if h.fade.alpha != 1 {
		t.Fatalf("jump within the same content did not show the thumb, alpha %v", h.fade.alpha)
	}
	tt.Advance(settleFade)
	if h.fade.alpha != 0 {
		t.Fatalf("thumb still visible, alpha %v", h.fade.alpha)
	}

	// A switch while the thumb shows takes it away.
	tt.Scroll(100, 200, 0, 300)
	if h.fade.alpha != 1 {
		t.Fatalf("thumb not visible after scrolling, alpha %v", h.fade.alpha)
	}
	h.epoch++
	tt.Frame()
	if h.fade.alpha != 0 {
		t.Fatalf("thumb of the old content still visible, alpha %v", h.fade.alpha)
	}
	tt.Scroll(100, 200, 0, 300)
	tt.Advance(scrollFadeQuiet + 100*time.Millisecond)
	tt.Scroll(100, 200, 0, 300)
	if h.fade.alpha != 1 {
		t.Fatalf("thumb not visible when scrolling resumed, alpha %v", h.fade.alpha)
	}
}

func TestScrollFadeListWithoutRowsOrLayoutShowsNothing(t *testing.T) {
	h := &fadeList{rows: 0}
	tt := startFade(h.view, 300, 400)
	tt.Advance(time.Second)
	// Rows arrive, as a repository loads.
	h.rows = 200
	tt.Frame()
	tt.Advance(time.Second)
	if h.fade.alpha != 0 {
		t.Fatalf("rows arriving showed the thumb, alpha %v", h.fade.alpha)
	}
}

func TestScrollFadeEditor(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%v", dark), func(t *testing.T) {
			h := newFadeEditor(2000)
			tt := startFade(h.view, 600, 400)
			tt.SetDark(dark)
			tt.Move(300, 200)
			// The caret's mark sits at the top of the bar whenever the pointer is
			// over the editor; look below it.
			const below = 8
			if _, _, n := thumbPixels(tt.Image(), 600, below); n != 0 || h.fade.alpha != 0 {
				t.Fatalf("thumb drawn before any scrolling: %d pixels, alpha %v", n, h.fade.alpha)
			}

			tt.Scroll(300, 200, 0, 800)
			if h.state.ScrollY() == 0 {
				t.Fatal("editor did not scroll")
			}
			if _, _, n := thumbPixels(tt.Image(), 600, below); n == 0 || h.fade.alpha != 1 {
				t.Fatalf("thumb not visible after scrolling: %d pixels, alpha %v", n, h.fade.alpha)
			}
			tt.Advance(scrollFadeHold - 100*time.Millisecond)
			if _, _, n := thumbPixels(tt.Image(), 600, below); n == 0 || h.fade.alpha != 1 {
				t.Fatalf("thumb gone during the hold: %d pixels, alpha %v", n, h.fade.alpha)
			}
			tt.Advance(settleFade)
			if _, _, n := thumbPixels(tt.Image(), 600, below); n != 0 || h.fade.alpha != 0 {
				t.Fatalf("thumb still visible after the fade: %d pixels, alpha %v", n, h.fade.alpha)
			}
			if tt.FramePending() {
				t.Fatal("frame pending after the thumb faded out")
			}
			built := h.builds
			tt.Advance(10 * time.Second)
			if h.builds != built {
				t.Fatalf("%d frames built while idle", h.builds-built)
			}
		})
	}
}

func TestScrollFadeEditorContentSwitchDoesNotRevealThumb(t *testing.T) {
	h := newFadeEditor(2000)
	tt := startFade(h.view, 600, 400)
	tt.Move(300, 200)
	tt.Scroll(300, 200, 0, 20000)
	tt.Advance(settleFade)

	// A shorter file takes the editor to its start.
	h.epoch++
	h.state.Buffer.Reset("a\nb\nc\n")
	tt.Frame()
	if h.state.ScrollY() != 0 {
		t.Fatalf("editor kept its place in a file of three lines: %v", h.state.ScrollY())
	}
	for i := 0; i < 40; i++ {
		tt.Advance(16 * time.Millisecond)
		if h.fade.alpha != 0 {
			t.Fatalf("frame %d after the switch showed the thumb, alpha %v", i, h.fade.alpha)
		}
	}
}

// The marks of changes against the base stay on the bar whether or not the
// thumb shows: they are the editor's own, outside the theme's Scrollbar.
func TestScrollFadeLeavesEditorChangeMarksVisible(t *testing.T) {
	h := newFadeEditor(2000)
	base := strings.Split(h.state.Buffer.String(), "\n")
	for i := 100; i < 2000; i += 150 {
		base[i] = "changed"
	}
	h.state.DiffBase = editor.NewBuffer(strings.Join(base, "\n"))
	tt := startFade(h.view, 600, 400)
	marksOf := func(tt *ui.Tester) (n int) {
		img := tt.Image()
		// Below the caret's mark at the top, which shows while the pointer is
		// over the editor.
		for y := 8; y < img.Bounds().Dy(); y++ {
			ref := img.RGBAAt(300, y)
			for x := 590; x < 592; x++ {
				px := img.RGBAAt(x, y)
				if abs8(px.R, ref.R)+abs8(px.G, ref.G)+abs8(px.B, ref.B) > 6 {
					n++
				}
			}
		}
		return n
	}
	marks := func() int { return marksOf(tt) }
	// Without a base there are none to see.
	plain := newFadeEditor(2000)
	pt := startFade(plain.view, 600, 400)
	pt.Move(300, 200)
	if n := marksOf(pt); n != 0 {
		t.Fatalf("%d pixels of marks on the bar of an editor with no changes", n)
	}

	tt.Move(300, 200)
	if marks() == 0 {
		t.Fatal("no change marks drawn on the bar")
	}
	tt.Scroll(300, 200, 0, 800)
	tt.Advance(settleFade)
	if h.fade.alpha != 0 || marks() == 0 {
		t.Fatalf("change marks gone with the thumb: alpha %v, %d pixels", h.fade.alpha, marks())
	}
	tt.Move(300, 500)
	if marks() == 0 {
		t.Fatal("change marks gone when the pointer left the editor")
	}
}

func TestScrollFadeDragHiddenThumbOfList(t *testing.T) {
	h := &fadeList{rows: 400}
	tt := startFade(h.view, 300, 400)
	tt.Move(100, 200)
	tt.Scroll(100, 200, 0, 600)
	top, bottom, n := thumbPixels(tt.Image(), 300, 0)
	if n == 0 {
		t.Fatal("thumb not drawn after scrolling")
	}
	first0, _ := h.list.Visible()
	tt.Advance(settleFade)
	if _, _, n := thumbPixels(tt.Image(), 300, 0); n != 0 {
		t.Fatalf("thumb still drawn after the fade: %d pixels", n)
	}

	// The thumb is gone but the bar is not: a press where it was grabs it.
	x, y := float32(300-6), float32(top+bottom)/2
	tt.Move(x, y)
	tt.Press(x, y)
	tt.Move(x, y+30)
	first1, _ := h.list.Visible()
	if first1 <= first0+10 {
		t.Fatalf("dragging the hidden thumb did not scroll: first row %d, was %d", first1, first0)
	}
	if _, _, n := thumbPixels(tt.Image(), 300, 0); n == 0 || h.fade.alpha != 1 {
		t.Fatalf("thumb not visible while dragged: %d pixels, alpha %v", n, h.fade.alpha)
	}

	// Held still, the thumb fades like any other; moving again brings it
	// back and the drag goes on.
	tt.Advance(2 * time.Second)
	tt.Move(x, y+80)
	first2, _ := h.list.Visible()
	if first2 <= first1+10 || h.fade.alpha != 1 {
		t.Fatalf("drag stopped after a pause: first row %d, was %d, alpha %v", first2, first1, h.fade.alpha)
	}
	tt.Release(x, y+80)
	tt.Advance(settleFade)
	if _, _, n := thumbPixels(tt.Image(), 300, 0); n != 0 || h.fade.alpha != 0 || tt.FramePending() {
		t.Fatalf("thumb not hidden after the drag: %d pixels, alpha %v, pending %v", n, h.fade.alpha, tt.FramePending())
	}
}

func TestScrollFadeDragHiddenThumbOfEditor(t *testing.T) {
	h := newFadeEditor(2000)
	tt := startFade(h.view, 600, 400)
	tt.Move(300, 200)
	tt.Scroll(300, 200, 0, 800)
	const below = 8
	top, bottom, n := thumbPixels(tt.Image(), 600, below)
	if n == 0 {
		t.Fatal("thumb not drawn after scrolling")
	}
	y0 := h.state.ScrollY()
	tt.Advance(settleFade)
	if _, _, n := thumbPixels(tt.Image(), 600, below); n != 0 {
		t.Fatalf("thumb still drawn after the fade: %d pixels", n)
	}

	x, y := float32(600-5), float32(top+bottom)/2
	tt.Move(x, y)
	tt.Press(x, y)
	tt.Move(x, y+30)
	y1 := h.state.ScrollY()
	if y1 <= y0+500 {
		t.Fatalf("dragging the hidden thumb did not scroll: %v, was %v", y1, y0)
	}
	if _, _, n := thumbPixels(tt.Image(), 600, below); n == 0 || h.fade.alpha != 1 {
		t.Fatalf("thumb not visible while dragged: %d pixels, alpha %v", n, h.fade.alpha)
	}
	tt.Advance(2 * time.Second)
	tt.Move(x, y+80)
	if y2 := h.state.ScrollY(); y2 <= y1+500 || h.fade.alpha != 1 {
		t.Fatalf("drag stopped after a pause: %v, was %v, alpha %v", y2, y1, h.fade.alpha)
	}
	tt.Release(x, y+80)
	tt.Advance(settleFade)
	if _, _, n := thumbPixels(tt.Image(), 600, below); n != 0 || h.fade.alpha != 0 || tt.FramePending() {
		t.Fatalf("thumb not hidden after the drag: %d pixels, alpha %v, pending %v", n, h.fade.alpha, tt.FramePending())
	}
}

// newFadeApp is the app on a repository with more files, changes and
// commits than its panes show.
func newFadeApp(t *testing.T, mode int) *app {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("file%02d.txt", i)
		var sb strings.Builder
		for l := 0; l < 400; l++ {
			fmt.Fprintf(&sb, "line %d of %s\n", l, name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, name)
	}
	var changes []git.FileStatus
	for i := 0; i < 40; i++ {
		changes = append(changes, git.FileStatus{Path: fmt.Sprintf("changed-%02d.go", i), Work: 'M'})
	}
	var commits []git.Commit
	for i := 0; i < 60; i++ {
		var parents []string
		if i < 59 {
			parents = []string{fmt.Sprintf("hash-%02d", i+1)}
		}
		commits = append(commits, git.Commit{
			Hash:      fmt.Sprintf("hash-%02d", i),
			ShortHash: fmt.Sprintf("c%02d", i),
			Parents:   parents,
			Subject:   fmt.Sprintf("commit-msg-%02d", i),
		})
	}
	a := newApp(dir)
	a.repo = &git.RepoSnapshot{
		Root:        dir,
		Branch:      "main",
		Unstaged:    changes,
		StatusByRel: map[string]string{},
		TreeRoots:   files,
		TreeDirs:    map[string][]string{},
		AllFiles:    files,
		Commits:     commits,
		Graph:       git.ComputeGraph(commits),
	}
	a.activeMode = mode
	a.selectExplorerFile("file00.txt")
	return a
}

// fadesOf names the scrollers of a, to look at them all.
func fadesOf(a *app) map[string]*scrollFade {
	return map[string]*scrollFade{"tree": &a.fades.tree, "code": &a.fades.code, "changes": &a.fades.changes, "graph": &a.fades.graph}
}

// stripDiff counts the pixels of r that differ between two frames.
func stripDiff(a, b *image.RGBA, r image.Rectangle) (n int) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p, q := a.RGBAAt(x, y), b.RGBAAt(x, y)
			if abs8(p.R, q.R)+abs8(p.G, q.G)+abs8(p.B, q.B) > 6 {
				n++
			}
		}
	}
	return n
}

func TestScrollFadeIsWiredToEachScroller(t *testing.T) {
	findRow := func(text string) func(*ui.Tester) (float32, float32) {
		return func(tt *ui.Tester) (float32, float32) {
			r, ok := tt.Find(text)
			if !ok {
				t.Fatalf("%q not in view: %v", text, tt.Texts())
			}
			return r.X + 4, r.Y + r.H/2
		}
	}
	for _, tc := range []struct {
		name string
		mode int
		at   func(*ui.Tester) (float32, float32)
		// strip is where the thumb is in the window, where nothing else is.
		strip image.Rectangle
	}{
		{"tree", modeFiles, findRow("file10.txt"), image.Rect(272, 120, 277, 780)},
		{"code", modeFiles, func(*ui.Tester) (float32, float32) { return 800, 400 }, image.Rect(1272, 60, 1278, 780)},
		{"changes", modeGit, findRow("changed-03.go"), image.Rectangle{}},
		{"graph", modeGit, findRow("commit-msg-03"), image.Rectangle{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newFadeApp(t, tc.mode)
			tt := ui.NewTester(a.view, 1280, 800)
			tt.Advance(time.Second)
			for name, f := range fadesOf(a) {
				if f.alpha != 0 {
					t.Fatalf("%s thumb visible before any scrolling, alpha %v", name, f.alpha)
				}
			}
			if tt.FramePending() {
				t.Fatal("frame pending before any scrolling")
			}

			x, y := tc.at(tt)
			tt.Move(x, y)
			tt.Scroll(x, y, 0, 400)
			shown := tt.Image()
			for name, f := range fadesOf(a) {
				want := float32(0)
				if name == tc.name {
					want = 1
				}
				if f.alpha != want {
					t.Fatalf("after scrolling the %s, the %s thumb has alpha %v, want %v", tc.name, name, f.alpha, want)
				}
			}
			tt.Advance(settleFade)
			if alpha := fadesOf(a)[tc.name].alpha; alpha != 0 {
				t.Fatalf("%s thumb visible after the fade, alpha %v", tc.name, alpha)
			}
			if tt.FramePending() {
				t.Fatal("frame pending after the thumb faded out")
			}
			if tc.strip.Empty() {
				return
			}
			hidden := tt.Image()
			if stripDiff(shown, hidden, tc.strip) == 0 {
				t.Fatalf("%s thumb not drawn while scrolling", tc.name)
			}
			// With the pointer away no thumb is drawn at all: the frame is the same.
			tt.Move(640, 12)
			if n := stripDiff(hidden, tt.Image(), tc.strip); n != 0 {
				t.Fatalf("%s thumb still drawn after the fade: %d pixels", tc.name, n)
			}
		})
	}
}

func TestScrollFadeSwitchingFilesDoesNotRevealCodeThumb(t *testing.T) {
	a := newFadeApp(t, modeFiles)
	// A file of three lines clamps the place the last one left, and one of
	// long lines wraps and measures over the frames after the switch.
	long := strings.Repeat("long line of text ", 20) + "\n"
	for name, text := range map[string]string{"short.txt": "a\nb\nc\n", "mid.txt": strings.Repeat(long, 120)} {
		if err := os.WriteFile(filepath.Join(a.repo.Root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.repo.TreeRoots = append([]string{"short.txt", "mid.txt"}, a.repo.TreeRoots...)
	a.repo.AllFiles = a.repo.TreeRoots
	for _, wrap := range []bool{false, true} {
		a.codeSoftWrap = wrap
		tt := ui.NewTester(a.view, 1280, 800)
		tt.Advance(time.Second)
		tt.Move(800, 400)
		tt.Scroll(800, 400, 0, 3000)
		tt.Advance(settleFade)

		for _, name := range []string{"short.txt", "mid.txt", "file03.txt", "mid.txt"} {
			if err := tt.Click(name); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 60; i++ {
				tt.Advance(16 * time.Millisecond)
				if a.fades.code.alpha != 0 {
					t.Fatalf("wrap=%v: frame %d after opening %s drew the code thumb, alpha %v", wrap, i, name, a.fades.code.alpha)
				}
			}
		}
		// Scrolling the new file shows it again.
		tt.Move(800, 400)
		tt.Scroll(800, 400, 0, 100)
		if a.fades.code.alpha != 1 {
			t.Fatalf("wrap=%v: scrolling the new file did not show the thumb, alpha %v", wrap, a.fades.code.alpha)
		}
	}
}

func TestScrollFadeModeSwitchDoesNotRevealThumbs(t *testing.T) {
	a := newFadeApp(t, modeGit)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Advance(time.Second)
	r, ok := tt.Find("commit-msg-03")
	if !ok {
		t.Fatalf("commit-msg-03 not in view: %v", tt.Texts())
	}
	tt.Move(r.X+4, r.Y+4)
	tt.Scroll(r.X+4, r.Y+4, 0, 400)
	tt.Advance(settleFade)

	for _, mode := range []int{modeFiles, modeGit, modeFiles, modeGit} {
		a.activeMode = mode
		a.contentSeq++
		for i := 0; i < 40; i++ {
			tt.Frame()
			tt.Advance(16 * time.Millisecond)
			for name, f := range fadesOf(a) {
				if f.alpha != 0 {
					t.Fatalf("mode %d, frame %d: %s thumb visible, alpha %v", mode, i, name, f.alpha)
				}
			}
		}
	}
}
