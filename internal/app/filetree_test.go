package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// newIgnoredRepo makes a repository with a project and the things Git ignores
// around it. Its names sort so that an ignored directory comes first.
func newIgnoredRepo(t *testing.T) string {
	t.Helper()
	dir := newTestRepo(t, map[string]string{
		".gitignore":     ".cache/\nnode_modules/\ndist/\n*.log\n.env\n",
		"src/main.go":    "package main\n",
		"src/app/run.go": "package app\n",
		"README.md":      "# readme\n",
	})
	for rel, content := range map[string]string{
		".cache/x":                    "x\n",
		"node_modules/react/index.js": "module.exports = 1\n",
		"node_modules/react/deep/a":   "a\n",
		"node_modules/lodash/main.js": "lodash\n",
		"dist/bundle.js":              "built\n",
		".env":                        "SECRET=1\n",
		"src/debug.log":               "log line\n",
	} {
		writeFile(t, dir, rel, content)
	}
	return dir
}

func nodeNames(nodes []flatTreeNode) []string {
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.name
	}
	return names
}

func findNode(nodes []flatTreeNode, p string) (flatTreeNode, bool) {
	for _, n := range nodes {
		if n.path == p {
			return n, true
		}
	}
	return flatTreeNode{}, false
}

func mustNode(t *testing.T, nodes []flatTreeNode, p string) flatTreeNode {
	t.Helper()
	n, ok := findNode(nodes, p)
	if !ok {
		t.Fatalf("no row for %s in %v", p, nodeNames(nodes))
	}
	return n
}

func TestIgnoredFilesShowInTheTreeAsIgnored(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	nodes := a.treeNodes("")

	for _, p := range []string{".cache", "node_modules", "dist"} {
		n := mustNode(t, nodes, p)
		if !n.isDir || !n.ignored || n.open || n.badge != "" {
			t.Errorf("%s: %+v", p, n)
		}
	}
	if n := mustNode(t, nodes, ".env"); n.isDir || !n.ignored {
		t.Errorf(".env: %+v", n)
	}
	for _, p := range []string{"src", "README.md", ".gitignore"} {
		if n := mustNode(t, nodes, p); n.ignored {
			t.Errorf("%s is marked ignored", p)
		}
	}

	// They keep their place by name rather than going to the bottom, and an
	// ignored file in a directory of the project is marked by itself; that
	// directory is the first of the project's, which is open by default.
	if !h.tt.HasText("node_modules") || !h.tt.HasText(".env") {
		t.Fatalf("not on screen: %v", h.tt.Texts())
	}
	if !slices.Equal(nodeNames(nodes)[:4], []string{".cache", "dist", "node_modules", "src"}) {
		t.Fatalf("rows: %v", nodeNames(nodes))
	}
	if n := mustNode(t, nodes, "src/debug.log"); !n.ignored {
		t.Errorf("src/debug.log: %+v", n)
	}
	if n := mustNode(t, nodes, "src/main.go"); n.ignored {
		t.Errorf("src/main.go: %+v", n)
	}
}

func TestAWindowOpensOnTheProjectNotOnWhatIsIgnored(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	if !a.openDirs["src"] || a.openDirs[".cache"] || a.openDirs["node_modules"] {
		t.Fatalf("open directories: %v", a.openDirs)
	}
	if a.selectedFile == "" || a.repo.IsIgnored(a.selectedFile) {
		t.Fatalf("opened %q", a.selectedFile)
	}
	// The ignored files do not count towards the size of the repository, which
	// the performance log compares versions by.
	if n := len(a.repo.AllFiles); n != 4 {
		t.Fatalf("AllFiles: %v", a.repo.AllFiles)
	}
}

func TestIgnoredDirectoryLoadsWhenItOpensWithEverythingMarked(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	if _, ok := findNode(a.treeNodes(""), "node_modules/react"); ok {
		t.Fatal("its contents are there before it opens")
	}
	if slices.Contains(a.repo.AllFiles, "node_modules/react/index.js") {
		t.Fatal("the snapshot lists what is in an ignored directory")
	}

	if err := h.tt.Click("node_modules"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	nodes := a.treeNodes("")
	react := mustNode(t, nodes, "node_modules/react")
	if !react.isDir || !react.ignored || react.depth != 1 {
		t.Fatalf("react: %+v", react)
	}
	if !h.tt.HasText("react") || !h.tt.HasText("lodash") {
		t.Fatalf("not on screen: %v", h.tt.Texts())
	}

	if err := h.tt.Click("react"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	idx := mustNode(t, a.treeNodes(""), "node_modules/react/index.js")
	if !idx.ignored || idx.isDir || idx.depth != 2 {
		t.Fatalf("index.js: %+v", idx)
	}
}

func TestOpeningAnIgnoredFileShowsItAndSaysSo(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	if h.tt.HasText("Ignored") {
		t.Fatal("a project file is tagged as ignored")
	}
	if err := h.tt.Click(".env"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.selectedFile != ".env" {
		t.Fatalf("selected %q", a.selectedFile)
	}
	if got := a.codeState.Buffer.String(); got != "SECRET=1\n" {
		t.Fatalf("shows %q", got)
	}
	if a.codeState.DiffBase != nil {
		t.Fatal("an ignored file has change markers")
	}
	if !h.tt.HasText("Ignored") {
		t.Fatalf("no tag: %v", h.tt.Texts())
	}
}

func TestAnIgnoredFileStaysOpenAcrossRefreshes(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	a.toggleDir("dist")
	h.settle()
	a.openFile("dist/bundle.js")
	h.settle()

	a.externalChange(scopeStatus)
	h.settle()
	a.externalChange(scopeFull)
	h.settle()
	if a.selectedFile != "dist/bundle.js" {
		t.Fatalf("a refresh moved on to %q", a.selectedFile)
	}
	if got := a.codeState.Buffer.String(); got != "built\n" {
		t.Fatalf("shows %q", got)
	}

	// It follows the file, and a generated file that goes away is not replaced
	// by another: the view says so, and shows it again when it comes back.
	writeFile(t, h.dir, "dist/bundle.js", "rebuilt\n")
	a.externalChange(scopeStatus)
	h.settle()
	if got := a.codeState.Buffer.String(); got != "rebuilt\n" {
		t.Fatalf("shows %q", got)
	}
	if err := os.RemoveAll(filepath.Join(h.dir, "dist")); err != nil {
		t.Fatal(err)
	}
	a.externalChange(scopeStatus)
	h.settle()
	if a.selectedFile != "dist/bundle.js" {
		t.Fatalf("moved on to %q", a.selectedFile)
	}
	if !h.tt.HasText("This file is no longer on disk") {
		t.Fatalf("does not say it is gone: %v", h.tt.Texts())
	}
	writeFile(t, h.dir, "dist/bundle.js", "built again\n")
	a.externalChange(scopeStatus)
	h.settle()
	if got := a.codeState.Buffer.String(); got != "built again\n" || h.tt.HasText("This file is no longer on disk") {
		t.Fatalf("shows %q; %v", got, h.tt.Texts())
	}
}

func TestATrackedFileThatBecomesIgnoredStaysOpen(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	h := newHarness(t, dir)
	a := h.a
	a.openFile("b.txt")
	h.settle()
	mustGit(t, dir, "rm", "--cached", "-q", "b.txt")
	writeFile(t, dir, ".gitignore", "b.txt\n")
	a.externalChange(scopeStatus)
	h.settle()
	if a.selectedFile != "b.txt" || !a.repo.IsIgnored("b.txt") {
		t.Fatalf("selected %q, ignored %v", a.selectedFile, a.repo.IsIgnored("b.txt"))
	}
}

func TestTheTreeShowsWhatIsOnDiskThatGitDoesNotList(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"real/inside.txt": "inside\n", "a.txt": "a\n"})
	sub := newTestRepo(t, map[string]string{"s.txt": "in the submodule\n"})
	mustGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", sub, "sub")
	mustGit(t, dir, "commit", "-m", "submodule")
	if err := os.Mkdir(filepath.Join(dir, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", filepath.Join(dir, "pipe")).Run(); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	h := newHarness(t, dir)
	a := h.a
	nodes := a.treeNodes("")
	for _, p := range []string{"sub", "linkdir", "emptydir", "real"} {
		if n := mustNode(t, nodes, p); !n.isDir {
			t.Errorf("%s is not a directory: %+v", p, n)
		}
	}
	if _, ok := findNode(nodes, "pipe"); ok {
		t.Error("a pipe is shown")
	}
	if _, ok := findNode(nodes, ".git"); ok {
		t.Error(".git is shown")
	}

	for _, d := range []string{"sub", "linkdir", "emptydir"} {
		a.toggleDir(d)
	}
	h.settle()
	nodes = a.treeNodes("")
	mustNode(t, nodes, "sub/s.txt")
	if _, ok := findNode(nodes, "sub/.git"); ok {
		t.Error("the .git of a submodule is shown")
	}
	mustNode(t, nodes, "linkdir/inside.txt")
	if !slices.Contains(nodeNames(nodes), "Empty folder") {
		t.Errorf("an empty folder does not say so: %v", nodeNames(nodes))
	}

	// A file in there opens, and stays open across a refresh.
	a.openFile("sub/s.txt")
	h.settle()
	a.externalChange(scopeFull)
	h.settle()
	if a.selectedFile != "sub/s.txt" || a.codeState.Buffer.String() != "in the submodule\n" {
		t.Fatalf("selected %q showing %q", a.selectedFile, a.codeState.Buffer.String())
	}
}

func TestATrackedFileThatWasDeletedStaysInTheTree(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"dir/a.txt": "a\n", "dir/b.txt": "b\n"})
	if err := os.Remove(filepath.Join(dir, "dir/b.txt")); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, dir)
	a := h.a
	n := mustNode(t, a.treeNodes(""), "dir/b.txt")
	if n.badge != "D" {
		t.Fatalf("%+v", n)
	}
	a.openFile("dir/b.txt")
	h.settle()
	if !h.tt.HasText("This file is no longer on disk") || !h.tt.HasText("View Diff") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestTheTreeSortsAsAPersonReads(t *testing.T) {
	dir := newTestRepo(t, map[string]string{
		"README.md": "r\n", "arbor.txt": "a\n", "file2.txt": "2\n", "file10.txt": "10\n", "Zed/z": "z\n", "alpha/a": "a\n",
	})
	h := newHarness(t, dir)
	got := nodeNames(h.a.treeNodes(""))
	// alpha is the directory open by default.
	want := []string{"alpha", "a", "Zed", "arbor.txt", "file2.txt", "file10.txt", "README.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSearchFindsIgnoredFilesAfterThoseOfTheProject(t *testing.T) {
	dir := newIgnoredRepo(t)
	writeFile(t, dir, "src/main.js", "project\n")
	mustGit(t, dir, "add", "src/main.js")
	mustGit(t, dir, "commit", "-m", "js")
	h := newHarness(t, dir)
	a := h.a

	a.explorerQuery = "main."
	h.settle()
	nodes := a.treeNodes("main.")
	var order []string
	for _, n := range nodes {
		order = append(order, n.path)
	}
	want := []string{"src/main.go", "src/main.js", "\x00ignored", "node_modules/lodash/main.js"}
	if !slices.Equal(order, want) {
		t.Fatalf("rows %q, want %q", order, want)
	}
	if n := nodes[3]; !n.ignored || n.name != "main.js" || n.dir != "node_modules/lodash" {
		t.Fatalf("ignored result: %+v", n)
	}
	if n := nodes[0]; n.ignored || n.name != "main.go" || n.dir != "src" {
		t.Fatalf("project result: %+v", n)
	}
	if !h.tt.HasText("Ignored files") {
		t.Fatalf("no section: %v", h.tt.Texts())
	}

	// A file directly in the root, and a query only ignored files match.
	a.explorerQuery = ".ENV"
	h.settle()
	nodes = a.treeNodes(".env")
	if n := mustNode(t, nodes, ".env"); !n.ignored || n.dir != "" {
		t.Fatalf(".env: %+v", n)
	}

	// With nothing ignored to show there is no section for it, and with nothing
	// at all the list says so.
	a.explorerQuery = "readme"
	h.settle()
	if h.tt.HasText("Ignored files") {
		t.Fatalf("an empty section: %v", h.tt.Texts())
	}
	a.explorerQuery = "no such file anywhere"
	h.settle()
	if !h.tt.HasText("No matching files") {
		t.Fatalf("%v", h.tt.Texts())
	}

	// .DS_Store and friends are not to be found either.
	writeFile(t, dir, "node_modules/.DS_Store", "x")
	a.explorerQuery = "ds_store"
	a.externalChange(scopeStatus)
	h.settle()
	if !h.tt.HasText("No matching files") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestSearchFollowsAFileThatAppears(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	a.explorerQuery = "fresh"
	h.settle()
	if !h.tt.HasText("No matching files") {
		t.Fatalf("%v", h.tt.Texts())
	}
	writeFile(t, h.dir, "dist/fresh.js", "x\n")
	a.externalChange(scopeStatus)
	h.settle()
	mustNode(t, a.treeNodes("fresh"), "dist/fresh.js")
}

func TestSearchListsIgnoredFilesOnlyWhenSomeoneSearches(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	a.externalChange(scopeStatus)
	h.settle()
	if s := a.tree.search; s.index != nil || s.loading {
		t.Fatalf("listed them without a search: %d", len(s.index))
	}
	a.onlyChanged = true
	a.explorerQuery = "main"
	h.settle()
	if s := a.tree.search; s.index != nil || s.loading {
		t.Fatalf("listed them for the changed files: %d", len(s.index))
	}
	a.onlyChanged = false
	h.settle()
	if s := a.tree.search; s.index == nil {
		t.Fatal("did not list them for a search")
	}
}

func TestTheRowsAreOnlyBuiltWhenTheyChange(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	first := a.treeNodes("")
	for i := 0; i < 5; i++ {
		h.tt.Frame()
	}
	if &a.treeNodes("")[0] != &first[0] {
		t.Fatal("the rows were built again with nothing changed")
	}
	a.toggleDir("node_modules")
	h.settle()
	again := a.treeNodes("")
	if &again[0] == &first[0] || !mustNode(t, again, "node_modules").open {
		t.Fatal("the rows did not follow opening a directory")
	}
	// A refresh that finds the same on disk still gives a new snapshot.
	before := a.repo
	a.externalChange(scopeStatus)
	h.settle()
	if a.repo == before {
		t.Fatal("test needs a new snapshot")
	}
	mustNode(t, a.treeNodes(""), "node_modules/react")
}

func TestOpeningASearchResultRevealsItInTheTree(t *testing.T) {
	dir := newIgnoredRepo(t)
	for i := 0; i < 60; i++ {
		writeFile(t, dir, "many/"+strings.Repeat("x", 3)+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt", "x\n")
	}
	h := newHarness(t, dir)
	a := h.a
	a.toggleDir("many")
	h.settle()
	a.explorerQuery = "index.js"
	h.settle()
	if err := h.tt.Click("index.js"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.selectedFile != "node_modules/react/index.js" {
		t.Fatalf("selected %q", a.selectedFile)
	}
	if !a.openDirs["node_modules"] || !a.openDirs["node_modules/react"] {
		t.Fatalf("directories above it: %v", a.openDirs)
	}
	if !strings.Contains(a.codeState.Buffer.String(), "module.exports") {
		t.Fatalf("shows %q", a.codeState.Buffer.String())
	}

	// While the search is on it stays where it is, and leaving it shows the row.
	a.explorerQuery = ""
	h.settle()
	nodes := a.treeNodes("")
	n := mustNode(t, nodes, "node_modules/react/index.js")
	if n.depth != 2 || !n.ignored {
		t.Fatalf("%+v", n)
	}
	first, last := a.explorerList.Visible()
	at := slices.IndexFunc(nodes, func(x flatTreeNode) bool { return x.path == n.path })
	if at < first || at > last {
		t.Fatalf("row %d is out of view (%d-%d)", at, first, last)
	}
	if a.tree.reveal != "" {
		t.Fatalf("still revealing %q", a.tree.reveal)
	}

	// It does not come back after the user scrolls away.
	h.tt.Scroll(100, 300, 0, -5000)
	h.tt.Advance(time.Second)
	a.toggleDir("many")
	h.settle()
	if a.tree.reveal != "" {
		t.Fatal("revealing again")
	}
}

func TestAFileWithNoTextToShowSaysWhy(t *testing.T) {
	dir := newTestRepo(t, map[string]string{"a.txt": "a\n"})
	writeFile(t, dir, "empty.txt", "")
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), append([]byte("\x89PNG\r\n\x1a\n\x00"), make([]byte, 2000)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, dir)
	a := h.a

	for _, c := range []struct{ file, title, detail string }{
		{"empty.txt", "This file is empty", ""},
		{"logo.png", "Binary file", "2.0 KB. It can't be shown as text."},
	} {
		a.openFile(c.file)
		h.settle()
		if !h.tt.HasText(c.title) || (c.detail != "" && !h.tt.HasText(c.detail)) {
			t.Fatalf("%s: %v", c.file, h.tt.Texts())
		}
		if got := a.codeState.Buffer.String(); got != "" {
			t.Fatalf("%s left %q in the editor", c.file, got)
		}
	}
	if os.Geteuid() != 0 {
		a.openFile("locked.txt")
		h.settle()
		if !h.tt.HasText("Can't read this file") || !h.tt.HasText("You don't have permission to read it.") {
			t.Fatalf("%v", h.tt.Texts())
		}
	}

	// Back to text, the notice goes.
	a.openFile("a.txt")
	h.settle()
	if h.tt.HasText("Binary file") || h.tt.HasText("Can't read this file") {
		t.Fatalf("%v", h.tt.Texts())
	}
	if got := a.codeState.Buffer.String(); got != "a\n" {
		t.Fatalf("shows %q", got)
	}
	// ⌘F has nothing to look through in a file without text, and must not open a
	// find bar for the next one.
	a.openFile("logo.png")
	h.settle()
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.settle()
	if h.tt.HasText("Find") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestAFolderThatCannotBeReadSaysSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := newIgnoredRepo(t)
	locked := filepath.Join(dir, "dist")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	h := newHarness(t, dir)
	h.a.toggleDir("dist")
	h.settle()
	if !h.tt.HasText("Can't read this folder") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestExpandedIgnoredDirectoryFollowsTheDisk(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	fw := h.watch()
	h.settle()
	real := func(rel string) string {
		p, err := filepath.EvalSymlinks(filepath.Join(h.dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	waitSource := func(p string) *fakeSource {
		t.Helper()
		var s *fakeSource
		h.until("the watch on "+p, func() bool {
			fw.mu.Lock()
			defer fw.mu.Unlock()
			s = fw.sources[p]
			return s != nil
		})
		return s
	}

	repos, statuses := h.countLoads()
	a.toggleDir("dist")
	h.settle()
	src := waitSource(real("dist"))
	fw.mu.Lock()
	opts := fw.opts[real("dist")]
	fw.mu.Unlock()
	if opts.Recursive || !opts.HintsOnly {
		t.Fatalf("the watch of a single directory: %+v", opts)
	}
	a.openFile("dist/bundle.js")
	h.settle()

	// A change shows in the tree and in the open file, without reading Git.
	writeFile(t, h.dir, "dist/new.js", "new\n")
	writeFile(t, h.dir, "dist/bundle.js", "rebuilt\n")
	src.send(t, hint("."))
	h.until("the change", func() bool {
		h.a.live.loads.Wait()
		_, ok := findNode(a.treeNodes(""), "dist/new.js")
		return ok && a.codeState.Buffer.String() == "rebuilt\n"
	})
	if repos.Load()+statuses.Load() != 0 {
		t.Fatalf("read Git %d times for it", repos.Load()+statuses.Load())
	}

	// Closing the directory ends its watch, unless the file open is in it.
	a.openFile("README.md")
	h.settle()
	a.toggleDir("dist")
	h.settle()
	select {
	case <-src.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch of a closed directory goes on")
	}
	if len(a.live.w.dirs) != 0 {
		t.Fatalf("watches: %v", a.live.w.dirs)
	}

	// The file open keeps its directory followed with no row showing it.
	a.explorerQuery = "index.js"
	h.settle()
	a.openFile("node_modules/react/index.js")
	h.settle()
	a.explorerQuery = ""
	h.settle()
	src = waitSource(real("node_modules/react"))
	writeFile(t, h.dir, "node_modules/react/index.js", "module.exports = 2\n")
	src.send(t, hint("."))
	h.until("the change to the open file", func() bool {
		h.a.live.loads.Wait()
		return a.codeState.Buffer.String() == "module.exports = 2\n"
	})

	// Their watches end with the session.
	a.stopWatch()
	select {
	case <-src.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch goes on after the session")
	}
}

func TestDirectoryOutOfSightIsNotWatched(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	h.watch()
	h.settle()
	a.toggleDir("node_modules")
	a.toggleDir("node_modules/react")
	h.settle()
	if len(a.live.w.dirs) != 2 {
		t.Fatalf("watches: %v", a.live.w.dirs)
	}
	// Closing the one above takes both out of view.
	a.toggleDir("node_modules")
	h.settle()
	if len(a.live.w.dirs) != 0 {
		t.Fatalf("watches: %v", a.live.w.dirs)
	}
	// Directories of the project have the repository's watch.
	a.toggleDir("src/app")
	h.settle()
	if len(a.live.w.dirs) != 0 {
		t.Fatalf("watches: %v", a.live.w.dirs)
	}
}

func TestRealWatcherFollowsAnExpandedIgnoredDirectory(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	a.live.startWatch = startFileWatch
	a.stopWatch()
	a.ensureWatch()
	w := a.live.w
	<-w.ready
	if w.sources.Load() != 2 {
		t.Skipf("the file watcher did not start here (%d of 2 watches)", w.sources.Load())
	}
	a.toggleDir("dist")
	h.settle()
	a.openFile("dist/bundle.js")
	h.settle()
	time.Sleep(300 * time.Millisecond) // let the watches settle in

	writeFile(t, h.dir, "dist/created.js", "x\n")
	h.until("the new file in the tree", func() bool {
		_, ok := findNode(a.treeNodes(""), "dist/created.js")
		return ok
	})
	writeFile(t, h.dir, "dist/bundle.js", "rebuilt\n")
	h.until("the open file to follow", func() bool { return a.codeState.Buffer.String() == "rebuilt\n" })

	// A link to a directory is watched where it really is.
	if err := os.Symlink("dist", filepath.Join(h.dir, "dist-link")); err != nil {
		t.Fatal(err)
	}
	mustGit(t, h.dir, "add", "dist-link")
	mustGit(t, h.dir, "commit", "-m", "link")
	h.until("the link in the tree", func() bool {
		n, ok := findNode(a.treeNodes(""), "dist-link")
		return ok && n.isDir
	})
	a.toggleDir("dist-link")
	h.until("what is behind the link", func() bool {
		_, ok := findNode(a.treeNodes(""), "dist-link/bundle.js")
		return ok
	})
	time.Sleep(300 * time.Millisecond)
	writeFile(t, h.dir, "dist/through-link.js", "x\n")
	h.until("the change behind the link", func() bool {
		_, ok := findNode(a.treeNodes(""), "dist-link/through-link.js")
		return ok
	})
}

func TestSwitchingRepositoryForgetsTheTree(t *testing.T) {
	h := newHarness(t, newIgnoredRepo(t))
	a := h.a
	a.toggleDir("node_modules")
	a.explorerQuery = "main"
	h.settle()
	a.resetRepoView()
	if len(a.tree.dirs) != 0 || a.tree.search.index != nil || a.tree.built {
		t.Fatalf("%+v", a.tree)
	}
}
