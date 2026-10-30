package main

import (
	"os"
	"path/filepath"
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

	fileA := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(fileA, []byte("package main\n\nfunc main() {\n\tprintln(\"v1\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "hello.go")
	mustGit("commit", "-m", "feat: initial commit")

	// Modify hello.go in working tree so we have both a commit and a working tree diff
	if err := os.WriteFile(fileA, []byte("package main\n\nfunc main() {\n\tprintln(\"v2\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newApp(dir)
	if a.repo == nil {
		t.Fatalf("expected repo loaded, got error: %s", a.repoErr)
	}
	if len(a.repo.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(a.repo.Commits))
	}
	if len(a.repo.Unstaged) != 1 {
		t.Fatalf("expected 1 unstaged file, got %d", len(a.repo.Unstaged))
	}

	tt := ui.NewTester(a.view, 1280, 800)
	if !tt.HasText("FILES") || !tt.HasText("hello.go") {
		t.Fatalf("Explorer view missing expected texts: %v", tt.Texts())
	}

	// Switch to Git & Graph view via View Diff button
	if err := tt.Click("View Diff →"); err != nil {
		t.Fatalf("Click View Diff: %v", err)
	}
	if a.activeTab != tabGit {
		t.Fatalf("expected activeTab == tabGit, got %d", a.activeTab)
	}
	if !tt.HasText("SOURCE CONTROL") || !tt.HasText("feat: initial commit") {
		t.Fatalf("Git view missing expected texts: %v", tt.Texts())
	}
}
