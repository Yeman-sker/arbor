// Package perf records how long user-visible operations take.
//
// Events are built only from the closed enumerations in this file, so paths,
// repository, branch and file names, commit messages, code and raw Git errors
// have no way into a record. With no sink installed every call is a cheap no-op.
package perf

// Op is a user-visible operation. Add new ones at the end of the block and give
// them a name and a budget in opInfo; values are persisted by name, not number.
type Op uint8

const (
	OpNone Op = iota
	OpStartupFirstFrame
	OpStartupReady
	OpModeSwitch
	OpOpenFile
	OpOpenDiff
	OpExpandCommit
	OpSwitchBranch
	OpSearch
	OpStage
	OpCommit
	OpNewWindow
	OpExternalChange
	OpRefresh
	OpCommitCard
	OpBranchPicker
	opCount
)

// Budgets are the targets agreed for ARB-5. Optimization work must not change
// them or the benchmark fixtures; a change here is its own reviewed commit.
var opInfo = [opCount]struct {
	name     string
	budgetMs float64
}{
	OpNone:              {"none", 0},
	OpStartupFirstFrame: {"startup_first_frame", 300},
	OpStartupReady:      {"startup_ready", 800},
	OpModeSwitch:        {"mode_switch", 50},
	OpOpenFile:          {"open_file", 100},
	OpOpenDiff:          {"open_diff", 100},
	OpExpandCommit:      {"expand_commit", 100},
	OpSwitchBranch:      {"switch_branch", 500},
	OpSearch:            {"search", 50},
	OpStage:             {"stage", 300},
	OpCommit:            {"commit", 300},
	OpNewWindow:         {"new_window", 300},
	OpExternalChange:    {"external_change", 1000},
	OpRefresh:           {"refresh", 1000},
	OpCommitCard:        {"commit_card", 100},
	OpBranchPicker:      {"branch_picker", 50},
}

func (o Op) String() string {
	if o >= opCount {
		return "unknown"
	}
	return opInfo[o].name
}

// BudgetMs is the agreed target for the operation, 0 when it has none.
func (o Op) BudgetMs() float64 {
	if o >= opCount {
		return 0
	}
	return opInfo[o].budgetMs
}

// Ops lists every recordable operation in catalog order.
func Ops() []Op {
	ops := make([]Op, 0, opCount-1)
	for o := OpNone + 1; o < opCount; o++ {
		ops = append(ops, o)
	}
	return ops
}

// Phase is a named stage inside an operation.
type Phase uint8

const (
	PhaseNone Phase = iota
	PhaseGitCheckout
	PhaseGitStatus
	PhaseGitLog
	PhaseGitShow
	PhaseGitWrite
	PhaseLoadRepo
	PhaseLoadFile
	PhaseComputeGraph
	PhaseHighlight
	PhaseApply
	PhaseLayout
	PhasePaint
	phaseCount
)

var phaseNames = [phaseCount]string{
	PhaseNone:         "none",
	PhaseGitCheckout:  "git_checkout",
	PhaseGitStatus:    "git_status",
	PhaseGitLog:       "git_log",
	PhaseGitShow:      "git_show",
	PhaseGitWrite:     "git_write",
	PhaseLoadRepo:     "load_repo",
	PhaseLoadFile:     "load_file",
	PhaseComputeGraph: "compute_graph",
	PhaseHighlight:    "highlight",
	PhaseApply:        "apply",
	PhaseLayout:       "layout",
	PhasePaint:        "paint",
}

func (p Phase) String() string {
	if p >= phaseCount {
		return "unknown"
	}
	return phaseNames[p]
}

// Result is how an operation ended. A failure is never described further:
// Git error text can contain paths and branch names.
type Result uint8

const (
	ResultOK Result = iota
	ResultError
	ResultCancelled
	// ResultUnseen is an operation done while its window was not painting, such
	// as a refresh in the background: nobody waited for it, so its duration is
	// only the work and it is kept out of the percentiles.
	ResultUnseen
)

func (r Result) String() string {
	switch r {
	case ResultOK:
		return "ok"
	case ResultError:
		return "error"
	case ResultCancelled:
		return "cancelled"
	case ResultUnseen:
		return "unseen"
	}
	return "unknown"
}

// Size buckets a repository by tracked and untracked file count.
type Size uint8

const (
	SizeUnknown Size = iota
	SizeSmall
	SizeMedium
	SizeLarge
)

// SizeOf classifies a repository from its file count.
func SizeOf(files int) Size {
	switch {
	case files < 1000:
		return SizeSmall
	case files < 10000:
		return SizeMedium
	}
	return SizeLarge
}

func (s Size) String() string {
	switch s {
	case SizeSmall:
		return "small"
	case SizeMedium:
		return "medium"
	case SizeLarge:
		return "large"
	}
	return "unknown"
}
