package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestComputeGraphTopology(t *testing.T) {
	commits := []Commit{
		{Hash: "c3", ShortHash: "c3", Parents: []string{"c2", "b1"}, Subject: "Merge branch feature"},
		{Hash: "b1", ShortHash: "b1", Parents: []string{"c1"}, Subject: "feat: add graph"},
		{Hash: "c2", ShortHash: "c2", Parents: []string{"c1"}, Subject: "fix: main line"},
		{Hash: "c1", ShortHash: "c1", Parents: nil, Subject: "init"},
	}
	rows := ComputeGraph(commits)
	if len(rows) != 4 {
		t.Fatalf("expected 4 graph rows, got %d", len(rows))
	}
	if !rows[0].IsMerge || rows[0].MaxCols < 2 {
		t.Errorf("expected merge row with >=2 cols, got %+v", rows[0])
	}
	if rows[1].NodeCol != 1 {
		t.Errorf("expected branched commit b1 in lane 1, got %d", rows[1].NodeCol)
	}
	if rows[3].NodeCol != 0 {
		t.Errorf("expected root commit c1 to converge to lane 0, got %d", rows[3].NodeCol)
	}
}

func TestArborViewsWithTempGitRepo(t *testing.T) {
	dir := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "test@arbor.dev")
	mustGit("config", "user.name", "Arbor Tester")

	subDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fileA := filepath.Join(dir, "hello.go")
	fileB := filepath.Join(subDir, "util.go")
	if err := os.WriteFile(fileA, []byte("package main\n\nfunc main() {\n\tprintln(\"v1\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package pkg\n\nfunc Helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", ".")
	mustGit("commit", "-m", "feat: initial commit")

	// Modify hello.go in working tree
	if err := os.WriteFile(fileA, []byte("package main\n\nfunc main() {\n\tprintln(\"v2\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newApp(dir)
	if a.repo == nil {
		t.Fatalf("expected repo loaded, got error: %s", a.repoErr)
	}

	tt := ui.NewTester(a.view, 1280, 800)
	if !tt.HasText("Files") || !tt.HasText("hello.go") {
		t.Fatalf("Explorer view missing expected texts: %v", tt.Texts())
	}

	// Switch to Git view via View Diff button
	if err := tt.Click("View Diff"); err != nil {
		t.Fatalf("Click View Diff: %v", err)
	}
	if a.activeMode != modeGit {
		t.Fatalf("expected activeMode == modeGit, got %d", a.activeMode)
	}
	if !tt.HasText("CHANGES") || !tt.HasText("feat: initial commit") {
		t.Fatalf("Git view missing expected texts: %v", tt.Texts())
	}
}

func TestDiffSeparatorDoesNotBlockScroll(t *testing.T) {
	var oldLines, newLines []string
	for i := 1; i <= 200; i++ {
		oldLines = append(oldLines, fmt.Sprintf("line %d", i))
		if i%25 == 2 {
			newLines = append(newLines, fmt.Sprintf("line %d modified", i))
		} else {
			newLines = append(newLines, fmt.Sprintf("line %d", i))
		}
	}
	dir := t.TempDir()
	a := newApp(dir)
	a.repo = &RepoSnapshot{
		Root:        dir,
		Branch:      "main",
		StatusByRel: map[string]string{},
		TreeDirs:    map[string][]string{},
	}
	a.activeMode = modeGit
	a.diffKind = diffWorking
	a.diffTitle = "scroll.go"
	a.setDiffBuffers("scroll.go", strings.Join(oldLines, "\n"), strings.Join(newLines, "\n"))

	tt := ui.NewTester(a.view, 1200, 320)
	// Find the "18 unmodified lines" separator text
	sepRectBefore, ok := tt.Find("18 unmodified lines")
	if !ok {
		t.Fatalf("expected '18 unmodified lines' separator in view, got texts: %v", tt.Texts())
	}

	// Scroll directly on top of the "18 unmodified lines" text
	tt.Scroll(sepRectBefore.X+sepRectBefore.W/2, sepRectBefore.Y+sepRectBefore.H/2, 0, 80)
	tt.Frame()

	sepRectAfter, ok := tt.Find("18 unmodified lines")
	if !ok {
		t.Fatalf("separator disappeared after small scroll")
	}
	if sepRectAfter.Y >= sepRectBefore.Y {
		t.Fatalf("scrolling over separator did not move diff: Y before=%v, Y after=%v", sepRectBefore.Y, sepRectAfter.Y)
	}

	// Clicking the expand icon button on the separator still expands the collapsed lines
	if err := tt.Click("Expand 18 unmodified lines"); err != nil {
		t.Fatalf("Click expand separator button: %v", err)
	}
	tt.Advance(300 * time.Millisecond)
}

func TestGitSidebarSplitResizeAndGraphScroll(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)

	var commits []Commit
	for i := 0; i < 40; i++ {
		hash := fmt.Sprintf("hash-%02d", i)
		var parents []string
		if i < 39 {
			parents = []string{fmt.Sprintf("hash-%02d", i+1)}
		}
		commits = append(commits, Commit{
			Hash:      hash,
			ShortHash: fmt.Sprintf("c%02d", i),
			Parents:   parents,
			Subject:   fmt.Sprintf("commit-msg-%02d", i),
		})
	}

	a.repo = &RepoSnapshot{
		Root:        dir,
		Branch:      "main",
		Unstaged:    []FileStatus{{Path: "a.go", Work: 'M'}, {Path: "b.go", Work: 'M'}},
		StatusByRel: map[string]string{"a.go": "M", "b.go": "M"},
		TreeDirs:    map[string][]string{},
		Commits:     commits,
		Graph:       ComputeGraph(commits),
	}
	a.activeMode = modeGit

	tt := ui.NewTester(a.view, 1280, 600)

	graphHdrBefore, ok := tt.Find("GRAPH (40)")
	if !ok {
		t.Fatalf("expected GRAPH (40) header in view, got texts: %v", tt.Texts())
	}
	initialChangesH := a.gitChangesH

	// 1. Drag the horizontal divider right above the 28px GRAPH header row downward by 60px
	hdrTop := graphHdrBefore.Y - (28-graphHdrBefore.H)/2
	divX := graphHdrBefore.X + graphHdrBefore.W/2
	divY := hdrTop - 0.5
	tt.Press(divX, divY)
	tt.Move(divX, divY+60)
	tt.Release(divX, divY+60)
	tt.Frame()

	if a.gitChangesH <= initialChangesH+50 {
		t.Fatalf("expected gitChangesH to increase after dragging divider down: before=%v, after=%v", initialChangesH, a.gitChangesH)
	}
	graphHdrAfter, ok := tt.Find("GRAPH (40)")
	if !ok || graphHdrAfter.Y <= graphHdrBefore.Y+50 {
		t.Fatalf("expected GRAPH header Y to move down: before=%v, after=%v", graphHdrBefore.Y, graphHdrAfter.Y)
	}

	// 2. Verify commit history scrolls with mouse wheel when there are many commits
	if !tt.HasText("commit-msg-00") {
		t.Fatalf("expected top commit-msg-00 visible initially")
	}
	if tt.HasText("commit-msg-35") {
		t.Fatalf("expected deep commit-msg-35 to be offscreen initially")
	}

	firstCommitRect, ok := tt.Find("commit-msg-00")
	if !ok {
		t.Fatalf("expected to find commit-msg-00 rect")
	}
	scrollX := firstCommitRect.X + 40
	scrollY := firstCommitRect.Y + 60
	for range 8 {
		tt.Scroll(scrollX, scrollY, 0, 160)
		tt.Frame()
	}

	if !tt.HasText("commit-msg-35") {
		t.Fatalf("expected commit-msg-35 to scroll into view after wheel scrolling, visible texts: %v", tt.Texts())
	}

	// 3. Verify wheel scrolling also works when Changes has many files
	var manyChanges []FileStatus
	for i := 0; i < 20; i++ {
		manyChanges = append(manyChanges, FileStatus{Path: fmt.Sprintf("changed-%02d.go", i), Work: 'M'})
	}
	a.repo.Unstaged = manyChanges
	tt.Frame()

	firstFileRect, ok := tt.Find("changed-00.go")
	if !ok {
		t.Fatalf("expected changed-00.go visible in Changes list")
	}
	for range 5 {
		tt.Scroll(firstFileRect.X+20, firstFileRect.Y+20, 0, 120)
		tt.Frame()
	}
	if !tt.HasText("changed-15.go") {
		t.Fatalf("expected changed-15.go to scroll into view in Changes list, visible texts: %v", tt.Texts())
	}
}

func TestLongRepoNameAndMultiLaneGraphNoOverlap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "track4-noisy-label-fine-grained-recognition")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := newApp(dir)

	// Build a multi-lane topology (8 concurrent branch tips merging into main)
	commits := []Commit{
		{Hash: "b1", ShortHash: "0a31202d", Parents: []string{"m1"}, Refs: []string{"method/proto-em"}, Subject: "Add a second Fisher readout iteration"},
		{Hash: "b2", ShortHash: "fd9e2130", Parents: []string{"m2"}, Refs: []string{"method/package"}, Subject: "Package README: record GPU inference"},
		{Hash: "b3", ShortHash: "69a3fc51", Parents: []string{"m1", "m2"}, Refs: []string{"method/attn-zoom"}, Subject: "Attention zoom fold-0: rerun on semi6"},
		{Hash: "b4", ShortHash: "d2608a1f", Parents: []string{"m1", "m2"}, Subject: "Merge main into attention zoom branch"},
		{Hash: "b5", ShortHash: "4c37d2b8", Parents: []string{"m2"}, Subject: "Posttrain series: LiNeS 4566 official"},
		{Hash: "b6", ShortHash: "3e6bf0db", Parents: []string{"m2"}, Subject: "Status: H71/H72 dead, fold-0 ruler"},
		{Hash: "m1", ShortHash: "cde9078d", Parents: []string{"m2"}, Refs: []string{"method/report"}, Subject: "Trunk series: Lipsum-FT (H70)"},
		{Hash: "m2", ShortHash: "b7d6110d", Parents: nil, Subject: "Merge main documentation into attention"},
	}

	a.repo = &RepoSnapshot{
		Root:        dir,
		Branch:      "main",
		Branches:    []string{"main", "method/proto-em", "method/package"},
		Unstaged:    []FileStatus{{Path: "CLAUDE.md", Work: 'M'}},
		StatusByRel: map[string]string{"CLAUDE.md": "M"},
		TreeDirs:    map[string][]string{},
		Commits:     commits,
		Graph:       ComputeGraph(commits),
	}
	a.activeMode = modeGit
	a.sidebarW = 180 // intentionally too narrow: view must clamp to >= 252

	tt := ui.NewTester(a.view, 1280, 800)
	if a.sidebarW < 252 {
		t.Fatalf("expected sidebarW clamped to >= 252, got %v", a.sidebarW)
	}

	// 1. Verify top bar controls are inside sidebarW and do not overlap
	gitTabRect, ok := tt.Find("Git")
	if !ok {
		t.Fatalf("expected Git tab in titlebar")
	}
	refreshRect, ok := tt.Find("Refresh (⌘R)")
	if !ok {
		t.Fatalf("expected Refresh button in titlebar")
	}
	if refreshRect.X+refreshRect.W > a.sidebarW || gitTabRect.X+gitTabRect.W > refreshRect.X {
		t.Fatalf("titlebar overflow/overlap: gitTab=%+v refresh=%+v sidebarW=%v", gitTabRect, refreshRect, a.sidebarW)
	}

	// 2. Verify Repo/Branch/Pull/Push bar controls all fit within sidebarW without overlapping
	repoBtnRect, ok := tt.Find("track4-noisy-label-fine-grained-recognition")
	if !ok {
		t.Fatalf("expected repo button in sidebar")
	}
	branchBtnRect, ok := tt.Find("main")
	if !ok {
		t.Fatalf("expected branch button in sidebar")
	}
	pullRect, ok := tt.Find("Pull (git pull --rebase)")
	if !ok {
		t.Fatalf("expected Pull button in sidebar")
	}
	pushRect, ok := tt.Find("Push (git push)")
	if !ok {
		t.Fatalf("expected Push button in sidebar")
	}
	if repoBtnRect.W <= 20 || repoBtnRect.X+repoBtnRect.W > branchBtnRect.X+1 {
		t.Fatalf("repo button overlapped branch button: repo=%+v branch=%+v", repoBtnRect, branchBtnRect)
	}
	if branchBtnRect.X+branchBtnRect.W > pullRect.X+1 || pullRect.X+pullRect.W > pushRect.X+1 || pushRect.X+pushRect.W > a.sidebarW {
		t.Fatalf("repo toolbar overflowed sidebarW=%v: branch=%+v pull=%+v push=%+v", a.sidebarW, branchBtnRect, pullRect, pushRect)
	}

	// 3. Verify multi-lane Graph rows: graphW never exceeds ref pill X, and ref pill never overlaps subject or hash
	for i, cm := range commits {
		gW := GraphCellWidth(a.repo.Graph[i].MaxCols)
		subjRect, ok := tt.Find(cm.Subject)
		if !ok {
			t.Fatalf("commit %d (%s) subject not found", i, cm.ShortHash)
		}
		// Graph box starts at row X (margin 4 + pad 2 = 6), so its right edge is 6 + gW
		graphRight := float32(6) + gW
		if len(cm.Refs) > 0 {
			refRect, ok := tt.Find(cm.Refs[0])
			if !ok {
				t.Fatalf("commit %d ref %s not found", i, cm.Refs[0])
			}
			if refRect.X < graphRight-0.5 {
				t.Fatalf("commit %d (%s) graph (right=%v) overlapped ref pill (X=%v)", i, cm.ShortHash, graphRight, refRect.X)
			}
			if subjRect.X < refRect.X+refRect.W-0.5 {
				t.Fatalf("commit %d (%s) ref pill (%+v) overlapped subject (%+v)", i, cm.ShortHash, refRect, subjRect)
			}
		} else if subjRect.X < graphRight-0.5 {
			t.Fatalf("commit %d (%s) graph (right=%v) overlapped subject (X=%v)", i, cm.ShortHash, graphRight, subjRect.X)
		}
		if hashRect, ok := tt.Find(cm.ShortHash); ok {
			if subjRect.X+subjRect.W > hashRect.X+0.5 {
				t.Fatalf("commit %d (%s) subject (%+v) overlapped hash (%+v)", i, cm.ShortHash, subjRect, hashRect)
			}
		}
	}
}

func TestLoadRepoPerformanceSkipsIgnoredBulkFiles(t *testing.T) {
	dir := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "test@arbor.dev")
	mustGit("config", "user.name", "Arbor Tester")

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("datasets/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create an ignored datasets directory with many files
	dataDir := filepath.Join(dir, "datasets", "train")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		_ = os.WriteFile(filepath.Join(dataDir, fmt.Sprintf("img_%04d.jpg", i)), []byte("x"), 0o644)
	}
	mustGit("add", ".gitignore", "main.py")
	mustGit("commit", "-m", "initial")

	t0 := time.Now()
	snap, err := LoadRepo(dir)
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("LoadRepo failed: %v", err)
	}
	for _, f := range snap.AllFiles {
		if strings.HasPrefix(f, "datasets/") {
			t.Fatalf("expected gitignored datasets/ to be excluded from AllFiles, found %s", f)
		}
	}
	if elapsed > 2*time.Second {
		t.Fatalf("LoadRepo took too long (%v)", elapsed)
	}

	// Simulate a rogue hook flipping core.bare=true on a worktree repository
	mustGit("config", "core.bare", "true")
	snapBare, err := LoadRepo(dir)
	if err != nil {
		t.Fatalf("LoadRepo should succeed even when core.bare=true on a .git worktree: %v", err)
	}
	if len(snapBare.AllFiles) == 0 || len(snapBare.Commits) == 0 {
		t.Fatalf("expected files and commits when core.bare=true, got files=%d commits=%d", len(snapBare.AllFiles), len(snapBare.Commits))
	}
}


