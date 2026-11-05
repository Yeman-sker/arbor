package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arbor/internal/git"

	"github.com/egoist/mygo/ui/editor"
)

// logLines returns lines that are all different, at least size bytes of them.
func logLines(size int) string {
	var b strings.Builder
	b.Grow(size + 32)
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "line %d of a long log\n", i)
	}
	return b.String()
}

const (
	testMB      = 1 << 20
	bigLogBytes = 40 * testMB
)

// newBigFileRepo returns a repository with a log too large to show whole, which Git
// doesn't list as adding it would take a while, and a small file beside it.
func newBigFileRepo(t *testing.T) (dir, log string) {
	t.Helper()
	dir = newTestRepo(t, map[string]string{"a.go": "package a\n"})
	log = logLines(bigLogBytes)
	writeFile(t, dir, "big.log", log)
	return dir, log
}

func (h *harness) wantBar(want string) {
	h.t.Helper()
	if !h.tt.HasText(want) {
		h.t.Fatalf("no %q in %q", want, h.tt.Texts())
	}
}

func (h *harness) clickBar(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatal(err)
	}
	h.settle()
}

// wantPartOf checks that the editor holds just the bytes the view says it shows.
func (h *harness) wantPartOf(file string) git.FilePreview {
	h.t.Helper()
	p, ok := h.a.filePart()
	if !ok {
		h.t.Fatalf("all of the file shows; texts %q", h.tt.Texts())
	}
	if got := h.a.codeState.Buffer.Len(); got != int(p.End-p.Start) {
		h.t.Fatalf("the editor holds %d bytes, of a part of %d", got, p.End-p.Start)
	}
	if h.a.codeState.Buffer.String() != file[p.Start:p.End] {
		h.t.Fatalf("the editor doesn't hold the bytes from %d to %d", p.Start, p.End)
	}
	return p
}

func TestALargeFileSaysHowMuchOfItShows(t *testing.T) {
	dir, log := newBigFileRepo(t)
	h := newHarness(t, dir)

	h.open("big.log")
	h.wantBar("Showing the first 16.0 MB of 40.0 MB")
	h.wantPartOf(log)
	if !strings.HasPrefix(h.a.codeState.Buffer.String(), "line 0 of a long log\n") {
		t.Error("the text doesn't start with the file")
	}

	h.open("a.go")
	if _, ok := h.a.filePart(); ok || h.tt.HasText("Showing") {
		t.Errorf("a small file has no bar: %q", h.tt.Texts())
	}
}

func TestTheBarMovesThroughAFile(t *testing.T) {
	dir, log := newBigFileRepo(t)
	h := newHarness(t, dir)
	h.open("big.log")
	first := h.wantPartOf(log)
	if h.a.fileLineNumbers() != editor.LineNumbersOn {
		t.Error("the first part counts its lines from the start of the file")
	}

	// Nothing comes before the start.
	if err := h.tt.Click("Previous Part"); err == nil {
		h.settle()
	}
	if err := h.tt.Click("Go to Start of File"); err == nil {
		h.settle()
	}
	if got := h.wantPartOf(log); got != first {
		t.Fatalf("moved before the start, to %d", got.Start)
	}

	h.clickBar("Next Part")
	next := h.wantPartOf(log)
	if next.Start != first.End {
		t.Fatalf("next part from %d, want %d", next.Start, first.End)
	}
	h.wantBar("Showing 16.0 MB of 40.0 MB, starting at 40.0%")
	if h.a.fileLineNumbers() != editor.LineNumbersOff {
		t.Error("a part further in numbers its lines from 1")
	}
	if got := h.a.codeState.Cursor(); got != 0 {
		t.Errorf("the next part starts at %d, not at its top", got)
	}

	h.clickBar("Previous Part")
	back := h.wantPartOf(log)
	if back.End != next.Start {
		t.Fatalf("previous part up to %d, want %d", back.End, next.Start)
	}
	if got := h.a.codeState.Cursor(); got != h.a.codeState.Buffer.Len() {
		t.Errorf("the previous part is at %d of %d, not at its bottom", got, h.a.codeState.Buffer.Len())
	}

	h.clickBar("Go to End of File")
	end := h.wantPartOf(log)
	if end.End != int64(len(log)) {
		t.Fatalf("the end of the file is at %d of %d", end.End, len(log))
	}
	h.wantBar("Showing the last 16.0 MB of 40.0 MB")
	if got := h.a.codeState.Cursor(); got != h.a.codeState.Buffer.Len() {
		t.Errorf("the end is at %d of %d, not at its bottom", got, h.a.codeState.Buffer.Len())
	}

	// Nothing comes after the end.
	if err := h.tt.Click("Next Part"); err == nil {
		h.settle()
	}
	if got := h.wantPartOf(log); got != end {
		t.Fatalf("moved past the end, to %d", got.Start)
	}

	h.clickBar("Go to Start of File")
	if got := h.wantPartOf(log); got != first {
		t.Fatalf("the start of the file is at %d", got.Start)
	}
	h.wantBar("Showing the first 16.0 MB of 40.0 MB")
}

// A log being written to doesn't move what is being read, and what it has grown by
// can be gone to.
func TestAPartStaysPutWhenTheFileGrows(t *testing.T) {
	dir, log := newBigFileRepo(t)
	h := newHarness(t, dir)
	h.open("big.log")
	h.clickBar("Go to End of File")
	end := h.wantPartOf(log)
	h.a.codeState.SetScrollY(400)

	f, err := os.OpenFile(filepath.Join(dir, "big.log"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	const more = "one more line\n"
	if _, err := f.WriteString(more); err != nil {
		t.Fatal(err)
	}
	f.Close()
	log += more

	h.a.openFile("big.log")
	h.settle()
	got := h.wantPartOf(log)
	if got.Start != end.Start || got.Size != int64(len(log)) {
		t.Errorf("from %d of %d, want it from %d of %d", got.Start, got.Size, end.Start, len(log))
	}
	if y := h.a.codeState.ScrollY(); y != 400 {
		t.Errorf("scrolled to %v, want it to stay at 400", y)
	}

	h.clickBar("Next Part")
	if got := h.wantPartOf(log); got.End != int64(len(log)) || !strings.HasSuffix(h.a.codeState.Buffer.String(), more) {
		t.Errorf("the new end of the file is not on screen: %d to %d", got.Start, got.End)
	}
}

func TestAnotherFileStartsAtItsStart(t *testing.T) {
	dir, log := newBigFileRepo(t)
	writeFile(t, dir, "other.log", log)
	h := newHarness(t, dir)
	h.open("big.log")
	h.clickBar("Go to End of File")

	h.open("other.log")
	if got := h.wantPartOf(log); got.Start != 0 {
		t.Errorf("the other file opens at %d", got.Start)
	}
	h.wantBar("Showing the first 16.0 MB of 40.0 MB")

	h.open("big.log")
	if got := h.wantPartOf(log); got.Start != 0 {
		t.Errorf("coming back, the file opens at %d", got.Start)
	}
}

// editLines changes n lines evenly spread through text.
func editLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	step := len(lines) / n
	for i := 0; i < n; i++ {
		lines[i*step+step/2] = fmt.Sprintf("edited %d", i)
	}
	return strings.Join(lines, "\n")
}

// Comparing takes time on the UI thread that grows with the size of a file and with
// what changed in it: past a point the file shows without its marks, as the marks
// are worth less than a window that stops.
func TestChangeMarksStayWithinWhatTheyCost(t *testing.T) {
	small := logLines(alwaysMarkedBytes / 2)
	large := logLines(4 * testMB)
	dir := newTestRepo(t, map[string]string{
		"small.txt":     small,
		"few.txt":       large,
		"many.txt":      large,
		"untouched.txt": large,
	})
	writeFile(t, dir, "small.txt", editLines(small, 2000))
	writeFile(t, dir, "few.txt", editLines(large, 5))
	writeFile(t, dir, "many.txt", editLines(large, 2000))
	h := newHarness(t, dir)

	for _, tc := range []struct {
		file  string
		marks bool
	}{
		{"small.txt", true},
		{"few.txt", true},
		{"untouched.txt", true},
		{"many.txt", false},
	} {
		h.open(tc.file)
		if got := h.a.codeState.DiffBase != nil; got != tc.marks {
			t.Errorf("%s: marks %v, want %v", tc.file, got, tc.marks)
		}
	}
}

// A part has nothing to be compared with, and the whole of the file at HEAD could be
// much more than can be held.
func TestAPartOfAFileHasNoChangeMarks(t *testing.T) {
	long := logLines(17 * testMB)
	dir := newTestRepo(t, map[string]string{"long.log": long})
	writeFile(t, dir, "long.log", "changed\n"+long)
	h := newHarness(t, dir)
	h.open("long.log")
	if _, ok := h.a.filePart(); !ok {
		t.Fatal("all of the file shows")
	}
	if h.a.codeState.DiffBase != nil {
		t.Error("a part of a file has marks")
	}
}

func TestFilePartText(t *testing.T) {
	const gb = int64(1) << 30
	const part = int64(16 * testMB)
	for _, tc := range []struct {
		p    git.FilePreview
		want string
	}{
		{git.FilePreview{Size: 2 * gb, End: part}, "Showing the first 16.0 MB of 2.0 GB"},
		{git.FilePreview{Size: 2 * gb, Start: 2*gb - part, End: 2 * gb}, "Showing the last 16.0 MB of 2.0 GB"},
		{git.FilePreview{Size: 2 * gb, Start: gb, End: gb + part}, "Showing 16.0 MB of 2.0 GB, starting at 50.0%"},
	} {
		if got := filePartText(tc.p); got != tc.want {
			t.Errorf("%q, want %q", got, tc.want)
		}
	}
}
