package perfreport

import (
	"arbor/internal/perfagg"
	"arbor/perf"
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Target says where an agent starts on an operation.
type Target struct {
	// Code is where the operation lives.
	Code string `json:"code"`
	// Benchmarks are the benchmarks that measure it, as families: a benchmark
	// belongs to "OpenFile" or "Frame/search" by name up to a slash.
	Benchmarks []string `json:"benchmarks"`
	// Partial says the benchmarks cover only part of the operation, so a win on
	// them may not show for users: confirm with Scenario.
	Partial bool `json:"partial,omitempty"`
	// Scenario is the e2e scenario that drives the real app through it.
	Scenario string `json:"scenario,omitempty"`
}

// Reproduce says how to see the operation's cost.
func (t Target) Reproduce() string {
	var parts []string
	for _, b := range t.Benchmarks {
		parts = append(parts, "Benchmark"+b)
	}
	if t.Scenario != "" {
		parts = append(parts, "e2e "+t.Scenario)
	}
	return strings.Join(parts, ", ")
}

// Targets maps every catalog operation to its instrumentation and benchmarks.
// A test fails when an operation has none.
var Targets = map[string]Target{
	"startup_first_frame": {"main.go main, windows.go open, instrument.go", []string{"NewApp"}, true, "startup"},
	"startup_ready":       {"main.go applyRepoSnapshot, git.go LoadRepo", []string{"LoadRepo", "ApplyRepoSnapshot"}, true, "startup"},
	"mode_switch":         {"instrument.go switchMode, main.go view", []string{"Frame/files", "Frame/git"}, false, "mode_switch"},
	"open_file":           {"content.go, instrument.go", []string{"OpenFile"}, false, "open_file"},
	"open_diff":           {"content.go, instrument.go", []string{"OpenDiff"}, false, "open_diff"},
	"expand_commit":       {"content.go, git.go LoadCommitDetails", []string{"ExpandCommit", "LoadCommitDetails"}, false, "expand_commit"},
	"switch_branch":       {"live.go checkoutBranch, gitqueue.go", []string{"LoadRepo"}, true, "switch_branch"},
	"search":              {"instrument.go, main.go explorer filter", []string{"Frame/search"}, false, "search"},
	"stage":               {"live.go runGitOp, git.go", []string{"GitStatus", "LoadRepo"}, true, "stage_commit"},
	"commit":              {"live.go runGitOp, git.go LoadRepo", []string{"LoadRepo"}, true, "stage_commit"},
	"new_window":          {"windows.go open", []string{"NewApp"}, true, "new_window"},
	"external_change":     {"live.go, watch.go", []string{"GitStatus", "LoadRepo"}, true, "external_change"},
	"refresh":             {"live.go, gitload.go", []string{"LoadRepo"}, true, ""},
	"commit_card":         {"commitcard.go", []string{"Frame/git"}, true, ""},
	"branch_picker":       {"picker.go", []string{"Frame/git"}, true, ""},
}

// MinSamples is the fewest samples of an operation worth ranking.
const MinSamples = perfagg.MinSamples

// SchemaVersion is the format of the JSON report. Agents read it, so a change of
// meaning or a removed field bumps it.
const SchemaVersion = 2

// Flags say why a number should not be taken at face value. They are stable
// names an agent can act on.
const (
	// FlagThin: too few samples to rank on.
	FlagThin = "thin"
	// FlagUnexplained: the phases miss much of the duration in over a fifth of
	// the samples, so the number may not be what it says: fix the measurement.
	FlagUnexplained = "phases_unexplained"
	// FlagMixedSizes: the samples are of several repository sizes, so read bySize.
	FlagMixedSizes = "mixed_sizes"
)

// Report is the ranked state of every operation.
type Report struct {
	Schema  int        `json:"schema"`
	From    time.Time  `json:"from"`
	To      time.Time  `json:"to"`
	Events  int        `json:"events"`
	Bad     int        `json:"badLines"`
	Ops     []OpReport `json:"operations"`
	NoData  []string   `json:"noData"`
	ThinOps []string   `json:"thinData"`
}

// OpReport is what a report says about one operation.
type OpReport struct {
	Op     string `json:"op"`
	Count  int    `json:"count"`
	Errors int    `json:"errors"`
	Cancel int    `json:"cancelled"`
	// Unseen finished while the window was not painting: nobody waited.
	Unseen int `json:"unseen"`
	// Unexplained samples have phases that miss much of their duration.
	Unexplained int `json:"unexplained"`
	// Flags are the reasons to doubt the numbers; none means they can be used.
	Flags  []string `json:"flags,omitempty"`
	P50    float64  `json:"p50Ms"`
	P95    float64  `json:"p95Ms"`
	P99    float64  `json:"p99Ms"`
	Budget float64  `json:"budgetMs"`
	// Score is frequency times how far p95 exceeds the budget, in ms: the
	// waiting that being over budget costs in this period.
	Score float64 `json:"score"`
	// Trend is the change of p50 from version TrendFrom to the next with enough
	// samples, TrendTo, as a fraction, whatever the report's period. TrendVerdict
	// says whether that is a change at all (see perfagg.Judge).
	Trend        *float64            `json:"trend,omitempty"`
	TrendVerdict string              `json:"trendVerdict,omitempty"`
	TrendFrom    string              `json:"trendFrom,omitempty"`
	TrendTo      string              `json:"trendTo,omitempty"`
	Dominant     string              `json:"dominantPhase,omitempty"`
	Phases       []perfagg.PhaseStat `json:"phases,omitempty"`
	Versions     []Group             `json:"byVersion,omitempty"`
	Sizes        []Group             `json:"bySize,omitempty"`
	Slowest      []Sample            `json:"slowest,omitempty"`
	Target       Target              `json:"target"`
	Thin         bool                `json:"thin,omitempty"`
}

// Group is the operation within one version or size.
type Group struct {
	Key string  `json:"key"`
	N   int     `json:"n"`
	P50 float64 `json:"p50Ms"`
	P95 float64 `json:"p95Ms"`
}

// Sample is one slow operation.
type Sample struct {
	T      time.Time          `json:"t"`
	Ms     float64            `json:"ms"`
	Size   string             `json:"size"`
	Result string             `json:"result"`
	Phases map[string]float64 `json:"phases,omitempty"`
}

// Build makes the report of the days days up to now from a filled model.
func Build(m *perfagg.Model, days int, now time.Time, bad int) *Report {
	r := m.Last(days, now)
	rep := &Report{Schema: SchemaVersion, From: m.DayStart(r.From), To: m.DayStart(r.To + 1), Bad: bad}
	for _, op := range perf.Ops() {
		name := op.String()
		q := perfagg.Query{Op: name, R: r}
		st := m.Stat(q, 5)
		if st.Total() == 0 {
			rep.NoData = append(rep.NoData, name)
			continue
		}
		rep.Events += st.Total()
		o := OpReport{
			Op: name, Count: st.N, Errors: st.Errors, Cancel: st.Cancelled, Unseen: st.Unseen, Unexplained: st.Unexplained,
			P50: st.P50, P95: st.P95, P99: st.P99, Budget: op.BudgetMs(),
			Phases: st.Phases, Target: Targets[name], Thin: st.N < MinSamples,
		}
		if over := o.P95 - o.Budget; over > 0 && st.N > 0 {
			o.Score = float64(st.N) * over
		}
		o.Trend, o.TrendVerdict, o.TrendFrom, o.TrendTo = versionTrend(m, name)
		if len(st.Phases) > 0 {
			o.Dominant = st.Phases[0].Phase
		}
		for _, g := range m.ByVersion(q) {
			o.Versions = append(o.Versions, Group{g.Key, g.N, g.P50, g.P95})
		}
		for _, g := range m.BySize(q) {
			o.Sizes = append(o.Sizes, Group{g.Key, g.N, g.P50, g.P95})
		}
		for _, e := range st.Slowest {
			s := Sample{T: e.T, Ms: e.Ms, Size: e.Size, Result: e.Result}
			if len(e.Phases) > 0 {
				s.Phases = make(map[string]float64, len(e.Phases))
				for _, p := range e.Phases {
					s.Phases[p.Name] = p.Ms
				}
			}
			o.Slowest = append(o.Slowest, s)
		}
		if o.Thin {
			rep.ThinOps = append(rep.ThinOps, name)
			o.Flags = append(o.Flags, FlagThin)
		}
		if st.N > 0 && float64(st.Unexplained) > 0.2*float64(st.N) {
			o.Flags = append(o.Flags, FlagUnexplained)
		}
		if len(o.Sizes) > 1 {
			o.Flags = append(o.Flags, FlagMixedSizes)
		}
		rep.Ops = append(rep.Ops, o)
	}
	sort.SliceStable(rep.Ops, func(i, j int) bool {
		a, b := rep.Ops[i], rep.Ops[j]
		if a.Doubtful() != b.Doubtful() {
			return !a.Doubtful()
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Op < b.Op
	})
	return rep
}

// Has reports whether the operation carries flag.
func (o OpReport) Has(flag string) bool { return slices.Contains(o.Flags, flag) }

// Doubtful reports whether the operation's numbers cannot be ranked on: too few,
// or not measuring what they say. Such an operation comes after the others.
func (o OpReport) Doubtful() bool { return o.Thin || o.Has(FlagUnexplained) }

// versionTrend compares the last two versions with enough samples.
func versionTrend(m *perfagg.Model, op string) (*float64, string, string, string) {
	var prev, cur perfagg.VersionPoint
	for _, v := range m.Summarize(op, perfagg.Filter{}).Points {
		if !v.Thin {
			prev, cur = cur, v
		}
	}
	if prev.Version == "" || prev.P50 <= 0 {
		return nil, "", "", ""
	}
	t := (cur.P50 - prev.P50) / prev.P50
	verdict, _ := m.CompareVersions(op, "", prev.Version, cur.Version)
	return &t, verdict, prev.Version, cur.Version
}

// JSON is the report for agents to parse.
func (r *Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

func ms(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("%.2f s", v/1000)
	case v >= 10:
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}

// Markdown is the report for reading. The first section is the work list.
func (r *Report) Markdown() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Arbor performance report\n\n%s to %s · %d operations", r.From.Format("2006-01-02"), r.To.AddDate(0, 0, -1).Format("2006-01-02"), r.Events)
	if r.Bad > 0 {
		fmt.Fprintf(&b, " · %d unreadable lines skipped", r.Bad)
	}
	b.WriteString("\n\n")
	if r.Events == 0 {
		b.WriteString("No data in this period. Diagnostics may be off, or the period too short.\n")
		return b.String()
	}
	b.WriteString("## Ranking\n\nRanked by frequency × how far p95 exceeds its budget. Operations with fewer than ")
	fmt.Fprintf(&b, "%d samples, or whose phases do not explain their time, come last: their numbers are not evidence yet.\n\n", MinSamples)
	b.WriteString("| # | Operation | Count | p50 | p95 | Budget | Over by | Since last version | Slowest phase |\n| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for i, o := range r.Ops {
		over, trend, phase, name := "-", "-", "-", o.Op
		if o.P95 > o.Budget {
			over = ms(o.P95 - o.Budget)
		}
		if o.Trend != nil {
			trend = fmt.Sprintf("%s, %+.0f%% (%s to %s)", o.TrendVerdict, *o.Trend*100, o.TrendFrom, o.TrendTo)
		}
		if o.Dominant != "" {
			phase = o.Dominant
		}
		if len(o.Flags) > 0 {
			name += " (" + strings.Join(o.Flags, ", ") + ")"
		}
		fmt.Fprintf(&b, "| %d | %s | %d | %s | %s | %s | %s | %s | %s |\n", i+1, name, o.Count, ms(o.P50), ms(o.P95), ms(o.Budget), over, trend, phase)
	}
	if len(r.NoData) > 0 {
		fmt.Fprintf(&b, "\nNo data: %s.\n", strings.Join(r.NoData, ", "))
	}
	for _, o := range r.Ops {
		if o.Score == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\nStart in `%s`. Reproduce with `%s`.\n", o.Op, o.Target.Code, o.Target.Reproduce())
		if o.Errors+o.Cancel > 0 {
			fmt.Fprintf(&b, "\n%d errors and %d cancelled are not in the durations.\n", o.Errors, o.Cancel)
		}
		if o.Has(FlagUnexplained) {
			fmt.Fprintf(&b, "\nThe phases miss much of the time in %d of %d samples: fix the measurement before optimizing this.\n", o.Unexplained, o.Count)
		}
		if len(o.Phases) > 0 {
			b.WriteString("\n| Phase | p50 | p95 | Share |\n| --- | ---: | ---: | ---: |\n")
			for _, p := range o.Phases {
				fmt.Fprintf(&b, "| %s | %s | %s | %.0f%% |\n", p.Phase, ms(p.P50), ms(p.P95), p.Share*100)
			}
		}
		groups := func(title string, gs []Group) {
			if len(gs) < 2 {
				return
			}
			fmt.Fprintf(&b, "\n| %s | Count | p50 | p95 |\n| --- | ---: | ---: | ---: |\n", title)
			for _, g := range gs {
				fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", g.Key, g.N, ms(g.P50), ms(g.P95))
			}
		}
		groups("Size", o.Sizes)
		groups("Version", o.Versions)
		if len(o.Slowest) > 0 {
			b.WriteString("\nSlowest samples:\n\n")
			for _, s := range o.Slowest {
				fmt.Fprintf(&b, "- %s, %s, %s", ms(s.Ms), s.Size, s.T.UTC().Format("2006-01-02 15:04"))
				keys := make([]string, 0, len(s.Phases))
				for k := range s.Phases {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&b, ", %s %s", k, ms(s.Phases[k]))
				}
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}
