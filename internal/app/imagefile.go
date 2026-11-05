package app

import (
	"arbor/internal/git"
	"bytes"
	"fmt"
	"image"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Image files in the Files view: what a picture looks like says more than that
// it is a binary file. They are decoded in the background, and an image only
// shows once it is all there.

const (
	// maxImagePixels is the largest picture that opens: a 48 megapixel photo is
	// fine, and past this its pixels would be gigabytes to hold on to.
	maxImagePixels = 64_000_000
	// maxImageBytes keeps from reading a file only to find out it holds too much.
	maxImageBytes = 256 << 20
	// maxSVGBytes is the largest SVG that opens. An SVG is drawn on the UI thread
	// at each size it shows at, which takes about 100 ms for each megabyte of a
	// chart or a map.
	maxSVGBytes = 1 << 20
	// minSVGSide is what a small SVG is scaled up to if there is room: an icon's 16
	// pixels are only a suggestion, and too small to look at.
	minSVGSide = 240
	// imageMargin is the room around a picture.
	imageMargin = 24
	// imageCheckSize is the side of a square of what shows through a transparent
	// picture.
	imageCheckSize = 12
)

// fileImage is what opening an image file found: a picture, or why there is none.
type fileImage struct {
	// src is nil when the picture can't be shown, and title and detail say why.
	src           ui.ImageSource
	title, detail string
	width, height int
	svg           bool
	// size and modTime tell whether the file changed: a picture is too much work
	// to decode again each time something in its directory does.
	size    int64
	modTime time.Time
}

// summary says how large the picture is, at its own size.
func (im *fileImage) summary() string {
	if im.src == nil {
		return formatFileSize(im.size)
	}
	return fmt.Sprintf("%d × %d · %s", im.width, im.height, formatFileSize(im.size))
}

// isImagePath reports whether a path is that of a file the view shows as a
// picture. A name is enough to go by: a file that is not what it says says so.
func isImagePath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}

func isSVGPath(p string) bool {
	return strings.EqualFold(path.Ext(p), ".svg")
}

// imageSlot is held to decode an image, so that going through a directory of
// photos holds one in memory rather than one for each key pressed. It is for the
// whole program: windows side by side do not have more memory than one.
var imageSlot = make(chan struct{}, 1)

// loadFileImage reads the image the load seq is for, unless a newer load came
// while it waited for its turn, in which case it reports that with current false.
// known is the image already on screen for the same file. There is no image when
// the file has none to show and it has text, as a pointer of Git LFS does.
func (c *contentState) loadFileImage(root, rel string, seq uint64, kind git.PreviewKind, known *fileImage) (im *fileImage, current bool) {
	imageSlot <- struct{}{}
	defer func() { <-imageSlot }()
	if c.newestFile.Load() != seq {
		return nil, false
	}
	im = readFileImage(root, rel, known)
	if im != nil && im.src == nil && kind == git.PreviewText && !im.svg {
		return nil, true
	}
	return im, true
}

// readFileImage reads and decodes an image file. It returns nil when the file
// can't be read at all, which ReadFilePreview has words for.
func readFileImage(root, rel string, known *fileImage) *fileImage {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	if known != nil && known.size == info.Size() && known.modTime.Equal(info.ModTime()) {
		return known
	}
	im := &fileImage{svg: isSVGPath(rel), size: info.Size(), modTime: info.ModTime()}
	limit := int64(maxImageBytes)
	if im.svg {
		limit = maxSVGBytes
	}
	if im.size > limit {
		im.title = "This image is too large to preview"
		im.detail = fmt.Sprintf("%s. Images up to %s can be shown.", formatFileSize(im.size), formatFileSize(limit))
		return im
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	decodeImage(im, data)
	if im.src == nil && im.title == "" {
		ext := strings.TrimPrefix(strings.ToUpper(path.Ext(rel)), ".")
		im.title = "Can't show this image"
		im.detail = fmt.Sprintf("It may be damaged, or not really a %s file.", ext)
	}
	return im
}

// decodeImage fills in the picture from the bytes of its file, or leaves it out.
// These bytes can be anything, and nothing in them should take the app down.
func decodeImage(im *fileImage, data []byte) {
	defer func() {
		if recover() != nil {
			im.src = nil
		}
	}()
	if im.svg {
		s, err := ui.ParseSVG(data)
		if err != nil {
			return
		}
		w, h := s.Size()
		if w <= 0 || h <= 0 {
			return
		}
		im.src, im.width, im.height = s, int(w+0.5), int(h+0.5)
		return
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return
	}
	// Know the size before making room for all of its pixels.
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		im.title = "This image is too large to preview"
		im.detail = fmt.Sprintf("%d × %d pixels. Images up to %d megapixels can be shown.", cfg.Width, cfg.Height, maxImagePixels/1_000_000)
		return
	}
	bm, err := ui.DecodeBitmap(data)
	if err != nil {
		return
	}
	w, h := bm.Size()
	im.src, im.width, im.height = bm, w, h
}

// openFileImage returns the image of the file on screen, nil when it has none. It
// goes by what is loaded, which stays until the next file has loaded.
func (a *app) openFileImage() *fileImage {
	f := &a.live.content.file
	if !f.ok {
		return nil
	}
	return f.image
}

// shownImage returns the image to show in place of the text of the file on
// screen, nil to show its text or to say why there is none.
func (a *app) shownImage() *fileImage {
	im := a.openFileImage()
	if im != nil && im.svg && a.svgSource && a.live.content.file.preview.Kind == git.PreviewText {
		return nil
	}
	return im
}

// imageRect is where a picture of the size (w, h) goes in the box r: centered, and
// no larger than its own size unless it is under minSide, up to the box.
func imageRect(r ui.Rect, w, h, minSide float32) ui.Rect {
	box := ui.Rect{X: r.X + imageMargin, Y: r.Y + imageMargin, W: r.W - 2*imageMargin, H: r.H - 2*imageMargin}
	if box.W <= 0 || box.H <= 0 || w <= 0 || h <= 0 {
		return ui.Rect{}
	}
	scale := max(1, minSide/max(w, h))
	scale = min(scale, box.W/w, box.H/h)
	dw, dh := w*scale, h*scale
	return ui.Rect{X: box.X + (box.W-dw)/2, Y: box.Y + (box.H-dh)/2, W: dw, H: dh}
}

// renderFileImage shows a picture, or why there is none.
func renderFileImage(c *ui.Context, im *fileImage, name string) {
	if im.src == nil {
		renderNotice(c, c.Theme(), im.title, im.detail)
		return
	}
	var w, h, minSide float32
	switch s := im.src.(type) {
	case *ui.Bitmap:
		iw, ih := s.Size()
		w, h = float32(iw), float32(ih)
	case *ui.SVG:
		w, h = s.Size()
		minSide = minSVGSide
	}
	src, dark := im.src, c.Theme().Dark
	ui.Box(c.Key("image-file")).Grow(1).FillWidth().MinHeight(0).Clip().
		Role(ui.RoleImage).Label(name).
		Draw(func(p *ui.Painter, r ui.Rect) {
			dst := imageRect(r, w, h, minSide)
			if dst.W <= 0 || dst.H <= 0 {
				return
			}
			paintImageBacking(p, dst, dark)
			p.Image(src, dst, ui.Contain)
		})
}

// paintImageBacking paints the squares that show what of a picture is transparent.
// They are the same for all pictures, not the theme's, as a picture's colors are
// its own: an icon in black has to be seen on the dark theme as well.
func paintImageBacking(p *ui.Painter, r ui.Rect, dark bool) {
	a, b := ui.RGB(0xFF, 0xFF, 0xFF), ui.RGB(0xE4, 0xE4, 0xE4)
	if dark {
		a, b = ui.RGB(0x3C, 0x3C, 0x3C), ui.RGB(0x4C, 0x4C, 0x4C)
	}
	p.Fill(r, a, 0)
	p.Clip(r, 0, func() {
		rows, cols := int(r.H/imageCheckSize)+1, int(r.W/imageCheckSize)+1
		for row := range rows {
			for col := row % 2; col < cols; col += 2 {
				p.Fill(ui.Rect{X: r.X + float32(col*imageCheckSize), Y: r.Y + float32(row*imageCheckSize), W: imageCheckSize, H: imageCheckSize}, b, 0)
			}
		}
	})
}
