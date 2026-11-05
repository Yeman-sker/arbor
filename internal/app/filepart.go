package app

import (
	"arbor/internal/git"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// A file too large for the editor to hold shows a part at a time. The most useful
// part of a log is often its end, so that has to be one click away, and what is
// showing is said outside the text, which is the file's own.

const (
	// alwaysMarkedBytes is the size up to which a file has its changes marked
	// however many there are: comparing it takes a moment at worst.
	alwaysMarkedBytes = 512 << 10
	// maxMarkWork limits what marking the changes of a larger file may take, in
	// changed lines times bytes. The editor compares on the UI thread, in time
	// that grows with both: this is 75 to 150 ms, measured from 1 MB to 15 MB.
	maxMarkWork = 200 << 20
)

// changeMarksAffordable returns whether a file can have its changes marked without
// the window stopping to compare them. It runs off the UI thread, as it asks Git.
func changeMarksAffordable(root, rel string, size int64) bool {
	if size <= alwaysMarkedBytes {
		return true
	}
	changed, ok := git.ChangedLines(root, rel)
	return ok && int64(changed)*size <= maxMarkWork
}

// partMove says where to go in a part of a file once it has loaded.
type partMove uint8

const (
	partStay partMove = iota
	partTop
	partBottom
)

// showFilePart loads another part of the file on screen. The one on screen stays
// until it is there.
func (a *app) showFilePart(place git.PreviewPlace, move partMove) {
	c := &a.live.content
	c.place, c.partMove = place, move
	a.openFile(a.selectedFile)
}

// filePart returns what was read of the text on screen, when it is a part of a file.
func (a *app) filePart() (git.FilePreview, bool) {
	f := &a.live.content.file
	if !f.ok || f.preview.Kind != git.PreviewText || !f.preview.Partial() {
		return git.FilePreview{}, false
	}
	return f.preview, true
}

// fileLineNumbers returns whether the editor numbers its lines. A part further into
// a file would count from 1, and the number of lines before it is not known without
// reading them all: no number says less than a wrong one.
func (a *app) fileLineNumbers() editor.LineNumbers {
	if p, ok := a.filePart(); ok && p.Start > 0 {
		return editor.LineNumbersOff
	}
	return editor.LineNumbersOn
}

// filePartText says how much of a file shows.
func filePartText(p git.FilePreview) string {
	total, shown := formatFileSize(p.Size), formatFileSize(p.End-p.Start)
	switch {
	case p.Start == 0:
		return fmt.Sprintf("Showing the first %s of %s", shown, total)
	case p.End >= p.Size:
		return fmt.Sprintf("Showing the last %s of %s", shown, total)
	}
	return fmt.Sprintf("Showing %s of %s, starting at %.1f%%", shown, total, float64(p.Start)/float64(p.Size)*100)
}

// renderFilePartBar says how much of the file shows and moves through the rest, for
// a file that doesn't show whole.
func (a *app) renderFilePartBar(c *ui.Context, t *ui.Theme) {
	p, ok := a.filePart()
	if !ok {
		return
	}
	ui.Row(c.Key("file-part")).Height(32).Shrink(0).FillWidth().Padding(0, 10, 0, 14).Gap(2).
		AlignItems(ui.Center).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, filePartText(p)).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0).Clip()
		ui.Spacer(c)
		first, last := p.Start == 0, p.End >= p.Size
		if iconBtnState(c, "part-start", IconChevronsUp, "Go to Start of File", false, first) {
			a.showFilePart(git.PreviewPlace{}, partTop)
		}
		if iconBtnState(c, "part-previous", IconChevronUp, "Previous Part", false, first) {
			a.showFilePart(git.PreviewPlace{Kind: git.PlaceUpTo, Offset: p.Start}, partBottom)
		}
		if iconBtnState(c, "part-next", IconChevronDown, "Next Part", false, last) {
			a.showFilePart(git.PreviewPlace{Offset: p.End}, partTop)
		}
		if iconBtnState(c, "part-end", IconChevronsDown, "Go to End of File", false, last) {
			a.showFilePart(git.PreviewPlace{Kind: git.PlaceEnd}, partBottom)
		}
	})
}
