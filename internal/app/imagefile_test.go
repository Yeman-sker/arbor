package app

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const smallSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="16" viewBox="0 0 24 16"><rect width="24" height="16" fill="#f00"/></svg>`

func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

func pngFile(t *testing.T, w, h int, c color.Color) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, solid(w, h, c)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// pngHeader is the start of a PNG that says it is of a size, and has none of its
// pixels: all that it takes to find out it is too large.
func pngHeader(w, h uint32) string {
	var ihdr bytes.Buffer
	ihdr.WriteString("IHDR")
	binary.Write(&ihdr, binary.BigEndian, w)
	binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{8, 6, 0, 0, 0})
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	binary.Write(&out, binary.BigEndian, uint32(ihdr.Len()-4))
	out.Write(ihdr.Bytes())
	binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(ihdr.Bytes()))
	return out.String()
}

// newImagesRepo is a project with an image of each kind, and what can go wrong.
func newImagesRepo(t *testing.T) string {
	t.Helper()
	var j, g bytes.Buffer
	if err := jpeg.Encode(&j, solid(8, 6, color.White), nil); err != nil {
		t.Fatal(err)
	}
	pal := image.NewPaletted(image.Rect(0, 0, 5, 7), color.Palette{color.Black, color.White})
	if err := gif.Encode(&g, pal, nil); err != nil {
		t.Fatal(err)
	}
	return newTestRepo(t, map[string]string{
		"a.go":          "package a\n",
		"art/dot.png":   pngFile(t, 4, 3, color.RGBA{255, 0, 0, 255}),
		"art/photo.JPG": j.String(),
		"art/still.gif": g.String(),
		"art/icon.svg":  smallSVG,
		"art/bad.png":   "\x89PNG\r\n\x1a\n\x00\x00not a picture at all",
		"art/bad.svg":   "<svg this is not",
		"art/lfs.png":   "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 12\n",
		"art/huge.png":  pngHeader(20000, 20000),
	})
}

func (h *harness) open(rel string) {
	h.t.Helper()
	h.a.selectExplorerFile(rel)
	h.settle()
}

func TestAnImageShowsAsAPictureWithItsSize(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	for _, tc := range []struct {
		file string
		size string
	}{
		{"art/dot.png", "4 × 3 · "},
		{"art/photo.JPG", "8 × 6 · "},
		{"art/still.gif", "5 × 7 · "},
		{"art/icon.svg", "24 × 16 · "},
	} {
		h.open(tc.file)
		im := h.a.shownImage()
		if im == nil || im.src == nil {
			t.Fatalf("%s: no picture: %+v", tc.file, im)
		}
		found := false
		for _, s := range h.tt.Texts() {
			found = found || strings.HasPrefix(s, tc.size)
		}
		if !found {
			t.Errorf("%s: no %q in %v", tc.file, tc.size, h.tt.Texts())
		}
		if h.tt.HasText("Binary file") || h.tt.HasText("Code editor") {
			t.Errorf("%s: %v", tc.file, h.tt.Texts())
		}
		if h.tt.HasText("Find in File (⌘F)") || h.tt.HasText("Toggle Soft Wrap") {
			t.Errorf("%s has buttons for text: %v", tc.file, h.tt.Texts())
		}
		if !h.tt.HasText(tc.file) {
			t.Errorf("%s: nothing for assistive technology: %v", tc.file, h.tt.Texts())
		}
	}
}

func TestAPictureIsPaintedWhereItFits(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	writeFile(t, h.dir, "wide.png", pngFile(t, 8000, 100, color.RGBA{255, 0, 0, 255}))
	h.open("wide.png")
	h.tt.Advance(time.Second)
	h.settle()

	img := h.tt.Image()
	scale := float32(img.Bounds().Dx()) / 1280
	left := float32(h.a.sidebarW)
	red := func(x, y float32) bool {
		r, g, b, _ := img.At(int(x*scale), int(y*scale)).RGBA()
		return r>>8 > 200 && g>>8 < 60 && b>>8 < 60
	}
	// It takes the width of the pane, within the margin, and its height follows.
	midY := float32(headerHeight) + (800-float32(headerHeight))/2
	if !red(left+(1280-left)/2, midY) {
		t.Fatalf("the middle of the pane is not the picture")
	}
	if !red(left+imageMargin+4, midY) || !red(1280-imageMargin-4, midY) {
		t.Fatalf("the picture does not reach across the pane")
	}
	if red(left+6, midY) || red(left+(1280-left)/2, midY-40) {
		t.Fatalf("the picture is painted outside where it fits")
	}
}

func TestImageRect(t *testing.T) {
	box := ui.Rect{X: 100, Y: 50, W: 848, H: 648} // 800 x 600 inside the margin
	for _, tc := range []struct {
		name          string
		w, h, minSide float32
		wantW, wantH  float32
	}{
		{"its own size when it fits", 100, 50, 0, 100, 50},
		{"scaled down to the width", 1600, 100, 0, 800, 50},
		{"scaled down to the height", 300, 1200, 0, 150, 600},
		{"both, the tighter one winning", 1600, 1600, 0, 600, 600},
		{"a small SVG scaled up", 24, 12, 240, 240, 120},
		{"an SVG scaled up no further than the box", 10, 1, 2000, 800, 80},
		{"a large SVG stays its own size", 400, 300, 240, 400, 300},
	} {
		got := imageRect(box, tc.w, tc.h, tc.minSide)
		if got.W != tc.wantW || got.H != tc.wantH {
			t.Errorf("%s: %v x %v, want %v x %v", tc.name, got.W, got.H, tc.wantW, tc.wantH)
		}
		if cx, cy := got.X+got.W/2, got.Y+got.H/2; cx != box.X+box.W/2 || cy != box.Y+box.H/2 {
			t.Errorf("%s: not centered: %+v", tc.name, got)
		}
	}
	if r := imageRect(ui.Rect{W: 20, H: 20}, 10, 10, 0); r.W != 0 {
		t.Errorf("no room should show nothing: %+v", r)
	}
}

func TestAnImageThatCantBeShownSaysWhy(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))

	h.open("art/bad.png")
	if !h.tt.HasText("Can't show this image") || !h.tt.HasText("It may be damaged, or not really a PNG file.") {
		t.Fatalf("%v", h.tt.Texts())
	}

	// All it takes to refuse this one is its header.
	h.open("art/huge.png")
	if !h.tt.HasText("This image is too large to preview") || !h.tt.HasText("20000 × 20000 pixels. Images up to 64 megapixels can be shown.") {
		t.Fatalf("%v", h.tt.Texts())
	}

	writeFile(t, h.dir, "big.svg", "<svg>"+strings.Repeat(" ", maxSVGBytes)+"</svg>")
	h.open("big.svg")
	if !h.tt.HasText("This image is too large to preview") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestAnImageNameOnTextShowsTheText(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	h.open("art/lfs.png")
	if h.a.openFileImage() != nil {
		t.Fatalf("image: %+v", h.a.openFileImage())
	}
	if !h.a.openFileShowsText() || !strings.HasPrefix(h.a.codeState.Buffer.String(), "version https://git-lfs") {
		t.Fatalf("editor shows %q", h.a.codeState.Buffer.String())
	}
}

func TestAnSVGCanShowItsSource(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	a := h.a
	h.open("art/icon.svg")
	if a.shownImage() == nil || a.svgSource {
		t.Fatal("not showing the picture")
	}
	if err := h.tt.Click("Show SVG Source"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if a.shownImage() != nil || !a.openFileShowsText() {
		t.Fatal("not showing the source")
	}
	if a.codeState.Buffer.String() != smallSVG {
		t.Fatalf("editor shows %q", a.codeState.Buffer.String())
	}
	if !h.tt.HasText("Find in File (⌘F)") {
		t.Fatalf("%v", h.tt.Texts())
	}

	// The choice holds for the next SVG, and has no bearing on other images.
	writeFile(t, h.dir, "other.svg", smallSVG)
	h.open("other.svg")
	if a.shownImage() != nil {
		t.Fatal("the next SVG did not show its source")
	}
	h.open("art/dot.png")
	if im := a.shownImage(); im == nil || im.src == nil {
		t.Fatal("a PNG has no source to show")
	}
	if h.tt.HasText("Show SVG Source") {
		t.Fatalf("%v", h.tt.Texts())
	}

	// One that is broken still has text to read.
	h.open("art/bad.svg")
	if !a.openFileShowsText() || a.codeState.Buffer.String() != "<svg this is not" {
		t.Fatalf("editor shows %q", a.codeState.Buffer.String())
	}
	if err := h.tt.Click("Show SVG Source"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if !h.tt.HasText("Can't show this image") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestAnUnchangedImageIsNotDecodedAgain(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	a := h.a
	h.open("art/dot.png")
	before := a.openFileImage()
	seq := a.contentSeq

	// What a watch reports for anything in its directory.
	h.open("art/dot.png")
	if a.openFileImage() != before {
		t.Fatal("decoded again")
	}
	if a.contentSeq != seq {
		t.Fatal("shown as another file")
	}

	// A change that is the same size still counts.
	p := filepath.Join(h.dir, "art", "dot.png")
	writeFile(t, h.dir, "art/dot.png", pngFile(t, 4, 3, color.RGBA{0, 255, 0, 255}))
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	h.open("art/dot.png")
	if a.openFileImage() == before || a.openFileImage().src == nil {
		t.Fatal("kept the old picture")
	}
	if a.contentSeq != seq {
		t.Fatal("shown as another file")
	}
}

func TestAnImageGoneFromDiskSaysSo(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	writeFile(t, h.dir, "gone.png", pngFile(t, 2, 2, color.White))
	h.open("gone.png")
	if err := os.Remove(filepath.Join(h.dir, "gone.png")); err != nil {
		t.Fatal(err)
	}
	h.open("gone.png")
	if !h.tt.HasText("This file is no longer on disk") {
		t.Fatalf("%v", h.tt.Texts())
	}
}

func TestALoadNoLongerWantedDoesNotDecode(t *testing.T) {
	var c contentState
	seq := c.nextFile()
	c.nextFile()
	dir := t.TempDir()
	writeFile(t, dir, "a.png", pngFile(t, 2, 2, color.White))
	if im, current := c.loadFileImage(dir, "a.png", seq, 0, nil); current || im != nil {
		t.Fatalf("decoded for a load replaced by another: %+v", im)
	}
	if im, current := c.loadFileImage(dir, "a.png", c.newestFile.Load(), 0, nil); !current || im == nil || im.src == nil {
		t.Fatalf("the newest load: %+v, %v", im, current)
	}
}

func TestKeysGoThroughImagesAndOutOfThem(t *testing.T) {
	h := newHarness(t, newImagesRepo(t))
	a := h.a
	h.open("art/bad.png")
	h.press(ui.KeyDown) // bad.svg
	h.press(ui.KeyDown) // dot.png
	h.wantOpen("art/dot.png")
	if im := a.shownImage(); im == nil || im.src == nil {
		t.Fatalf("%+v", im)
	}
	h.open("a.go")
	if a.shownImage() != nil || !a.openFileShowsText() || a.codeState.Buffer.String() != "package a\n" {
		t.Fatalf("back on text: editor shows %q", a.codeState.Buffer.String())
	}
}

func TestDecodingLeavesNothingBehind(t *testing.T) {
	// Each of these has been a way for a decoder to fail: no slot may stay taken.
	h := newHarness(t, newImagesRepo(t))
	for _, f := range []string{"art/bad.png", "art/huge.png", "art/bad.svg", "art/dot.png"} {
		h.open(f)
	}
	select {
	case imageSlot <- struct{}{}:
		<-imageSlot
	default:
		t.Fatal("the slot for decoding stayed taken")
	}
}
