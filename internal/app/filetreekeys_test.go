package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// newKeysRepo is a project whose rows are, with src open:
//
//	empty/  src/  src/c/  src/a.go  src/b.go  tools/  z.md
func newKeysRepo(t *testing.T) string {
	t.Helper()
	dir := newTestRepo(t, map[string]string{
		"src/a.go":      "package a\n",
		"src/b.go":      "package b\n",
		"src/c/d.go":    "package c\n",
		"tools/util.go": "package tools\n",
		"z.md":          "# z\n",
	})
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// press sends a key and waits for what it started.
func (h *harness) press(key ui.Key) {
	h.t.Helper()
	h.tt.Key(0, key)
	h.settle()
}

func (h *harness) wantCursor(want string) {
	h.t.Helper()
	if got := h.a.tree.shownCursor(); got != want {
		h.t.Fatalf("cursor on %q, want %q; rows %q", got, want, rowPaths(h.a))
	}
}

func (h *harness) wantOpen(want string) {
	h.t.Helper()
	f := &h.a.live.content.file
	if h.a.selectedFile != want || !f.ok || f.path != want {
		h.t.Fatalf("open %q (shown %q), want %q", h.a.selectedFile, f.path, want)
	}
}

func rowPaths(a *app) []string {
	var out []string
	for _, n := range a.tree.nodes {
		out = append(out, n.path)
	}
	return out
}

func TestTreeHasTheKeysOnceItShows(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	if !h.tt.Focused("z.md") {
		t.Fatal("the tree does not have the keyboard focus")
	}
	if h.tt.Focused("Search files…") {
		t.Fatal("the search field has the keyboard focus")
	}

	// Coming back from another mode gives it back.
	h.a.switchMode(modeGit)
	h.settle()
	h.a.switchMode(modeFiles)
	h.settle()
	if !h.tt.Focused("z.md") {
		t.Fatal("the tree did not get the keyboard focus back")
	}
}

func TestDownAndUpGoThroughTheRowsOpeningFiles(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a
	if !slices.Equal(rowPaths(a), []string{"empty", "src", "src/c", "src/a.go", "src/b.go", "tools", "z.md"}) {
		t.Fatalf("rows: %q", rowPaths(a))
	}
	h.wantOpen("src/a.go")
	h.wantCursor("src/a.go")

	h.press(ui.KeyDown)
	h.wantCursor("src/b.go")
	h.wantOpen("src/b.go")
	if text := a.codeState.Buffer.String(); text != "package b\n" {
		t.Fatalf("editor shows %q", text)
	}

	// A directory has nothing to show: the file stays open under it.
	h.press(ui.KeyDown)
	h.wantCursor("tools")
	h.wantOpen("src/b.go")

	h.press(ui.KeyDown)
	h.wantCursor("z.md")
	h.wantOpen("z.md")

	// The last row is as far as it goes.
	h.press(ui.KeyDown)
	h.wantCursor("z.md")

	h.press(ui.KeyUp)
	h.press(ui.KeyUp)
	h.wantCursor("src/b.go")
	h.wantOpen("src/b.go")

	// Back over the file that was open and past it, to a directory.
	h.press(ui.KeyUp)
	h.press(ui.KeyUp)
	h.wantCursor("src/c")
	h.wantOpen("src/a.go")
}

func TestHomeAndEndGoToTheEnds(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	h.press(ui.KeyEnd)
	h.wantCursor("z.md")
	h.wantOpen("z.md")
	h.press(ui.KeyHome)
	h.wantCursor("empty")
	h.wantOpen("z.md")
}

func TestRightOpensADirectoryAndGoesIntoIt(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a
	h.press(ui.KeyEnd)
	h.press(ui.KeyUp)
	h.wantCursor("tools")

	h.press(ui.KeyRight)
	h.wantCursor("tools")
	if !a.openDirs["tools"] {
		t.Fatal("tools did not open")
	}
	mustNode(t, a.tree.nodes, "tools/util.go")

	h.press(ui.KeyRight)
	h.wantCursor("tools/util.go")
	h.wantOpen("tools/util.go")

	// A file has nothing to go into.
	h.press(ui.KeyRight)
	h.wantCursor("tools/util.go")
}

func TestLeftClosesADirectoryOrGoesToTheOneAbove(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a
	h.wantCursor("src/a.go")

	h.press(ui.KeyLeft)
	h.wantCursor("src")
	h.wantOpen("src/a.go") // going up does not change what is open
	if !a.openDirs["src"] {
		t.Fatal("going up from a file closed the directory")
	}

	h.press(ui.KeyLeft)
	h.wantCursor("src")
	if a.openDirs["src"] {
		t.Fatal("src did not close")
	}
	if _, ok := findNode(a.tree.nodes, "src/a.go"); ok {
		t.Fatalf("rows: %q", rowPaths(a))
	}

	// At the top with nowhere to go, it stays.
	h.press(ui.KeyLeft)
	h.wantCursor("src")
}

func TestKeysSkipARowThatIsOnlyANote(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	h.press(ui.KeyHome)
	h.wantCursor("empty")
	h.press(ui.KeyRight)
	if _, ok := findNode(h.a.tree.nodes, "\x00note:empty"); !ok {
		t.Fatalf("rows: %q", rowPaths(h.a))
	}

	// Nothing to go into, and the next row down is past the note.
	h.press(ui.KeyRight)
	h.wantCursor("empty")
	h.press(ui.KeyDown)
	h.wantCursor("src")
	h.press(ui.KeyUp)
	h.wantCursor("empty")
}

func TestEnterOpensAndClosesADirectory(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a
	h.press(ui.KeyLeft)
	h.wantCursor("src")
	h.press(ui.KeyEnter)
	if a.openDirs["src"] {
		t.Fatal("src did not close")
	}
	h.press(ui.KeyEnter)
	if !a.openDirs["src"] {
		t.Fatal("src did not open")
	}
	h.wantCursor("src")
}

func TestKeysGoOnFromTheRowClicked(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	if err := h.tt.Click("z.md"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.wantOpen("z.md")
	h.press(ui.KeyUp)
	h.wantCursor("tools")

	if err := h.tt.Click("tools"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.wantCursor("tools")
	if !h.a.openDirs["tools"] {
		t.Fatal("tools did not open")
	}
	h.press(ui.KeyRight)
	h.wantCursor("tools/util.go")
}

func TestKeysGoOnFromAboveARowThatWentAway(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a

	// What the cursor is on is closed away by a click on the directory above it.
	h.press(ui.KeyDown)
	h.wantCursor("src/b.go")
	a.toggleDir("src")
	h.settle()
	h.press(ui.KeyDown)
	h.wantCursor("tools")
}

func TestKeysInTheEditorStayInTheEditor(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	if err := h.tt.Click("Code editor"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.tt.Focused("z.md") {
		t.Fatal("the click left the focus in the tree")
	}
	h.press(ui.KeyDown)
	h.wantCursor("src/a.go")
	h.wantOpen("src/a.go")
}

func clickSearch(t *testing.T, h *harness) {
	t.Helper()
	if err := h.tt.Click("Search files…"); err != nil {
		t.Fatal(err)
	}
	h.settle()
}

func TestSearchFieldKeysMoveThroughTheResultsAndEnterOpens(t *testing.T) {
	dir := newIgnoredRepo(t)
	writeFile(t, dir, "src/main.js", "project\n")
	mustGit(t, dir, "add", "src/main.js")
	mustGit(t, dir, "commit", "-m", "js")
	h := newHarness(t, dir)
	h.settle()
	a := h.a
	opened := a.selectedFile
	clickSearch(t, h)

	h.tt.Type("main.")
	h.settle()
	if got, want := rowPaths(a), []string{"src/main.go", "src/main.js", "\x00ignored", "node_modules/lodash/main.js"}; !slices.Equal(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
	// The first result is where it starts, so that Enter takes it.
	h.wantCursor("src/main.go")

	// The keys go past the section's title, and open nothing on the way.
	h.press(ui.KeyDown)
	h.wantCursor("src/main.js")
	h.press(ui.KeyDown)
	h.wantCursor("node_modules/lodash/main.js")
	h.press(ui.KeyDown)
	h.wantCursor("node_modules/lodash/main.js")
	if a.selectedFile != opened {
		t.Fatalf("moving opened %q", a.selectedFile)
	}

	// The caret's keys stay in the field.
	h.press(ui.KeyLeft)
	h.press(ui.KeyRight)
	h.wantCursor("node_modules/lodash/main.js")
	if a.explorerQuery != "main." {
		t.Fatalf("query %q", a.explorerQuery)
	}

	h.press(ui.KeyEnter)
	h.wantOpen("node_modules/lodash/main.js")
	if !a.openDirs["node_modules/lodash"] {
		t.Fatalf("not revealed: %v", a.openDirs)
	}
	if a.explorerQuery != "main." {
		t.Fatalf("Enter changed the query: %q", a.explorerQuery)
	}

	// More typing is a new search.
	h.press(ui.KeyUp)
	h.wantCursor("src/main.js")
	h.tt.Type("js")
	h.settle()
	h.wantCursor("src/main.js")
	h.press(ui.KeyEnter)
	h.wantOpen("src/main.js")
}

func TestEscapeLeavesTheSearchForTheTree(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a
	clickSearch(t, h)
	h.tt.Type("util")
	h.settle()
	h.press(ui.KeyEnter)
	h.wantOpen("tools/util.go")

	h.press(ui.KeyEscape)
	if a.explorerQuery != "" {
		t.Fatalf("query %q", a.explorerQuery)
	}
	if !h.tt.Focused("z.md") {
		t.Fatal("the tree does not have the keyboard focus")
	}
	h.wantCursor("tools/util.go")
	h.press(ui.KeyLeft)
	h.wantCursor("tools")
}

func TestSearchWithoutResultsTakesTheKeysQuietly(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	a := h.a
	opened := a.selectedFile
	clickSearch(t, h)
	h.tt.Type("nothinglikethis")
	h.settle()
	if !h.tt.HasText("No matching files") {
		t.Fatalf("%v", h.tt.Texts())
	}
	h.press(ui.KeyDown)
	h.press(ui.KeyUp)
	h.press(ui.KeyEnter)
	if a.selectedFile != opened {
		t.Fatalf("opened %q", a.selectedFile)
	}
}

func TestChangedFilesListTakesTheKeys(t *testing.T) {
	dir := newKeysRepo(t)
	writeFile(t, dir, "src/b.go", "package b // changed\n")
	writeFile(t, dir, "z.md", "# changed\n")
	h := newHarness(t, dir)
	h.settle()
	a := h.a
	a.onlyChanged = true
	h.settle()
	if got := rowPaths(a); !slices.Equal(got, []string{"src/b.go", "z.md"}) {
		t.Fatalf("rows %q", got)
	}
	h.press(ui.KeyDown)
	h.wantOpen("z.md")
	h.press(ui.KeyUp)
	h.wantOpen("src/b.go")
	a.onlyChanged = false
	h.settle()
	h.wantCursor("src/b.go")
}

func TestManyKeysAtOnceShowTheLastFile(t *testing.T) {
	h := newHarness(t, newKeysRepo(t))
	h.settle()
	for range 3 {
		h.tt.Key(0, ui.KeyDown)
	}
	h.settle()
	h.wantCursor("z.md")
	h.wantOpen("z.md")
	if text := h.a.codeState.Buffer.String(); !strings.HasPrefix(text, "# z") {
		t.Fatalf("editor shows %q", text)
	}
}
