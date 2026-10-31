package main

import (
	"arbor/internal/fixture"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Benchmark fixtures.
//
// The benchmarks run on the repositories of package arbor/internal/fixture:
// "small" (200 files) and "large" (20000 files), each with 200 feature
// branches and 450 commits on main, and the same fixed dirty working tree
// (four modified, two staged, two untracked files). They are generated once
// per test binary into a temporary directory and removed at exit.
//
// The fixtures are byte-for-byte reproducible and pinned by object id. They are
// the measuring stick of the ARB-5 ratchet: never edit them, their generator or
// their pins to make a benchmark faster or a comparison pass. A deliberate
// change belongs in its own reviewed commit that bumps fixture.Version and
// re-records every baseline.

var fixtures = struct {
	mu   sync.Mutex
	root string
	dirs map[string]string
}{dirs: map[string]string{}}

func TestMain(m *testing.M) {
	code := m.Run()
	fixtures.mu.Lock()
	if fixtures.root != "" {
		os.RemoveAll(fixtures.root)
	}
	fixtures.mu.Unlock()
	os.Exit(code)
}

// benchRepo returns the dirty fixture repository for s, building it on first use.
// Benchmarks must treat it as read-only.
func benchRepo(b *testing.B, s fixture.Spec) string {
	b.Helper()
	fixtures.mu.Lock()
	defer fixtures.mu.Unlock()
	if dir, ok := fixtures.dirs[s.Name]; ok {
		return dir
	}
	if fixtures.root == "" {
		root, err := os.MkdirTemp("", "arbor-bench-")
		if err != nil {
			b.Fatal(err)
		}
		fixtures.root = root
	}
	dir := filepath.Join(fixtures.root, s.Name)
	if err := fixture.BuildDirty(dir, s); err != nil {
		b.Fatalf("build %s fixture: %v", s.Name, err)
	}
	fixtures.dirs[s.Name] = dir
	return dir
}

var benchSpecs = []fixture.Spec{fixture.Small, fixture.Large}

// perSize runs fn once for each fixture size as the sub-benchmarks small and large.
func perSize(b *testing.B, fn func(b *testing.B, s fixture.Spec, dir string)) {
	for _, s := range benchSpecs {
		b.Run(s.Name, func(b *testing.B) {
			dir := benchRepo(b, s)
			b.ReportAllocs()
			fn(b, s, dir)
		})
	}
}

func BenchmarkLoadRepo(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		for b.Loop() {
			if _, err := LoadRepo(dir); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGitStatus(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		for b.Loop() {
			if _, err := runGit(dir, "status", "--porcelain=v2", "--branch", "-uall"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkComputeGraph(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		snap, err := LoadRepo(dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(snap.Commits) < 400 {
			b.Fatalf("fixture has %d commits", len(snap.Commits))
		}
		for b.Loop() {
			ComputeGraph(snap.Commits)
		}
	})
}

func BenchmarkNewApp(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		for b.Loop() {
			if a := newApp(dir); a.repo == nil {
				b.Fatalf("newApp: %s", a.repoErr)
			}
		}
	})
}

// BenchmarkApplyRepoSnapshot applies a loaded snapshot to a fresh app, as
// opening a repository does: it picks the first file and the first diff.
func BenchmarkApplyRepoSnapshot(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		snap, err := LoadRepo(dir)
		if err != nil {
			b.Fatal(err)
		}
		empty := b.TempDir()
		for b.Loop() {
			b.StopTimer()
			a := newApp(empty)
			b.StartTimer()
			a.applyRepoSnapshot(snap, nil)
			if a.repo == nil || a.selectedFile == "" || a.diffKind == diffNone {
				b.Fatal("snapshot not applied")
			}
		}
	})
}

// mainTipIndex finds the commit main points at, which the fixture makes touch
// between two and seven files.
func mainTipIndex(snap *RepoSnapshot) int {
	for i, c := range snap.Commits {
		for _, r := range c.Refs {
			if strings.HasPrefix(r, "HEAD -> ") {
				return i
			}
		}
	}
	return 0
}

// BenchmarkLoadCommitDetails is the Git side of expanding a commit.
func BenchmarkLoadCommitDetails(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		snap, err := LoadRepo(dir)
		if err != nil {
			b.Fatal(err)
		}
		hash := snap.Commits[mainTipIndex(snap)].Hash
		for b.Loop() {
			if _, files, err := LoadCommitDetails(dir, hash); err != nil || len(files) == 0 {
				b.Fatalf("details: %v (%d files)", err, len(files))
			}
		}
	})
}

// BenchmarkExpandCommit is the whole work of clicking a commit: its details and
// the diff of its first file.
func BenchmarkExpandCommit(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		a := newApp(dir)
		idx := mainTipIndex(a.repo)
		for b.Loop() {
			a.expandedCommit = ""
			a.toggleCommit(idx)
			if a.expandedCommit == "" || a.diffKind != diffCommitFile {
				b.Fatal("commit not expanded")
			}
		}
	})
}

func BenchmarkOpenFile(b *testing.B) {
	perSize(b, func(b *testing.B, s fixture.Spec, dir string) {
		a := newApp(dir)
		paths := []string{s.Path(s.Files()/2 + 3), s.Path(s.Files()/3 + 1)}
		for _, p := range paths {
			if _, ok := a.repo.TreeDirs[filepath.Dir(p)]; !ok {
				b.Fatalf("%s not in the tree", p)
			}
		}
		n := 0
		for b.Loop() {
			a.selectExplorerFile(paths[n%len(paths)])
			n++
		}
	})
}

func BenchmarkOpenDiff(b *testing.B) {
	perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
		a := newApp(dir)
		if len(a.repo.Unstaged) == 0 {
			b.Fatal("fixture has no unstaged changes")
		}
		st := a.repo.Unstaged[0]
		for b.Loop() {
			a.selectWorkingFile(st)
		}
	})
}

// BenchmarkFrame is the CPU cost of one settled frame of a view, rendered in
// memory by ui.NewTester: it excludes the GPU and presentation, so compare it
// only with itself. Files is the explorer, Git the changes list, graph and diff,
// and Search the explorer filtered by a query.
func BenchmarkFrame(b *testing.B) {
	views := []struct {
		name string
		set  func(a *app)
	}{
		{"files", func(a *app) { a.activeMode = modeFiles }},
		{"git", func(a *app) { a.activeMode = modeGit }},
		{"search", func(a *app) { a.activeMode = modeFiles; a.explorerQuery = "f01" }},
	}
	for _, v := range views {
		b.Run(v.name, func(b *testing.B) {
			perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
				a := newApp(dir)
				v.set(a)
				a.contentSeq++
				tt := ui.NewTester(a.view, 1280, 800)
				// Finish the entrance animations so a frame is a settled frame.
				tt.Advance(2 * time.Second)
				tt.Frame()
				if tt.FramePending() {
					b.Fatal("the view keeps asking for frames, so a frame is not a single settled frame")
				}
				for b.Loop() {
					tt.Frame()
				}
			})
		})
	}
}

// BenchmarkFrameEntrance is tt.Frame() on a view whose entrance animation is
// still running: the tester's clock stands still, so the view keeps asking for
// frames and Frame draws 20 of them. It is the regime of the ad-hoc frame
// numbers recorded before the ratchet (about 5 ms Files, 9 ms Git); the Frame
// benchmark is the cost of one settled frame.
func BenchmarkFrameEntrance(b *testing.B) {
	for _, v := range []struct {
		name string
		mode int
	}{{"files", modeFiles}, {"git", modeGit}} {
		b.Run(v.name, func(b *testing.B) {
			perSize(b, func(b *testing.B, _ fixture.Spec, dir string) {
				a := newApp(dir)
				a.activeMode = v.mode
				a.contentSeq++
				tt := ui.NewTester(a.view, 1280, 800)
				if !tt.FramePending() {
					b.Fatal("entrance animation already finished")
				}
				for b.Loop() {
					tt.Frame()
				}
			})
		})
	}
}

func TestBenchFixturesAreWired(t *testing.T) {
	// A cheap guard that the benchmark helpers agree with the fixture layout.
	s := fixture.Small
	if s.Files() != 200 || fixture.Large.Files() != 20000 {
		t.Fatalf("fixture sizes changed: %d, %d", s.Files(), fixture.Large.Files())
	}
	if s.Branches != 200 || s.Commits < 400 {
		t.Fatalf("fixture history changed: %d branches, %d commits", s.Branches, s.Commits)
	}
}
