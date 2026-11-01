package app

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// Scrollbars that stay out of the way. mygo draws a scroll container's
// thumb while the pointer is over it; scrollFade hides it unless the content
// is scrolling, by fading the alpha of the theme's Scrollbar color for the
// subtree it wraps (ui.Themed). The thumb is only hidden, not removed: a
// press where it was still grabs it.
//
// The thumb is fully visible while the scroll position changes, stays for
// scrollFadeHold after it stops, then fades out over scrollFadeOut. No frame
// is asked for once it is hidden.
const (
	scrollFadeHold = 800 * time.Millisecond
	scrollFadeOut  = 200 * time.Millisecond
	// scrollFadeQuiet is how long after a content switch (and after the
	// scroller first shows) its position may change without counting as
	// scrolling: an editor clamps its place to a shorter file and measures
	// wrapped lines, and a list lays out, over a few frames after the switch.
	scrollFadeQuiet = 300 * time.Millisecond
)

// scrollMark is where a scroller was when the view built it.
type scrollMark struct {
	// ok is false while the position is unknown, as for a list that has yet
	// to lay out.
	ok bool
	// epoch identifies the content shown (app.contentSeq), and size how much
	// of it there is (a list's rows). Another epoch or size is a content
	// switch, not scrolling.
	epoch uint64
	size  int
	// a and b are the position: a list's first and last row in view, an
	// editor's ScrollY.
	a, b float64
}

// scrollFade is the state of one fading scroller. Keep one per scroller in
// the app, alive between frames; its zero value is ready, and hidden.
//
// A scroller's own handlers hear its scroll events before any wrapper, so
// scrollFade polls the position once in each frame the view builds. The
// polling asks for no frames itself: a scroll builds one anyway, and only
// the hold and the fade that follow ask for more.
type scrollFade struct {
	// theme is the copy of the app's theme the wrapper builds with. The
	// window paints from it after the build, so its Scrollbar alpha is set
	// once the scroller is built and its position is known, not a frame
	// late.
	theme *ui.Theme
	// alpha is the factor on the Scrollbar color set in the last frame.
	alpha float32
	seen  bool
	mark  scrollMark
	// movedAt is when the position last changed by scrolling, zero for not
	// since the content last switched. A change before quietUntil does not
	// count.
	movedAt, quietUntil time.Time
}

// scrollFades are the scrollers of the app that fade.
type scrollFades struct {
	tree, changes, graph, code scrollFade
}

// Wrap builds body in a column whose scroll bar thumbs, and those of the
// scrollers in body, take the fade's alpha. It returns the column for
// sizing (Grow, MinHeight, ...); it paints nothing of its own, and base is
// the app's theme. body builds the scroller, sized to fill the column, and
// returns where it is scrolled: Wrap is for scrollers List and Editor do
// not cover, such as a list in a popover.
func (f *scrollFade) Wrap(c *ui.Context, base *ui.Theme, body func() scrollMark) ui.Element {
	if f.theme == nil {
		f.theme = new(ui.Theme)
	}
	*f.theme = *base
	wrap := ui.Themed(c, f.theme).Background(ui.Transparent)
	var m scrollMark
	wrap.Children(func() { m = body() })
	f.alpha = f.step(c, m)
	f.theme.Scrollbar = base.Scrollbar.Alpha(f.alpha)
	return wrap
}

// List is ui.List in a fading wrapper that grows to fill its parent. It
// returns the list, for its own sizing and padding. epoch should change
// when what the list shows is replaced, as app.contentSeq does.
func (f *scrollFade) List(c *ui.Context, base *ui.Theme, epoch uint64, s *ui.ListState, n int, row func(i int)) ui.Element {
	var list ui.Element
	f.Wrap(c, base, func() scrollMark {
		list = ui.List(c, s, n, row)
		first, last := s.Visible()
		return scrollMark{ok: last >= first, epoch: epoch, size: n, a: float64(first), b: float64(last)}
	}).Grow(1).MinHeight(0)
	return list
}

// Editor is editor.View in a fading wrapper that grows to fill its parent.
// It returns the editor, which needs a size of its own (Grow, FillWidth).
// Only its vertical thumb follows scrolling: the editor does not report
// sideways scrolling, so the horizontal thumb shows while the lines scroll
// up or down. The marks of changes, matches and diagnostics on the bar stay.
func (f *scrollFade) Editor(c *ui.Context, base *ui.Theme, epoch uint64, st *editor.State) ui.Element {
	var ed ui.Element
	f.Wrap(c, base, func() scrollMark {
		ed = editor.View(c, st)
		return scrollMark{ok: true, epoch: epoch, a: st.ScrollY()}
	}).Grow(1).FillWidth().MinHeight(0)
	return ed
}

// step takes where the scroller is, and returns the alpha of its thumb now.
func (f *scrollFade) step(c *ui.Context, m scrollMark) float32 {
	now := c.Now()
	switch {
	case !m.ok:
		f.seen = false
	case !f.seen || m.epoch != f.mark.epoch || m.size != f.mark.size:
		// First sight, or content that switched: its position is not
		// scrolling, and the thumb of the content before it goes.
		f.seen, f.mark, f.movedAt = true, m, time.Time{}
		f.quietUntil = now.Add(scrollFadeQuiet)
	case m.a != f.mark.a || m.b != f.mark.b:
		f.mark = m
		if !now.Before(f.quietUntil) {
			f.movedAt = now
		}
	}
	if f.movedAt.IsZero() {
		return 0
	}
	out := scrollFadeOut
	if c.Preferences().ReduceMotion {
		out = 0
	}
	idle := now.Sub(f.movedAt)
	switch {
	case idle < scrollFadeHold:
		c.After(scrollFadeHold - idle)
		return 1
	case idle < scrollFadeHold+out:
		c.AnimationFrame()
		return 1 - float32(idle-scrollFadeHold)/float32(out)
	}
	return 0
}
