package app

import "github.com/egoist/mygo/ui"

// The keyboard in the file tree. Its rows are not chosen by ui.ListState's
// Selected: that fills the row with the accent color and only skips sticky
// headers, while the tree has rows that are not entries in the middle of it, and
// a directory can be where the keys are while a file stays open.
//
// With the tree focused, moving onto a file opens it, so that going through the
// files is reading them. With the search field focused the keys only move through
// the results and Enter opens one, as what is open should not change while looking
// for something.

// treeCursorEdgeAlpha is how much of the accent color the edge of the row under
// the cursor takes.
const treeCursorEdgeAlpha = 0.55

// treeFiltered reports whether the rows are a flat list rather than the tree.
func (a *app) treeFiltered() bool {
	return a.tree.key.query != "" || a.onlyChanged
}

// settleCursor keeps the cursor on the rows just built. A new search starts at
// its first result, and leaving one goes back to the file that is open. In the
// tree, a cursor without a row stays: the directories above it may still be on
// their way from disk.
func (a *app) settleCursor(query, lastQuery string) {
	t := &a.tree
	if query != lastQuery {
		t.cursor = ""
	}
	t.firstEntry = ""
	found := false
	for i := range t.nodes {
		n := &t.nodes[i]
		if n.kind != nodeEntry {
			continue
		}
		if t.firstEntry == "" {
			t.firstEntry = n.path
		}
		if n.path == t.cursor {
			found = true
			break
		}
	}
	switch {
	case query == "" && !a.onlyChanged:
		if t.cursor == "" {
			t.cursor = a.selectedFile
		}
	case !found:
		t.cursor = ""
	}
}

// shownCursor returns the path of the row the keys act on, which is the first
// one in a list that has none yet.
func (t *fileTree) shownCursor() string {
	if t.cursor == "" && (t.key.query != "" || t.key.onlyChanged) {
		return t.firstEntry
	}
	return t.cursor
}

// cursorRow returns the row the keys move from, -1 for none. When the cursor has
// no row, because something closed or deleted what it was on, the keys go on from
// the nearest directory above it that is showing, or else from the file open.
func (a *app) cursorRow() int {
	nodes := a.tree.nodes
	for p := a.tree.shownCursor(); p != ""; p = treeParent(p) {
		if i := treeRowOf(nodes, p); i >= 0 {
			return i
		}
	}
	if a.selectedFile != "" {
		return treeRowOf(nodes, a.selectedFile)
	}
	return -1
}

func treeRowOf(nodes []flatTreeNode, p string) int {
	for i := range nodes {
		if nodes[i].path == p {
			return i
		}
	}
	return -1
}

// nextEntry returns the first file or directory after row i in the direction d,
// -1 for none.
func nextEntry(nodes []flatTreeNode, i, d int) int {
	for i += d; i >= 0 && i < len(nodes); i += d {
		if nodes[i].kind == nodeEntry {
			return i
		}
	}
	return -1
}

// putCursor moves the cursor to row i and brings it into view. With open, a file
// there opens.
func (a *app) putCursor(i int, open bool) {
	n := &a.tree.nodes[i]
	a.tree.cursor = n.path
	a.explorerList.ScrollIntoView(i)
	if open && !n.isDir && n.path != a.selectedFile {
		a.openTreeFile(n.path)
	}
}

// openTreeFile opens a file of the rows. One found in a flat list also gets its
// place in the tree, for when the list is gone.
func (a *app) openTreeFile(p string) {
	if a.treeFiltered() {
		a.revealInTree(p)
	}
	a.openFile(p)
}

// moveCursor moves the cursor by a number of entries, and no further than the
// last one. From nowhere it goes to the first.
func (a *app) moveCursor(by int, open bool) {
	nodes := a.tree.nodes
	from := a.cursorRow()
	to := from
	if from < 0 {
		to = nextEntry(nodes, -1, 1)
	} else {
		d := 1
		if by < 0 {
			d, by = -1, -by
		}
		for ; by > 0; by-- {
			j := nextEntry(nodes, to, d)
			if j < 0 {
				break
			}
			to = j
		}
	}
	if to >= 0 {
		a.putCursor(to, open)
	}
}

// cursorInto is the Right key: it opens a directory, and from an open one goes to
// what is in it.
func (a *app) cursorInto() {
	i := a.cursorRow()
	if i < 0 {
		return
	}
	nodes := a.tree.nodes
	n := nodes[i]
	switch {
	case !n.isDir:
	case !n.open:
		a.tree.cursor = n.path
		a.toggleDir(n.path)
	case i+1 < len(nodes) && nodes[i+1].kind == nodeEntry:
		a.putCursor(i+1, true)
	}
}

// cursorOut is the Left key: it closes a directory, and from anything else goes
// to the directory it is in.
func (a *app) cursorOut() {
	i := a.cursorRow()
	if i < 0 {
		return
	}
	nodes := a.tree.nodes
	n := nodes[i]
	if n.isDir && n.open {
		a.tree.cursor = n.path
		a.toggleDir(n.path)
		return
	}
	// A directory's row is always above what is in it.
	if parent := treeParent(n.path); parent != "" {
		for j := i - 1; j >= 0; j-- {
			if nodes[j].path == parent {
				a.putCursor(j, false)
				return
			}
		}
	}
}

// activateCursor is the Enter key: a file opens, and a directory opens or closes.
func (a *app) activateCursor() {
	i := a.cursorRow()
	if i < 0 {
		return
	}
	n := a.tree.nodes[i]
	a.tree.cursor = n.path
	if n.isDir {
		a.toggleDir(n.path)
		return
	}
	a.openTreeFile(n.path)
}

// declareTreeKeys declares the keys of the tree while it has the keyboard focus.
func (a *app) declareTreeKeys(c *ui.Context) {
	l := &a.explorerList
	l.OnShortcut(c, 0, ui.KeyDown, func() { a.moveCursor(1, true) })
	l.OnShortcut(c, 0, ui.KeyUp, func() { a.moveCursor(-1, true) })
	l.OnShortcut(c, 0, ui.KeyRight, a.cursorInto)
	l.OnShortcut(c, 0, ui.KeyLeft, a.cursorOut)
	l.OnShortcut(c, 0, ui.KeyEnter, a.activateCursor)
	l.OnShortcut(c, 0, ui.KeyHome, func() {
		if i := nextEntry(a.tree.nodes, -1, 1); i >= 0 {
			a.putCursor(i, true)
		}
	})
	l.OnShortcut(c, 0, ui.KeyEnd, func() {
		if i := nextEntry(a.tree.nodes, len(a.tree.nodes), -1); i >= 0 {
			a.putCursor(i, true)
		}
	})
}

// onSearchKey is the search field's OnTextKey. It only records what to move: the
// frame that follows applies it to the rows as they then are. Left and Right stay
// the field's own, as they move its caret.
func (a *app) onSearchKey(k ui.TextKey) bool {
	if k.Mods != 0 {
		return false
	}
	t := &a.tree
	switch k.Key {
	case ui.KeyDown:
		t.searchMoves++
	case ui.KeyUp:
		t.searchMoves--
	case ui.KeyEnter:
		t.searchEnter = true
	case ui.KeyEscape:
		k.SetText("")
		t.focusAsked = false // back to the tree
	default:
		return false
	}
	return true
}

// applySearchKeys does what the search field's keys recorded.
func (a *app) applySearchKeys(query string) {
	t := &a.tree
	if t.searchMoves == 0 && !t.searchEnter {
		return
	}
	moves, enter := t.searchMoves, t.searchEnter
	t.searchMoves, t.searchEnter = 0, false
	a.treeNodes(query)
	if moves != 0 {
		a.moveCursor(moves, false)
	}
	if enter {
		a.activateCursor()
	}
}
