package app

import (
	"arbor/internal/git"
	"arbor/perf"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/editor"
)

// loadedFile is what the Files view shows.
type loadedFile struct {
	ok         bool
	path       string
	text, head string
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

	file loadedFile
	diff loadedDiff
}

// openExplorerFile loads the text of relPath, and of the same file in HEAD
// for the change markers, for the Files view. The selection has changed
// already; the text arrives from the background.
func (a *app) openExplorerFile(relPath string) {
	root := a.repo.Root
	c := &a.live.content
	c.fileSeq++
	seq := c.fileSeq
	a.goLoad(func() func() {
		text := git.ReadWorktreeFile(root, relPath)
		head := git.ReadFileAtRef(root, "HEAD", relPath)
		return func() {
			if c.fileSeq != seq || a.selectedFile != relPath {
				return
			}
			a.showFile(relPath, text, head)
			a.pending.file.publish(a, seq, perf.PhaseLoadFile, perf.ResultOK)
		}
	})
}

// showFile puts the text of a file in the Files view. When it is the file
// already shown and nothing in it changed, the editor is not touched; when
// its text changed, the scroll position is kept.
func (a *app) showFile(rel, text, head string) {
	f := &a.live.content.file
	same := f.ok && f.path == rel
	if same && f.text == text && f.head == head {
		return
	}
	st := a.codeState
	scrollY := st.ScrollY()
	if !same {
		a.contentSeq++
		st.Buffer.SetLanguage(editor.LanguageFor(rel))
	}
	if !same || f.text != text {
		st.Buffer.Reset(text)
	}
	if !same || f.head != head {
		if head != "" {
			st.DiffBase = editor.NewBuffer(head)
		} else {
			st.DiffBase = nil
		}
	}
	if same {
		st.SetScrollY(scrollY)
	}
	*f = loadedFile{ok: true, path: rel, text: text, head: head}
}

// clearExplorerFile closes the file of the Files view.
func (a *app) clearExplorerFile() {
	a.selectedFile = ""
	a.live.content.fileSeq++
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
// a file that is not there.
func (a *app) reselectExplorerFile(snap *git.RepoSnapshot) {
	if inTree(snap, a.selectedFile) {
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
		lang := editor.LanguageFor(path)
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
