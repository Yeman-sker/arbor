package app

import (
	"fmt"
	"strings"
	"testing"
)

func numberedLines(n int, changed map[int]string) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if s, ok := changed[i]; ok {
			fmt.Fprintf(&sb, "line %d %s\n", i, s)
		} else {
			fmt.Fprintf(&sb, "line %d\n", i)
		}
	}
	return sb.String()
}

func TestReloadLeavesUnchangedContentAloneAndKeepsScroll(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": numberedLines(400, nil)})
	writeFile(t, dir, "a.txt", numberedLines(400, map[int]string{5: "edited"}))
	h := newHarness(t, dir)
	a := h.a
	if a.selectedFile != "a.txt" || a.diffKind != diffWorking {
		t.Fatalf("selected %q, diff kind %v", a.selectedFile, a.diffKind)
	}
	codeVer := a.codeState.Buffer.Version()
	oldVer, newVer := a.diffState.Old.Version(), a.diffState.New.Version()

	// Nothing changed on disk: no buffer is touched, by either kind of refresh.
	a.externalChange(scopeStatus)
	h.settle()
	a.externalChange(scopeFull)
	h.settle()
	if a.codeState.Buffer.Version() != codeVer {
		t.Fatal("the open file was reset although it did not change")
	}
	if a.diffState.Old.Version() != oldVer || a.diffState.New.Version() != newVer {
		t.Fatal("the diff was reset although it did not change")
	}

	// A change to the file that is open: the new text shows, the scroll stays.
	a.codeState.SetScrollY(400)
	h.tt.Frame()
	scrolled := a.codeState.ScrollY()
	if scrolled == 0 {
		t.Fatal("test needs a scrolled editor")
	}
	writeFile(t, dir, "a.txt", numberedLines(400, map[int]string{5: "edited", 300: "again"}))
	a.externalChange(scopeStatus)
	h.settle()
	if got := a.codeState.Buffer.String(); !strings.Contains(got, "line 300 again") {
		t.Fatal("the open file did not follow the change")
	}
	if a.codeState.Buffer.Version() == codeVer {
		t.Fatal("buffer version did not move")
	}
	if got := a.codeState.ScrollY(); got != scrolled {
		t.Fatalf("scroll = %v, want it kept at %v", got, scrolled)
	}
	// Only the side of the diff that changed was reset, so the lines the user
	// expanded in the other one stay expanded.
	if a.diffState.New.Version() == newVer || a.diffState.Old.Version() != oldVer {
		t.Fatalf("diff versions old %d->%d new %d->%d", oldVer, a.diffState.Old.Version(), newVer, a.diffState.New.Version())
	}
	if !strings.Contains(a.diffState.New.String(), "line 300 again") {
		t.Fatal("the diff did not follow the change")
	}
}

// DiffState has no ScrollY/SetScrollY to save and restore, so this pins what
// the app relies on instead: resetting one side's buffer keeps the viewer's
// scroll position while the lines laid out stay the same.
func TestDiffKeepsItsScrollWhenASideIsReset(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	h := newHarness(t, dir)
	a := h.a
	h.tt.SetSize(1200, 400)
	a.activeMode = modeGit
	a.diffKind = diffWorking
	a.diffTitle = "x.go"
	old := numberedLines(400, nil)
	a.showDiff("k", "x.go", old, numberedLines(400, map[int]string{50: "A", 160: "A", 300: "A"}))
	h.tt.Frame()
	for range 6 {
		h.tt.Scroll(600, 200, 0, 120)
		h.tt.Frame()
	}
	const sep = "97 unmodified lines" // the gap after the last change
	before, ok := h.tt.Find(sep)
	if !ok || before.Y <= 0 {
		t.Fatalf("separator not on screen after scrolling: %v", h.tt.Texts())
	}
	oldVer := a.diffState.Old.Version()
	a.showDiff("k", "x.go", old, numberedLines(400, map[int]string{50: "A", 160: "A", 300: "B"}))
	h.tt.Frame()
	after, ok := h.tt.Find(sep)
	if !ok || after.Y != before.Y {
		t.Fatalf("diff scrolled from y=%v to y=%v when its new side was reset", before.Y, after.Y)
	}
	if a.diffState.Old.Version() != oldVer {
		t.Fatal("the unchanged old side was reset")
	}
}

func TestSwitchingFilesStillLoadsInTheBackground(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "first\n", "b.txt": "second\n"})
	h := newHarness(t, dir)
	a := h.a
	a.selectExplorerFile("b.txt")
	a.selectExplorerFile("a.txt") // quickly: only the last choice may show
	h.settle()
	if a.selectedFile != "a.txt" || a.codeState.Buffer.String() != "first\n" {
		t.Fatalf("selected %q shows %q", a.selectedFile, a.codeState.Buffer.String())
	}
	a.selectExplorerFile("b.txt")
	h.settle()
	if a.codeState.Buffer.String() != "second\n" {
		t.Fatalf("shows %q", a.codeState.Buffer.String())
	}
}

func TestRefreshNeverCollapsesAnExpandedCommit(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	writeFile(t, dir, "b.txt", "b\n")
	mustGit(t, dir, "add", "b.txt")
	mustGit(t, dir, "commit", "-m", "second")
	// The newest commit changes no file, so expanding it selects no diff and
	// a refresh used to toggle it shut.
	mustGit(t, dir, "commit", "--allow-empty", "-m", "empty")

	h := newHarness(t, dir)
	a := h.a
	if len(a.repo.Commits) != 3 || a.diffKind != diffNone {
		t.Fatalf("commits=%d diffKind=%v", len(a.repo.Commits), a.diffKind)
	}
	if a.expandedCommit != a.repo.Commits[0].Hash {
		t.Fatalf("newest commit not expanded at first: %q", a.expandedCommit)
	}
	for _, scope := range []refreshScope{scopeFull, scopeStatus} {
		a.externalChange(scope)
		h.settle()
		if a.expandedCommit != a.repo.Commits[0].Hash {
			t.Fatalf("scope %d: refresh collapsed the expanded commit", scope)
		}
	}

	// The user opens an older commit; a refresh in the middle of loading it
	// must not take the expansion away or give it to the newest commit.
	older := a.repo.Commits[1].Hash
	a.toggleCommit(1)
	a.externalChange(scopeFull)
	h.settle()
	if a.expandedCommit != older {
		t.Fatalf("expanded %q, want the commit the user opened (%q)", a.expandedCommit, older)
	}
	if len(a.commitFiles) != 1 || a.diffKind != diffCommitFile || a.diffTitle != "b.txt" {
		t.Fatalf("files=%v kind=%v title=%q", a.commitFiles, a.diffKind, a.diffTitle)
	}
	if got := a.diffState.New.String(); got != "b\n" {
		t.Fatalf("diff shows %q", got)
	}

	// Collapsing is still the user's to do.
	a.toggleCommit(1)
	if a.expandedCommit != "" {
		t.Fatal("toggle no longer collapses")
	}
}

func TestExpandedCommitThatDisappearsIsForgotten(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	writeFile(t, dir, "b.txt", "b\n")
	mustGit(t, dir, "add", "b.txt")
	mustGit(t, dir, "commit", "-m", "to be amended")
	h := newHarness(t, dir)
	a := h.a
	a.toggleCommit(0)
	h.settle()
	if a.diffKind != diffCommitFile {
		t.Fatalf("diff kind = %v", a.diffKind)
	}
	gone := a.expandedCommit
	mustGit(t, dir, "commit", "--amend", "-m", "amended")
	a.externalChange(scopeFull)
	h.settle()
	if a.expandedCommit == gone || a.selectedCommit != nil && a.selectedCommit.Hash == gone {
		t.Fatal("the amended-away commit is still selected")
	}
	if a.diffKind == diffCommitFile && a.diffTitle != "" && a.selectedCommit == nil {
		t.Fatal("diff still shows a file of the vanished commit")
	}
}
