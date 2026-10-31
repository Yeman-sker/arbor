package main

import (
	"context"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
)

const (
	// watchDebounce is how long the watchers wait for changes to stop before
	// reporting them, and watchMaxDelay the longest they wait while more keep
	// coming (a build writing files for a minute still refreshes every 2 s).
	watchDebounce = 300 * time.Millisecond
	watchMaxDelay = 2 * time.Second
)

// hintSource is what the watcher reads changes from: a *mygo.FileWatcher, or
// a stand-in in tests, which cannot start a real one without a running app.
type hintSource interface {
	Next(ctx context.Context) ([]mygo.FileEvent, error)
	Close() error
}

// watchStarter starts a watch on the directory at dir.
type watchStarter func(ctx context.Context, dir string, opts mygo.FileWatchOptions) (hintSource, error)

func startFileWatch(ctx context.Context, dir string, opts mygo.FileWatchOptions) (hintSource, error) {
	w, err := mygo.WatchFiles(ctx, dir, opts)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// repoWatch follows one repository for changes made outside Arbor: its
// working tree, and the git directories where HEAD, the refs and the index
// live. It belongs to the app that made it and ends with its context.
type repoWatch struct {
	root   string
	ctx    context.Context
	cancel context.CancelFunc
	// ready is closed once the watches are started, or have failed to.
	ready chan struct{}
	// sources counts the watches that started.
	sources atomic.Int32

	mu                sync.Mutex
	gitDir, commonDir string
	// fp is the fingerprint of the git directories at the last full refresh
	// or event, so that the lock files and logs Git writes while it works do
	// not count as changes.
	fp string

	// ignored holds the directories (relative, slash separated) that Git
	// ignores as a whole. Only read by the watch.
	ignored atomic.Pointer[map[string]bool]
}

func newRepoWatch(root string) *repoWatch {
	ctx, cancel := context.WithCancel(context.Background())
	return &repoWatch{root: root, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
}

// stopWatch ends the watch of the app's repository.
func (a *app) stopWatch() {
	if w := a.live.w; w != nil {
		w.cancel()
		a.live.w = nil
	}
}

// attachWindow connects the app to its window, once it exists: results of
// background work are published through it, and it is watched for focus and
// for closing.
func (a *app) attachWindow() {
	a.endSession()
	a.live.post = a.win.Update
	if a.repo != nil {
		a.live.lastFull = time.Now() // newApp has just read the repository
	}
	a.win.OnFocus(a.onFocus)
	a.win.OnClosed(a.windowClosed)
	a.ensureWatch()
}

// windowClosed is the window going away. Everything that runs for it stops:
// the watchers, the queued Git work, the loads and the jobs of the commit
// cards; what is already on its way back through Window.Update finds the app
// closed and is dropped, as Update runs it whether the window is there or not.
func (a *app) windowClosed() {
	if a.closed {
		return
	}
	a.closed = true
	a.endSession()
	a.cards.stop()
	a.queued = nil
	a.branchPick.open, a.repoPick.open = false, false
	if a.reg != nil {
		a.reg.windowClosed(a)
	}
}

// ensureWatch watches the repository on screen, unless it already does. It
// is cheap to call after every refresh.
func (a *app) ensureWatch() {
	if a.live.post == nil || a.repo == nil {
		return
	}
	root := a.repo.Root
	if w := a.live.w; w != nil && w.root == root {
		return
	}
	a.stopWatch()
	w := newRepoWatch(root)
	a.live.w = w
	start := a.live.startWatch
	if start == nil {
		start = startFileWatch
	}
	pub := a.publisher()
	go a.runWatch(w, pub, start)
}

// runWatch starts the watches of w and reads their changes until w ends. It
// runs on its own goroutine: enrolling a big tree takes a while. A watch that
// cannot start leaves the repository without that watch; the refresh on focus
// still catches up.
func (a *app) runWatch(w *repoWatch, pub func(func()), start watchStarter) {
	defer close(w.ready)
	gitDir, commonDir, err := gitDirs(w.root)
	if err != nil {
		return
	}
	ignored := IgnoredDirs(w.root)
	w.ignored.Store(&ignored)
	w.mu.Lock()
	w.gitDir, w.commonDir = gitDir, commonDir
	w.fp = gitFingerprint(gitDir, commonDir)
	w.mu.Unlock()

	tree := mygo.FileWatchOptions{
		HintsOnly: true,
		Recursive: true,
		Ignore:    w.ignoreWorktree,
		Debounce:  watchDebounce,
		MaxDelay:  watchMaxDelay,
	}
	gitOpts := tree
	gitOpts.Ignore = ignoreGitDirTree

	if src, err := start(w.ctx, w.root, tree); err == nil {
		w.sources.Add(1)
		go w.pump(src, func(evs []mygo.FileEvent) bool { return worktreeChanged(evs) }, func() { a.externalChange(scopeStatus) }, pub)
	}
	dirs := []string{gitDir}
	if commonDir != gitDir {
		dirs = append(dirs, commonDir)
	}
	for _, dir := range dirs {
		if src, err := start(w.ctx, dir, gitOpts); err == nil {
			w.sources.Add(1)
			go w.pump(src, func([]mygo.FileEvent) bool { return w.gitDirChanged() }, func() { a.externalChange(scopeFull) }, pub)
		}
	}
}

// pump reads the batches of src until it ends. For each one that relevant
// accepts it asks the UI thread to run act.
func (w *repoWatch) pump(src hintSource, relevant func([]mygo.FileEvent) bool, act func(), pub func(func())) {
	defer src.Close()
	for {
		evs, err := src.Next(w.ctx)
		if err != nil {
			return
		}
		if relevant(evs) {
			pub(act)
		}
	}
}

// worktreeChanged reports whether a batch from the working tree watch is
// worth a refresh: it says something changed, and not just that the directory
// itself went away or that Git touched its own directory.
func worktreeChanged(evs []mygo.FileEvent) bool {
	for _, ev := range evs {
		if ev.Op == mygo.FileRemove && ev.Path == "." {
			continue
		}
		if ev.Path == ".git" || strings.HasPrefix(ev.Path, ".git/") {
			continue
		}
		return true
	}
	return false
}

// ignoreWorktree is the Ignore of the working tree watch: the .git directory
// (or file, in a linked worktree) and the directories Git ignores.
func (w *repoWatch) ignoreWorktree(p string, isDir bool) bool {
	if path.Base(p) == ".git" {
		return true
	}
	if m := w.ignored.Load(); m != nil {
		return isDir && (*m)[p]
	}
	return false
}

// ignoreGitDirTree is the Ignore of the git directory watches. Of the
// directories only refs/ holds anything that matters; objects/, logs/, hooks/
// and the rest change all the time while Git works.
func ignoreGitDirTree(p string, isDir bool) bool {
	if !isDir {
		return false
	}
	top, _, _ := strings.Cut(p, "/")
	return top != "refs"
}

// gitDirChanged reports whether HEAD, a ref or the index changed since the
// last time it was asked, or a refresh read them. A directory watch can only
// say that something in a directory changed, which includes lock files Git
// creates and removes while it works; comparing the files themselves tells
// whether anything real happened.
func (w *repoWatch) gitDirChanged() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gitDir == "" {
		return false
	}
	fp := gitFingerprint(w.gitDir, w.commonDir)
	if fp == w.fp {
		return false
	}
	w.fp = fp
	return true
}

// recordFingerprint notes the state of the git directories as a full refresh
// is about to read it, so that the events our own operations cause are not
// taken for changes from outside. It is safe on a nil watch.
func (w *repoWatch) recordFingerprint() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gitDir != "" {
		w.fp = gitFingerprint(w.gitDir, w.commonDir)
	}
}

// refreshIgnored reads the ignored directories again, as .gitignore may
// have changed. It is safe on a nil watch.
func (w *repoWatch) refreshIgnored() {
	if w == nil || w.ctx.Err() != nil {
		return
	}
	if dirs := IgnoredDirs(w.root); dirs != nil {
		w.ignored.Store(&dirs)
	}
}
