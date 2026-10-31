package main

import (
	"arbor/perf"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// A picker is a popover with a search field over a list of choices and a few
// actions under it: the branch picker, and the repository menu of the sidebar.
// The caller provides the rows; the picker filters them, keeps the highlight,
// and scrolls. Focus stays in the search field all along, so the arrow keys,
// Enter and Escape are read from it (OnTextKey) and the list never takes the
// keyboard.

const (
	pickerWidth     = 340
	pickerMaxHeight = 420
	pickerRowHeight = 28
	pickerSearchH   = 40
	pickerListPad   = 4 // above the first row and below the last
	pickerEmptyH    = 56
	pickerPageStep  = 8
)

// pickerRow is one choice.
type pickerRow struct {
	// Key identifies the row among its picker's rows.
	Key string
	// Label is the text of the row. The filter matches it, and a tooltip
	// shows it whole when it is cut short.
	Label string
	// Hint is dim text after the label, such as a directory. The filter
	// matches it too.
	Hint string
	// Tip is the tooltip of the label; Label when empty.
	Tip string
	// Current marks what is chosen now.
	Current bool
	// Tag is a small pill after the label, and TagTip its tooltip.
	Tag, TagTip string
	// Right is dim text at the right edge, such as when a branch was last
	// committed to.
	Right string
}

// pickerAction is a row under the list that does something instead of
// choosing.
type pickerAction struct {
	Key   string
	Label string
	Icon  *ui.SVG
	Do    func()
}

// pickerSpec is what a picker shows in one frame.
type pickerSpec struct {
	// Key tells this picker's search field from another's.
	Key         string
	Placeholder string
	// Empty is shown when no row matches.
	Empty string
	// Source says what Build reads: when it is not equal to what the last
	// call gave (a comparable value, such as a pointer), Build is called
	// again. It is also called when the picker opens.
	Source any
	Build  func() []pickerRow
	// Choose is called, after the picker closed, with the row clicked or
	// highlighted when Enter was pressed.
	Choose func(row pickerRow)
	// ChooseNew, when set, is called in its place for a row chosen with Shift
	// or Alt held: clicked so, or Shift+Enter.
	ChooseNew func(row pickerRow)
	// Actions are the rows under the list, for the text in the search field
	// and whether a row's label is exactly that text.
	Actions func(query string, exact bool) []pickerAction
	// Paints, if set, has the picker end the spans waiting on it at the
	// first paint of the panel.
	Paints *perf.PaintTracker
}

// picker is the state of one picker, kept in the app. The zero value is a
// closed picker.
type picker struct {
	open  bool
	query string

	// hi is the highlighted item: the matching rows, then the actions.
	hi int
	// The highlight follows the item under the pointer when the pointer moves
	// (moved, from where it was: px, py), but not while the list scrolls
	// under a pointer that rests.
	moved  bool
	px, py float32
	// moves and enter are keys pressed in the search field, applied when the
	// frame is built, with the rows that the text then in the field leaves.
	moves int
	enter bool
	// alt tells that Enter came with Shift.
	alt bool

	wasOpen bool
	built   bool
	source  any
	rows    []pickerRow
	lower   []string

	matched bool
	matchQ  string
	matches []int
	exact   bool
	// epoch changes when the filter replaces the rows shown, which is not
	// scrolling to the scroll bar.
	epoch uint64

	list ui.ListState
	fade scrollFade
}

// reset readies the picker for a new opening.
func (p *picker) reset() {
	p.query = ""
	p.hi, p.moves, p.enter = 0, 0, false
	p.built, p.matched = false, false
	p.rows, p.lower, p.matches = nil, nil, nil
	p.list = ui.ListState{}
	p.epoch++
}

// onKey is the search field's OnTextKey. It only records: the frame that
// follows applies it to the rows as they then are.
func (p *picker) onKey(k ui.TextKey) bool {
	if k.Mods == ui.Shift && k.Key == ui.KeyEnter {
		p.enter, p.alt = true, true
		return true
	}
	if k.Mods != 0 {
		return false
	}
	switch k.Key {
	case ui.KeyDown:
		p.moves++
	case ui.KeyUp:
		p.moves--
	case ui.KeyPageDown:
		p.moves += pickerPageStep
	case ui.KeyPageUp:
		p.moves -= pickerPageStep
	case ui.KeyEnter:
		p.enter = true
	case ui.KeyEscape:
		p.open = false
	default:
		return false
	}
	return true
}

// view shows the picker below anchor while it is open.
func (p *picker) view(c *ui.Context, t *ui.Theme, anchor ui.Element, spec pickerSpec) {
	if !p.open {
		p.wasOpen = false
		return
	}
	if !p.wasOpen {
		p.reset()
		p.wasOpen = true
	}
	ui.PopoverBase(c, anchor, &p.open, func(panel ui.Element) { p.panel(c, t, panel, spec) })
}

func (p *picker) panel(c *ui.Context, t *ui.Theme, panel ui.Element, spec pickerSpec) {
	if !p.built || p.source != spec.Source {
		p.rows = spec.Build()
		p.lower = lowerLabels(p.rows)
		p.source, p.built, p.matched = spec.Source, true, false
	}
	q := strings.TrimSpace(p.query)
	if !p.matched || q != p.matchQ {
		if p.matched {
			// The text changed: the best match is the first.
			p.hi = 0
			p.list.ScrollTo(0, ui.Start)
			p.epoch++
		}
		p.matches = filterRows(p.rows, p.lower, q)
		p.exact = false
		if q != "" {
			for _, i := range p.matches {
				if p.rows[i].Label == q {
					p.exact = true
					break
				}
			}
		}
		p.matchQ, p.matched = q, true
	}
	var actions []pickerAction
	if spec.Actions != nil {
		actions = spec.Actions(q, p.exact)
	}
	n := len(p.matches)
	total := n + len(actions)

	// The keys move the highlight; so does the pointer, in the items below.
	px, py, _ := panel.PointerPosition()
	p.moved = px != p.px || py != p.py
	p.px, p.py = px, py
	if p.moves != 0 {
		p.hi += p.moves
		p.moves = 0
		if total > 0 {
			p.hi = min(max(p.hi, 0), total-1)
			if p.hi < n {
				p.list.ScrollIntoView(p.hi)
			}
		}
	}
	p.hi = min(max(p.hi, 0), max(total-1, 0))

	// Sizes: the list takes the room the search field and the actions leave
	// up to the picker's height, and no more than its rows need.
	maxH := float32(pickerMaxHeight)
	if _, h := c.Size(); h > 0 {
		maxH = min(maxH, max(h-80, 160))
	}
	footH := float32(0)
	if len(actions) > 0 {
		footH = float32(len(actions))*pickerRowHeight + 2*pickerListPad + 1
	}
	listH := float32(pickerEmptyH)
	if n > 0 {
		listH = min(float32(n)*pickerRowHeight+2*pickerListPad, max(maxH-2-pickerSearchH-1-footH, pickerRowHeight+2*pickerListPad))
	}

	shadow := ui.RGBA(0, 0, 0, 0.12)
	if t.Dark {
		shadow = ui.RGBA(0, 0, 0, 0.42)
	}
	panel.Width(pickerWidth).MaxHeight(maxH).Radius(8).Clip().
		Background(t.Surface).Border(1, t.Border).Shadow(0, 6, 22, 0, shadow).
		TextColor(t.Text).FontSize(13).Margin(4, 0, 0, 0).
		Transition(ui.ElementTransition{
			Duration: 120 * time.Millisecond,
			Enter:    &ui.Motion{Opacity: 0, Y: -4},
			Exit:     &ui.Motion{Opacity: 0, Y: -4},
		})

	picked, alt := -1, false
	ui.Row(c).Height(pickerSearchH).Shrink(0).Padding(0, 12).Gap(8).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Icon(c, IconSearch).Size(14, 14).Shrink(0).TextColor(t.TextMuted)
		ui.TextInputBase(c.Key("picker-q:"+spec.Key), &p.query).Placeholder(spec.Placeholder).
			Label(spec.Placeholder).FontSize(13).Grow(1).Shrink(1).MinWidth(0).
			AutoFocus().OnTextKey(p.onKey)
	})

	if n == 0 {
		ui.Row(c).Height(pickerEmptyH).Shrink(0).Center().Children(func() {
			ui.Text(c, spec.Empty).FontSize(12).TextColor(t.TextMuted)
		})
	} else {
		p.list.Key = func(i int) any { return p.rows[p.matches[i]].Key }
		p.fade.Wrap(c, t, func() scrollMark {
			ui.List(c, &p.list, n, func(i int) {
				if p.rowView(c, t, i, p.rows[p.matches[i]]) {
					picked, alt = i, c.Modifiers()&(ui.Shift|ui.Alt) != 0
				}
			}).Grow(1).MinHeight(0).Padding(pickerListPad, 4)
			first, last := p.list.Visible()
			return scrollMark{ok: last >= first, epoch: p.epoch, size: n, a: float64(first), b: float64(last)}
		}).Height(listH).Shrink(0)
	}

	if len(actions) > 0 {
		ui.Column(c).Shrink(0).Padding(pickerListPad, 4).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			for k, act := range actions {
				if p.actionView(c, t, n+k, act) {
					picked = n + k
				}
			}
		})
	}

	if spec.Paints != nil && spec.Paints.Pending() {
		paints := spec.Paints
		ui.Box(c).Absolute().Width(1).Height(1).Draw(func(*ui.Painter, ui.Rect) { paints.Painted() })
	}

	if p.enter {
		p.enter = false
		if total > 0 {
			picked, alt = p.hi, p.alt
		}
		p.alt = false
	}
	switch {
	case picked < 0:
	case picked < n:
		row := p.rows[p.matches[picked]]
		p.open = false
		switch {
		case alt && spec.ChooseNew != nil:
			spec.ChooseNew(row)
		case spec.Choose != nil:
			spec.Choose(row)
		}
	case picked-n < len(actions):
		act := actions[picked-n]
		p.open = false
		if act.Do != nil {
			act.Do()
		}
	}
}

// pickerItem is the row every item of a picker is: 28 high, highlighted like
// the selected row of the file tree.
func pickerItem(c *ui.Context, t *ui.Theme, key, label string, highlighted bool) ui.Element {
	row := ui.Row(c.Key(key)).Height(pickerRowHeight).Padding(0, 8).Gap(8).Radius(5).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	bg, bd := t.Surface.Alpha(0), t.Accent.Alpha(0)
	switch {
	case highlighted:
		bg, bd = t.Accent.Alpha(0.10), t.Accent.Alpha(0.22)
		if row.Pressed() {
			bg = t.Accent.Alpha(0.15)
		}
	case row.Pressed():
		bg = t.SurfacePressed
	case row.Hovered():
		bg = t.SurfaceHover
	}
	return row.Background(row.AnimateColor("bg", bg, microColorAnim)).
		Border(1, row.AnimateColor("bd", bd, microColorAnim))
}

// follow moves the highlight to item idx when the pointer moved onto it. The
// items built before it have been styled with the old highlight, so another
// frame shows the new.
func (p *picker) follow(c *ui.Context, row ui.Element, idx int) {
	if p.moved && p.hi != idx && row.Hovered() {
		p.hi = idx
		c.Invalidate()
	}
}

// rowView builds the item for matching row i and reports whether it was
// clicked.
func (p *picker) rowView(c *ui.Context, t *ui.Theme, i int, r pickerRow) bool {
	row := pickerItem(c, t, "pk:"+r.Key, r.Label, p.hi == i)
	p.follow(c, row, i)
	row.Children(func() {
		// The marker's room is there whether it shows or not, so the labels
		// line up.
		ui.Row(c).Width(12).Shrink(0).Center().Children(func() {
			if r.Current {
				ui.Icon(c, IconCheck).Size(12, 12).TextColor(t.Accent)
			}
		})
		tip := r.Tip
		if tip == "" {
			tip = r.Label
		}
		name := ui.Text(c, r.Label).FontSize(12.5).SingleLine().Shrink(1).MinWidth(0).Tooltip(tip)
		if r.Current {
			name.FontWeight(500)
		}
		if r.Tag != "" {
			pill := ui.Row(c).Padding(1, 5).Radius(3.5).Shrink(0).AlignItems(ui.Center).
				Background(t.TextMuted.Alpha(0.14))
			if r.TagTip != "" {
				pill.Tooltip(r.TagTip)
			}
			pill.Children(func() {
				ui.Text(c, r.Tag).FontSize(10).TextColor(t.TextMuted).SingleLine()
			})
		}
		if r.Hint != "" {
			ui.Text(c, r.Hint).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1).Shrink(2).MinWidth(0)
		} else {
			ui.Spacer(c)
		}
		if r.Right != "" {
			ui.Text(c, r.Right).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
	})
	return row.Clicked()
}

// actionView builds action k, item index idx, and reports whether it was
// clicked.
func (p *picker) actionView(c *ui.Context, t *ui.Theme, idx int, act pickerAction) bool {
	row := pickerItem(c, t, "pka:"+act.Key, act.Label, p.hi == idx)
	p.follow(c, row, idx)
	row.Children(func() {
		if act.Icon != nil {
			ui.Row(c).Width(12).Shrink(0).Center().Children(func() {
				ui.Icon(c, act.Icon).Size(12, 12).TextColor(t.TextMuted)
			})
		}
		ui.Text(c, act.Label).FontSize(12.5).SingleLine().Shrink(1).MinWidth(0).Tooltip(act.Label)
	})
	return row.Clicked()
}

// lowerLabels are the strings the filter searches, one for each row.
func lowerLabels(rows []pickerRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		if r.Hint != "" {
			out[i] = strings.ToLower(r.Label + "\x00" + r.Hint)
		} else {
			out[i] = strings.ToLower(r.Label)
		}
	}
	return out
}

// filterRows returns the indexes of the rows that contain every word of query,
// in the order of the rows except that those whose label is the query come
// first, then those that start with its first word, then those that have it
// in the label rather than the hint.
func filterRows(rows []pickerRow, lower []string, query string) []int {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		all := make([]int, len(rows))
		for i := range all {
			all[i] = i
		}
		return all
	}
	whole := strings.Join(words, " ")
	var tiers [4][]int
	for i := range rows {
		hay := lower[i]
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		label := hay
		if k := strings.IndexByte(hay, 0); k >= 0 {
			label = hay[:k]
		}
		switch {
		case label == whole:
			tiers[0] = append(tiers[0], i)
		case strings.HasPrefix(label, words[0]):
			tiers[1] = append(tiers[1], i)
		case strings.Contains(label, words[0]):
			tiers[2] = append(tiers[2], i)
		default:
			tiers[3] = append(tiers[3], i)
		}
	}
	out := make([]int, 0, len(tiers[0])+len(tiers[1])+len(tiers[2])+len(tiers[3]))
	for _, tier := range tiers {
		out = append(out, tier...)
	}
	return out
}

// shortAgo says how long before now t was in the fewest characters: "now",
// "5m", "3h", "2d", "3w", "4mo", "2y". It is empty for a time not known.
func shortAgo(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d/(30*24*time.Hour)))
	}
	return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
}

// ============================================================================
// The branch picker
// ============================================================================

// toggleBranchPicker opens the branch picker, or closes it. The span of the
// operation ends at the first paint with the panel on screen.
func (a *app) toggleBranchPicker() {
	a.branchPick.open = !a.branchPick.open
	if a.branchPick.open {
		a.paints.Arm(perf.Begin(perf.OpBranchPicker), perf.ResultOK)
	}
}

// branchRows are the rows of the branch picker for repo: the current branch
// on top, then the others as the snapshot has them, the most recently
// committed to first.
func branchRows(repo *RepoSnapshot, now time.Time) []pickerRow {
	rows := make([]pickerRow, 0, len(repo.Branches))
	cur := -1
	for _, b := range repo.Branches {
		r := pickerRow{Key: b.Name, Label: b.Name, Current: b.Name == repo.Branch, Right: shortAgo(now, b.Time)}
		if r.Current {
			cur = len(rows)
		} else if b.Worktree != "" {
			r.Tag, r.TagTip = "worktree", "Checked out in "+b.Worktree
		}
		rows = append(rows, r)
	}
	if cur > 0 {
		current := rows[cur]
		copy(rows[1:cur+1], rows[:cur])
		rows[0] = current
	}
	return rows
}

// ============================================================================
// The repository menu
// ============================================================================

// repoMenuSource says what the rows of the repository menu were read from.
type repoMenuSource struct {
	repo *RepoSnapshot
	rev  uint64
}

// repoPicker shows the repository menu under anchor, the repository button:
// the repository on show and the recent ones, then Open… and Open in New
// Window…. A repository is chosen to show it in this window, or with Shift or
// Alt held, in a window of its own; one that a window shows already is
// brought to the front instead, either way.
func (a *app) repoPicker(c *ui.Context, t *ui.Theme, anchor ui.Element) {
	repo, reg := a.repo, a.reg
	src := repoMenuSource{repo: repo}
	if reg != nil {
		src.rev = reg.rev
	}
	a.repoPick.view(c, t, anchor, pickerSpec{
		Key:         "repo",
		Placeholder: "Search repositories…",
		Empty:       "No repositories match",
		Source:      src,
		Build: func() []pickerRow {
			if reg != nil {
				return reg.repoRows(a)
			}
			return []pickerRow{{Key: repo.Root, Label: repoName(repo.Root), Hint: tildePath(filepath.Dir(repo.Root)), Current: true}}
		},
		Choose: func(r pickerRow) {
			if !r.Current {
				a.requestOpen(r.Key, false)
			}
		},
		ChooseNew: func(r pickerRow) {
			if !r.Current {
				a.requestOpen(r.Key, true)
			}
		},
		Actions: func(string, bool) []pickerAction {
			if reg == nil {
				return nil
			}
			return []pickerAction{
				{Key: "open", Label: "Open…", Icon: IconFolderOpen, Do: func() { a.openRepoDialog(false) }},
				{Key: "open-new", Label: "Open in New Window…", Icon: IconFolderOpen, Do: func() { a.openRepoDialog(true) }},
			}
		},
	})
}

// openNewBranch opens the dialog that creates a branch, with name in it.
func (a *app) openNewBranch(name string) {
	a.newBranchName = name
	a.newBranchOpen = true
}

// branchPicker shows the branch picker under anchor, the branch button.
func (a *app) branchPicker(c *ui.Context, t *ui.Theme, anchor ui.Element) {
	repo := a.repo
	a.branchPick.view(c, t, anchor, pickerSpec{
		Key:         "branch",
		Placeholder: "Search branches…",
		Empty:       "No branches match",
		Source:      repo,
		Paints:      &a.paints,
		Build:       func() []pickerRow { return branchRows(repo, a.cards.clock()) },
		Choose: func(r pickerRow) {
			if !r.Current {
				a.checkoutBranch(r.Key, false)
			}
		},
		Actions: func(query string, exact bool) []pickerAction {
			if query != "" && !exact {
				return []pickerAction{{
					Key: "create", Label: `Create branch "` + query + `"`, Icon: IconPlus,
					Do: func() { a.openNewBranch(query) },
				}}
			}
			return []pickerAction{{Key: "new", Label: "New branch", Icon: IconPlus, Do: func() { a.openNewBranch("") }}}
		},
	})
}
