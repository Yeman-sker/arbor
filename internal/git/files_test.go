package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// makeFifo makes a named pipe, on the systems that have them.
func makeFifo(t *testing.T, p string) {
	t.Helper()
	if err := exec.Command("mkfifo", p).Run(); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
}

// newIgnoreRepo makes a repository with the kinds of ignored paths there are.
func newIgnoreRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, err := Run(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@arbor.dev")
	run("config", "user.name", "Arbor Tester")
	run("config", "commit.gpgsign", "false")
	gitignore := "node_modules/\n*.log\n.env\nsemi/\n.DS_Store\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}
	write("src/main.go")
	write("semi/tracked.txt")
	run("add", ".gitignore", "src/main.go")
	run("add", "-f", "semi/tracked.txt")
	run("commit", "-m", "initial")
	for _, rel := range []string{
		"node_modules/react/index.js", ".env", "src/debug.log", "logs/a.log", "logs/deep/b.log",
		"mixed/a.log", "mixed/keep.txt", "semi/other.txt", ".DS_Store", "src/.DS_Store",
		"my debug.log", "中文.log", `quo"te.log`,
	} {
		write(rel)
	}
	return dir
}

func TestSnapshotNamesWhatGitIgnoresWithoutListingIt(t *testing.T) {
	dir := newIgnoreRepo(t)
	snap, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Ignored directories stay one entry each, whatever is in them.
	wantDir := map[string]bool{"node_modules": true, "logs": true}
	wantFile := []string{".env", "src/debug.log", "mixed/a.log", "semi/other.txt", "my debug.log", "中文.log", `quo"te.log`}
	for d := range wantDir {
		isDir, ok := snap.Ignored[d]
		if !ok || !isDir {
			t.Errorf("Ignored[%q] = %v, %v; want a directory", d, isDir, ok)
		}
		if kids, ok := snap.TreeDirs[d]; !ok || (d == "node_modules" && len(kids) != 0) {
			t.Errorf("TreeDirs[%q] = %v, %v", d, kids, ok)
		}
	}
	for _, f := range wantFile {
		if isDir, ok := snap.Ignored[f]; !ok || isDir {
			t.Errorf("Ignored[%q] = %v, %v; want a file", f, isDir, ok)
		}
	}
	// A directory that also holds what is not ignored is not ignored itself.
	for _, d := range []string{"mixed", "semi", "src"} {
		if _, ok := snap.Ignored[d]; ok {
			t.Errorf("%s is marked ignored", d)
		}
	}
	for _, p := range []string{".DS_Store", "src/.DS_Store"} {
		if _, ok := snap.Ignored[p]; ok || slices.Contains(snap.TreeRoots, p) || slices.Contains(snap.TreeDirs["src"], p) {
			t.Errorf("%s is shown", p)
		}
	}

	// The tree has them; the list of files a window opens from does not.
	for _, f := range snap.AllFiles {
		if snap.IsIgnored(f) {
			t.Errorf("AllFiles holds ignored %s", f)
		}
	}
	if !sort.StringsAreSorted(snap.AllFiles) {
		t.Errorf("AllFiles not sorted: %v", snap.AllFiles)
	}
	if !slices.Contains(snap.TreeRoots, "node_modules") || !slices.Contains(snap.TreeRoots, ".env") {
		t.Errorf("roots: %v", snap.TreeRoots)
	}
	if !slices.Contains(snap.TreeDirs["src"], "src/debug.log") {
		t.Errorf("src: %v", snap.TreeDirs["src"])
	}
	// They have no status: they are not changes.
	for p := range snap.Ignored {
		if _, ok := snap.StatusByRel[p]; ok {
			t.Errorf("%s has a status", p)
		}
	}
	for _, st := range append(snap.Staged, snap.Unstaged...) {
		if snap.IsIgnored(st.Path) {
			t.Errorf("%s is listed as a change", st.Path)
		}
	}

	if !snap.IsIgnored("node_modules/react/index.js") || !snap.IsIgnored("logs/deep/b.log") || snap.IsIgnored("src/main.go") {
		t.Error("IsIgnored does not go by the directories above")
	}
}

func TestWorktreeStateKeepsWhatIsIgnoredCurrent(t *testing.T) {
	dir := newIgnoreRepo(t)
	full, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fresh.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadWorktreeState(dir)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeWorktreeState(full, st)
	if !merged.IsIgnored("fresh.log") || !slices.Contains(merged.TreeRoots, "fresh.log") {
		t.Fatalf("merged snapshot misses the new ignored file: %v", merged.TreeRoots)
	}
	if full.IsIgnored("fresh.log") {
		t.Fatal("the snapshot before was changed")
	}
}

func TestListIgnoredFilesGoesIntoIgnoredDirectories(t *testing.T) {
	dir := newIgnoreRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "other", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := ListIgnoredFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"node_modules/react/index.js", ".env", "logs/deep/b.log", "中文.log"} {
		if !slices.Contains(files, want) {
			t.Errorf("%s is missing from %v", want, files)
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".DS_Store") || strings.HasSuffix(f, "/") {
			t.Errorf("listed %q", f)
		}
	}
	if slices.Contains(files, "src/main.go") || slices.Contains(files, "mixed/keep.txt") {
		t.Errorf("listed what is not ignored: %v", files)
	}
}

func TestReadDirSaysWhatIsThere(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"sub/real.txt", "file2.txt", "file10.txt", "README.md", "arbor.txt"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{".git", "emptydir", "Zdir"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file2.txt", filepath.Join(root, "linkfile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	makeFifo(t, filepath.Join(root, "pipe"))

	got, err := ReadDir(root, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []DirEntry{
		{Path: "emptydir", IsDir: true},
		{Path: "linkdir", IsDir: true, Link: true},
		{Path: "sub", IsDir: true},
		{Path: "Zdir", IsDir: true},
		{Path: "arbor.txt"},
		{Path: "broken", Link: true},
		{Path: "file2.txt"},
		{Path: "file10.txt"},
		{Path: "linkfile", Link: true},
		{Path: "README.md"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ReadDir:\n got %+v\nwant %+v", got, want)
	}

	got, err = ReadDir(root, "sub")
	if err != nil || !slices.Equal(got, []DirEntry{{Path: "sub/real.txt"}}) {
		t.Fatalf("ReadDir(sub) = %+v, %v", got, err)
	}
	if _, err := ReadDir(root, "nope"); err == nil {
		t.Fatal("ReadDir of nothing did not fail")
	}
}

func TestChildrenMergesTheDiskWithWhatGitLists(t *testing.T) {
	snap := &RepoSnapshot{
		TreeRoots: []string{"src", "gone.txt", "sub"},
		TreeDirs:  map[string][]string{"src": {"src/deleted.go", "src/main.go"}},
	}
	// Git alone, before the disk has been read.
	got := snap.Children("", nil)
	want := []DirEntry{{Path: "src", IsDir: true}, {Path: "gone.txt"}, {Path: "sub"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// The disk knows sub is a directory, which Git lists as one entry, and has
	// what Git doesn't list. What Git lists and the disk doesn't stays: a tracked
	// file that was deleted.
	got = snap.Children("", []DirEntry{
		{Path: "emptydir", IsDir: true},
		{Path: "src", IsDir: true},
		{Path: "sub", IsDir: true},
	})
	want = []DirEntry{
		{Path: "emptydir", IsDir: true},
		{Path: "src", IsDir: true},
		{Path: "sub", IsDir: true},
		{Path: "gone.txt"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	got = snap.Children("src", []DirEntry{{Path: "src/main.go"}, {Path: "src/new.go"}})
	want = []DirEntry{{Path: "src/deleted.go"}, {Path: "src/main.go"}, {Path: "src/new.go"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEmbeddedRepositoryIsADirectoryNotAFile(t *testing.T) {
	dir := newIgnoreRepo(t)
	inner := filepath.Join(dir, "embedded")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x"},
	} {
		if _, err := Run(inner, args...); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, isDir := snap.TreeDirs["embedded"]; !isDir {
		t.Fatalf("embedded is not a directory; roots %v", snap.TreeRoots)
	}
	if slices.Contains(snap.AllFiles, "embedded") {
		t.Fatal("embedded is listed as a file to open")
	}
}

func TestReadFilePreview(t *testing.T) {
	root := t.TempDir()
	write := func(name string, data []byte, perm os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, perm); err != nil {
			t.Fatal(err)
		}
	}
	write("text.txt", []byte("hello\n"), 0o644)
	write("empty.txt", nil, 0o644)
	write("logo.png", []byte("\x89PNG\r\n\x1a\n\x00\x00"), 0o644)
	write("big.txt", []byte(strings.Repeat("x", maxPreviewBytes+10)), 0o644)
	write("locked.txt", []byte("secret"), 0o000)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	makeFifo(t, filepath.Join(root, "pipe"))
	if err := os.Symlink("pipe", filepath.Join(root, "linkpipe")); err != nil {
		t.Fatal(err)
	}

	if p := ReadFilePreview(root, "text.txt"); p.Kind != PreviewText || p.Text != "hello\n" || p.Size != 6 {
		t.Errorf("text: %+v", p)
	}
	if p := ReadFilePreview(root, "empty.txt"); p.Kind != PreviewEmpty {
		t.Errorf("empty: %+v", p)
	}
	if p := ReadFilePreview(root, "logo.png"); p.Kind != PreviewBinary || p.Size != 10 || p.Text != "" {
		t.Errorf("binary: %+v", p)
	}
	if p := ReadFilePreview(root, "big.txt"); p.Kind != PreviewText || p.Partial() || strings.Contains(p.Text, "truncated") || p.Size != maxPreviewBytes+10 {
		t.Errorf("big: kind %v size %d", p.Kind, p.Size)
	}
	if p := ReadFilePreview(root, "nope.txt"); p.Kind != PreviewMissing {
		t.Errorf("missing: %+v", p)
	}
	if p := ReadFilePreview(root, "dir"); p.Kind != PreviewDirectory {
		t.Errorf("directory: %+v", p)
	}
	if p := ReadFilePreview(root, "linkdir"); p.Kind != PreviewDirectory {
		t.Errorf("link to a directory: %+v", p)
	}
	if p := ReadFilePreview(root, "broken"); p.Kind != PreviewUnreadable || !strings.Contains(p.Reason, "symbolic link") {
		t.Errorf("broken link: %+v", p)
	}
	if os.Geteuid() != 0 { // root reads everything
		if p := ReadFilePreview(root, "locked.txt"); p.Kind != PreviewUnreadable || !strings.Contains(p.Reason, "permission") {
			t.Errorf("no permission: %+v", p)
		}
	}

	// Opening a pipe waits for a writer that may never come.
	for _, name := range []string{"pipe", "linkpipe"} {
		done := make(chan FilePreview, 1)
		go func() { done <- ReadFilePreview(root, name) }()
		select {
		case p := <-done:
			if p.Kind != PreviewUnreadable {
				t.Errorf("%s: %+v", name, p)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("reading %s hangs", name)
		}
	}
}

func TestCompareNatural(t *testing.T) {
	sorted := []string{
		".env", "A.txt", "a.txt", "arbor", "file1", "file02", "file2", "file10", "file100",
		"README.md", "v1.2.txt", "v1.10.txt", "z", "中文",
	}
	got := slices.Clone(sorted)
	slices.Reverse(got)
	slices.SortFunc(got, compareNatural)
	if !slices.Equal(got, sorted) {
		t.Fatalf("got %v", got)
	}
	for _, s := range sorted {
		if compareNatural(s, s) != 0 {
			t.Errorf("%s differs from itself", s)
		}
	}
}

// numberedLines returns lines that are all different, at least size bytes of them.
func numberedLines(size int) []byte {
	var b bytes.Buffer
	b.Grow(size + 32)
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "line %d of a long log\n", i)
	}
	return b.Bytes()
}

func writeBigFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// wantPart checks that p holds the bytes of data it says it does, as whole lines.
func wantPart(t *testing.T, name string, p FilePreview, data []byte) {
	t.Helper()
	if p.Kind != PreviewText || p.Size != int64(len(data)) {
		t.Fatalf("%s: kind %v, size %d of %d", name, p.Kind, p.Size, len(data))
	}
	if p.Text != string(data[p.Start:p.End]) {
		t.Fatalf("%s: text is not the bytes from %d to %d", name, p.Start, p.End)
	}
	if p.End-p.Start > previewPartBytes {
		t.Errorf("%s: %d bytes, more than a part", name, p.End-p.Start)
	}
	if p.Start > 0 && data[p.Start-1] != '\n' {
		t.Errorf("%s: starts inside a line, at %d", name, p.Start)
	}
	if !strings.HasSuffix(p.Text, "\n") {
		t.Errorf("%s: ends inside a line, at %d", name, p.End)
	}
}

func TestALargeFileIsReadAPartAtATime(t *testing.T) {
	root := t.TempDir()
	data := numberedLines(previewPartBytes*2 + previewPartBytes/2)
	writeBigFile(t, root, "big.log", data)
	size := int64(len(data))

	first := ReadFilePreview(root, "big.log")
	wantPart(t, "first", first, data)
	if first.Start != 0 || first.End >= size || !first.Partial() {
		t.Errorf("first: %d to %d of %d, partial %v", first.Start, first.End, size, first.Partial())
	}

	next := ReadFilePreviewAt(root, "big.log", PreviewPlace{Offset: first.End})
	wantPart(t, "next", next, data)
	if next.Start != first.End || next.End >= size {
		t.Errorf("next: %d to %d, after %d", next.Start, next.End, first.End)
	}

	back := ReadFilePreviewAt(root, "big.log", PreviewPlace{Kind: PlaceUpTo, Offset: next.End})
	wantPart(t, "back", back, data)
	if back.End != next.End || back.Start <= 0 {
		t.Errorf("back: %d to %d, up to %d", back.Start, back.End, next.End)
	}

	end := ReadFilePreviewAt(root, "big.log", PreviewPlace{Kind: PlaceEnd})
	wantPart(t, "end", end, data)
	if end.End != size || end.Start <= 0 || !end.Partial() {
		t.Errorf("end: %d to %d of %d", end.Start, end.End, size)
	}

	// A file can have become shorter since the part was asked for, as a rotated log.
	for name, place := range map[string]PreviewPlace{
		"from past the end":  {Offset: size + 100},
		"up to past the end": {Kind: PlaceUpTo, Offset: size + 100},
	} {
		if got := ReadFilePreviewAt(root, "big.log", place); got != end {
			t.Errorf("%s: %d to %d, want the end of the file", name, got.Start, got.End)
		}
	}
}

// Going on from where a part ends, or back from where one starts, misses nothing.
func TestThePartsOfAFileAddUpToIt(t *testing.T) {
	root := t.TempDir()
	data := numberedLines(previewPartBytes*2 + previewPartBytes/2)
	writeBigFile(t, root, "big.log", data)

	var forward strings.Builder
	for place := (PreviewPlace{}); ; {
		p := ReadFilePreviewAt(root, "big.log", place)
		forward.WriteString(p.Text)
		if p.End >= p.Size {
			break
		}
		place = PreviewPlace{Offset: p.End}
	}
	if forward.String() != string(data) {
		t.Errorf("forward: %d bytes of %d", forward.Len(), len(data))
	}

	var backward []string
	for place := (PreviewPlace{Kind: PlaceEnd}); ; {
		p := ReadFilePreviewAt(root, "big.log", place)
		backward = append(backward, p.Text)
		if p.Start == 0 {
			break
		}
		place = PreviewPlace{Kind: PlaceUpTo, Offset: p.Start}
	}
	slices.Reverse(backward)
	if got := strings.Join(backward, ""); got != string(data) {
		t.Errorf("backward: %d bytes of %d", len(got), len(data))
	}
}

// Minified code can be one line longer than a part: it cuts between characters.
func TestAPartWithoutLineBreaksEndsBetweenCharacters(t *testing.T) {
	root := t.TempDir()
	data := []byte(strings.Repeat("界", (previewPartBytes+previewPartBytes/2)/3))
	writeBigFile(t, root, "one-line.min.js", data)

	first := ReadFilePreview(root, "one-line.min.js")
	end := ReadFilePreviewAt(root, "one-line.min.js", PreviewPlace{Kind: PlaceEnd})
	next := ReadFilePreviewAt(root, "one-line.min.js", PreviewPlace{Offset: first.End})
	back := ReadFilePreviewAt(root, "one-line.min.js", PreviewPlace{Kind: PlaceUpTo, Offset: end.Start})
	for name, p := range map[string]FilePreview{"first": first, "end": end, "next": next, "back": back} {
		if !utf8.ValidString(p.Text) || p.Text == "" {
			t.Errorf("%s: %d to %d cuts a character", name, p.Start, p.End)
		}
		if p.Text != string(data[p.Start:p.End]) {
			t.Errorf("%s: text is not the bytes from %d to %d", name, p.Start, p.End)
		}
	}
}

func TestAPartReadsWhatTheFileHasGrownTo(t *testing.T) {
	root := t.TempDir()
	data := numberedLines(previewPartBytes + 1024)
	writeBigFile(t, root, "big.log", data)
	first := ReadFilePreview(root, "big.log")
	last := ReadFilePreviewAt(root, "big.log", PreviewPlace{Offset: first.End})

	const more = "one more line\n"
	data = append(data, more...)
	writeBigFile(t, root, "big.log", data)

	// A part with room for it takes it in.
	again := ReadFilePreviewAt(root, "big.log", PreviewPlace{Offset: last.Start})
	wantPart(t, "last", again, data)
	if again.Start != last.Start || again.End != int64(len(data)) || !strings.HasSuffix(again.Text, more) {
		t.Errorf("last: %d to %d of %d", again.Start, again.End, len(data))
	}

	// A full one stays as it was, and what follows is a part further on.
	again = ReadFilePreviewAt(root, "big.log", PreviewPlace{Offset: first.Start})
	wantPart(t, "first", again, data)
	if again.Start != first.Start || again.End != first.End || again.Size != int64(len(data)) {
		t.Errorf("first: %d to %d of %d", again.Start, again.End, again.Size)
	}
}

func TestAFileAsLargeAsAPartShowsWhole(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte("x"), previewPartBytes)
	writeBigFile(t, root, "exact.txt", data)
	p := ReadFilePreview(root, "exact.txt")
	if p.Partial() || p.Start != 0 || p.End != int64(len(data)) || len(p.Text) != len(data) {
		t.Errorf("%d to %d of %d, partial %v", p.Start, p.End, p.Size, p.Partial())
	}
}

// Whether a file is text goes by how it starts, so that no part of it says otherwise.
func TestAPartIsBinaryIfTheFileStartsThatWay(t *testing.T) {
	root := t.TempDir()
	binary := append([]byte("\x00\x01"), numberedLines(previewPartBytes+1024)...)
	writeBigFile(t, root, "data.bin", binary)
	text := append(numberedLines(binarySniffBytes+16), 0, 1, 2)
	text = append(text, numberedLines(previewPartBytes+1024)...)
	writeBigFile(t, root, "install.sh", text)
	for _, place := range []PreviewPlace{{}, {Offset: previewPartBytes}, {Kind: PlaceEnd}, {Kind: PlaceUpTo, Offset: previewPartBytes}} {
		if p := ReadFilePreviewAt(root, "data.bin", place); p.Kind != PreviewBinary {
			t.Errorf("data.bin at %+v: kind %v", place, p.Kind)
		}
		if p := ReadFilePreviewAt(root, "install.sh", place); p.Kind != PreviewText {
			t.Errorf("install.sh at %+v: kind %v", place, p.Kind)
		}
	}
}

func TestChangedLines(t *testing.T) {
	dir, _ := makeRepo(t, []commitSpec{{file: "first.txt", msg: "first", date: "2024-01-02T03:04:05Z"}})
	write := func(rel, content string) {
		t.Helper()
		writeBigFile(t, dir, rel, []byte(content))
	}
	write("a.txt", "1\n2\n3\n4\n")
	write("[a].txt", "1\n")
	write("logo.bin", "\x00\x01\x02")
	testGit(t, dir, "add", ".")
	testGit(t, dir, "commit", "-m", "files")

	for _, tc := range []struct {
		rel     string
		content string
		want    int
		ok      bool
	}{
		{"a.txt", "1\n2\n3\n4\n", 0, true},
		{"a.txt", "1\nchanged\n3\n4\nadded\n", 3, true},
		{"[a].txt", "1\n", 0, true}, // the name is not a pattern, which would match a.txt
		{"logo.bin", "\x00\x01\x03", 0, false},
		{"not-listed.txt", "new\n", 0, true},
	} {
		write(tc.rel, tc.content)
		if got, ok := ChangedLines(dir, tc.rel); got != tc.want || ok != tc.ok {
			t.Errorf("%s: %d, %v; want %d, %v", tc.rel, got, ok, tc.want, tc.ok)
		}
	}

	noHead := t.TempDir()
	testGit(t, noHead, "init", "-b", "main")
	writeBigFile(t, noHead, "a.txt", []byte("1\n"))
	if _, ok := ChangedLines(noHead, "a.txt"); ok {
		t.Error("a repository with no commit has nothing to compare with")
	}
}

func TestReadTextAtRefReadsAllOfAFile(t *testing.T) {
	dir, _ := makeRepo(t, []commitSpec{{file: "first.txt", msg: "first", date: "2024-01-02T03:04:05Z"}})
	long := numberedLines(maxPreviewBytes * 2)
	writeBigFile(t, dir, "long.txt", long)
	writeBigFile(t, dir, "logo.bin", []byte("\x00\x01\x02"))
	testGit(t, dir, "add", ".")
	testGit(t, dir, "commit", "-m", "files")

	if got := ReadTextAtRef(dir, "HEAD", "long.txt"); got != string(long) {
		t.Errorf("long.txt: %d bytes of %d", len(got), len(long))
	}
	for _, rel := range []string{"logo.bin", "not-there.txt"} {
		if got := ReadTextAtRef(dir, "HEAD", rel); got != "" {
			t.Errorf("%s: %d bytes, want no text", rel, len(got))
		}
	}
}
