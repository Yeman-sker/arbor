package main

import (
	"arbor/perf"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

var pickerNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// branchesApp is an app showing a made-up repository with n branches named
// feature/branch-000.. on main, so the picker has many rows without Git: the
// newest first, as the snapshot has them.
func branchesApp(t *testing.T, n int) (*app, *ui.Tester) {
	t.Helper()
	dir := t.TempDir()
	a := newApp(dir)
	branches := []Branch{{Name: "main", Current: true, Time: pickerNow.Add(-time.Hour)}}
	for i := 0; i < n; i++ {
		branches = append(branches, Branch{
			Name: fmt.Sprintf("feature/branch-%03d", i),
			Time: pickerNow.Add(-time.Duration(i+2) * 3 * time.Hour),
		})
	}
	a.repo = &RepoSnapshot{
		Root:        dir,
		Branch:      "main",
		Branches:    branches,
		StatusByRel: map[string]string{},
		TreeDirs:    map[string][]string{},
	}
	a.cards.now = func() time.Time { return pickerNow }
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	return a, tt
}

func openPicker(t *testing.T, a *app, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click(a.repo.Branch); err != nil {
		t.Fatalf("clicking the branch button: %v", err)
	}
	tt.Frame()
	if !a.branchPick.open {
		t.Fatal("the picker did not open")
	}
}

// highlighted is the label of the item the picker has highlighted.
func highlighted(p *picker) string {
	n := len(p.matches)
	if p.hi < n {
		return p.rows[p.matches[p.hi]].Label
	}
	return fmt.Sprintf("<action %d>", p.hi-n)
}

func TestParseBranches(t *testing.T) {
	out := strings.Join([]string{
		" \tfeature/old\t1000\t",
		"*\tmain\t2000\t/repo",
		" \tfeature/in-tree\t3000\t/tmp/other tree/wt",
		" \tno-date\t\t",
		" \tzeta\t3000\t",
		"",
		"garbage",
		" \t\t5\t",
	}, "\n")
	got := parseBranches(out)
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	// The current branch first, then the newest tip, ties by name; a branch
	// with no date last.
	if want := []string{"main", "feature/in-tree", "zeta", "feature/old", "no-date"}; !slices.Equal(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	if !got[0].Current || got[0].Worktree != "" {
		t.Fatalf("the current branch is %+v: its own worktree is not 'elsewhere'", got[0])
	}
	if got[1].Worktree != "/tmp/other tree/wt" {
		t.Fatalf("worktree = %q", got[1].Worktree)
	}
	if !got[3].Time.Equal(time.Unix(1000, 0)) || !got[4].Time.IsZero() {
		t.Fatalf("times = %v, %v", got[3].Time, got[4].Time)
	}
}

func TestLoadRepoReadsBranchWorktreesAndTimes(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-01T10:00:00+00:00")
	mustGit(t, dir, "checkout", "-q", "-b", "older")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "older")
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-20T10:00:00+00:00")
	mustGit(t, dir, "checkout", "-q", "-b", "newer")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "newer")
	mustGit(t, dir, "checkout", "-q", "main")
	wt := filepath.Join(t.TempDir(), "a worktree")
	mustGit(t, dir, "worktree", "add", "-q", wt, "older")
	realWt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}

	snap, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range snap.Branches {
		names = append(names, b.Name)
	}
	if want := []string{"main", "newer", "older"}; !slices.Equal(names, want) {
		t.Fatalf("branches = %v, want %v", names, want)
	}
	byName := map[string]Branch{}
	for _, b := range snap.Branches {
		byName[b.Name] = b
	}
	if !byName["main"].Current || byName["newer"].Current {
		t.Fatalf("current flags wrong: %+v", snap.Branches)
	}
	if byName["older"].Worktree != realWt || byName["newer"].Worktree != "" || byName["main"].Worktree != "" {
		t.Fatalf("worktrees: older=%q newer=%q main=%q (want %q)", byName["older"].Worktree, byName["newer"].Worktree, byName["main"].Worktree, realWt)
	}
	if got := byName["newer"].Time.UTC(); !got.Equal(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("newer tip time = %v", got)
	}
}

func TestShortAgo(t *testing.T) {
	now := pickerNow
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "now"},
		{-time.Hour, "now"},
		{5 * time.Minute, "5m"},
		{59*time.Minute + 30*time.Second, "59m"},
		{3 * time.Hour, "3h"},
		{47 * time.Hour, "1d"},
		{6 * 24 * time.Hour, "6d"},
		{14 * 24 * time.Hour, "2w"},
		{45 * 24 * time.Hour, "1mo"},
		{300 * 24 * time.Hour, "10mo"},
		{800 * 24 * time.Hour, "2y"},
	} {
		if got := shortAgo(now, now.Add(-tc.ago)); got != tc.want {
			t.Errorf("%v ago = %q, want %q", tc.ago, got, tc.want)
		}
	}
	if shortAgo(now, time.Time{}) != "" {
		t.Error("an unknown time must not be shown as an age")
	}
}

func TestFilterRowsRanksAndMatchesWords(t *testing.T) {
	rows := []pickerRow{
		{Key: "1", Label: "release/feature-x"},
		{Key: "2", Label: "feature"},
		{Key: "3", Label: "feature-login"},
		{Key: "4", Label: "main", Hint: "~/work/feature-repo"},
		{Key: "5", Label: "Feature-Zed"},
	}
	idx := func(q string) []string {
		var keys []string
		for _, i := range filterRows(rows, lowerLabels(rows), q) {
			keys = append(keys, rows[i].Key)
		}
		return keys
	}
	if got := idx(""); !slices.Equal(got, []string{"1", "2", "3", "4", "5"}) {
		t.Fatalf("empty query = %v: every row, in the caller's order", got)
	}
	// The label itself, then labels starting with the word, then labels with
	// it inside, then rows that match only through their hint; the order of
	// the rows within each.
	if got := idx("feature"); !slices.Equal(got, []string{"2", "3", "5", "1", "4"}) {
		t.Fatalf("'feature' = %v", got)
	}
	if got := idx("  FEATURE   x "); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("two words = %v", got)
	}
	if got := idx("nothing"); len(got) != 0 {
		t.Fatalf("no match = %v", got)
	}
}

func TestBranchRowsPutTheCurrentBranchFirst(t *testing.T) {
	repo := &RepoSnapshot{Branch: "mid", Branches: []Branch{
		{Name: "new", Time: pickerNow.Add(-time.Hour)},
		{Name: "wt", Worktree: "/elsewhere", Time: pickerNow.Add(-2 * time.Hour)},
		{Name: "mid", Current: false, Worktree: "/stale", Time: pickerNow.Add(-72 * time.Hour)},
		{Name: "old"},
	}}
	rows := branchRows(repo, pickerNow)
	var names []string
	for _, r := range rows {
		names = append(names, r.Label)
	}
	// Whatever the snapshot's flags say, the branch the window shows is first.
	if want := []string{"mid", "new", "wt", "old"}; !slices.Equal(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	if !rows[0].Current || rows[0].Tag != "" || rows[0].Right != "3d" {
		t.Fatalf("current row = %+v: marked, and its own worktree is no tag", rows[0])
	}
	if rows[2].Tag != "worktree" || !strings.Contains(rows[2].TagTip, "/elsewhere") || rows[1].Tag != "" {
		t.Fatalf("worktree tags: %+v %+v", rows[1], rows[2])
	}
	if rows[1].Right != "1h" || rows[3].Right != "" {
		t.Fatalf("times: %q, %q", rows[1].Right, rows[3].Right)
	}
}

func TestBranchPickerShowsEveryBranch(t *testing.T) {
	const n = 260
	a, tt := branchesApp(t, n)
	openPicker(t, a, tt)

	if got := len(a.branchPick.matches); got != n+1 {
		t.Fatalf("%d rows, want %d: the old menu stopped at 40", got, n+1)
	}
	// The current branch is on top, marked, the rest by recency.
	if got := highlighted(&a.branchPick); got != "main" {
		t.Fatalf("highlight starts on %q", got)
	}
	if !tt.HasText("feature/branch-000") || !tt.HasText("1h") || !tt.HasText("6h") {
		t.Fatalf("first rows missing: %v", tt.Texts())
	}
	if tt.HasText("feature/branch-200") {
		t.Fatal("rows past the first screenful are built: the list is not virtual")
	}

	// Walk the keyboard to the end: every row comes into view on the way.
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		tt.Key(0, ui.KeyDown)
		tt.Frame()
		label := highlighted(&a.branchPick)
		if want := fmt.Sprintf("feature/branch-%03d", i); label != want {
			t.Fatalf("after %d Downs the highlight is on %q, want %q", i+1, label, want)
		}
		if !tt.HasText(label) {
			t.Fatalf("highlighted %q is not on screen: %v", label, tt.Texts())
		}
		seen[label] = true
	}
	if len(seen) != n {
		t.Fatalf("reached %d rows, want %d", len(seen), n)
	}
	// Past the last row the highlight stays on the footer's action.
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if got := highlighted(&a.branchPick); got != "<action 0>" {
		t.Fatalf("after the last row the highlight is on %q", got)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if got := highlighted(&a.branchPick); got != "<action 0>" {
		t.Fatalf("Down at the very end moved the highlight to %q", got)
	}
	for i := 0; i < n+3; i++ {
		tt.Key(0, ui.KeyUp)
	}
	tt.Frame()
	if got := highlighted(&a.branchPick); got != "main" || !tt.HasText("feature/branch-000") {
		t.Fatalf("Up to the top: highlight %q, texts %v", got, tt.Texts())
	}
}

func TestBranchPickerFiltersAsYouType(t *testing.T) {
	a, tt := branchesApp(t, 260)
	openPicker(t, a, tt)

	tt.Type("branch-25")
	tt.Frame()
	// branch-250 to branch-259, which were nowhere near the first screenful.
	if got := len(a.branchPick.matches); got != 10 {
		t.Fatalf("%d matches for branch-25", got)
	}
	if !tt.HasText("feature/branch-250") || !tt.HasText("feature/branch-259") || tt.HasText("feature/branch-100") {
		t.Fatalf("filtered rows: %v", tt.Texts())
	}
	if got := highlighted(&a.branchPick); got != "feature/branch-250" {
		t.Fatalf("a new query highlights its first match, not %q", got)
	}

	tt.Type("9")
	tt.Frame()
	if got := len(a.branchPick.matches); got != 1 || !tt.HasText("feature/branch-259") || tt.HasText("feature/branch-250") {
		t.Fatalf("matches = %d, texts %v", got, tt.Texts())
	}

	tt.Type("zzz")
	tt.Frame()
	if !tt.HasText("No branches match") || len(a.branchPick.matches) != 0 {
		t.Fatalf("empty result: %v", tt.Texts())
	}
	// An empty result still offers to create the branch.
	if !tt.HasText(`Create branch "branch-259zzz"`) {
		t.Fatalf("no create row: %v", tt.Texts())
	}
}

func TestBranchPickerCreateRowAndPrefill(t *testing.T) {
	a, tt := branchesApp(t, 5)
	openPicker(t, a, tt)

	// Without a query the bottom row is New branch.
	if !tt.HasText("New branch") || tt.HasText("Create branch") {
		t.Fatalf("footer without a query: %v", tt.Texts())
	}
	tt.Type("feature/branch-001")
	tt.Frame()
	// A name that is there exactly is not offered again.
	if !tt.HasText("New branch") || tt.HasText("Create branch") {
		t.Fatalf("footer for an existing name: %v", tt.Texts())
	}
	// A prefix is not an exact match.
	for range "1" {
		tt.Key(0, ui.KeyBackspace)
	}
	tt.Frame()
	if !tt.HasText(`Create branch "feature/branch-00"`) {
		t.Fatalf("no create row for a prefix: %v", tt.Texts())
	}

	if err := tt.Click(`Create branch "feature/branch-00"`); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(300 * time.Millisecond)
	if a.branchPick.open {
		t.Fatal("the picker stayed open")
	}
	if !a.newBranchOpen || a.newBranchName != "feature/branch-00" {
		t.Fatalf("modal open=%v name=%q", a.newBranchOpen, a.newBranchName)
	}
	if !tt.HasText("Create & Checkout Branch") {
		t.Fatalf("the existing dialog is not on screen: %v", tt.Texts())
	}

	// New branch opens the same dialog empty.
	a.newBranchOpen = false
	tt.Frame()
	openPicker(t, a, tt)
	if err := tt.Click("New branch"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.newBranchOpen || a.newBranchName != "" {
		t.Fatalf("New branch: open=%v name=%q", a.newBranchOpen, a.newBranchName)
	}
}

func TestBranchPickerEnterOnTheCreateRow(t *testing.T) {
	a, tt := branchesApp(t, 3)
	openPicker(t, a, tt)
	tt.Type("fresh")
	tt.Frame()
	if len(a.branchPick.matches) != 0 || highlighted(&a.branchPick) != "<action 0>" {
		t.Fatalf("matches=%d highlight=%s", len(a.branchPick.matches), highlighted(&a.branchPick))
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !a.newBranchOpen || a.newBranchName != "fresh" || a.branchPick.open {
		t.Fatalf("open=%v name=%q picker=%v", a.newBranchOpen, a.newBranchName, a.branchPick.open)
	}
}

// pickerRepo is a repository on main with branches feature-a and feature-b
// (a has a commit more), and an untouched working tree.
func pickerRepo(t *testing.T) string {
	t.Helper()
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n"})
	mustGit(t, dir, "branch", "feature-b")
	mustGit(t, dir, "checkout", "-q", "-b", "feature-a")
	writeFile(t, dir, "a.txt", "a\n")
	mustGit(t, dir, "commit", "-qam", "a")
	mustGit(t, dir, "checkout", "-q", "main")
	return dir
}

func TestBranchPickerEnterSwitchesToTheHighlightedBranch(t *testing.T) {
	dir := pickerRepo(t)
	h := newHarness(t, dir)
	a := h.a
	openPicker(t, a, h.tt)

	if rows := branchRows(a.repo, time.Now()); rows[0].Label != "main" || !rows[0].Current {
		t.Fatalf("rows = %+v", rows)
	}
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	got := highlighted(&a.branchPick)
	if got == "main" {
		t.Fatal("Down did not move the highlight")
	}
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if a.branchPick.open {
		t.Fatal("the picker stayed open after Enter")
	}
	if a.repo.Branch != got {
		t.Fatalf("branch = %q, want the highlighted %q", a.repo.Branch, got)
	}
	if strings.TrimSpace(mustGit(t, dir, "branch", "--show-current")) != got {
		t.Fatalf("Git is on another branch than %q", got)
	}
}

func TestBranchPickerClickSwitchesAndEscapeDoesNothing(t *testing.T) {
	dir := pickerRepo(t)
	h := newHarness(t, dir)
	a := h.a

	openPicker(t, a, h.tt)
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEscape)
	h.settle()
	if a.branchPick.open {
		t.Fatal("Escape left the picker open")
	}
	if a.repo.Branch != "main" || a.live.writes != 0 || a.newBranchOpen {
		t.Fatalf("Escape did something: branch=%q writes=%d modal=%v", a.repo.Branch, a.live.writes, a.newBranchOpen)
	}
	if h.tt.HasText("Search branches…") || h.tt.HasText("feature-b") {
		t.Fatalf("the picker is still on screen: %v", h.tt.Texts())
	}

	// Clicking the current branch's row only closes the picker.
	openPicker(t, a, h.tt)
	h.tt.Type("mai")
	h.tt.Frame()
	if err := h.tt.Click("main"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.branchPick.open || a.repo.Branch != "main" || a.live.writes != 0 {
		t.Fatalf("current row: open=%v branch=%q writes=%d", a.branchPick.open, a.repo.Branch, a.live.writes)
	}

	openPicker(t, a, h.tt)
	if err := h.tt.Click("feature-b"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.repo.Branch != "feature-b" {
		t.Fatalf("branch = %q after clicking feature-b", a.repo.Branch)
	}
	// The branch just left shows where it belongs now: current on top.
	openPicker(t, a, h.tt)
	if got := highlighted(&a.branchPick); got != "feature-b" {
		t.Fatalf("the picker opens on %q, want the current branch", got)
	}
}

func TestBranchPickerOutsideClickCloses(t *testing.T) {
	a, tt := branchesApp(t, 4)
	openPicker(t, a, tt)
	tt.ClickAt(900, 600)
	tt.Frame()
	if a.branchPick.open {
		t.Fatal("a click outside left the picker open")
	}
	// The button opens it again and, pressed twice, closes it.
	openPicker(t, a, tt)
	if err := tt.Click("main"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.branchPick.open {
		t.Fatal("the button did not close the picker")
	}
}

func TestBranchPickerPointerMovesTheHighlight(t *testing.T) {
	a, tt := branchesApp(t, 6)
	openPicker(t, a, tt)
	r, ok := tt.Find("feature/branch-003")
	if !ok {
		t.Fatal("row missing")
	}
	tt.Move(r.X+10, r.Y+r.H/2)
	tt.Frame()
	tt.Frame()
	if got := highlighted(&a.branchPick); got != "feature/branch-003" {
		t.Fatalf("the highlight is on %q under the pointer", got)
	}
	// The keys move on from there.
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if got := highlighted(&a.branchPick); got != "feature/branch-004" {
		t.Fatalf("Down from the pointer's row: %q", got)
	}
	// A pointer that rests does not take the highlight back.
	tt.Frame()
	tt.Frame()
	if got := highlighted(&a.branchPick); got != "feature/branch-004" {
		t.Fatalf("the resting pointer took the highlight to %q", got)
	}
}

func TestBranchPickerMarksWorktreesAndKeepsTheConflictPaths(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "base\n", "keep.txt": "keep\n"})
	mustGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "feature\n")
	mustGit(t, dir, "commit", "-qam", "feature change")
	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "branch", "busy")
	wt := filepath.Join(t.TempDir(), "other-worktree")
	mustGit(t, dir, "worktree", "add", "-q", wt, "busy")
	realWt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.txt", "dirty\n")

	h := newHarness(t, dir)
	a := h.a
	openPicker(t, a, h.tt)
	if !h.tt.HasText("worktree") {
		t.Fatalf("no worktree tag: %v", h.tt.Texts())
	}
	if n := countText(h.tt, "worktree"); n != 1 {
		t.Fatalf("%d worktree tags, want one (for busy)", n)
	}

	// A branch that is in another worktree: the error names it, as before.
	if err := h.tt.Click("busy"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.live.askOpen {
		t.Fatal("a branch in another worktree must not offer to stash")
	}
	if !hasTextContaining(h.tt, realWt) || !hasTextContaining(h.tt, "busy") {
		t.Fatalf("the toast does not name the worktree: %v", h.tt.Texts())
	}
	if a.repo.Branch != "main" {
		t.Fatalf("branch = %q", a.repo.Branch)
	}

	// A branch that local changes are in the way of: the stash question, from
	// the picker's Enter too.
	openPicker(t, a, h.tt)
	h.tt.Type("feature")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.settle()
	if !a.live.askOpen || a.live.ask == nil || a.live.ask.branch != "feature" {
		t.Fatalf("no stash question; toasts=%v", a.live.toasts)
	}
	if !h.tt.HasText("Stash & Switch") {
		t.Fatalf("dialog missing: %v", h.tt.Texts())
	}
	if err := h.tt.Click("Stash & Switch"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.repo.Branch != "feature" || stashCount(t, dir) != 1 {
		t.Fatalf("branch=%q stashes=%d", a.repo.Branch, stashCount(t, dir))
	}
}

// countText counts the texts on screen that are exactly s.
func countText(tt *ui.Tester, s string) int {
	n := 0
	for _, text := range tt.Texts() {
		if text == s {
			n++
		}
	}
	return n
}

func TestBranchPickerSpanEndsAtTheFirstPaintOfThePanel(t *testing.T) {
	var got []perf.Event
	perf.SetSink(func(e perf.Event) { got = append(got, e) })
	defer perf.SetSink(nil)

	a, tt := branchesApp(t, 8)
	tt.Frame()
	got = nil
	openPicker(t, a, tt)
	if len(got) != 1 || got[0].Op != perf.OpBranchPicker || got[0].Result != perf.ResultOK {
		t.Fatalf("events after opening = %+v", got)
	}
	if a.paints.Pending() {
		t.Fatal("the span is still waiting for a paint")
	}
	// Closing and filtering record nothing.
	tt.Type("x")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(got) != 1 {
		t.Fatalf("more events: %+v", got)
	}
}

func TestBranchPickerSurvivesARefreshWhileOpen(t *testing.T) {
	dir := pickerRepo(t)
	h := newHarness(t, dir)
	a := h.a
	openPicker(t, a, h.tt)
	h.tt.Type("feature")
	h.tt.Frame()
	if len(a.branchPick.matches) != 2 {
		t.Fatalf("matches = %d", len(a.branchPick.matches))
	}
	// Something outside Arbor adds a branch; a refresh brings a new snapshot.
	mustGit(t, dir, "branch", "feature-c")
	a.reload("")
	h.settle()
	if !a.branchPick.open || a.branchPick.query != "feature" {
		t.Fatalf("the refresh closed the picker or lost the query: open=%v query=%q", a.branchPick.open, a.branchPick.query)
	}
	if len(a.branchPick.matches) != 3 || !h.tt.HasText("feature-c") {
		t.Fatalf("the new branch is not listed: %v", h.tt.Texts())
	}
}

func TestBranchPickerNamesCarryATooltip(t *testing.T) {
	a, tt := branchesApp(t, 6)
	long := "feature/an-extremely-long-branch-name-that-cannot-fit-in-a-340-wide-picker-row-at-all"
	a.repo.Branches[2].Name = long
	openPicker(t, a, tt)
	before := countText(tt, long)
	if before == 0 {
		t.Fatalf("the long branch is not listed: %v", tt.Texts())
	}
	r, ok := tt.Find(long)
	if !ok {
		t.Fatal("no rect")
	}
	// On the name, past the marker's room.
	tt.Move(r.X+80, r.Y+r.H/2)
	tt.Frame()
	tt.Advance(1500 * time.Millisecond)
	tt.Frame()
	if after := countText(tt, long); after <= before {
		t.Fatalf("hovering the cut-off name showed no tooltip (%d texts before, %d after)", before, after)
	}
}

func TestPickerDarkAndLightBuild(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, tt := branchesApp(t, 30)
		tt.SetDark(dark)
		openPicker(t, a, tt)
		tt.Advance(300 * time.Millisecond)
		if !tt.HasText("feature/branch-000") {
			t.Fatalf("dark=%v: %v", dark, tt.Texts())
		}
	}
}
