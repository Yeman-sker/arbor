package main

import (
	"arbor/internal/machine"
	"arbor/internal/perfagg"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"arbor/perf"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
)

// The Performance window shows the climb: one chart per operation, with date
// along x and duration up y from 0. Reading and aggregating runs off the UI
// thread and publishes a finished set of cards.

const (
	perfSourceUsage = iota
	perfSourceBench
)

// perfCardMinWidth decides how many cards fit in a row.
const perfCardMinWidth = 340

// trendPoint is one point of a chart: Mid is the line, Lo and Hi the band.
type trendPoint struct {
	T           time.Time
	Mid, Lo, Hi float64
	N           int
	Gap         bool
}

// trendCard is everything a card draws. It is built off the UI thread and only
// read afterwards.
type trendCard struct {
	Key, Title string
	Budget     float64
	Current    float64
	HasCurrent bool
	Change     float64
	HasChange  bool
	N          int
	From, To   time.Time
	Points     []trendPoint
	Marks      []perfagg.VersionMark
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
	days   int
	size   string
	detail string
	hover  map[string]*int
	list   ui.ListState

	// seq rejects results of jobs an newer one replaced.
	seq     uint64
	loading bool
	cards   []trendCard
	info    string

	mu     sync.Mutex // guards model and bench, which jobs share
	model  *perfagg.Model
	bench  []perfstore.BenchRow
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
	return &perfView{userData: userData, days: 30, now: time.Now, hover: map[string]*int{}}
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
	seq, source, days, size, now := p.seq, p.source, p.days, p.size, p.now()
	p.loading = true
	p.run(func() func() {
		cards, info := p.compute(source, days, size, now)
		return func() {
			if seq != p.seq {
				return
			}
			p.loading, p.cards, p.info = false, cards, info
		}
	})
}

func (p *perfView) compute(source, days int, size string, now time.Time) ([]trendCard, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		if p.flush != nil {
			p.flush()
		}
		p.model = perfagg.NewModel(time.Local)
		_, _ = perfstore.ReadDir(perfstore.LogDir(p.userData), p.model.Add)
		p.bench, _, _ = perfstore.ReadBench(perfstore.BenchDir(p.userData))
		p.loaded = true
	}
	if source == perfSourceBench {
		return benchCards(p.bench, machine.Detect().Key, days, size, now)
	}
	return usageCards(p.model, days, size, now), fmt.Sprintf("%d operations recorded", p.model.Events())
}

// usageCards has one card per catalog operation.
func usageCards(m *perfagg.Model, days int, size string, now time.Time) []trendCard {
	var cards []trendCard
	for _, op := range perf.Ops() {
		s := m.Summarize(op.String(), perfagg.Filter{Size: size, Days: days, End: now})
		c := trendCard{
			Key: s.Op, Title: s.Op, Budget: op.BudgetMs(), N: s.N, From: s.From, To: s.To, Marks: s.Marks,
			Current: s.Current, HasCurrent: s.N > 0, Change: s.Change, HasChange: s.HasChange, Summary: &s,
		}
		for _, d := range s.Days {
			c.Points = append(c.Points, trendPoint{T: d.Day.Add(12 * time.Hour), Mid: d.P50, Lo: d.P50, Hi: d.P95, N: d.N, Gap: d.Gap})
		}
		cards = append(cards, c)
	}
	// What has data comes first; the rest keep the catalog's order.
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].N > 0 && cards[j].N == 0 })
	return cards
}

// benchCards has one card per benchmark of this machine class.
func benchCards(rows []perfstore.BenchRow, key string, days int, size string, now time.Time) ([]trendCard, string) {
	from := now.AddDate(0, 0, -days)
	byName := map[string][]perfstore.BenchRow{}
	for _, r := range rows {
		if r.Machine != key || r.T.Before(from) || (size != "" && !strings.HasSuffix(r.Name, "/"+size)) {
			continue
		}
		byName[r.Name] = append(byName[r.Name], r)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var cards []trendCard
	for _, n := range names {
		rs := byName[n]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].T.Before(rs[j].T) })
		c := trendCard{Key: n, Title: n, N: len(rs), From: from, To: now, HasCurrent: true}
		last := rs[len(rs)-1]
		c.Current = last.Ns / 1e6
		if first := rs[0]; len(rs) > 1 && first.Ns > 0 {
			c.Change, c.HasChange = (last.Ns-first.Ns)/first.Ns, true
		}
		version := rs[0].Version
		for _, r := range rs {
			lo, hi := r.MinNs, r.MaxNs
			if lo == 0 || hi == 0 {
				lo, hi = r.Ns, r.Ns
			}
			c.Points = append(c.Points, trendPoint{T: r.T, Mid: r.Ns / 1e6, Lo: lo / 1e6, Hi: hi / 1e6, N: r.Runs})
			if r.Version != version && r.Version != "" {
				c.Marks = append(c.Marks, perfagg.VersionMark{Version: r.Version, At: r.T})
				version = r.Version
			}
		}
		cards = append(cards, c)
	}
	return cards, fmt.Sprintf("%d benchmarks on %s", len(cards), key)
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
		perfChips(c, t, "perf-source", []string{"Real usage", "Benchmarks"}, p.source, func(i int) {
			p.source, p.detail = i, ""
			p.rebuild()
		})
		ranges := []int{7, 30, 90}
		on := 1
		for i, d := range ranges {
			if d == p.days {
				on = i
			}
		}
		perfChips(c, t, "perf-range", []string{"7d", "30d", "90d"}, on, func(i int) {
			p.days = ranges[i]
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
	if len(p.cards) == 0 {
		ui.Column(c).Grow(1).FillWidth().Center().Gap(6).Children(func() {
			switch {
			case p.loading:
				ui.Text(c, "Loading…").TextColor(t.TextMuted)
			case p.source == perfSourceBench:
				ui.Text(c, "No benchmark history in this range").FontWeight(500)
				ui.Text(c, "Run: go run ./cmd/arbor-perf record").FontSize(12).TextColor(t.TextMuted).Font("monospace").Selectable()
			default:
				ui.Text(c, "No performance data yet").FontWeight(500)
			}
		})
		return
	}
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).Padding(16, 20).Gap(12).FillWidth().Children(func() {
			if p.source == perfSourceUsage && p.total() == 0 && !p.loading {
				ui.Text(c, "No operations recorded in this range yet. Use Arbor for a while and they will appear here.").TextColor(t.TextMuted)
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
		})
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
		p.chart(c, t, tc, 120)
	})
}

func (p *perfView) cardHeader(c *ui.Context, t *ui.Theme, tc *trendCard) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, tc.Title).FontSize(12.5).FontWeight(500).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		if tc.HasChange {
			col := t.TextMuted
			if tc.Change <= -0.02 {
				col = t.Success
			} else if tc.Change >= 0.02 {
				col = t.Danger
			}
			ui.Text(c, fmtChange(tc.Change)).FontSize(11.5).TextColor(col)
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
		line = pt.T.Format("Jan 2")
		if pt.Gap {
			line += fmt.Sprintf(" · %d samples, too few to draw", pt.N)
		} else if tc.Summary != nil {
			line += fmt.Sprintf(" · p50 %s · p95 %s · %d samples", fmtMs(pt.Mid), fmtMs(pt.Hi), pt.N)
		} else {
			line += fmt.Sprintf(" · %s · %s to %s", fmtMs(pt.Mid), fmtMs(pt.Lo), fmtMs(pt.Hi))
		}
	case tc.N == 0:
		line = "No data"
	case tc.Budget > 0 && tc.Current > tc.Budget:
		line = fmt.Sprintf("Over the %s budget · %d samples", fmtMs(tc.Budget), tc.N)
		col = t.Danger
	case tc.Budget > 0:
		line = fmt.Sprintf("Within the %s budget · %d samples", fmtMs(tc.Budget), tc.N)
	default:
		line = fmt.Sprintf("%d runs", tc.N)
	}
	ui.Text(c, line).FontSize(11).TextColor(col).SingleLine()
}

// chart draws a card's trend. Days with too few samples leave gaps in the line.
func (p *perfView) chart(c *ui.Context, t *ui.Theme, tc *trendCard, height float32) {
	from, to := tc.From, tc.To
	if !to.After(from) {
		to = from.Add(time.Hour)
	}
	x := chart.NewTime(from, to)
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
	for i, pt := range tc.Points {
		xs[i] = x.At(pt.T)
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
	type mark struct {
		x     float32
		label string
	}
	var marks []mark
	for _, m := range tc.Marks {
		marks = append(marks, mark{x.At(m.At), m.Version})
	}
	line, grid, muted, warn := t.Accent, t.Border, t.TextMuted, t.Warning
	label := func(_ chart.Side, tk chart.Tick) { ui.Text(c, tk.Label).FontSize(10).TextColor(t.TextMuted).NoWrap() }
	chart.Plot(c, chart.Axes{Left: ticks, Bottom: x.Ticks(3), Label: label}, func(area ui.Element) {
		area.Draw(func(pn *ui.Painter, r ui.Rect) {
			for _, tk := range ticks {
				gy := chart.Y(r, tk.At)
				pn.Line(r.X, gy, r.X+r.W, gy, 1, grid)
			}
			for _, m := range marks {
				mx := chart.X(r, m.x)
				pn.Line(mx, r.Y, mx, r.Y+r.H, 1, muted.Alpha(0.5))
				pn.Text(mx+3, r.Y+1, m.label, 10, muted)
			}
			if budget >= 0 {
				by := float32(math.Round(float64(chart.Y(r, budget))))
				for dx := r.X; dx < r.X+r.W; dx += 7 {
					pn.Fill(ui.Rect{X: dx, Y: by, W: min(4, r.X+r.W-dx), H: 1}, warn, 0)
				}
			}
			for _, rn := range runs {
				if len(rn.xs) == 1 {
					cx, cy := chart.X(r, rn.xs[0]), chart.Y(r, rn.mid[0])
					pn.Line(cx, chart.Y(r, rn.lo[0]), cx, chart.Y(r, rn.hi[0]), 2, line.Alpha(0.2))
					pn.Fill(ui.Rect{X: cx - 2, Y: cy - 2, W: 4, H: 4}, line, 2)
					continue
				}
				pn.FillPath(chart.Area(r, rn.xs, rn.lo, rn.hi, chart.CurveLinear), line.Alpha(0.16))
				pn.StrokePath(chart.Line(r, rn.xs, rn.mid, chart.CurveLinear), 1.5, line)
			}
		})
		sel := p.selection(tc.Key)
		if len(xs) > 0 {
			chart.Select(c, area, sel, xs, func(i int) string {
				return tc.Points[i].T.Format("Jan 2") + " " + fmtMs(tc.Points[i].Mid)
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
			p.chart(c, t, tc, 240)
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
							ui.Text(c, e.Size+" · v"+e.Version).Width(110).Shrink(0).FontSize(12).TextColor(t.TextMuted)
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
