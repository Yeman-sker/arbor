# Performance baseline (ARB-5 P1)

The state of Arbor before any optimization work, measured with the tools this phase adds.
Optimization commits are judged against these numbers. This file is a snapshot for reading; the
ratchet itself compares against a per-machine baseline kept in `.perf/` (outside git).

- **Date**: 2026-10-31
- **Code**: branch `arb/5-p1` **after merging `integration/arb-issues`** (ARB-3 scrollbars, ARB-6/4
  Git operations and live refresh with background content loading, ARB-7 commit graph rows and log
  format), so every number below reflects the merged behaviour: file, diff and commit contents load off
  the UI thread, `runGit` sets `LC_ALL=C` and `GIT_OPTIONAL_LOCKS=0`, and the Git operations report
  their own spans.
- **Machine class**: `apple-m4-darwin-arm64` — Apple M4, 10 cores, macOS 27.0.1, Go 1.27.3, git 2.54.0
- **Conditions**: a shared developer machine with other builds and test runs going on at the same
  time (load average 4 to 6), so every number carries contention noise: the same scenario varied by
  up to 3x between two runs an hour apart (for example `open_file` 9 ms and 26 ms). The NOISE
  columns are the measured run-to-run spread (two robust standard deviations, the rule the
  comparison uses). A baseline recorded on a quiet machine would be tighter; `arbor-perf record`
  warns when a benchmark's runs spread by more than 25%, and `GitStatus/small` does here.

## Fixtures

Both repositories come from `internal/fixture`, are byte-for-byte reproducible and pinned by object
id (`TestPinned` fails if either changes). They must not be edited to make a number better.

| | small | large |
| --- | ---: | ---: |
| Tracked files | 200 | 20 000 |
| Branches | 200 | 200 |
| Commits on main | 450 | 450 |
| Commits in total | 650 | 650 |
| Working tree | 4 modified, 2 staged, 2 untracked | the same |
| Main at | `bed7c4a2cfc3` | `fe378fe96f02` |

## Go benchmarks

`go run ./cmd/arbor-perf record -count 7` — median of 7 runs of each benchmark (`GitStatus` was
recorded again with 9 runs, and `compare` has since tightened entries that came out faster),
`-benchmem`. Frame benchmarks are **CPU-rendered `ui.NewTester` numbers**: they exclude the GPU and
presentation and say nothing about what a display shows, so compare them only with themselves.
NOISE is `2 sigma / median`; ALLOCS is allocations per operation.

| Benchmark | small median | small noise | small allocs | large median | large noise | large allocs |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| ApplyRepoSnapshot | 10.10 ms | ±3.4% | 298 | 10.83 ms | ±5.9% | 298 |
| ComputeGraph | 95.56 µs | ±1.3% | 1914 | 98.67 µs | ±0.5% | 1914 |
| ExpandCommit | 19.44 ms | ±2.4% | 505 | 20.16 ms | ±1.3% | 503 |
| Frame/files | 193.27 µs | ±0.3% | 126 | 234.73 µs | ±1.6% | 176 |
| Frame/git | 413.23 µs | ±2.1% | 665 | 378.10 µs | ±0.8% | 705 |
| Frame/search | 167.56 µs | ±3.4% | 86 | 844.71 µs | ±9.8% | 180 |
| FrameEntrance/files | 4.13 ms | ±3.5% | 2520 | 4.79 ms | ±0.3% | 3520 |
| FrameEntrance/git | 8.36 ms | ±2.6% | 13301 | 7.72 ms | ±1.2% | 14101 |
| GitStatus | 9.68 ms | ±15.2% | 122 | 39.95 ms | ±8.1% | 122 |
| LoadCommitDetails | 9.77 ms | ±0.5% | 246 | 10.32 ms | ±1.1% | 246 |
| LoadRepo | 20.29 ms | ±1.8% | 4398 | 53.45 ms | ±2.3% | 12992 |
| NewApp | 30.43 ms | ±1.0% | 4708 | 62.38 ms | ±3.0% | 13303 |
| OpenDiff | 4.86 ms | ±2.9% | 142 | 5.38 ms | ±1.4% | 142 |
| OpenFile | 4.94 ms | ±1.6% | 146 | 4.98 ms | ±1.1% | 147 |

Notes:

- `Frame/*` is one settled frame (animations finished, no frame pending). `FrameEntrance/*` is
  `tt.Frame()` while the entrance animation is still running, which makes the tester draw 20 frames;
  it is the regime the earlier ad-hoc "frame Files 4.9 / 5.5 ms, Git 9.1 / 10.5 ms" figures measured.
  A settled frame costs a twentieth of that.
- `OpenFile`, `OpenDiff` and `ExpandCommit` run with the app's loads inline (no window), so they
  include the Git and file reads that the real app does in the background.
- The earlier ad-hoc figures (before the merge) are not comparable: the log format, `LC_ALL` and the
  content loading changed, and the machine was loaded differently.
- Allocation counts of the benchmarks that start `git` (LoadRepo, NewApp) vary by a few objects
  between runs; the comparison allows a benchmark only the spread of its own baseline runs.
- Under contention `arbor-perf compare` re-runs a benchmark it suspects of a regression up to twice
  and judges the fastest round; a real slowdown shows in every round.

## Real-app scenarios

`go run ./e2e` — the app built with `-tags e2e`, driven through mygo's automation socket with real
pointer and keyboard input on a window of 1280 x 820, timings read from the app's own perf spans
(source `span`) or, for the external change, measured by the runner's clock (source `wall`). Budgets
are those of the perf catalog; the budget applies to the median of the runs.

| Scenario | Size | N | Median | Min – Max | Budget | Source | Status | Note |
| --- | --- | ---: | ---: | ---: | ---: | --- | --- | --- |
| startup_first_frame | small | 5 | 208.4 ms | 196.6 ms – 1025.5 ms | 300.0 ms | span | PASS | from the start of main(), cold process; excludes exec and runtime init |
| startup_ready | small | 5 | 208.4 ms | 196.5 ms – 1025.5 ms | 800.0 ms | span | PASS | from the start of main(), cold process; excludes exec and runtime init |
| startup_first_frame | large | 5 | 243.6 ms | 208.2 ms – 256.7 ms | 300.0 ms | span | PASS | from the start of main(), cold process; excludes exec and runtime init |
| startup_ready | large | 5 | 243.6 ms | 208.2 ms – 256.7 ms | 800.0 ms | span | PASS | from the start of main(), cold process; excludes exec and runtime init |
| mode_switch | small | 10 | 1.3 ms | 0.7 ms – 9.9 ms | 50.0 ms | span | PASS | Files<->Git, both directions |
| mode_switch | large | 10 | 2.5 ms | 0.6 ms – 9.2 ms | 50.0 ms | span | PASS | Files<->Git, both directions |
| open_file | small | 5 | 26.5 ms | 26.1 ms – 26.7 ms | 100.0 ms | span | PASS |  |
| open_file | large | 5 | 26.8 ms | 25.5 ms – 30.6 ms | 100.0 ms | span | PASS |  |
| open_diff | small | 5 | 26.9 ms | 26.3 ms – 28.1 ms | 100.0 ms | span | PASS | changed files of the working tree |
| open_diff_commit | small | 5 | 35.4 ms | 26.9 ms – 43.9 ms | 100.0 ms | span | PASS | files of an expanded commit |
| open_diff | large | 5 | 36.4 ms | 26.7 ms – 45.0 ms | 100.0 ms | span | PASS | changed files of the working tree |
| open_diff_commit | large | 5 | 43.7 ms | 35.8 ms – 44.0 ms | 100.0 ms | span | PASS | files of an expanded commit |
| expand_commit | small | 5 | 39.5 ms | 37.4 ms – 44.9 ms | 100.0 ms | span | PASS |  |
| expand_commit | large | 5 | 44.2 ms | 43.7 ms – 52.9 ms | 100.0 ms | span | PASS |  |
| search | small | 6 | 1.0 ms | 0.5 ms – 3.3 ms | 50.0 ms | span | PASS | each keystroke of a 3-letter query, typed then erased |
| search | large | 6 | 2.9 ms | 1.0 ms – 5.5 ms | 50.0 ms | span | PASS | each keystroke of a 3-letter query, typed then erased |
| switch_branch | small | 5 | 158.0 ms | 142.2 ms – 201.8 ms | 500.0 ms | span | PASS | branch menu item chosen until the checkout is painted; main <-> a branch 50 commits behind |
| switch_branch | large | 5 | 254.5 ms | 236.1 ms – 941.7 ms | 500.0 ms | span | PASS | branch menu item chosen until the checkout is painted; main <-> a branch 50 commits behind |
| stage | small | 5 | 59.6 ms | 47.3 ms – 85.3 ms | 300.0 ms | span | PASS | Stage clicked until the file is listed as staged |
| commit | small | 5 | 184.5 ms | 175.9 ms – 234.6 ms | 300.0 ms | span | PASS | Commit clicked until the changes list and graph show it |
| stage | large | 5 | 136.2 ms | 84.8 ms – 660.6 ms | 300.0 ms | span | PASS | Stage clicked until the file is listed as staged |
| commit | large | 5 | 409.5 ms | 293.8 ms – 801.5 ms | 300.0 ms | span | FAIL | Commit clicked until the changes list and graph show it |
| dropped_frames | small | 5 | 0.00% | 0.00% – 0.00% | 1.00% | frames | PASS | a frame over 16.7 ms is dropped; 240 wheel events per run, frames logged by MYGO_FRAME_STATS |
| scroll_frame_max | small | 5 | 2.5 ms | 1.4 ms – 3.8 ms | - | frames | INFO | slowest frame while scrolling |
| scroll_frame_count | small | 5 | 69 | 68 – 70 | - | frames | INFO | frames drawn per run |
| dropped_frames | large | 5 | 0.00% | 0.00% – 0.00% | 1.00% | frames | PASS | a frame over 16.7 ms is dropped; 240 wheel events per run, frames logged by MYGO_FRAME_STATS |
| scroll_frame_max | large | 5 | 2.5 ms | 1.3 ms – 3.8 ms | - | frames | INFO | slowest frame while scrolling |
| scroll_frame_count | large | 5 | 69 | 68 – 70 | - | frames | INFO | frames drawn per run |
| external_change | small | 5 | 353.0 ms | 324.4 ms – 355.4 ms | 1000.0 ms | wall | PASS | a file written until the explorer lists it: includes the file system event and the watcher's debounce |
| external_change_refresh | small | 5 | 38.0 ms | 22.8 ms – 42.2 ms | - | span | INFO | the app's own span: watcher event until the refresh is painted |
| external_change | large | 5 | 1664.2 ms | 915.9 ms – 1739.8 ms | 1000.0 ms | wall | FAIL | a file written until the explorer lists it: includes the file system event and the watcher's debounce |
| external_change_refresh | large | 5 | 1393.5 ms | 540.6 ms – 2163.8 ms | - | span | INFO | the app's own span: watcher event until the refresh is painted |
| idle_frames | small | 3 | 0 | 0 – 0 | 0 | frames | PASS | 3 windows of 5s on a settled window |
| idle_cpu | small | 3 | 0.00% | 0.00% – 0.00% | 1.00% | ps | PASS | share of one core; ps resolution is 10 ms |
| idle_frames | large | 3 | 0 | 0 – 0 | 0 | frames | PASS | 3 windows of 5s on a settled window |
| idle_cpu | large | 3 | 0.00% | 0.00% – 0.00% | 1.00% | ps | PASS | share of one core; ps resolution is 10 ms |
| memory_startup | small | 1 | 116 MB | 116 MB – 116 MB | - | ps | INFO | resident set after startup, settled |
| memory_after_use | small | 1 | 121 MB | 121 MB – 121 MB | - | ps | INFO | after mode switches and expanding commits |
| memory_startup | large | 1 | 119 MB | 119 MB – 119 MB | - | ps | INFO | resident set after startup, settled |
| memory_after_use | large | 1 | 130 MB | 130 MB – 130 MB | - | ps | INFO | after mode switches and expanding commits |
| new_window | - | 0 | - | - | - | - | SKIP | not implementable until ARB-1 (multi-window): the app has one window and no way to open another; budget 300 ms |

Reading the table:

- **Startup** is measured from the start of `main()` to the first paint that holds the new state: it
  excludes process exec and runtime initialisation. First frame and ready coincide because the
  repository is still loaded before the window is created; `ready` will separate once loading goes
  async. The 1.0 s maximum is the first cold launch after a build.
- **Mode switch** and **search** spans start where the build first sees the action and end at the
  paint, so they measure the app's work (build, layout, paint), not input delivery.
- **Open file, open diff, expand commit** end at the paint after the background load published its
  content (`content.go`); a click that replaces a pending load ends the earlier span as cancelled.
  Expanding a commit ends when its file list is shown; the diff of its first file loads after that.
- **switch_branch, stage, commit** are the spans `live.go` reports for those Git operations: the
  click until the result is painted.
- **external_change** is the runner's clock from writing a file until the explorer lists it, which
  includes the file system event and the watcher's debounce; `external_change_refresh` is the app's
  own span from the watcher event. The large repository exceeds the 1 s budget here and varies
  widely with load.
- **new_window** is skipped until ARB-1 (multi-window) exists.
- **dropped_frames** counts frames over 16.7 ms among the frames the engine logs
  (`MYGO_FRAME_STATS=all`) while 240 wheel events scroll the commit graph down and back.
- **idle** is frames drawn and CPU used by a settled window over 5 s.
- **memory** is the resident set (`ps`), the median of three samples, recorded as a baseline with no
  budget.

Over budget at this baseline (on this machine, under this load): `commit` on the large repository
(median 410 ms against 300 ms) and `external_change` on the large repository.

## Reproducing

```sh
go run ./cmd/arbor-perf record            # run the benchmarks, store this machine's baseline
go run ./cmd/arbor-perf compare           # run again; exit 1 on a regression; -update tightens on improvement
go run ./e2e                              # real-app scenarios (needs a desktop session)
go run ./e2e -only 'startup|search' -sizes small
```
