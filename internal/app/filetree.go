package app

import (
	"arbor/internal/git"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// The Files view is for looking at what is in a project, so its tree is what is
// on disk and Git only annotates it: what changed, what is ignored. The snapshot
// has what Git lists, without looking inside ignored directories. Every
// directory the tree shows open is also read from disk, which adds what Git
// cannot say: the contents of an ignored directory, of a submodule or of a link
// to a directory, and directories with nothing in them.

// ignoredIconAlpha is how much of an ignored row's icon shows while the pointer
// is elsewhere. Its text takes the theme's muted color, which stays readable on
// both themes; the icons are what make such a row recede, but they are also how
// a file's type is told at a glance, so they stay well visible.
const ignoredIconAlpha = 0.65

type treeNodeKind uint8

const (
	// nodeEntry is a file or a directory.
	nodeEntry treeNodeKind = iota
	// nodeHeader titles a section of the search results.
	nodeHeader
	// nodeNote is a line of text, as for a folder with nothing in it.
	nodeNote
)

type flatTreeNode struct {
	kind treeNodeKind
	// path identifies the row. It is the path of an entry.
	path string
	name string
	// dir is where a search result is, as it has no place in the tree.
	dir     string
	depth   int
	isDir   bool
	open    bool
	badge   string
	ignored bool
	// delayed keeps a note out of sight for a moment, so that what is usually
	// over in an instant does not flash.
	delayed bool
}

// dirListing is what reading a directory from disk found.
type dirListing struct {
	entries []git.DirEntry
	// note is what its row says when it shows nothing else.
	note string
}

func (l dirListing) equal(o dirListing) bool {
	return l.note == o.note && slices.Equal(l.entries, o.entries)
}

// treeKey is everything the rows of the tree are made from. It holds the
// snapshot itself, as those are replaced rather than changed in place.
type treeKey struct {
	repo        *git.RepoSnapshot
	version     uint64
	query       string
	onlyChanged bool
}

// fileTree is what the Files view knows beyond the snapshot. It is owned by the
// UI thread.
type fileTree struct {
	// dirs holds the directories read from disk, "" being the root.
	dirs map[string]dirListing
	// reads is the newest read on its way for each directory; only that one is
	// applied.
	reads   map[string]uint64
	readSeq uint64
	// version counts the changes to what the rows are made from, other than
	// those in treeKey.
	version uint64

	// nodes are the rows, built only when key changes: a view builds many
	// frames, and a tree can have many rows.
	nodes []flatTreeNode
	key   treeKey
	built bool

	// reveal is the file to scroll to once the tree shows it.
	reveal string
	search ignoredSearch
}

// ignoredSearch finds ignored files for the search box. Listing them takes one
// `git ls-files` however many there are, but there can be hundreds of thousands
// to look through for each key pressed, so both happen in the background.
type ignoredSearch struct {
	// index is every ignored file, nil until it is read for the first search.
	index []string
	// stale says the working tree changed since index was read.
	stale   bool
	loading bool
	// matches are the entries of index that hold matchedQuery, and wantQuery is
	// the query of the newest search on its way.
	matches      []string
	matchedQuery string
	wantQuery    string
	seq          uint64
}

// cancelWork forgets the background work under way, whose results will be
// dropped because the session it belongs to ended.
func (t *fileTree) cancelWork() {
	t.reads = nil
	t.search.loading = false
	t.search.wantQuery = ""
}

// treeNodes returns the rows of the tree for the search query, which is
// lowercase. It is called for every frame the view builds.
func (a *app) treeNodes(query string) []flatTreeNode {
	t := &a.tree
	key := treeKey{a.repo, t.version, query, a.onlyChanged}
	if !t.built || t.key != key {
		t.nodes = a.buildFlatTree(query)
		t.key, t.built = key, true
		a.scrollToReveal(query)
	}
	return t.nodes
}

func (a *app) buildFlatTree(query string) []flatTreeNode {
	if query != "" || a.onlyChanged {
		return a.buildFilteredTree(query)
	}
	var out []flatTreeNode
	a.appendTreeRows(&out, "", 0, false)
	return out
}

// appendTreeRows adds the rows of what is in dir, and in the directories open
// below it.
func (a *app) appendTreeRows(out *[]flatTreeNode, dir string, depth int, ignored bool) {
	listing, read := a.tree.dirs[dir]
	kids := a.repo.Children(dir, listing.entries)
	for _, k := range kids {
		kidIgnored := ignored || isListedIgnored(a.repo, k.Path)
		open := k.IsDir && a.openDirs[k.Path]
		*out = append(*out, flatTreeNode{
			path:    k.Path,
			name:    path.Base(k.Path),
			depth:   depth,
			isDir:   k.IsDir,
			open:    open,
			badge:   a.repo.StatusByRel[k.Path],
			ignored: kidIgnored,
		})
		if open {
			a.appendTreeRows(out, k.Path, depth+1, kidIgnored)
		}
	}
	if len(kids) == 0 && read && dir != "" && listing.note != "" {
		*out = append(*out, flatTreeNode{kind: nodeNote, path: "\x00note:" + dir, name: listing.note, depth: depth})
	}
}

func isListedIgnored(snap *git.RepoSnapshot, p string) bool {
	_, ok := snap.Ignored[p]
	return ok
}

// buildFilteredTree makes the rows for a search or for the changed files: a flat
// list, the project's own files first. An ignored file comes after them in a
// section of its own, or the 8 files of a project that hold "index" would drown
// among the 1,500 in its node_modules.
func (a *app) buildFilteredTree(query string) []flatTreeNode {
	var out []flatTreeNode
	for _, f := range a.repo.AllFiles {
		badge := a.repo.StatusByRel[f]
		if a.onlyChanged && badge == "" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(f), query) {
			continue
		}
		out = append(out, searchResult(f, badge, false))
	}
	if query == "" {
		return out
	}
	if !a.onlyChanged {
		s := &a.tree.search
		// Until the search for this query is back, the results of the one before
		// are narrowed down, so that the list does not empty with each key.
		exact := s.matchedQuery == query
		header := false
		for _, f := range s.matches {
			if !exact && !strings.Contains(strings.ToLower(f), query) {
				continue
			}
			if !header {
				out = append(out, flatTreeNode{kind: nodeHeader, path: "\x00ignored", name: "Ignored files"})
				header = true
			}
			out = append(out, searchResult(f, "", true))
		}
		if s.index == nil && s.loading {
			return append(out, flatTreeNode{kind: nodeNote, path: "\x00searching", name: "Searching ignored files…", delayed: true})
		}
	}
	if len(out) == 0 {
		out = append(out, flatTreeNode{kind: nodeNote, path: "\x00nomatch", name: "No matching files"})
	}
	return out
}

func searchResult(f, badge string, ignored bool) flatTreeNode {
	return flatTreeNode{path: f, name: path.Base(f), dir: treeParent(f), badge: badge, ignored: ignored}
}

// treeParent returns the directory of the path p in the tree, "" for the root.
func treeParent(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

// visibleDir is a directory whose contents the tree shows.
type visibleDir struct {
	path string
	// ownWatch says the repository's watch does not reach it: it skips ignored
	// directories, and does not follow links.
	ownWatch bool
}

// visibleDirs lists the directories whose contents the tree shows, starting
// with the root. A directory left open inside a closed one does not count.
func (a *app) visibleDirs() []visibleDir {
	out := []visibleDir{{}}
	var walk func(dir string, ignored, link bool)
	walk = func(dir string, ignored, link bool) {
		for _, k := range a.repo.Children(dir, a.tree.dirs[dir].entries) {
			if !k.IsDir || !a.openDirs[k.Path] {
				continue
			}
			kidIgnored, kidLink := ignored || isListedIgnored(a.repo, k.Path), link || k.Link
			out = append(out, visibleDir{k.Path, kidIgnored || kidLink})
			walk(k.Path, kidIgnored, kidLink)
		}
	}
	walk("", false, false)
	return out
}

// syncTree brings what the tree depends on up to date with what it shows:
// directories that came into view are read, and watched when the repository's
// own watch does not reach them. It does nothing that is not needed, so it is
// cheap to call after anything that may have changed that.
func (a *app) syncTree() {
	if a.repo == nil {
		return
	}
	t := &a.tree
	var missing []string
	watch := make(map[string]bool)
	for _, d := range a.visibleDirs() {
		_, read := t.dirs[d.path]
		_, reading := t.reads[d.path]
		if !read && !reading {
			missing = append(missing, d.path)
		}
		if d.ownWatch {
			watch[d.path] = true
		}
	}
	// A file opened from the search stays followed even when no row shows it.
	if f := a.selectedFile; f != "" && a.repo.IsIgnored(f) {
		if dir := treeParent(f); dir != "" && a.repo.IsIgnored(dir) {
			watch[dir] = true
		}
	}
	a.syncDirWatches(watch)
	a.readDirs(missing)
}

// readDirs reads directories from disk in the background.
func (a *app) readDirs(dirs []string) {
	if a.repo == nil || len(dirs) == 0 {
		return
	}
	t := &a.tree
	if t.reads == nil {
		t.reads = make(map[string]uint64)
	}
	t.readSeq++
	seq, root := t.readSeq, a.repo.Root
	for _, d := range dirs {
		t.reads[d] = seq
	}
	a.goLoad(func() func() {
		read := make([]dirListing, len(dirs))
		for i, d := range dirs {
			read[i] = readDirListing(root, d)
		}
		return func() { a.finishReadDirs(root, seq, dirs, read) }
	})
}

func readDirListing(root, dir string) dirListing {
	entries, err := git.ReadDir(root, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dirListing{} // gone; what Git still lists of it shows that
	case err != nil:
		return dirListing{note: "Can't read this folder"}
	case len(entries) == 0:
		return dirListing{note: "Empty folder"}
	}
	return dirListing{entries: entries}
}

func (a *app) finishReadDirs(root string, seq uint64, dirs []string, read []dirListing) {
	if a.repo == nil || a.repo.Root != root {
		return
	}
	t := &a.tree
	if t.dirs == nil {
		t.dirs = make(map[string]dirListing)
	}
	changed := false
	for i, d := range dirs {
		if t.reads[d] != seq {
			continue // a newer read of it is on its way
		}
		delete(t.reads, d)
		if old, ok := t.dirs[d]; !ok || !old.equal(read[i]) {
			t.dirs[d] = read[i]
			changed = true
		}
	}
	if changed {
		t.version++
		a.syncTree() // it may have shown directories that were left open
	}
}

// treeRepoChanged is a new snapshot on screen: what is on disk may have changed
// with it.
func (a *app) treeRepoChanged() {
	a.tree.search.stale = true
	dirs := a.visibleDirs()
	paths := make([]string, len(dirs))
	for i, d := range dirs {
		paths[i] = d.path
	}
	a.readDirs(paths)
	a.syncTree()
}

// dirChanged is what a watch of a single directory reports. Only that directory
// and the file open in it are read again: nothing in it concerns Git, as the
// repository's own watch would have noticed otherwise.
func (a *app) dirChanged(dir string) {
	a.tree.search.stale = true
	a.readDirs([]string{dir})
	if f := a.selectedFile; f != "" && treeParent(f) == dir {
		a.selectExplorerFile(f)
	}
}

// toggleDir opens or closes a directory of the tree. What is in it is read again
// each time it opens, as nothing may have been following it.
func (a *app) toggleDir(p string) {
	a.openDirs[p] = !a.openDirs[p]
	a.tree.version++
	if a.openDirs[p] {
		a.readDirs([]string{p})
	}
	a.syncTree()
}

// revealInTree opens the directories above rel, so that the tree shows the file
// once the filter that found it is gone, and scrolls to it then.
func (a *app) revealInTree(rel string) {
	for d := treeParent(rel); d != ""; d = treeParent(d) {
		a.openDirs[d] = true
	}
	a.tree.reveal = rel
	a.tree.version++
	a.syncTree()
}

// scrollToReveal scrolls to the file to reveal when the rows have just been
// built and hold it. Directories still being read may yet bring it into view;
// when none are, it is not to be had.
func (a *app) scrollToReveal(query string) {
	t := &a.tree
	if t.reveal == "" || query != "" || a.onlyChanged {
		return
	}
	for i := range t.nodes {
		if t.nodes[i].path == t.reveal {
			a.explorerList.ScrollIntoView(i)
			t.reveal = ""
			return
		}
	}
	if len(t.reads) == 0 {
		t.reveal = ""
	}
}

// syncIgnoredSearch starts what the search for query still needs: the list of
// ignored files, and the search of that list. It runs for every frame the view
// builds, and starts nothing while the work for this query is done or under way.
func (a *app) syncIgnoredSearch(query string) {
	if query == "" || a.onlyChanged || a.repo == nil {
		return
	}
	s := &a.tree.search
	if (s.index == nil || s.stale) && !s.loading {
		s.loading, s.stale = true, false
		root := a.repo.Root
		a.goLoad(func() func() {
			files, err := git.ListIgnoredFiles(root)
			return func() {
				s.loading = false
				if err != nil && s.index != nil {
					return // keep what was found before
				}
				if files == nil {
					files = []string{}
				}
				s.index = files
				s.wantQuery = "" // search the new list
				a.tree.version++
			}
		})
	}
	if s.index == nil || s.wantQuery == query {
		return
	}
	s.seq++
	seq, index := s.seq, s.index
	s.wantQuery = query
	a.goLoad(func() func() {
		var matches []string
		for _, f := range index {
			if strings.Contains(strings.ToLower(f), query) {
				matches = append(matches, f)
			}
		}
		return func() {
			if s.seq != seq {
				return
			}
			s.matches, s.matchedQuery = matches, query
			a.tree.version++
		}
	})
}

// renderTreeNote shows a row that is not an entry of the tree.
func renderTreeNote(c *ui.Context, t *ui.Theme, n flatTreeNode) {
	row := ui.Row(c.Key("tree:" + n.path)).AlignItems(ui.Center)
	switch n.kind {
	case nodeHeader:
		row.Height(26).Padding(6, 8, 0, 6)
		row.Children(func() {
			ui.Text(c, n.name).FontSize(11).FontWeight(500).TextColor(t.TextMuted).SingleLine()
		})
		return
	default:
		// Lined up with the names of the entries beside it.
		row.Height(24).Padding(0, 8, 0, float32(6+n.depth*14+32))
	}
	if n.delayed {
		k := row.Keyframes("appear", []ui.Keyframe{
			{At: 0, Value: 0},
			{At: 200 * time.Millisecond, Value: 0},
			{At: 320 * time.Millisecond, Value: 1, Ease: ui.EaseOut},
		}, ui.KeyframeOptions{})
		row.Opacity(k)
	}
	row.Children(func() {
		ui.Text(c, n.name).FontSize(12).TextColor(t.TextMuted).SingleLine()
	})
}

// renderIgnoredTag marks the file on screen as one Git ignores.
func renderIgnoredTag(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Padding(1, 5).Radius(3.5).AlignItems(ui.Center).Shrink(0).
		Background(t.TextMuted.Alpha(0.13)).Children(func() {
		ui.Text(c, "Ignored").FontSize(10.5).FontWeight(500).TextColor(t.TextMuted).SingleLine()
	})
}

// renderPreviewNotice fills the pane in place of the text of a file that has
// none to show, saying why.
func renderPreviewNotice(c *ui.Context, t *ui.Theme, p git.FilePreview) {
	title, detail := previewNotice(p)
	ui.Column(c.Key("preview-notice")).Grow(1).FillWidth().Center().Padding(24).Gap(6).Children(func() {
		ui.Icon(c, IconFile).Size(28, 28).TextColor(t.TextMuted.Alpha(0.6))
		ui.Text(c, title).FontSize(13).FontWeight(500).Margin(6, 0, 0, 0)
		if detail != "" {
			ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted)
		}
	})
}

// previewNotice words what opening a file found, when it found no text.
func previewNotice(p git.FilePreview) (title, detail string) {
	switch p.Kind {
	case git.PreviewEmpty:
		return "This file is empty", ""
	case git.PreviewBinary:
		return "Binary file", formatFileSize(p.Size) + ". It can't be shown as text."
	case git.PreviewMissing:
		return "This file is no longer on disk", ""
	case git.PreviewDirectory:
		return "This is a folder", "Open it in the sidebar to see what's inside."
	case git.PreviewUnreadable:
		return "Can't read this file", p.Reason
	}
	return "", ""
}

func formatFileSize(n int64) string {
	const kb, mb, gb = 1 << 10, 1 << 20, 1 << 30
	switch {
	case n < kb:
		return fmt.Sprintf("%d bytes", n)
	case n < mb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	case n < gb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/gb)
}
