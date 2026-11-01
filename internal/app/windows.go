package app

import (
	"arbor/perf"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Windows. Each window is an app of its own: its repository, its Git queue and
// watchers, its selection and its loads. What the windows share is the user's
// settings, the avatar cache (both safe from any goroutine), and the registry
// below: which windows are open, in which slot, showing which repository.
//
// The registry belongs to the UI thread, which is the main thread: menu
// clicks, window events and the views all run on it, and so does everything
// that background work hands back through Window.Update. Nothing here may be
// called from another goroutine; the dialog that picks a folder comes back
// through its done callback, on the UI thread.

const (
	// Windows open at this size, and a slot's position and size are
	// remembered under its StateKey.
	windowWidth  = 1280
	windowHeight = 820
	// cascadeStep is how far each slot's first position is from the last's,
	// and cascadeWrap how many windows cascade before they start over.
	cascadeStep = 28
	cascadeWrap = 8
	// dockRecents is how many recent repositories the Dock menu lists.
	dockRecents = 5
)

// hostWindow is the part of a native window that an app and the registry use.
// *mygo.Window implements it (through nativeWindow); tests use a stand-in, as
// they cannot create a real one without a running application.
type hostWindow interface {
	// Update runs fn on the UI thread and redraws. It runs fn even when the
	// window has closed meanwhile: callers guard against that.
	Update(fn func())
	Invalidate()
	// Show brings the window to the front and gives it the focus.
	Show()
	Close()
	SetTitle(title string)
	OnFocus(fn func()) (off func())
	OnClosed(fn func()) (off func())
	IsDestroyed() bool
}

// windowSpec says which window to create.
type windowSpec struct {
	// Slot is the window's place among the open ones, from 1.
	Slot int
	// StateKey names the slot for the saved position and size of its window.
	StateKey string
}

// windowHost creates windows whose content is view.
type windowHost interface {
	NewWindow(spec windowSpec, view func(c *ui.Context)) hostWindow
}

// folderPicker asks the user for a folder and calls done with it on the UI
// thread, or with "" when they cancel.
type folderPicker func(parent hostWindow, done func(dir string))

// slotKey is the StateKey of a slot: the first keeps the key the only window
// used before there were several, so that its saved position carries over.
func slotKey(slot int) string {
	if slot <= 1 {
		return "arbor-main"
	}
	return fmt.Sprintf("arbor-window-%d", slot)
}

// lowestFreeSlot is the lowest slot, from 1, that taken does not say is
// taken: a closed window's slot is used again by the next.
func lowestFreeSlot(taken func(slot int) bool) int {
	slot := 1
	for taken(slot) {
		slot++
	}
	return slot
}

// cascadeOrigin is where the window of a slot first opens: the first is
// centered (0, 0 asks for that), and each next is a step down and right of the
// one before, starting over after cascadeWrap. work is the work area of the
// display, in the window system's coordinates. A slot that has been used
// opens where it was left: the saved position overrides this one.
func cascadeOrigin(slot int, work mygo.Rectangle, w, h int) (x, y int) {
	if slot <= 1 || work.Width <= 0 || work.Height <= 0 {
		return 0, 0
	}
	off := ((slot-2)%cascadeWrap + 1) * cascadeStep
	x = work.X + max((work.Width-w)/2, 0) + off
	y = work.Y + max((work.Height-h)/2, 0) + off
	return x, y
}

// ============================================================================
// The real windows
// ============================================================================

// nativeWindow is a *mygo.Window as a hostWindow.
type nativeWindow struct{ *mygo.Window }

// Show raises the window, out of the Dock if it was minimized.
func (w nativeWindow) Show() {
	if w.IsMinimized() {
		w.Restore()
	}
	w.Window.Show()
}

// mygoHost creates mygo windows. Call it on the UI thread, once the
// application is ready.
type mygoHost struct{}

func (mygoHost) NewWindow(spec windowSpec, view func(c *ui.Context)) hostWindow {
	x, y := cascadeOrigin(spec.Slot, mygo.Screen.PrimaryDisplay().WorkArea, windowWidth, windowHeight)
	return nativeWindow{mygo.NewWindow(mygo.WindowOptions{
		Title:          "Arbor",
		Width:          windowWidth,
		Height:         windowHeight,
		X:              x,
		Y:              y,
		MinWidth:       820,
		MinHeight:      520,
		StateKey:       spec.StateKey,
		TitleBarStyle:  mygo.TitleBarHiddenInset,
		TitleBarHeight: headerHeight,
		Vibrancy:       mygo.VibrancySidebar,
		Content:        ui.View(view),
	})}
}

// dialogPickFolder asks for a folder with the system's dialog, as a sheet of
// parent when it is a window of ours. The dialog blocks, so it runs off the UI
// thread, and done is called back on it.
func dialogPickFolder(parent hostWindow, done func(dir string)) {
	var owner *mygo.Window
	if nw, ok := parent.(nativeWindow); ok {
		owner = nw.Window
	}
	go func() {
		dir := ""
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:    owner,
			Title:     "Open Git Repository",
			Directory: true,
		})
		if err == nil && len(paths) > 0 {
			dir = paths[0]
		}
		mygo.RunOnMain(func() { done(dir) })
	}()
}

// ============================================================================
// Which repository a window shows
// ============================================================================

// repoKey names the repository dir is in, for telling whether two requests
// are for the same one: the real path of the working tree it belongs to. A
// linked worktree is a working tree of its own, so it is another repository
// here; a directory that is in no working tree is its own key.
func repoKey(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	for cur := abs; ; {
		// A directory in the main working tree, a file in a linked one.
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		cur = parent
	}
}

// sameRepo reports whether a and b are the same repository: the same key, or
// directories that are one on disk although they are spelled differently
// (case on a case-insensitive file system).
func sameRepo(a, b string) bool {
	ka, kb := repoKey(a), repoKey(b)
	if ka == "" || kb == "" {
		return false
	}
	if ka == kb {
		return true
	}
	fa, err1 := os.Stat(ka)
	fb, err2 := os.Stat(kb)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// inRepo reports whether dir is in a working tree, and the root of it.
func inRepo(dir string) (string, bool) {
	key := repoKey(dir)
	if key == "" {
		return "", false
	}
	_, err := os.Lstat(filepath.Join(key, ".git"))
	return key, err == nil
}

// repoName is what a repository is called in the interface.
func repoName(dir string) string {
	if name := filepath.Base(dir); name != "." && name != string(filepath.Separator) {
		return name
	}
	return dir
}

// tildePath shortens a path in the user's home directory to ~/…, for display.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return p
}

// ============================================================================
// The registry
// ============================================================================

// managedWindow is a window the registry has open.
type managedWindow struct {
	slot int
	app  *app
	win  hostWindow
}

// windowRegistry knows the windows that are open. See the rules at the top.
type windowRegistry struct {
	host  windowHost
	store *sessionStore
	// pick asks for a folder; it is the system's dialog unless a test says so.
	pick folderPicker
	// newApp makes the app of a window.
	newApp func() *app
	// changed is called when the windows or the recent repositories changed,
	// for the menus that list them.
	changed func()

	wins []*managedWindow
	// rev counts the changes of the windows and the recent repositories, for
	// the lists that show them.
	rev uint64
	// started is set once the windows of the launch are open: before it, the
	// application is not to react to being activated.
	started bool
	// quitting is set once the application is quitting, when windows close
	// but the session is to stay as the last window left it.
	quitting bool
	// restoring is set while the windows of the last session open, which
	// leave the saved session as it was until they all have.
	restoring bool
	// quitsWithLastWindow is set where closing the last window quits the
	// application (everywhere but macOS): that window is then part of the
	// session still.
	quitsWithLastWindow bool
}

// newWindowRegistry makes the registry of an application whose windows host
// creates and whose session store keeps; without a store the session lives in
// memory only.
func newWindowRegistry(host windowHost, store *sessionStore) *windowRegistry {
	if store == nil {
		store = newSessionStore("")
	}
	r := &windowRegistry{host: host, store: store, pick: dialogPickFolder}
	r.newApp = func() *app { return newBareApp("") }
	return r
}

// Windows returns the open windows, in the order they opened.
func (r *windowRegistry) Windows() []*managedWindow { return slices.Clone(r.wins) }

// slotTaken reports whether a window is open in slot.
func (r *windowRegistry) slotTaken(slot int) bool {
	return slices.ContainsFunc(r.wins, func(m *managedWindow) bool { return m.slot == slot })
}

// find returns the window that shows the repository at dir, or is opening it.
func (r *windowRegistry) find(dir string) *managedWindow {
	for _, m := range r.wins {
		if m.app.repoDir != "" && sameRepo(m.app.repoDir, dir) {
			return m
		}
	}
	return nil
}

// managedFor returns the window of an app, or of a native window.
func (r *windowRegistry) managedFor(a *app) *managedWindow {
	for _, m := range r.wins {
		if m.app == a {
			return m
		}
	}
	return nil
}

func (r *windowRegistry) managedForNative(w *mygo.Window) *managedWindow {
	for _, m := range r.wins {
		if nw, ok := m.win.(nativeWindow); ok && nw.Window == w {
			return m
		}
	}
	return nil
}

// focus brings m to the front.
func (r *windowRegistry) focus(m *managedWindow) { m.win.Show() }

// NewWindow opens a window with no repository: it shows the recent ones.
func (r *windowRegistry) NewWindow() *managedWindow { return r.open(0, "", true) }

// OpenWindow shows the repository at dir in a window of its own: the one that
// shows it already, brought to the front, if there is one.
func (r *windowRegistry) OpenWindow(dir string) *managedWindow {
	if m := r.find(dir); m != nil {
		r.focus(m)
		return m
	}
	return r.open(0, dir, true)
}

// OpenHere shows the repository at dir in the window of a, unless another
// window shows it already, which is brought to the front instead. The window
// of a keeps what it shows if it shows dir.
func (r *windowRegistry) OpenHere(a *app, dir string) {
	if m := r.find(dir); m != nil {
		if m.app != a {
			r.focus(m)
		}
		return
	}
	a.openRepo(dir)
}

// open creates the window of slot (the lowest free one for 0), and loads dir
// in it when it is not empty. measure makes the operation one the user waits
// for, up to the first paint of the window.
func (r *windowRegistry) open(slot int, dir string, measure bool) *managedWindow {
	if slot <= 0 || r.slotTaken(slot) {
		slot = lowestFreeSlot(r.slotTaken)
	}
	span := perf.Span{}
	if measure {
		span = perf.Begin(perf.OpNewWindow)
	}
	a := r.newApp()
	a.startupFirstFrame()
	a.reg, a.slot = r, slot
	if dir != "" {
		a.loading = dir
	}
	// The window's first frame shows its content or the loading state, and
	// ends the span.
	a.paints.Arm(span, perf.ResultOK)
	win := r.host.NewWindow(windowSpec{Slot: slot, StateKey: slotKey(slot)}, a.view)
	if win == nil || win.IsDestroyed() {
		return nil
	}
	m := &managedWindow{slot: slot, app: a, win: win}
	r.wins = append(r.wins, m)
	a.win = win
	a.attachWindow()
	if dir != "" {
		a.openRepo(dir)
	}
	r.sync()
	return m
}

// Restore opens the windows of a launch: the repository named on the command
// line, else the windows of the last session each in its own slot, else the
// repository the process was started in, else a window with nothing in it.
// cwd is where the process started, for the last of these.
func (r *windowRegistry) Restore(cli, cwd string) {
	defer func() {
		r.started = true
		r.sync()
	}()
	if cli != "" {
		r.open(0, cli, false)
		return
	}
	r.restoring = true
	for _, w := range r.store.Get().Windows {
		r.open(w.Slot, w.Repo, false)
	}
	r.restoring = false
	if len(r.wins) > 0 {
		return
	}
	if root, ok := inRepo(cwd); ok && cwd != "" && cwd != string(filepath.Separator) {
		r.open(0, root, false)
		return
	}
	r.open(0, "", false)
}

// Activated is the application becoming active again, as when its Dock icon
// is clicked: with no window it opens one, and with windows that are all
// minimized it brings one back.
func (r *windowRegistry) Activated(hasVisibleWindows bool) {
	if !r.started || r.quitting || hasVisibleWindows {
		return
	}
	if n := len(r.wins); n > 0 {
		r.focus(r.wins[n-1])
		return
	}
	r.NewWindow()
}

// windowClosed forgets the window of a, which has closed.
func (r *windowRegistry) windowClosed(a *app) {
	m := r.managedFor(a)
	if m == nil {
		return
	}
	if len(r.wins) == 1 && r.quitsWithLastWindow && !r.quitting {
		// The last window going quits the application: the session stays as it
		// was with this window open.
		r.saveSession()
		r.quitting = true
	}
	r.wins = slices.DeleteFunc(r.wins, func(w *managedWindow) bool { return w == m })
	r.sync()
}

// BeginQuit is the application starting to quit: the windows about to close
// are part of the session, and stay in it.
func (r *windowRegistry) BeginQuit() {
	r.saveSession()
	r.quitting = true
	r.store.Flush()
}

// Flush waits for the session to be written.
func (r *windowRegistry) Flush() { r.store.Flush() }

// repoOpened is the window of a showing the repository at root: it is now
// the one used last.
func (r *windowRegistry) repoOpened(a *app, root string) {
	r.store.Update(func(s *session) { s.addRecent(root) })
	r.sync()
}

// forget drops dir from the recent repositories, as when it is gone.
func (r *windowRegistry) forget(dir string) {
	r.store.Update(func(s *session) { s.forget(dir) })
	r.sync()
}

// sync brings the session, and what lists the windows, up to date.
func (r *windowRegistry) sync() {
	r.rev++
	r.saveSession()
	if r.changed != nil && !r.quitting {
		r.changed()
	}
}

// saveSession writes the open windows into the session, unless the application
// is quitting.
func (r *windowRegistry) saveSession() {
	if r.quitting || r.restoring {
		return
	}
	// A window that is showing a repository or opening one; not one that
	// could not open its folder, which the next launch would only fail to
	// open again.
	var ws []sessionWindow
	for _, m := range r.wins {
		if a := m.app; a.repoDir != "" && (a.repo != nil || a.loading != "") {
			ws = append(ws, sessionWindow{Slot: m.slot, Repo: a.repoDir})
		}
	}
	r.store.Update(func(s *session) { s.Windows = ws })
}

// Recents returns the recent repositories, the one used last first.
func (r *windowRegistry) Recents() []string { return r.store.Get().Recents }

// openFolder asks for a folder and opens it: in a window of its own, or in
// from's window when from is not nil. A dialog that was cancelled does
// nothing.
func (r *windowRegistry) openFolder(from *app, newWindow bool) {
	var parent hostWindow
	if from != nil {
		parent = from.win
	}
	r.pick(parent, func(dir string) {
		if dir == "" || (from != nil && from.closed) {
			return
		}
		switch {
		case from == nil || newWindow:
			r.OpenWindow(dir)
		default:
			r.OpenHere(from, dir)
		}
	})
}

// repoRows are the rows of the repository menu of a: the repository it shows,
// then the recent ones, each marked when a window shows it already.
func (r *windowRegistry) repoRows(a *app) []pickerRow {
	var rows []pickerRow
	seen := map[string]bool{}
	add := func(dir string, current bool) {
		key := filepath.Clean(dir)
		if dir == "" || seen[key] {
			return
		}
		seen[key] = true
		row := pickerRow{
			Key:     key,
			Label:   repoName(key),
			Hint:    tildePath(filepath.Dir(key)),
			Tip:     tildePath(key),
			Current: current,
		}
		if !current {
			if m := r.find(key); m != nil && m.app != a {
				row.Tag, row.TagTip = "open", "Open in another window; choosing it shows that window"
			}
		}
		rows = append(rows, row)
	}
	if a.repo != nil {
		add(a.repo.Root, true)
	}
	for _, dir := range r.Recents() {
		add(dir, false)
	}
	return rows
}

// ============================================================================
// Menus
// ============================================================================

// fileMenuItems are the items of the File menu that open windows and
// repositories; the standard Close Window follows them.
func (r *windowRegistry) fileMenuItems() []*mygo.MenuItem {
	appOf := func(w *mygo.Window) *app {
		if m := r.managedForNative(w); m != nil {
			return m.app
		}
		return nil
	}
	items := []*mygo.MenuItem{
		{ID: "file.newWindow", Label: "New Window", Accelerator: "CmdOrCtrl+N",
			Click: func(*mygo.MenuItem, *mygo.Window) { r.NewWindow() }},
		{ID: "file.open", Label: "Open…", Accelerator: "CmdOrCtrl+O",
			Click: func(_ *mygo.MenuItem, w *mygo.Window) { r.openFolder(appOf(w), false) }},
		{ID: "file.openNewWindow", Label: "Open in New Window…", Accelerator: "CmdOrCtrl+Shift+O",
			Click: func(_ *mygo.MenuItem, w *mygo.Window) { r.openFolder(appOf(w), true) }},
		mygo.Separator(),
		{Role: mygo.RoleClose},
	}
	if runtime.GOOS != "darwin" {
		items = append(items, mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleQuit})
	}
	return items
}

// dockMenu is the menu of the Dock icon (macOS), above the standard items: a
// new window, and the recent repositories.
func (r *windowRegistry) dockMenu() *mygo.Menu {
	items := []*mygo.MenuItem{{ID: "dock.newWindow", Label: "New Window",
		Click: func(*mygo.MenuItem, *mygo.Window) { r.NewWindow() }}}
	recents := r.Recents()
	if len(recents) > dockRecents {
		recents = recents[:dockRecents]
	}
	if len(recents) > 0 {
		items = append(items, mygo.Separator())
	}
	for _, dir := range recents {
		items = append(items, &mygo.MenuItem{
			Label:   repoName(dir),
			ToolTip: tildePath(dir),
			Click:   func(*mygo.MenuItem, *mygo.Window) { r.OpenWindow(dir) },
		})
	}
	return mygo.NewMenu(items)
}

// ============================================================================
// What an app does with the registry
// ============================================================================

// later runs fn on the UI thread between frames. Whatever changes what a view
// reads as it builds (the repository it shows, the windows) is done so: from
// inside a build it would pull the state away from the code that is still
// building.
func (a *app) later(fn func()) {
	a.queued = append(a.queued, fn)
	if a.win != nil {
		a.win.Invalidate()
	}
}

// runPending runs what later queued. The view calls it before it reads any
// state.
func (a *app) runPending() {
	for len(a.queued) > 0 {
		fn := a.queued[0]
		a.queued = a.queued[1:]
		if !a.closed {
			fn()
		}
	}
}

// onUI runs fn on the UI thread from another goroutine, unless the window has
// closed by then. Without a window it runs at once.
func (a *app) onUI(fn func()) {
	post := a.live.post
	if post == nil {
		fn()
		return
	}
	post(func() {
		if !a.closed {
			fn()
		}
	})
}

// requestOpen shows the repository at dir in this window, or in a window of
// its own, or in the one that shows it already. The windows change between
// frames.
func (a *app) requestOpen(dir string, newWindow bool) {
	if dir == "" || a.closed {
		return
	}
	a.later(func() {
		switch {
		case a.reg == nil && !newWindow:
			a.openRepo(dir)
		case a.reg == nil:
		case newWindow:
			a.reg.OpenWindow(dir)
		default:
			a.reg.OpenHere(a, dir)
		}
	})
}

// openRepoDialog asks for a folder, and opens it in this window or in a window
// of its own.
func (a *app) openRepoDialog(newWindow bool) {
	if a.reg == nil || a.closed {
		return
	}
	a.later(func() { a.reg.openFolder(a, newWindow) })
}
