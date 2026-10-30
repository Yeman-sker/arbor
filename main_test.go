package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if !tt.HasText("CHANGES (1)") || !tt.HasText("feat: initial commit") {
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
}
