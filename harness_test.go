package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// fakeUI stands in for the UI thread: Window.Update queues, and the test
// goroutine runs the queue when it says so.
type fakeUI struct {
	mu  sync.Mutex
	fns []func()
}

func (u *fakeUI) post(fn func()) {
	u.mu.Lock()
	u.fns = append(u.fns, fn)
	u.mu.Unlock()
}

// drain runs what was posted, including what that posts in turn, and returns
// how many functions ran.
func (u *fakeUI) drain() int {
	n := 0
	for {
		u.mu.Lock()
		if len(u.fns) == 0 {
			u.mu.Unlock()
			return n
		}
		fn := u.fns[0]
		u.fns = u.fns[1:]
		u.mu.Unlock()
		fn()
		n++
	}
}

// harness is an app on a temp repository with a fake UI thread.
type harness struct {
	t    *testing.T
	dir  string
	a    *app
	fake *fakeUI
	tt   *ui.Tester
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newTestRepo makes a repository on branch main with a commit holding files.
func newTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@arbor.dev")
	mustGit(t, dir, "config", "user.name", "Arbor Tester")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	for rel, content := range files {
		writeFile(t, dir, rel, content)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "initial")
	return dir
}

// newHarness opens the repository at dir in an app whose background work
// publishes to a fake UI thread.
func newHarness(t *testing.T, dir string) *harness {
	t.Helper()
	a := newApp(dir)
	if a.repo == nil {
		t.Fatalf("repository not loaded: %s", a.repoErr)
	}
	fake := &fakeUI{}
	a.live.post = fake.post
	// No file watching unless a test asks for it: the changes our own
	// operations make would otherwise start refreshes at unpredictable times.
	a.live.startWatch = func(context.Context, string, mygo.FileWatchOptions) (hintSource, error) {
		return nil, errors.New("watching is off in this test")
	}
	h := &harness{t: t, dir: dir, a: a, fake: fake}
	h.tt = ui.NewTester(a.view, 1280, 800)
	t.Cleanup(a.endSession)
	h.settle()
	return h
}

// settle lets the queue, the background loads and the UI thread run to
// completion, building frames between.
func (h *harness) settle() {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		if q := h.a.live.q; q != nil {
			q.wait()
		}
		n := h.fake.drain()
		h.tt.Frame()
		if n == 0 && h.quiet() {
			return
		}
	}
	h.t.Fatal("background work did not settle")
}

func (h *harness) quiet() bool {
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	return len(h.fake.fns) == 0
}

// until settles repeatedly until cond holds, for work that depends on the
// operating system (file watchers).
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.fake.drain()
		h.tt.Frame()
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

// hasTextContaining reports whether some text on screen contains s.
func hasTextContaining(tt *ui.Tester, s string) bool {
	for _, text := range tt.Texts() {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}
