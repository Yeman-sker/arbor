# The performance climb

How Arbor gets faster on purpose: real usage says what hurts, a benchmark proves a change helped, and a later release confirms it for real users. It is triggered by hand for now.

## Tools

| Command | What it does |
| --- | --- |
| `go run ./cmd/arbor-perf report --since 7d` | Ranked report of this computer's logs. `--input file` reads an export a user sent; `--format json` and `--out dir` are for agents. |
| `go run ./cmd/arbor-perf record` | Stores this machine's benchmark baseline in `.perf/` (outside Git). |
| `go run ./cmd/arbor-perf compare` | Runs the benchmarks, fails on a regression, tightens the baseline on an improvement. |
| `go run ./e2e [-only re] [-sizes small]` | Drives the real app through the scenarios and checks budgets. |

Real-usage data exists only where someone turned on View → Performance Diagnostics. View → Performance Window shows it as trends, and its Benchmarks source shows the runs of `record` and `compare`.

## Steps

1. **Read the report.** Take the top operation. The ranking is frequency × how far p95 exceeds the budget. Operations marked `(thin)` have too few samples to act on.
2. **Find the phase.** The report names the slowest phase, the code to start in and the benchmark or e2e scenario to use. If the phase breakdown can't explain the time, add a phase first, as its own commit.
3. **Reproduce it in a benchmark.** Run `compare` or the e2e scenario until the slowness shows on the pinned fixtures. If no benchmark covers it, add one in its own commit, then `record`.
4. **Make one change.** One idea per commit, so a result can be attributed to it.
5. **Prove it.** `arbor-perf compare` must show the improvement and no regression elsewhere. Quote its output in the commit message. Run on a quiet machine: the run-to-run noise is real, and `record` warns about it.
6. **Confirm in the field.** After a release, the chart for that operation should step down at the version line in the Performance window. If it doesn't, the benchmark doesn't represent the real cost, so fix the benchmark.

## Rules

- **Budgets are not yours to change.** They are in `perf/catalog.go`, and only the owner moves them.
- **Fixtures are not yours to change.** They are pinned by object id, and `TestPinned` fails if they change. Weakening one would fake a win.
- **Measurement changes are separate commits** from optimizations, so that a number never moves for two reasons at once.
- **Privacy stays closed.** Events are built from closed enumerations in `perf/catalog.go`. Never add a free-form field, and never add upload code: `TestNoPackageOfThePerfSystemImportsTheNetwork` and `TestPrivacySentinel` guard this.

## Known starting points

Examples from `docs/perf-baseline.md`, on the large fixture: `commit` was 410 ms against a 300 ms budget, and `external_change` was 0.8 to 1.7 s against 1 s.
