package main

import (
	"arbor/internal/machine"
	"arbor/internal/perfagg"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"arbor/perf"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
)

// The Performance window is where a person looks over the climb, which agents do
// through arbor-perf. Real usage is one chart per operation, with the builds
// along x, oldest first and evenly spaced, and duration up y from 0. Climb is what
// each kept change did to its benchmarks, and every round tried. Reading and
// aggregating runs off the UI thread and publishes a finished set of cards.

const (
	perfSourceUsage = iota
	perfSourceClimb
)

// perfCardMinWidth decides how many cards fit in a row.
const perfCardMinWidth = 340

// perfRanges are how many of the latest builds the window can cover; 0 is all.
var (
	perfRanges      = []int{5, 10, 0}
	perfRangeLabels = []string{"Last 5", "Last 10", "All"}
)

// trendPoint is one build on a chart: Mid is the line, Lo and Hi the band. Label
// is its version or short commit, and T when it ran, which only hover shows.
type trendPoint struct {
	Label       string
	T           time.Time
	Mid, Lo, Hi float64
	N           int
	// Thin is a build with too few samples to judge, drawn hollow. Gap is one
	// with none, which keeps its slot so the cards line up.
	Thin, Gap bool
}

// trendCard is everything a card draws. It is built off the UI thread and only
// read afterwards.
type trendCard struct {
	Key, Title string
	Budget     float64
	// Current is the number on the card, measured on Build with N samples or runs.
	Current    float64
	HasCurrent bool
	Build      string
	N          int
	// Verdict says whether Current differs from build Base (perfagg.Judge), and
	// Change by how much as a fraction, when HasChange.
	Verdict   string
	Change    float64
	HasChange bool
	Base      string
	Points    []trendPoint
	// Summary is there for real usage, and gives the detail view.
	Summary *perfagg.Summary
}

// perfView is the state of the window.
type perfView struct {
	userData string
	win      hostWindow
	closed   bool
	now      func() time.Time
	flush    func()

	source int
	last   int
	size   string
	detail string
	hover  map[string]*int
	list   ui.ListState

	// seq rejects results of jobs an newer one replaced.
	seq     uint64
	loading bool
	cards   []trendCard
	info    string
	rounds  []perfstore.Round // newest first, for the Climb source

	mu     sync.Mutex // guards model and ledger, which jobs share
	model  *perfagg.Model
	ledger []perfstore.Round
	loaded bool

	// Export preview.
	exportReport func() ([]byte, *perfreport.Export, error)
	saveExport   func(name string, done func(path string))
	preview      string
	previewData  []byte
	previewOpen  bool
	notice       string
}

func newPerfView(userData string) *perfView {
	return &perfView{userData: userData, last: 10, now: time.Now, hover: map[string]*int{}}
}

func (p *perfView) close() {
	if p.win != nil && !p.closed {
		p.win.Close()
	}
}

// run does job off the UI thread and publishes through the window; without a
// window, as in tests, it does both now.
func (p *perfView) run(job func() func()) {
	if p.win == nil {
		job()()
		return
	}
	win := p.win
	go func() {
		publish := job()
		win.Update(func() {
			if !p.closed {
				publish()
			}
		})
	}()
}

// reload reads the data again and rebuilds the cards.
func (p *perfView) reload() {
	if p.closed {
		return
	}
	p.mu.Lock()
	p.loaded = false
	p.mu.Unlock()
	p.rebuild()
}

// rebuild makes the cards for the current choices.
func (p *perfView) rebuild() {
	p.seq++
	seq, source, last, size := p.seq, p.source, p.last, p.size
	p.loading = true
	p.run(func() func() {
		cards, info, rounds := p.compute(source, last, size)
		return func() {
			if seq != p.seq {
				return
			}
			p.loading, p.cards, p.info, p.rounds = false, cards, info, rounds
		}
	})
}

func (p *perfView) compute(source, last int, size string) ([]trendCard, string, []perfstore.Round) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		if p.flush != nil {
			p.flush()
		}
		p.model = perfagg.NewModel(time.Local)
		_, _ = perfstore.ReadDir(perfstore.LogDir(p.userData), p.model.Add)
		p.ledger, _ = perfstore.ReadRounds(filepath.Join(perfstore.BenchDir(p.userData), perfstore.RoundsFile))
		p.loaded = true
	}
	if source == perfSourceClimb {
		cards, info := climbCards(p.ledger, machine.Detect().Key, last, size)
		rounds := slices.Clone(p.ledger)
		slices.Reverse(rounds)
		return cards, info, rounds
	}
	return usageCards(p.model, last, size), fmt.Sprintf("%d operations recorded", p.model.Events()), nil
}

// usageCards has one card per catalog operation, with a slot per build.
func usageCards(m *perfagg.Model, last int, size string) []trendCard {
	var cards []trendCard
	for _, op := range perf.Ops() {
		s := m.Summarize(op.String(), perfagg.Filter{Size: size, Last: last})
		c := trendCard{
			Key: s.Op, Title: s.Op, Budget: op.BudgetMs(), Current: s.Current, HasCurrent: s.N > 0, Build: s.CurrentFrom,
			Verdict: s.Verdict, Change: s.Change, HasChange: s.HasChange, Base: s.BaseVersion, Summary: &s,
		}
		for _, v := range s.Points {
			c.Points = append(c.Points, trendPoint{Label: v.Version, T: v.First, Mid: v.P50, Lo: v.P50, Hi: v.P95, N: v.N, Thin: v.Thin, Gap: v.N == 0})
			if v.Version == s.CurrentFrom {
				c.N = v.N
			}
		}
		cards = append(cards, c)
	}
	// What has data comes first; the rest keep the catalog's order.
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].N > 0 && cards[j].N == 0 })
	return cards
}

// climbCards has one card per benchmark a kept change moved, on this machine
// class: where it started and then where each kept change left it.
func climbCards(rounds []perfstore.Round, key string, last int, size string) ([]trendCard, string) {
	byName := map[string][]trendPoint{}
	kept, tried := 0, 0
	for _, r := range rounds {
		if r.Kind != perfstore.RoundTry {
			continue
		}
		tried++
		if r.Verdict != perfstore.VerdictKeep || r.Machine != key {
			continue
		}
		kept++
		for _, b := range r.Target {
			if size != "" && !strings.HasSuffix(b.Name, "/"+size) {
				continue
			}
			if byName[b.Name] == nil {
				byName[b.Name] = []trendPoint{benchPoint(r.Base, r.T, b.BaseNs)}
			}
			byName[b.Name] = append(byName[b.Name], benchPoint(r.Commit, r.T, b.CurNs))
		}
	}
	var cards []trendCard
	for _, n := range slices.Sorted(maps.Keys(byName)) {
		pts := byName[n]
		if last > 0 && len(pts) > last {
			pts = pts[len(pts)-last:]
		}
		first, newest := pts[0], pts[len(pts)-1]
		c := trendCard{Key: n, Title: n, Current: newest.Mid, HasCurrent: true, Build: newest.Label, N: len(pts) - 1, Points: pts}
		if first.Mid > 0 {
			c.Change, c.HasChange, c.Base = (newest.Mid-first.Mid)/first.Mid, true, first.Label
		}
		cards = append(cards, c)
	}
	return cards, fmt.Sprintf("%d kept of %d rounds", kept, tried)
}

func benchPoint(commit string, t time.Time, ns float64) trendPoint {
	return trendPoint{Label: commit, T: t, Mid: ns / 1e6, Lo: ns / 1e6, Hi: ns / 1e6}
}

// buildTicks labels the slots along the bottom, as many as fit in width at
// the longest label, always with the newest.
func buildTicks(points []trendPoint, width float32) []chart.Tick {
	// A build with uncommitted changes takes less room as commit*.
	short := func(label string) string { return strings.Replace(label, "+dirty", "*", 1) }
	longest := 1
	for _, pt := range points {
		longest = max(longest, len(short(pt.Label)))
	}
	fit := max(1, int(width/float32(longest*6+12)))
	step := max(1, (len(points)+fit-1)/fit)
	var ticks []chart.Tick
	for i := len(points) - 1; i >= 0; i -= step {
		ticks = append(ticks, chart.Tick{At: slotX(i, len(points)), Label: short(points[i].Label)})
	}
	slices.Reverse(ticks)
	return ticks
}

// slotX is the middle of slot i of n, so the slots are evenly spaced.
func slotX(i, n int) float32 {
	return (float32(i) + 0.5) / float32(n)
}

func fmtMs(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("%.2f s", v/1000)
	case v >= 100:
		return fmt.Sprintf("%.0f ms", v)
	case v >= 1:
		return fmt.Sprintf("%.1f ms", v)
	}
	return fmt.Sprintf("%.0f µs", v*1000)
}

func fmtChange(v float64) string {
	if v < 0 {
		return fmt.Sprintf("−%.0f%%", -v*100)
	}
	return fmt.Sprintf("+%.0f%%", v*100)
}

func (p *perfView) view(c *ui.Context) {
	t := applyLinearTheme(c)
	c.Root().Background(t.Background).TextColor(t.Text).FontSize(13)
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		p.header(c, t)
		switch {
		case p.previewOpen:
			p.previewView(c, t)
		case p.detail != "":
			p.detailView(c, t)
		default:
			p.grid(c, t)
		}
	})
}

// chips is a row of choices of which one is on.
func perfChips(c *ui.Context, t *ui.Theme, key string, labels []string, on int, pick func(i int)) {
	ui.Row(c.Key(key)).Gap(2).Padding(2).Radius(7).Background(t.Text.Alpha(0.05)).Shrink(0).Children(func() {
		for i, label := range labels {
			chip := ui.Row(c.Key(key+":"+label)).Padding(3, 9).Radius(5).Cursor(ui.CursorPointer).Label(label)
			if i == on {
				chip.Background(t.Surface)
			}
			chip.Children(func() {
				text := ui.Text(c, label).FontSize(12)
				if i == on {
					text.FontWeight(500)
				} else {
					text.TextColor(t.TextMuted)
				}
			})
			if chip.Clicked() && i != on {
				pick(i)
			}
		}
	})
}

func (p *perfView) header(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Padding(12, 20).Gap(10).AlignItems(ui.Center).Shrink(0).Wrap().BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Gap(1).Children(func() {
			ui.Text(c, "Performance").FontSize(15).FontWeight(600)
			ui.Text(c, p.info).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
		})
		ui.Spacer(c)
		perfChips(c, t, "perf-source", []string{"Real usage", "Climb"}, p.source, func(i int) {
			p.source, p.detail = i, ""
			p.rebuild()
		})
		perfChips(c, t, "perf-range", perfRangeLabels, slices.Index(perfRanges, p.last), func(i int) {
			p.last = perfRanges[i]
			p.rebuild()
		})
		sizes := []string{"", "small", "medium", "large"}
		son := 0
		for i, s := range sizes {
			if s == p.size {
				son = i
			}
		}
		perfChips(c, t, "perf-size", []string{"All sizes", "Small", "Medium", "Large"}, son, func(i int) {
			p.size = sizes[i]
			p.rebuild()
		})
		if p.exportReport != nil && ghostBtn(c, "perf-export", IconDownload, "Export", "Export a report you can share", false) {
			p.beginExport()
		}
	})
}

func (p *perfView) grid(c *ui.Context, t *ui.Theme) {
	w, _ := c.Size()
	cols := max(1, int((w-40+12)/(perfCardMinWidth+12)))
	if len(p.cards) == 0 && len(p.rounds) == 0 {
		ui.Column(c).Grow(1).FillWidth().Center().Gap(6).Children(func() {
			switch {
			case p.loading:
				ui.Text(c, "Loading…").TextColor(t.TextMuted)
			case p.source == perfSourceClimb:
				ui.Text(c, "No rounds yet").FontWeight(500)
				ui.Text(c, "An agent starts with: go run ./cmd/arbor-perf climb next").FontSize(12).TextColor(t.TextMuted).Font("monospace").Selectable()
			default:
				ui.Text(c, "No performance data yet").FontWeight(500)
			}
		})
		return
	}
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).Padding(16, 20).Gap(12).FillWidth().Children(func() {
			if p.source == perfSourceUsage && p.total() == 0 && !p.loading {
				ui.Text(c, "No operations recorded on these versions yet. Use Arbor for a while and they will appear here.").TextColor(t.TextMuted)
			}
			for i := 0; i < len(p.cards); i += cols {
				ui.Row(c).Gap(12).FillWidth().Children(func() {
					for j := i; j < i+cols; j++ {
						if j < len(p.cards) {
							p.card(c, t, &p.cards[j])
						} else {
							ui.Box(c).Grow(1).Width(0)
						}
					}
				})
			}
			if len(p.rounds) > 0 {
				p.roundList(c, t)
			}
		})
	})
}

// roundList is every round tried, newest first: what was tried, and what came of
// it, including what was thrown away.
func (p *perfView) roundList(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(8).FillWidth().Padding(8, 0, 0, 0).Children(func() {
		ui.Text(c, "Rounds").FontWeight(600)
		for _, r := range p.rounds {
			outcome := r.Verdict
			if r.Kind != perfstore.RoundTry {
				outcome = r.Kind
			} else if r.Reason != "" {
				outcome += " · " + strings.ReplaceAll(r.Reason, "_", " ")
			}
			col := t.TextMuted
			switch r.Verdict {
			case perfstore.VerdictKeep:
				col = t.Success
			case perfstore.VerdictRevert:
				col = t.Danger
			}
			ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Text(c, r.T.Local().Format("Jan 2")).Width(44).Shrink(0).FontSize(12).TextColor(t.TextMuted)
				ui.Text(c, r.Op).Width(130).Shrink(0).FontSize(12).SingleLine()
				ui.Text(c, outcome).Width(240).Shrink(0).FontSize(12).TextColor(col).SingleLine()
				ui.Text(c, r.Note).FontSize(12).TextColor(t.TextMuted).Shrink(1).MinWidth(0).SingleLine()
			})
		}
	})
}

func (p *perfView) total() int {
	n := 0
	for i := range p.cards {
		n += p.cards[i].N
	}
	return n
}

func (p *perfView) selection(key string) *int {
	sel := p.hover[key]
	if sel == nil {
		v := -1
		sel = &v
		p.hover[key] = sel
	}
	return sel
}

func (p *perfView) card(c *ui.Context, t *ui.Theme, tc *trendCard) {
	card := ui.Column(c.Key("perf-card:"+tc.Key)).Grow(1).Width(0).MinWidth(0).Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Label(tc.Title)
	if tc.Summary != nil {
		card.Cursor(ui.CursorPointer)
		if card.Clicked() {
			p.detail = tc.Key
		}
	}
	card.Children(func() {
		p.cardHeader(c, t, tc)
		p.chart(c, t, tc, 120, perfCardMinWidth-70)
	})
}

func (p *perfView) cardHeader(c *ui.Context, t *ui.Theme, tc *trendCard) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, tc.Title).FontSize(12.5).FontWeight(500).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		if text, col := changeText(t, tc); text != "" {
			ui.Text(c, text).FontSize(11.5).TextColor(col).NoWrap()
		}
		if tc.HasCurrent {
			ui.Text(c, fmtMs(tc.Current)).FontSize(14).FontWeight(600)
		}
	})
	// A line that says what the pointer is over, or the card's standing.
	sel := p.selection(tc.Key)
	line := ""
	col := t.TextMuted
	switch {
	case *sel >= 0 && *sel < len(tc.Points):
		pt := tc.Points[*sel]
		line = pt.Label
		switch {
		case pt.Gap:
			line += " · No samples"
		case tc.Summary != nil:
			line += fmt.Sprintf(" · since %s · p50 %s · p95 %s · %d samples", pt.T.Local().Format("Jan 2"), fmtMs(pt.Mid), fmtMs(pt.Hi), pt.N)
			if pt.Thin {
				line += ", too few to judge"
			}
		default:
			line += fmt.Sprintf(" · %s · %s · %s to %s", pt.T.Local().Format("Jan 2"), fmtMs(pt.Mid), fmtMs(pt.Lo), fmtMs(pt.Hi))
		}
	case !tc.HasCurrent:
		line = "No data"
	case tc.Budget > 0 && tc.Current > tc.Budget:
		line = fmt.Sprintf("Over the %s budget · %d samples on %s", fmtMs(tc.Budget), tc.N, tc.Build)
		col = t.Danger
	case tc.Budget > 0:
		line = fmt.Sprintf("Within the %s budget · %d samples on %s", fmtMs(tc.Budget), tc.N, tc.Build)
	default:
		line = fmt.Sprintf("%d kept changes, now at %s", tc.N, tc.Build)
	}
	ui.Text(c, line).FontSize(11).TextColor(col).SingleLine()
}

// changeText says how the card's number compares with its earlier build. It stays
// quiet unless the difference can be told from noise, and a log cannot show that
// across repository sizes.
func changeText(t *ui.Theme, tc *trendCard) (string, ui.Color) {
	switch {
	case tc.Base == "":
		return "", t.TextMuted
	case tc.Verdict == perfagg.VerdictMixed:
		return "Pick a size to compare", t.TextMuted
	case tc.Verdict == perfagg.VerdictFlat:
		return "No clear change since " + tc.Base, t.TextMuted
	case !tc.HasChange:
		return "", t.TextMuted
	case tc.Change < 0:
		return fmtChange(tc.Change) + " since " + tc.Base, t.Success
	}
	return fmtChange(tc.Change) + " since " + tc.Base, t.Danger
}

// chart draws a card's trend over its builds. Builds with no samples leave gaps
// in the line; width is about how much room the bottom labels have.
func (p *perfView) chart(c *ui.Context, t *ui.Theme, tc *trendCard, height, width float32) {
	top := tc.Budget
	for _, pt := range tc.Points {
		if !pt.Gap && pt.Hi > top {
			top = pt.Hi
		}
	}
	if top <= 0 {
		top = 1
	}
	y := chart.NewLinear(0, top*1.1).Nice(3)
	ticks := y.Ticks(3)
	xs := make([]float32, len(tc.Points))
	for i := range tc.Points {
		xs[i] = slotX(i, len(tc.Points))
	}
	type run struct{ xs, mid, lo, hi []float32 }
	var runs []run
	var cur run
	for i, pt := range tc.Points {
		if pt.Gap {
			if len(cur.xs) > 0 {
				runs, cur = append(runs, cur), run{}
			}
			continue
		}
		cur.xs = append(cur.xs, xs[i])
		cur.mid = append(cur.mid, y.At(pt.Mid))
		cur.lo = append(cur.lo, y.At(pt.Lo))
		cur.hi = append(cur.hi, y.At(pt.Hi))
	}
	if len(cur.xs) > 0 {
		runs = append(runs, cur)
	}
	budget := float32(-1)
	if tc.Budget > 0 {
		budget = y.At(tc.Budget)
	}
	type dot struct {
		x, y float32
		thin bool
	}
	var dots []dot
	for i, pt := range tc.Points {
		if !pt.Gap {
			dots = append(dots, dot{xs[i], y.At(pt.Mid), pt.Thin})
		}
	}
	line, grid, warn, surface := t.Accent, t.Border, t.Warning, t.Surface
	label := func(_ chart.Side, tk chart.Tick) { ui.Text(c, tk.Label).FontSize(10).TextColor(t.TextMuted).NoWrap() }
	chart.Plot(c, chart.Axes{Left: ticks, Bottom: buildTicks(tc.Points, width), Label: label}, func(area ui.Element) {
		area.Draw(func(pn *ui.Painter, r ui.Rect) {
			for _, tk := range ticks {
				gy := chart.Y(r, tk.At)
				pn.Line(r.X, gy, r.X+r.W, gy, 1, grid)
			}
			if budget >= 0 {
				by := float32(math.Round(float64(chart.Y(r, budget))))
				for dx := r.X; dx < r.X+r.W; dx += 7 {
					pn.Fill(ui.Rect{X: dx, Y: by, W: min(4, r.X+r.W-dx), H: 1}, warn, 0)
				}
			}
			for _, rn := range runs {
				if len(rn.xs) == 1 {
					cx := chart.X(r, rn.xs[0])
					pn.Line(cx, chart.Y(r, rn.lo[0]), cx, chart.Y(r, rn.hi[0]), 2, line.Alpha(0.2))
					continue
				}
				pn.FillPath(chart.Area(r, rn.xs, rn.lo, rn.hi, chart.CurveLinear), line.Alpha(0.16))
				pn.StrokePath(chart.Line(r, rn.xs, rn.mid, chart.CurveLinear), 1.5, line)
			}
			// Each build is a thing of its own, so show it while there is room.
			if r.W >= float32(len(xs))*8 {
				for _, d := range dots {
					at := ui.Rect{X: chart.X(r, d.x) - 3, Y: chart.Y(r, d.y) - 3, W: 6, H: 6}
					if d.thin {
						pn.Fill(at, surface, 3)
						pn.Stroke(at, line, 3, 1.25)
					} else {
						pn.Fill(at, line, 3)
					}
				}
			}
		})
		sel := p.selection(tc.Key)
		if len(xs) > 0 {
			chart.Select(c, area, sel, xs, func(i int) string {
				return tc.Points[i].Label + " " + fmtMs(tc.Points[i].Mid)
			})
			if !area.Hovered() {
				*sel = -1
			}
			if i := *sel; i >= 0 && i < len(xs) {
				chart.RuleX(c, xs[i], 1).Background(t.TextMuted.Alpha(0.6))
				if !tc.Points[i].Gap {
					chart.Point(c, xs[i], y.At(tc.Points[i].Mid), 7, 7).Radius(3.5).Background(t.Accent).Border(1, t.Surface)
				}
			}
		}
	}).Height(height).Gap(4).FontSize(10).Label(tc.Title + " trend")
}

func (p *perfView) detailView(c *ui.Context, t *ui.Theme) {
	var tc *trendCard
	for i := range p.cards {
		if p.cards[i].Key == p.detail {
			tc = &p.cards[i]
		}
	}
	if tc == nil || tc.Summary == nil {
		p.detail = ""
		return
	}
	s := tc.Summary
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).Padding(16, 20).Gap(16).FillWidth().Children(func() {
			ui.Row(c).Children(func() {
				if ghostBtn(c, "perf-back", IconChevronLeft, "All operations", "Back to all operations", false) {
					p.detail = ""
				}
			})
			p.cardHeader(c, t, tc)
			w, _ := c.Size()
			p.chart(c, t, tc, 240, w-90)
			if s.N > 0 {
				ui.Text(c, "On "+s.Version).FontWeight(600)
			}
			ui.Row(c).Gap(24).Wrap().Children(func() {
				stat := func(name, value string) {
					ui.Column(c).Gap(1).Children(func() {
						ui.Text(c, name).FontSize(11).TextColor(t.TextMuted)
						ui.Text(c, value).FontSize(14).FontWeight(500)
					})
				}
				stat("p50", fmtMs(s.P50))
				stat("p95", fmtMs(s.P95))
				stat("p99", fmtMs(s.P99))
				stat("Slowest", fmtMs(s.Max))
				stat("Samples", fmt.Sprint(s.N))
				stat("Errors", fmt.Sprint(s.Errors))
				stat("Cancelled", fmt.Sprint(s.Cancelled))
				stat("Unseen", fmt.Sprint(s.Unseen))
			})
			if len(s.Phases) > 0 {
				ui.Column(c).Gap(6).FillWidth().Children(func() {
					ui.Text(c, "Phases").FontWeight(600)
					for _, ph := range s.Phases {
						ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
							ui.Text(c, ph.Phase).Width(130).Shrink(0).FontSize(12)
							ui.Box(c).Grow(1).Width(0).Height(6).Radius(3).Background(t.Text.Alpha(0.06)).Children(func() {
								ui.Box(c).WidthPercent(float32(ph.Share * 100)).FillHeight().Radius(3).Background(t.Accent)
							})
							ui.Text(c, fmt.Sprintf("%s · p95 %s · %.0f%%", fmtMs(ph.P50), fmtMs(ph.P95), ph.Share*100)).FontSize(12).TextColor(t.TextMuted).Width(190).Shrink(0)
						})
					}
				})
			}
			if len(s.Slowest) > 0 {
				ui.Column(c).Gap(4).FillWidth().Children(func() {
					ui.Text(c, "Slowest samples").FontWeight(600)
					for _, e := range s.Slowest {
						var parts []string
						for _, ph := range e.Phases {
							parts = append(parts, ph.Name+" "+fmtMs(ph.Ms))
						}
						ui.Row(c).Gap(12).Children(func() {
							ui.Text(c, fmtMs(e.Ms)).Width(70).Shrink(0).FontSize(12).FontWeight(500)
							ui.Text(c, e.T.In(time.Local).Format("Jan 2 15:04")).Width(90).Shrink(0).FontSize(12).TextColor(t.TextMuted)
							ui.Text(c, e.Size).Width(60).Shrink(0).FontSize(12).TextColor(t.TextMuted)
							ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(t.TextMuted).Shrink(1).MinWidth(0)
						})
					}
				})
			}
		})
	})
}

// beginExport shows what a report would contain before anything is saved.
func (p *perfView) beginExport() {
	if p.exportReport == nil {
		return
	}
	p.notice, p.preview, p.previewData, p.previewOpen = "", "Preparing…", nil, true
	build := p.exportReport
	p.run(func() func() {
		data, x, err := build()
		return func() {
			if err != nil {
				p.preview = "Could not read the performance data."
				return
			}
			p.previewData = data
			p.preview = exportPreview(data, x)
		}
	})
}

// exportPreview is the start of the very file, under a plain summary.
func exportPreview(data []byte, x *perfreport.Export) string {
	const maxPreview = 200
	lines := strings.Split(string(data), "\n")
	more := ""
	if len(lines) > maxPreview {
		more = fmt.Sprintf("\n… and %d more lines of the same form", len(lines)-maxPreview)
		lines = lines[:maxPreview]
	}
	return fmt.Sprintf("%d operations, %d kept in the file. Machine class: %s, %s. No paths, names, commit messages, code or Git errors.\n\n%s%s",
		x.Events, len(x.Entries), x.OS, x.CPU, strings.Join(lines, "\n"), more)
}

func (p *perfView) previewView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).Padding(16, 20).Gap(12).Children(func() {
		ui.Text(c, "Export Performance Report").FontSize(14).FontWeight(600)
		ui.Text(c, "This is what the file contains. It is saved where you choose and sent nowhere.").FontSize(12).TextColor(t.TextMuted)
		ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Radius(8).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, p.preview).Font("monospace").FontSize(11.5).Padding(10, 12).Selectable().FillWidth()
		})
		if p.notice != "" {
			ui.Text(c, p.notice).FontSize(12).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Spacer(c)
			if ghostBtn(c, "perf-export-cancel", nil, "Close", "Close without saving", false) {
				p.previewOpen = false
			}
			if p.previewData != nil && ghostBtn(c, "perf-export-save", IconDownload, "Save…", "Choose where to save the report", false) && p.saveExport != nil {
				data := p.previewData
				p.saveExport(exportFileName(p.now()), func(path string) {
					if path == "" {
						return
					}
					if err := writeFileAtomic(path, data); err != nil {
						p.notice = "Could not save the report: " + errorKind(err)
					} else {
						p.previewOpen = false
					}
					if p.win != nil {
						p.win.Invalidate()
					}
				})
			}
		})
	})
}

func errorKind(err error) string {
	switch {
	case os.IsPermission(err):
		return "permission denied"
	case os.IsNotExist(err):
		return "the folder no longer exists"
	}
	return "the file could not be written"
}
