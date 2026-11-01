package main

import (
	"arbor/internal/fixture"
	"arbor/internal/perfagg"
	"arbor/internal/perfreport"
	"arbor/internal/perfstore"
	"arbor/perf"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// climb is how an agent works on performance. Each command prints JSON with
// stable names and leaves the judging to the tool, so an agent acts on verdicts
// rather than on its reading of numbers: next says what to do and shows the
// evidence, try judges a commit, and log is the memory between sessions. The
// method is hill climbing: one change at a time, measured, with results an agent
// cannot fit its change to (docs/perf-climb.md).

// Actions of climb next.
const (
	ActionValidate = "validate_instrument"
	ActionMeasure  = "fix_measurement"
	ActionClassify = "classify_failures"
	ActionAddBench = "add_benchmark"
	ActionOptimize = "optimize"
	ActionCollect  = "collect_data"
	ActionStop     = "stop"
)

// Reasons of a round.
const (
	ReasonImproved     = "train_and_holdout_improved"
	ReasonRegression   = "regression"
	ReasonHoldoutWorse = "holdout_regressed"
	ReasonNoEffect     = "no_effect"
	ReasonNoisy        = "noise_above_min_effect"
	ReasonHoldoutNoisy = "holdout_too_noisy"
	ReasonOverfit      = "train_improved_holdout_flat"
	ReasonNoTarget     = "no_target_results"
	ReasonNoHoldout    = "holdout_unavailable"
)

// climbRules go out with every instruction to optimize.
var climbRules = []string{
	"One idea per commit, so a result can be attributed to it.",
	"Commit the change, then run climb try: an uncommitted tree is refused.",
	"Do what the verdict says: revert unless it is keep, and commit the ledger either way.",
	"Do not change budgets (perf/catalog.go) or fixtures (internal/fixture).",
	"Do not study or special-case the held-out fixture.",
	"A change to what is measured is its own commit, noted with climb log -measurement.",
}

// stallRounds is how many rounds of an operation without a keep count as a stall.
const stallRounds = 3

// checkMaxAge is how old a check may be before it is run again.
const checkMaxAge = 30 * 24 * time.Hour

// climbNow is the clock of the commands; tests replace it.
var climbNow = nowUTC

func climb(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "next":
		err = climbNext(args[1:], stdout, stderr)
	case "try":
		code, err = climbTry(args[1:], stdout, stderr)
	case "log":
		err = climbLog(args[1:], stdout, stderr)
	case "profile":
		err = climbProfile(args[1:], stdout, stderr)
	default:
		err = fmt.Errorf("unknown climb command %q", args[0])
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		}
		return 2
	}
	return code
}

// ---------------------------------------------------------------------------
// next

// Evidence is why climb next chose an operation. Source is field for logs of real
// use and e2e for the scenarios on the fixtures.
type Evidence struct {
	Source      string              `json:"source"`
	N           int                 `json:"n"`
	P50Ms       float64             `json:"p50Ms"`
	P95Ms       float64             `json:"p95Ms,omitempty"`
	BudgetMs    float64             `json:"budgetMs"`
	Size        string              `json:"size,omitempty"`
	AgeDays     int                 `json:"ageDays,omitempty"`
	Flags       []string            `json:"flags,omitempty"`
	Unexplained int                 `json:"unexplained,omitempty"`
	Unseen      int                 `json:"unseen,omitempty"`
	Phases      []perfagg.PhaseStat `json:"phases,omitempty"`
	BySize      []perfreport.Group  `json:"bySize,omitempty"`
	ByVersion   []perfreport.Group  `json:"byVersion,omitempty"`
	Slowest     []perfreport.Sample `json:"slowest,omitempty"`
}

// NextStep is what climb next prints.
type NextStep struct {
	Schema int    `json:"schema"`
	Action string `json:"action"`
	// Reason is a stable name for why this action, Detail says it for a reader.
	Reason   string             `json:"reason"`
	Detail   string             `json:"detail,omitempty"`
	Op       string             `json:"op,omitempty"`
	Evidence *Evidence          `json:"evidence,omitempty"`
	Target   *perfreport.Target `json:"target,omitempty"`
	Commands []string           `json:"commands,omitempty"`
	Problems []string           `json:"problems,omitempty"`
	// Unresolvable are the target's benchmarks whose noise is above the smallest
	// change worth acting on: try them with more -rounds.
	Unresolvable []string          `json:"unresolvable,omitempty"`
	PastAttempts []perfstore.Round `json:"pastAttempts,omitempty"`
	Rules        []string          `json:"rules,omitempty"`
}

// e2eMetric is what next reads of an e2e report.
type e2eMetric struct {
	Scenario  string    `json:"scenario"`
	Size      string    `json:"size"`
	Samples   []float64 `json:"samples"`
	Budget    float64   `json:"budget"`
	HasBudget bool      `json:"hasBudget"`
	Status    string    `json:"status"`
	Median    float64   `json:"median"`
}

// nextInput is all that climb next decides from.
type nextInput struct {
	Report     *perfreport.Report
	Rounds     []perfstore.Round
	Check      *CheckResult
	E2E        []e2eMetric
	E2EAgeDays int
	Now        time.Time
}

// decideNext is the policy of the climb: trust the measuring first, then the
// numbers, then act on the worst of them, and stop when there is nothing to do.
func decideNext(in nextInput) NextStep {
	s := NextStep{Schema: SchemaVersion}
	if reason, detail, problems := checkTrouble(in.Check, in.Now); reason != "" {
		s.Action, s.Reason, s.Detail, s.Problems = ActionValidate, reason, detail, problems
		s.Commands = []string{"go run ./cmd/arbor-perf check"}
		return s
	}
	// A measurement that cannot explain itself is first: everything after this
	// trusts the numbers. Few samples are enough to see one.
	for _, o := range in.Report.Ops {
		if o.Has(perfreport.FlagUnexplained) && o.Unexplained >= 5 {
			s.Action, s.Reason, s.Op = ActionMeasure, perfreport.FlagUnexplained, o.Op
			s.Detail = fmt.Sprintf("In %d of %d samples the phases miss much of the time, so p50 %.0f ms may not be what users wait for. Find what the rest of the time is, make the span measure the wait, and note it.", o.Unexplained, o.Count, o.P50)
			s.Evidence = fieldEvidence(o)
			t := o.Target
			s.Target = &t
			s.Commands = []string{fmt.Sprintf("go run ./cmd/arbor-perf climb log -op %s -measurement \"what was wrong and how it was fixed\"", o.Op)}
			s.Rules = climbRules
			return s
		}
	}
	for _, op := range candidateOps(in) {
		past := roundsOf(in.Rounds, op.name)
		if stalled(past) {
			s.Action, s.Reason, s.Op = ActionClassify, "stalled", op.name
			s.Detail = fmt.Sprintf("%d rounds without a keep. Change nothing yet: read what each one found and decide whether the cause is the code, the benchmark or noise.", stallRounds)
			s.PastAttempts = past
			s.Commands = []string{fmt.Sprintf("go run ./cmd/arbor-perf climb log -op %s -classified \"why the rounds failed and what follows\"", op.name)}
			return s
		}
		t := perfreport.Targets[op.name]
		s.Op, s.Evidence, s.PastAttempts, s.Rules = op.name, op.evidence, past, climbRules
		s.Target = &t
		s.Reason = "over_budget_in_" + op.evidence.Source
		if len(t.Benchmarks) == 0 {
			s.Action = ActionAddBench
			s.Detail = "No benchmark covers this operation. Add one in its own commit, then run check."
			return s
		}
		s.Action = ActionOptimize
		s.Detail = fmt.Sprintf("Find the cause on the benchmarks, change one thing, commit, then run try. Only a verdict of keep stays.")
		if t.Partial {
			s.Detail += " The benchmarks cover only part of this operation, so confirm a keep with e2e -only " + t.Scenario + "."
		}
		s.Commands = []string{
			"go run ./cmd/arbor-perf climb profile -op " + op.name,
			fmt.Sprintf("go run ./cmd/arbor-perf climb try -op %s -hypothesis \"what should get faster and why\"", op.name),
		}
		for _, name := range in.Check.Unresolvable {
			if matchesTarget(name, t) {
				s.Unresolvable = append(s.Unresolvable, name)
			}
		}
		return s
	}
	if !enoughEvidence(in) {
		s.Action, s.Reason = ActionCollect, "too_little_evidence"
		s.Detail = "Neither real use nor the scenarios cover enough operations to say all is well."
		s.Commands = []string{"go run ./e2e"}
		return s
	}
	s.Action, s.Reason = ActionStop, "all_within_budget"
	s.Detail = "Everything measured is within budget. Further work has no target: stop rather than optimize what nobody waits for."
	return s
}

// checkTrouble says what is wrong with the last check, if anything.
func checkTrouble(c *CheckResult, now time.Time) (reason, detail string, problems []string) {
	switch {
	case c == nil:
		return "no_check", "The measuring has not been checked on this machine.", nil
	case c.Fixture != fixture.Version:
		return "fixture_changed", "The fixtures changed since the last check.", nil
	case !c.OK:
		return "check_failed", "The last check found the measuring wrong: fix these or run it again on a quiet machine, with more -rounds if noise is why.", c.Problems
	}
	if t, err := time.Parse(time.RFC3339, c.T); err == nil && now.Sub(t) > checkMaxAge {
		return "check_stale", "The last check is over a month old.", nil
	}
	return "", "", nil
}

type candidate struct {
	name     string
	evidence *Evidence
}

// candidateOps are the operations over budget, worst first: real use by the
// waiting it costs, then the scenarios by how far over they are.
func candidateOps(in nextInput) []candidate {
	var out []candidate
	seen := map[string]bool{}
	for _, o := range in.Report.Ops {
		if o.Score > 0 && !o.Doubtful() {
			out = append(out, candidate{o.Op, fieldEvidence(o)})
			seen[o.Op] = true
		}
	}
	var scenario []e2eMetric
	for _, m := range in.E2E {
		if _, known := perfreport.Targets[m.Scenario]; known && !seen[m.Scenario] && m.HasBudget && m.Status == "FAIL" && m.Budget > 0 {
			scenario = append(scenario, m)
		}
	}
	sort.SliceStable(scenario, func(i, j int) bool {
		return scenario[i].Median/scenario[i].Budget > scenario[j].Median/scenario[j].Budget
	})
	for _, m := range scenario {
		if !seen[m.Scenario] {
			seen[m.Scenario] = true
			out = append(out, candidate{m.Scenario, &Evidence{Source: "e2e", N: len(m.Samples), P50Ms: m.Median, BudgetMs: m.Budget, Size: m.Size, AgeDays: in.E2EAgeDays}})
		}
	}
	return out
}

// enoughEvidence says whether a lack of candidates means anything.
func enoughEvidence(in nextInput) bool {
	covered := map[string]bool{}
	for _, o := range in.Report.Ops {
		if !o.Doubtful() {
			covered[o.Op] = true
		}
	}
	for _, m := range in.E2E {
		if _, known := perfreport.Targets[m.Scenario]; known {
			covered[m.Scenario] = true
		}
	}
	return len(covered)*2 >= len(perf.Ops())
}

func fieldEvidence(o perfreport.OpReport) *Evidence {
	return &Evidence{Source: "field", N: o.Count, P50Ms: o.P50, P95Ms: o.P95, BudgetMs: o.Budget, Flags: o.Flags,
		Unexplained: o.Unexplained, Unseen: o.Unseen, Phases: o.Phases, BySize: o.Sizes, ByVersion: o.Versions, Slowest: o.Slowest}
}

func roundsOf(rounds []perfstore.Round, op string) []perfstore.Round {
	var out []perfstore.Round
	for _, r := range rounds {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

// stalled says the latest rounds of an operation failed with no classifying since.
func stalled(rounds []perfstore.Round) bool {
	n := 0
	for i := len(rounds) - 1; i >= 0; i-- {
		r := rounds[i]
		if r.Kind == perfstore.RoundClassified || r.Verdict == perfstore.VerdictKeep {
			break
		}
		if r.Kind == perfstore.RoundTry {
			n++
		}
	}
	return n >= stallRounds
}

// matchesTarget says whether a benchmark measures the target, on a fixture any
// agent may look at or the held-out one.
func matchesTarget(name string, t perfreport.Target) bool {
	for _, f := range t.Benchmarks {
		if strings.HasPrefix(name, f+"/") || name == f {
			return true
		}
	}
	return false
}

func isHoldout(name string) bool { return strings.HasSuffix(name, "/"+fixture.Holdout.Name) }

func climbNext(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("climb next", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", "", "repository root")
	since := fs.String("since", "30d", "how far back real use counts")
	input := fs.String("input", "", "a log directory or an export (default: this computer's logs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return err
	}
	days, err := parseSince(*since)
	if err != nil {
		return err
	}
	rounds, err := perfstore.ReadRounds(filepath.Join(root, perfstore.LedgerPath))
	if err != nil {
		return err
	}
	now := climbNow()
	in := nextInput{Rounds: rounds, Now: now}
	in.Report, err = fieldReport(*input, days, now, rounds)
	if err != nil {
		return err
	}
	m := DetectMachine()
	in.Check = loadCheck(checkPath(root, m))
	in.E2E, in.E2EAgeDays = loadE2E(filepath.Join(root, ".perf", "e2e", m.Key+".json"), now)
	return writeJSON(stdout, decideNext(in))
}

// fieldReport reads the logs, leaving out what each operation recorded before its
// measurement was last fixed: those numbers are known to mean something else.
func fieldReport(input string, days int, now time.Time, rounds []perfstore.Round) (*perfreport.Report, error) {
	fixed := map[string]time.Time{}
	for _, r := range rounds {
		if r.Kind == perfstore.RoundMeasurement && r.T.After(fixed[r.Op]) {
			fixed[r.Op] = r.T
		}
	}
	path := input
	if path == "" {
		path = perfstore.LogDir(perfstore.UserDataDir())
	}
	model := perfagg.NewModel(time.UTC)
	bad, err := perfreport.Load(path, func(e perfstore.Entry) {
		if t, ok := fixed[e.Op]; !ok || e.T.After(t) {
			model.Add(e)
		}
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return perfreport.Build(model, days, now, bad), nil
}

func loadCheck(path string) *CheckResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c CheckResult
	if json.Unmarshal(data, &c) != nil {
		return nil
	}
	return &c
}

func loadE2E(path string, now time.Time) ([]e2eMetric, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	var file struct {
		Metrics []e2eMetric `json:"metrics"`
	}
	if json.Unmarshal(data, &file) != nil {
		return nil, 0
	}
	age := 0
	if st, err := os.Stat(path); err == nil {
		age = int(now.Sub(st.ModTime()) / (24 * time.Hour))
	}
	return file.Metrics, age
}

func saveJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ---------------------------------------------------------------------------
// try

// TryResult is what climb try prints. Holdout is one word on purpose: its numbers
// and profile stay out of reach, so a change cannot be fitted to it.
type TryResult struct {
	Schema     int    `json:"schema"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
	Op         string `json:"op,omitempty"`
	Hypothesis string `json:"hypothesis,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Base       string `json:"base,omitempty"`
	Machine    string `json:"machine,omitempty"`
	Rounds     int    `json:"rounds,omitempty"`
	Target     []Pair `json:"target,omitempty"`
	Holdout    string `json:"holdout,omitempty"`
	// GuardsChecked counts the other benchmarks that had to hold, and Regressions
	// names those that did not (of the fixtures an agent may look at).
	GuardsChecked int      `json:"guardsChecked,omitempty"`
	Regressions   []string `json:"regressions,omitempty"`
	// Next says what to do with this verdict.
	Next string `json:"next,omitempty"`
	// ProfileDiff is a CPU profile of the target, the new build over the old:
	// where the time moved. Negative is less.
	ProfileDiff     string   `json:"profileDiff,omitempty"`
	ProfileTop      []string `json:"profileTop,omitempty"`
	BaselineUpdated bool     `json:"baselineUpdated,omitempty"`
	// LoadAvg is the machine's one-minute load as the round began.
	LoadAvg float64 `json:"loadAvg,omitempty"`
}

// decision is the judgement of a round.
type decision struct{ verdict, reason, holdout string }

// regression says what got worse, by how much: a cause near the change is found
// from the name, and whether it matters from the size.
func regression(p Pair) string {
	if p.Verdict != PairSlower {
		return p.Name + " allocates more"
	}
	return fmt.Sprintf("%s %+.0f%%", p.Name, p.DeltaPct)
}

// decide applies the rules of hill climbing. A change stays only when what it
// aims at and the held-out fixture both improve, and nothing else got worse; an
// improvement the held-out fixture does not share was fitted to the benchmarks.
func decide(target, targetHoldout, guards, otherHoldout []Pair, o Options) (decision, []string) {
	var regressions []string
	for _, p := range target {
		if p.Verdict == PairSlower || p.AllocRegression {
			regressions = append(regressions, regression(p))
		}
	}
	for _, p := range guards {
		if (p.Verdict == PairSlower && p.DeltaPct >= o.Threshold*100) || p.AllocRegression {
			regressions = append(regressions, regression(p))
		}
	}
	d := decision{holdout: holdoutResult(targetHoldout)}
	if len(target) == 0 {
		d.verdict, d.reason = perfstore.VerdictInconclusive, ReasonNoTarget
		return d, regressions
	}
	if len(regressions) > 0 {
		d.verdict, d.reason = perfstore.VerdictRevert, ReasonRegression
		return d, regressions
	}
	if d.holdout == PairSlower || holdoutGuardFailed(otherHoldout, o) {
		d.verdict, d.reason = perfstore.VerdictRevert, ReasonHoldoutWorse
		return d, nil
	}
	improved, unsure := false, false
	for _, p := range target {
		improved = improved || p.Verdict == PairFaster
		unsure = unsure || p.Verdict == PairInconclusive
	}
	switch {
	case !improved && unsure:
		d.verdict, d.reason = perfstore.VerdictInconclusive, ReasonNoisy
	case !improved:
		d.verdict, d.reason = perfstore.VerdictRevert, ReasonNoEffect
	case len(targetHoldout) == 0:
		d.verdict, d.reason = perfstore.VerdictInconclusive, ReasonNoHoldout
	case d.holdout == PairFaster:
		d.verdict, d.reason = perfstore.VerdictKeep, ReasonImproved
	case d.holdout == PairInconclusive:
		d.verdict, d.reason = perfstore.VerdictInconclusive, ReasonHoldoutNoisy
	default:
		d.verdict, d.reason = perfstore.VerdictRevert, ReasonOverfit
	}
	return d, nil
}

// holdoutResult reduces the held-out benchmarks to one word.
func holdoutResult(ps []Pair) string {
	result := PairFlat
	for _, p := range ps {
		switch {
		case p.Verdict == PairSlower || p.AllocRegression:
			return PairSlower
		case p.Verdict == PairFaster:
			result = PairFaster
		case p.Verdict == PairInconclusive && result == PairFlat:
			result = PairInconclusive
		}
	}
	return result
}

func holdoutGuardFailed(ps []Pair, o Options) bool {
	for _, p := range ps {
		if (p.Verdict == PairSlower && p.DeltaPct >= o.Threshold*100) || p.AllocRegression {
			return true
		}
	}
	return false
}

func nextAfter(verdict, reason string, t perfreport.Target) string {
	switch verdict {
	case perfstore.VerdictKeep:
		s := "Keep the commit and commit the ledger."
		if t.Partial {
			s += " The benchmarks cover part of this operation: confirm with go run ./e2e -only " + t.Scenario + "."
		}
		return s
	case perfstore.VerdictRevert:
		return "Revert the commit, and commit the ledger so the next attempt knows."
	}
	if reason == ReasonNoisy || reason == ReasonHoldoutNoisy {
		return "Nothing can be said at this noise. Run it again with more -rounds on a quiet machine; if it is still unclear, revert."
	}
	return "Nothing can be said from this run. Fix what it reports and run it again."
}

func climbTry(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("climb try", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", "", "repository root")
	op := fs.String("op", "", "the operation the change is for")
	hypothesis := fs.String("hypothesis", "", "what should get faster and why")
	baseRev := fs.String("base", "HEAD~1", "the commit to compare HEAD with")
	rounds := fs.Int("rounds", PairRounds, "pairs of runs")
	benchtime := fs.String("benchtime", "", "passed to the benchmarks")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return 2, err
	}
	refuse := func(reason, why string) (int, error) {
		err := writeJSON(stdout, TryResult{Schema: SchemaVersion, Verdict: "refused", Reason: reason, Next: why})
		return 2, err
	}
	target, known := perfreport.Targets[*op]
	switch {
	case !known:
		return refuse("unknown_op", "-op must be an operation of perf/catalog.go.")
	case strings.TrimSpace(*hypothesis) == "":
		return refuse("no_hypothesis", "-hypothesis says what should get faster and why, for whoever reads the ledger.")
	case len(target.Benchmarks) == 0:
		return refuse("no_benchmark", "Add a benchmark for this operation in its own commit first.")
	case *rounds < minPairRounds:
		return refuse("too_few_rounds", fmt.Sprintf("-rounds must be at least %d.", minPairRounds))
	}
	m := DetectMachine()
	if reason, detail, problems := checkTrouble(loadCheck(checkPath(root, m)), climbNow()); reason != "" {
		return refuse(reason, strings.TrimSpace(detail+" "+strings.Join(problems, "; "))+" Run go run ./cmd/arbor-perf check.")
	}
	dirty, err := treeDirty(root)
	if err != nil {
		return 2, fmt.Errorf("git status: %w", err)
	}
	if dirty {
		return refuse("dirty_tree", "Commit the change first: a verdict has to belong to a commit.")
	}
	commit, err := gitOutput(root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return 2, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	base, err := gitOutput(root, "rev-parse", "--short", *baseRev)
	if err != nil {
		return refuse("no_base", "-base "+*baseRev+" is not a commit.")
	}
	if base == commit {
		return refuse("same_commit", "-base is HEAD: there is nothing to compare.")
	}

	tmp, err := os.MkdirTemp("", "arbor-try-")
	if err != nil {
		return 2, err
	}
	defer os.RemoveAll(tmp)
	tree := filepath.Join(tmp, "base")
	if _, err := gitOutput(root, "worktree", "add", "--detach", "--quiet", tree, base); err != nil {
		return 2, fmt.Errorf("git worktree add: %w", err)
	}
	defer gitOutput(root, "worktree", "remove", "--force", tree)
	baseExe, curExe := filepath.Join(tmp, "base.test"), filepath.Join(tmp, "cur.test")
	fmt.Fprintln(stderr, "arbor-perf: building both commits...")
	if err := buildBinary(tree, baseExe); err != nil {
		return 2, err
	}
	if err := buildBinary(root, curExe); err != nil {
		return 2, err
	}
	load := loadAverage()
	env := append(fixtureEnv(), "ARBOR_PERF_HOLDOUT=1")
	pairs, err := measurePairs(binary{baseExe, tree, env}, binary{curExe, root, env}, benchRun{pattern: ".", benchtime: *benchtime}, *rounds, stderr)
	if err != nil {
		return 2, err
	}

	var tgt, tgtHoldout, guards, otherHoldout []Pair
	for _, p := range pairs {
		switch aims := matchesTarget(p.Name, target); {
		case aims && isHoldout(p.Name):
			tgtHoldout = append(tgtHoldout, p)
		case aims:
			tgt = append(tgt, p)
		case isHoldout(p.Name):
			otherHoldout = append(otherHoldout, p)
		default:
			guards = append(guards, p)
		}
	}
	d, regressions := decide(tgt, tgtHoldout, guards, otherHoldout, DefaultOptions)
	res := TryResult{
		Schema: SchemaVersion, Verdict: d.verdict, Reason: d.reason, Op: *op, Hypothesis: *hypothesis,
		Commit: commit, Base: base, Machine: m.Key, Rounds: *rounds, Target: tgt, Holdout: d.holdout,
		GuardsChecked: len(guards), Regressions: regressions, Next: nextAfter(d.verdict, d.reason, target), LoadAvg: load,
	}
	if d.verdict == perfstore.VerdictKeep {
		res.BaselineUpdated = tightenTarget(root, m, binary{curExe, root, fixtureEnv()}, *benchtime, target, stderr)
	}
	// Where the time moved helps whatever the verdict, and costs no judgement.
	res.ProfileDiff, res.ProfileTop = profileDiff(root, commit, binary{baseExe, tree, fixtureEnv()}, binary{curExe, root, fixtureEnv()}, target, *benchtime, stderr)

	round := perfstore.Round{T: climbNow(), Kind: perfstore.RoundTry, Op: *op, Note: *hypothesis, Commit: commit, Base: base,
		Machine: m.Key, Verdict: d.verdict, Reason: d.reason, Holdout: d.holdout, Regressions: regressions}
	for _, p := range tgt {
		round.Target = append(round.Target, perfstore.RoundBench{Name: p.Name, BaseNs: p.BaseNs, CurNs: p.CurNs, DeltaPct: p.DeltaPct, NoisePct: p.NoisePct, Verdict: p.Verdict})
	}
	if err := appendRound(root, round); err != nil {
		return 2, err
	}
	if err := writeJSON(stdout, res); err != nil {
		return 2, err
	}
	if d.verdict != perfstore.VerdictKeep {
		return 1, nil
	}
	return 0, nil
}

// tightenTarget locks a kept improvement into the baseline, for the target only:
// the other benchmarks were not what this round proved. It measures them again
// the way the baseline was measured, as the fastest of a few samples would make
// a later compare see a regression that is not there.
func tightenTarget(root string, m Machine, cur binary, benchtime string, t perfreport.Target, stderr io.Writer) bool {
	path := baselinePath(root, m)
	base, err := LoadBaseline(path)
	if err != nil || base.FixtureVersion != fixture.Version {
		return false
	}
	out, err := runBinary(cur, benchRun{pattern: benchPattern(t.Benchmarks), benchtime: benchtime, samples: DefaultOptions.MinRuns}, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return false
	}
	runs := out.Benchmarks
	aimed := map[string]Series{}
	for name, s := range runs {
		if matchesTarget(name, t) && !isHoldout(name) {
			aimed[name] = s
		}
	}
	updated, changed := Tighten(base.Benchmarks, aimed, Compare(base.Benchmarks, aimed, true, DefaultOptions))
	if !changed {
		return false
	}
	if err := SaveBaseline(path, newBaseline(m, fixture.Version, updated, time.Now())); err != nil {
		fmt.Fprintf(stderr, "arbor-perf: %v\n", err)
		return false
	}
	return true
}

// appendRound adds a line to the ledger, and to the copy the window reads.
func appendRound(root string, r perfstore.Round) error {
	if err := perfstore.AppendRound(filepath.Join(root, perfstore.LedgerPath), r); err != nil {
		return err
	}
	if data := perfstore.UserDataDir(); data != "" {
		_ = perfstore.AppendRound(filepath.Join(perfstore.BenchDir(data), perfstore.RoundsFile), r)
	}
	return nil
}

// ---------------------------------------------------------------------------
// log

func climbLog(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("climb log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", "", "repository root")
	op := fs.String("op", "", "only this operation")
	last := fs.Int("n", 0, "only the last n lines")
	measurement := fs.String("measurement", "", "note that the measurement of -op was fixed: its older field data stops counting")
	classified := fs.String("classified", "", "note why the last rounds of -op failed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return err
	}
	if *measurement != "" || *classified != "" {
		if _, known := perfreport.Targets[*op]; !known {
			return fmt.Errorf("-op %q is not an operation of perf/catalog.go", *op)
		}
		r := perfstore.Round{T: climbNow(), Op: *op}
		if *measurement != "" {
			r.Kind, r.Note = perfstore.RoundMeasurement, *measurement
		} else {
			r.Kind, r.Note = perfstore.RoundClassified, *classified
		}
		if err := appendRound(root, r); err != nil {
			return err
		}
		return writeJSON(stdout, r)
	}
	rounds, err := perfstore.ReadRounds(filepath.Join(root, perfstore.LedgerPath))
	if err != nil {
		return err
	}
	if *op != "" {
		rounds = roundsOf(rounds, *op)
	}
	if *last > 0 && len(rounds) > *last {
		rounds = rounds[len(rounds)-*last:]
	}
	if rounds == nil {
		rounds = []perfstore.Round{}
	}
	return writeJSON(stdout, struct {
		Schema int               `json:"schema"`
		Rounds []perfstore.Round `json:"rounds"`
	}{SchemaVersion, rounds})
}

// ---------------------------------------------------------------------------
// profile

// ProfileResult is what climb profile prints.
type ProfileResult struct {
	Schema     int      `json:"schema"`
	Op         string   `json:"op"`
	Benchmarks []string `json:"benchmarks"`
	// CoveragePct is how much of the time the profile saw as Go on a CPU. A low
	// one means the time goes to git processes or waiting, which it cannot show:
	// read the phases, or time the git commands.
	CoveragePct float64  `json:"coveragePct"`
	Note        string   `json:"note,omitempty"`
	File        string   `json:"file"`
	Top         []string `json:"top"`
}

// lowCoverage is the CPU share of the wall time below which a profile misses most.
const lowCoverage = 50

// runPProf prints a profile as text; tests replace it.
var runPProf = func(args ...string) (string, error) {
	out, err := exec.Command("go", append([]string{"tool", "pprof"}, args...)...).CombinedOutput()
	return string(out), err
}

var pprofHeader = regexp.MustCompile(`Duration: \S+, Total samples = \S+ \(\s*([0-9.]+)%\)`)

// pprofCoverage reads the share of the wall time a profile covers.
func pprofCoverage(text string) float64 {
	m := pprofHeader.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

// benchPattern selects the families for go test -bench, which matches each level
// of a name apart: several families run their whole parents.
func benchPattern(families []string) string {
	if len(families) == 1 {
		if top, sub, ok := strings.Cut(families[0], "/"); ok {
			return "^Benchmark" + top + "$/^" + sub + "$"
		}
	}
	return "^Benchmark(" + strings.Join(topLevels(families), "|") + ")$"
}

func topLevels(names []string) []string {
	var out []string
	for _, n := range names {
		top, _, _ := strings.Cut(n, "/")
		if !slices.Contains(out, top) {
			out = append(out, top)
		}
	}
	return out
}

func topLines(text string, n int) []string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

func climbProfile(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("climb profile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", "", "repository root")
	op := fs.String("op", "", "the operation")
	benchtime := fs.String("benchtime", "", "passed to the benchmarks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return err
	}
	target, known := perfreport.Targets[*op]
	if !known {
		return fmt.Errorf("-op %q is not an operation of perf/catalog.go", *op)
	}
	dir := filepath.Join(root, ".perf", "climb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	exe := filepath.Join(dir, "profile.test")
	defer os.Remove(exe)
	if err := buildBinary(root, exe); err != nil {
		return err
	}
	prof := filepath.Join(dir, "profile-"+*op+".prof")
	if _, err := runBinary(binary{exe, root, fixtureEnv()}, benchRun{pattern: benchPattern(target.Benchmarks), benchtime: *benchtime, cpuProfile: prof}, stderr); err != nil {
		return err
	}
	text, err := runPProf("-top", "-nodecount=25", prof)
	if err != nil {
		return fmt.Errorf("go tool pprof: %v\n%s", err, text)
	}
	res := ProfileResult{Schema: SchemaVersion, Op: *op, Benchmarks: target.Benchmarks, CoveragePct: math.Round(pprofCoverage(text)*10) / 10, File: prof, Top: topLines(text, 32)}
	if res.CoveragePct < lowCoverage {
		res.Note = "Most of the time is outside Go on a CPU, in git processes or waiting. This profile cannot show it: read the phases, or time the git commands."
	}
	return writeJSON(stdout, res)
}

// profileDiff profiles the target on both builds and shows where the time moved.
// A failure here loses the detail, not the verdict.
func profileDiff(root, commit string, a, b binary, t perfreport.Target, benchtime string, stderr io.Writer) (string, []string) {
	dir := filepath.Join(root, ".perf", "climb", commit)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil
	}
	pa, pb := filepath.Join(dir, "base.prof"), filepath.Join(dir, "cur.prof")
	pattern := benchPattern(t.Benchmarks)
	for _, side := range []struct {
		bin  binary
		prof string
	}{{a, pa}, {b, pb}} {
		if _, err := runBinary(side.bin, benchRun{pattern: pattern, benchtime: benchtime, cpuProfile: side.prof}, stderr); err != nil {
			fmt.Fprintf(stderr, "arbor-perf: no profile: %v\n", err)
			return "", nil
		}
	}
	text, err := runPProf("-top", "-nodecount=20", "-diff_base="+pa, pb)
	if err != nil {
		fmt.Fprintf(stderr, "arbor-perf: no profile: %v\n", err)
		return "", nil
	}
	out := filepath.Join(dir, "diff.txt")
	if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
		return "", nil
	}
	return out, topLines(text, 15)
}
