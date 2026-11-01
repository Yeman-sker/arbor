# The performance climb

How Arbor gets faster on purpose. An agent does the work and `arbor-perf climb` does the judging: it says what to work on, decides whether each change stays, and remembers every attempt. A person looks over it in View → Performance Window. The climb is started by hand for now.

The method is hill climbing as Anthropic describes it in [Automating eval design and hillclimbing with Claude](https://claude.dev/blog/automating-eval-design-and-hillclimbing/): real tasks, a reliable measure, a baseline, one change, keep or revert. The translation to performance work is ours. In particular, the post's held-out set is a fixture here.

## The loop

Run `climb next`, do what it says, and run it again. Every command prints JSON with stable names, and the numbers come with what is known about them.

| `action` | What to do |
| --- | --- |
| `validate_instrument` | Run `arbor-perf check`. Nothing is believed until the measuring is proven on this machine. |
| `fix_measurement` | The phases do not explain the time, so the number may not be what it says. Find out what it measures, fix the span in its own commit, then note it with `climb log -op O -measurement "..."`. Older field data of that operation stops counting. |
| `classify_failures` | Three rounds without a keep. Change nothing: read `pastAttempts`, decide whether the cause is the code, the benchmark or noise, and note it with `climb log -op O -classified "..."`. |
| `add_benchmark` | No benchmark covers the operation. Add one in its own commit, then run `check`. |
| `optimize` | Work on `op`. See below. |
| `collect_data` | There is too little evidence to say anything. Run `go run ./e2e`, or use the app with diagnostics on. |
| `stop` | Everything measured is within budget. Stop. |

To optimize:

1. **Read what `next` gives you.** `evidence` is the numbers and phases, `target` the code and benchmarks, `pastAttempts` what was already tried, and `unresolvable` the benchmarks too noisy to show a small change.
2. **Find the cause.** `climb profile -op O` shows where the benchmarks spend CPU. A low `coveragePct` means the time is in git processes or waiting, which a Go profile cannot show: read the phases, or time the git commands.
3. **Make one change and commit it.** One idea per commit, so a result can be attributed to it.
4. **Run `climb try -op O -hypothesis "..."`.** It builds this commit and its parent, runs them in turns on every benchmark, and prints a verdict. It adds the round to `perf/ledger.jsonl` whatever the verdict, and takes about 10 minutes.
5. **Do what the verdict says, and commit the ledger.** `next` in the result says how. When `target.partial` is set the benchmarks cover only part of the operation, so confirm a keep with `go run ./e2e -only <scenario>`.
6. **Confirm in the field.** After a release, `report` and the Real usage charts should show the operation faster on the new version, within one repository size. If they don't, the benchmark doesn't represent the real cost, so improve the benchmark rather than declaring victory.

## How a round is judged

A laptop doing other work runs the same code up to twice as slowly for tens of seconds at a time. So the two builds take turns on each benchmark, each pair of runs is under a second apart, and the judgement is on the median of the ratios within pairs. `noisePct` is half the width of that median's 95% confidence interval.

A benchmark is `faster` or `slower` when the interval excludes no change and the change reaches 5%, the smallest worth acting on. With this many benchmarks, some look changed by chance, so anything that looks changed is measured again in fresh rounds and judged on those alone. If the two disagree it is `inconclusive`. It is also `inconclusive` when its noise is above 5% and nothing moved, and `flat` otherwise.

| Verdict | Reason | When |
| --- | --- | --- |
| keep | `train_and_holdout_improved` | The target and the held-out fixture got faster, and nothing else got worse. |
| revert | `regression` | The target got slower, another benchmark got more than 10% slower, or something allocates more. |
| revert | `holdout_regressed` | The held-out fixture got worse. |
| revert | `train_improved_holdout_flat` | Only the fixtures you can see improved: the change fits them rather than the job. |
| revert | `no_effect` | The target did not move. |
| inconclusive | `noise_above_min_effect`, `holdout_too_noisy` | Run again with more `-rounds` on a quiet machine. If it stays unclear, revert. |

`try` refuses an uncommitted tree, because a verdict has to belong to a commit. It also refuses when the measuring has not passed `check`.

The held-out fixture (`fixture.Holdout`) has another shape, and `try` reports it as one word: no numbers and no profile. Do not read its results to decide what to change, and do not special-case its shape in the code.

## Proving the measuring

`arbor-perf check` runs one build against itself, which must show no change, and against itself slowed by 30%, which must show one. It keeps the result in `.perf/check/`, with the machine's load as `loadAvg`, and lists as `unresolvable` the benchmarks whose noise is above 5%. Run it again when the machine or the fixtures change, and when the last one is over a month old. It takes about 25 minutes.

Passing does not make every benchmark precise. A change shows when it is clearly larger than the benchmark's `noisePct`, so on a busy machine an unresolvable benchmark can show a 30% change and miss a 6% one. Other work on the machine only widens the noise. It should not produce false verdicts, so a round on a busy machine ends `inconclusive` rather than wrong: quiet the machine, or use more `-rounds`, and try again.

## What a field number means

Real-usage data exists only where someone turned on View → Performance Diagnostics.

- A span ends at the first paint of its result, and that wait is its `paint` phase, so the phases account for the whole duration. An operation whose paint comes more than 2 s after its work was done, usually because the window was out of sight, ends as `unseen`: nobody waited, so it stays out of the percentiles. A real stall that long ends the same way.
- `report` and `next` mark numbers they doubt with `flags`: `thin` (under 20 samples), `phases_unexplained`, `mixed_sizes`.
- A log is not an experiment. Two versions are only called different when their medians' 95% intervals do not overlap, the change is at least 5%, and both ran on the same one repository size. Anything else is `no_clear_change`, `too_few_samples` or `mixed_sizes`.

## Other tools

| Command | What it does |
| --- | --- |
| `go run ./cmd/arbor-perf report --since 7d` | Ranked report of this computer's logs. `--input file` reads an export a user sent; `--format json` and `--out dir` are for agents. |
| `go run ./cmd/arbor-perf record` | Stores this machine's benchmark baseline in `.perf/` (outside Git). |
| `go run ./cmd/arbor-perf compare` | Compares the benchmarks with the baseline and fails on a regression, as a check before a release. `--format json` is for agents. It moves the baseline only with `-update`; a kept round tightens it by itself. |
| `go run ./e2e [-only re] [-sizes small]` | Drives the real app through the scenarios and checks budgets. |

## Rules

- **Budgets are not yours to change.** They are in `perf/catalog.go`, and only the owner moves them.
- **Fixtures are not yours to change.** They are pinned by object id, and `TestPinned` fails if they change. Weakening one would fake a win.
- **Measurement changes are separate commits** from optimizations, so that a number never moves for two reasons at once.
- **Privacy stays closed.** Events are built from closed enumerations in `perf/catalog.go`. Never add a free-form field, and never add upload code: `TestNoPackageOfThePerfSystemImportsTheNetwork` and `TestPrivacySentinel` guard this. The ledger holds benchmark numbers and agents' notes, never anything from anyone's usage.

## Known starting points

Examples from `docs/perf-baseline.md`, on the large fixture: `commit` was 410 ms against a 300 ms budget, and `external_change` was 0.8 to 1.7 s against 1 s.
