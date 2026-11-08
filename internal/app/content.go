package app

import (
	"arbor/internal/git"
	"arbor/perf"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui/editor"
)

// loadedFile is what the Files view shows.
type loadedFile struct {
	ok      bool
	path    string
	preview git.FilePreview
	head    string
	// image is the picture of an image file, nil for any other file.
	image *fileImage
}

// loadedDiff is what the diff view shows; key says which diff it is.
type loadedDiff struct {
	ok         bool
	key        string
	old, newer string
}

// contentState tracks what the editors show and the loads that fill them.
// The texts are kept so that a reload that finds the same ones leaves the
// buffers alone: Buffer.Reset forgets the selection and the undo history, and
// a diff that is reset loses the lines the user expanded.
type contentState struct {
	// Each kind of load has a sequence number; only the newest is applied.
	fileSeq, diffSeq, detailSeq uint64
	// newestFile is fileSeq for the loads to read in the background, as one that
	// waits for its turn to decode an image can see that it is no longer wanted.
	newestFile atomic.Uint64
	// place is the part of the file on screen to read, the start of a file for
	// one just opened, and partMove where to go in it once it has loaded.
	place    git.PreviewPlace
	partMove partMove

	file loadedFile
	diff loadedDiff
}

// openExplorerFile loads the text of relPath, and of the same file in HEAD
// for the change markers, for the Files view. The selection has changed
// already; the text arrives from the background.
func (a *app) openExplorerFile(relPath string) {
	root := a.repo.Root
	listed := inTree(a.repo, relPath)
	c := &a.live.content
	seq := c.nextFile()
	var known *fileImage
	if c.file.ok && c.file.path == relPath {
		known = c.file.image
	}
	place := c.place
	a.goLoad(func() func() {
		preview := git.ReadFilePreviewAt(root, relPath, place)
		var image *fileImage
		if isImagePath(relPath) && (preview.Kind == git.PreviewText || preview.Kind == git.PreviewBinary) {
			var current bool
			if image, current = c.loadFileImage(root, relPath, seq, preview.Kind, known); !current {
				return nil
			}
		}
		// Only text has lines to mark, and Git has no other version of a file
		// it doesn't list. A part of a file has nothing to compare with the whole
		// of it, and asking Git for all of it could be gigabytes.
		head := ""
		if listed && preview.Kind == git.PreviewText && !preview.Partial() && changeMarksAffordable(root, relPath, preview.Size) {
			head = git.ReadTextAtRef(root, "HEAD", relPath)
		}
		return func() {
			if c.fileSeq != seq || a.selectedFile != relPath {
				return
			}
			a.showFile(relPath, preview, head, image)
			a.pending.file.publish(a, seq, perf.PhaseLoadFile, perf.ResultOK)
		}
	})
}

// nextFile begins the next load of a file, which replaces any before it.
func (c *contentState) nextFile() uint64 {
	c.fileSeq++
	c.newestFile.Store(c.fileSeq)
	return c.fileSeq
}

// showFile puts the text of a file in the Files view. When it is the file
// already shown and nothing in it changed, the editor is not touched; when
// its text changed, the scroll position is kept.
func (a *app) showFile(rel string, preview git.FilePreview, head string, image *fileImage) {
	content := &a.live.content
	move := content.partMove
	content.partMove = partStay
	// From here on it reads these lines again: a file growing at the end, or the
	// end of it being asked for, is no reason for them to move.
	if preview.Kind == git.PreviewText {
		content.place = git.PreviewPlace{Offset: preview.Start}
	}
	f := &content.file
	same := f.ok && f.path == rel
	if same && f.preview == preview && f.head == head && f.image == image {
		return
	}
	st := a.codeState
	scrollY := st.ScrollY()
	if !same {
		a.contentSeq++
		st.Buffer.SetLanguage(languageFor(rel))
	}
	if !same || f.preview.Text != preview.Text {
		st.Buffer.Reset(preview.Text)
	}
	if !same || f.head != head {
		if head != "" {
			st.DiffBase = editor.NewBuffer(head)
		} else {
			st.DiffBase = nil
		}
	}
	switch {
	case move == partTop:
		st.Run(editor.MoveDocStart)
		a.contentSeq++
	case move == partBottom:
		st.Run(editor.MoveDocEnd)
		a.contentSeq++
	case same:
		st.SetScrollY(scrollY)
	}
	*f = loadedFile{ok: true, path: rel, preview: preview, head: head, image: image}
}

// clearExplorerFile closes the file of the Files view.
func (a *app) clearExplorerFile() {
	a.selectedFile = ""
	a.fileOffGit = false
	a.live.content.nextFile()
	if a.live.content.file.ok {
		a.codeState.Buffer.Reset("")
		a.codeState.DiffBase = nil
		a.contentSeq++
	}
	a.live.content.file = loadedFile{}
}

// reselectExplorerFile reads the open file again after the repository
// changed. When the file is gone (another branch has no such file, it was
// deleted) another one is opened in its place, so that the view never shows
// a file that is not there. Only Git's list is looked at, so a file it does not
// list stays open: the view says when one is gone, and what a build generates
// comes back.
func (a *app) reselectExplorerFile(snap *git.RepoSnapshot) {
	listed := inTree(snap, a.selectedFile)
	if listed {
		a.fileOffGit = false
	}
	if listed || a.fileOffGit || snap.IsIgnored(a.selectedFile) {
		a.selectExplorerFile(a.selectedFile)
		return
	}
	if next := nearestFile(snap, a.selectedFile); next != "" {
		a.selectExplorerFile(next)
		return
	}
	a.clearExplorerFile()
}

func inTree(snap *git.RepoSnapshot, rel string) bool {
	if _, isDir := snap.TreeDirs[rel]; isDir {
		return false
	}
	i := sort.SearchStrings(snap.AllFiles, rel)
	return i < len(snap.AllFiles) && snap.AllFiles[i] == rel
}

// nearestFile picks the file to open when rel is gone: its neighbour in the
// same directory, else in the closest directory above that has files.
func nearestFile(snap *git.RepoSnapshot, rel string) string {
	files := snap.AllFiles
	if len(files) == 0 {
		return ""
	}
	i := sort.SearchStrings(files, rel)
	for dir := path.Dir(rel); ; dir = path.Dir(dir) {
		prefix := dir + "/"
		if dir == "." || dir == "/" {
			prefix = ""
		}
		if f := closestWithPrefix(files, i, prefix); f != "" {
			return f
		}
		if prefix == "" {
			return ""
		}
	}
}

// closestWithPrefix returns the file with prefix nearest to position i of the
// sorted files, looking at the next one before the previous one.
func closestWithPrefix(files []string, i int, prefix string) string {
	for lo, hi := i-1, i; lo >= 0 || hi < len(files); lo, hi = lo-1, hi+1 {
		if hi < len(files) && strings.HasPrefix(files[hi], prefix) {
			return files[hi]
		}
		if lo >= 0 && strings.HasPrefix(files[lo], prefix) {
			return files[lo]
		}
	}
	return ""
}

// openWorkingDiff loads the two sides of a changed file for the diff view.
func (a *app) openWorkingDiff(st git.FileStatus) {
	root := a.repo.Root
	key := fmt.Sprintf("work:%v:%s", st.Staged, st.Path)
	c := &a.live.content
	c.diffSeq++
	seq := c.diffSeq
	a.goLoad(func() func() {
		oldText, newText := git.LoadWorkingDiffTexts(root, st)
		return func() {
			if c.diffSeq == seq {
				a.showDiff(key, st.Path, oldText, newText)
				a.pending.diff.publish(a, seq, perf.PhaseGitShow, perf.ResultOK)
			}
		}
	})
}

// openCommitDiff loads the two sides of a file of a commit.
func (a *app) openCommitDiff(hash string, cf git.CommitFile) {
	root := a.repo.Root
	key := "commit:" + hash + ":" + cf.Path
	c := &a.live.content
	c.diffSeq++
	seq := c.diffSeq
	a.goLoad(func() func() {
		oldText, newText := git.LoadCommitFileDiffTexts(root, hash, cf)
		return func() {
			if c.diffSeq == seq {
				a.showDiff(key, cf.Path, oldText, newText)
				a.pending.diff.publish(a, seq, perf.PhaseGitShow, perf.ResultOK)
			}
		}
	})
}

// showDiff puts a diff in the diff view. When it is the diff already shown,
// only the sides whose text changed are reset, which keeps the scroll
// position and the lines the user expanded; when nothing changed the viewer
// is not touched.
func (a *app) showDiff(key, path, oldText, newText string) {
	d := &a.live.content.diff
	same := d.ok && d.key == key
	if same && d.old == oldText && d.newer == newText {
		return
	}
	if !same {
		a.contentSeq++
		lang := languageFor(path)
		a.diffState.Old.SetLanguage(lang)
		a.diffState.New.SetLanguage(lang)
	}
	if !same || d.old != oldText {
		a.diffState.Old.Reset(oldText)
	}
	if !same || d.newer != newText {
		a.diffState.New.Reset(newText)
	}
	*d = loadedDiff{ok: true, key: key, old: oldText, newer: newText}
}

// expandCommit expands the commit at idx of the list and loads its files. A
// commit that is expanded stays so: unlike toggleCommit it never collapses.
func (a *app) expandCommit(idx int) {
	if a.repo == nil || idx < 0 || idx >= len(a.repo.Commits) {
		return
	}
	cm := a.repo.Commits[idx]
	if a.expandedCommit == cm.Hash {
		return
	}
	a.expandedCommit = cm.Hash
	a.selectedCommit = &cm
	a.commitFiles = nil
	a.openCommit(cm.Hash)
}

// openCommit loads the files of a commit and opens the first one.
func (a *app) openCommit(hash string) {
	root := a.repo.Root
	c := &a.live.content
	c.detailSeq++
	seq := c.detailSeq
	a.goLoad(func() func() {
		_, files, err := git.LoadCommitDetails(root, hash)
		return func() {
			if c.detailSeq != seq || a.expandedCommit != hash {
				return
			}
			if err != nil {
				a.commitFiles = nil
				a.pending.detail.publish(a, seq, perf.PhaseGitShow, perf.ResultError)
				return
			}
			a.commitFiles = files
			if len(files) > 0 {
				a.selectCommitFile(0)
			}
			a.pending.detail.publish(a, seq, perf.PhaseGitShow, perf.ResultOK)
		}
	})
}

// expandDefaultCommit expands the newest commit when nothing is expanded. It
// only ever expands: a refresh must not collapse, or replace, a commit the
// user opened.
func (a *app) expandDefaultCommit() {
	if a.expandedCommit != "" {
		return
	}
	a.expandCommit(0)
}

// syncCommitSelection forgets a commit that is no longer in the list (reset,
// amended, another repository) with the files and diff that belonged to it.
func (a *app) syncCommitSelection(snap *git.RepoSnapshot) {
	gone := func(hash string) bool {
		for i := range snap.Commits {
			if snap.Commits[i].Hash == hash {
				return false
			}
		}
		return true
	}
	if a.expandedCommit != "" && gone(a.expandedCommit) {
		a.expandedCommit = ""
		a.commitFiles = nil
		a.selectedCommitF = -1
		a.live.content.detailSeq++
	}
	if a.selectedCommit != nil && gone(a.selectedCommit.Hash) {
		a.selectedCommit = nil
		a.commitFiles = nil
		a.selectedCommitF = -1
		if a.diffKind == diffCommitFile {
			a.diffKind = diffNone
			a.diffTitle = ""
			a.diffBadge = ""
			a.live.content.diffSeq++
		}
	}
}
