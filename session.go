package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
)

// The session is what Arbor remembers between runs about which repositories
// it had open: the ones used lately, for the repository menu and the empty
// window, and which window (slot) showed which repository, so that the next
// launch can open them again. It lives in session.json in the user data
// directory, apart from settings.json: it changes with every window opened
// or closed, and losing it costs a few clicks, not a preference.

const (
	// maxRecents is how many repositories the session remembers.
	maxRecents = 10
	// maxSessionSlot bounds the window slots a file may name, against a file
	// that was edited or is garbage.
	maxSessionSlot = 1000
)

// sessionWindow is the repository one window showed.
type sessionWindow struct {
	Slot int    `json:"slot"`
	Repo string `json:"repo"`
}

// session is the content of session.json.
type session struct {
	// Recents are repository roots, the one used last first.
	Recents []string `json:"recents"`
	// Windows are the windows open, by slot, lowest first.
	Windows []sessionWindow `json:"windows"`
}

// addRecent puts dir first among the recents, once, and keeps the newest
// maxRecents.
func (s *session) addRecent(dir string) {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return
	}
	s.Recents = slices.DeleteFunc(s.Recents, func(r string) bool { return r == dir })
	s.Recents = append([]string{dir}, s.Recents...)
	if len(s.Recents) > maxRecents {
		s.Recents = s.Recents[:maxRecents]
	}
}

// forget takes dir out of the recents.
func (s *session) forget(dir string) {
	dir = filepath.Clean(dir)
	s.Recents = slices.DeleteFunc(s.Recents, func(r string) bool { return r == dir })
}

// clone returns a copy that shares nothing with s.
func (s session) clone() session {
	s.Recents = slices.Clone(s.Recents)
	s.Windows = slices.Clone(s.Windows)
	return s
}

// tidy drops what a session cannot use: a path that is not absolute, a
// repository listed twice, a slot out of range or twice, and what is past
// the cap.
func (s *session) tidy() {
	var recents []string
	for _, r := range s.Recents {
		if r = filepath.Clean(r); filepath.IsAbs(r) && !slices.Contains(recents, r) {
			recents = append(recents, r)
		}
	}
	if len(recents) > maxRecents {
		recents = recents[:maxRecents]
	}
	s.Recents = recents

	var windows []sessionWindow
	slots, repos := map[int]bool{}, map[string]bool{}
	for _, w := range s.Windows {
		repo := filepath.Clean(w.Repo)
		if !filepath.IsAbs(repo) || w.Slot < 1 || w.Slot > maxSessionSlot || slots[w.Slot] || repos[repo] {
			continue
		}
		slots[w.Slot], repos[repo] = true, true
		windows = append(windows, sessionWindow{Slot: w.Slot, Repo: repo})
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Slot < windows[j].Slot })
	s.Windows = windows
}

// dropMissing takes the directories that are not there out of the session.
func (s *session) dropMissing(exists func(dir string) bool) {
	s.Recents = slices.DeleteFunc(s.Recents, func(r string) bool { return !exists(r) })
	s.Windows = slices.DeleteFunc(s.Windows, func(w sessionWindow) bool { return !exists(w.Repo) })
}

func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// readSession reads session.json. A file that is missing, cannot be read or
// is not what it should be gives the empty session, and a part of it that is
// fine is kept when another is not.
func readSession(path string) session {
	var s session
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var raw struct {
		Recents json.RawMessage `json:"recents"`
		Windows json.RawMessage `json:"windows"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return s
	}
	_ = json.Unmarshal(raw.Recents, &s.Recents)
	_ = json.Unmarshal(raw.Windows, &s.Windows)
	s.tidy()
	return s
}

// sessionStore keeps the session of the process: in memory, owned by the UI
// thread, and in session.json, written in the background so that opening or
// closing a window never waits for the disk.
type sessionStore struct {
	path string // "" keeps the session in memory only
	cur  session

	// mu serializes the writes of the file, and seq numbers them: a write
	// that finds a newer one queued behind it leaves the file to that one.
	mu  sync.Mutex
	seq uint64
	wg  sync.WaitGroup
	// err is the error of the last write, for tests.
	err error
}

// newSessionStore reads the session at path, without the directories that are
// no longer there.
func newSessionStore(path string) *sessionStore {
	s := &sessionStore{path: path, cur: readSession(path)}
	s.cur.dropMissing(dirExists)
	return s
}

// Get returns a copy of the session.
func (s *sessionStore) Get() session { return s.cur.clone() }

// Update changes the session with fn and saves it in the background. Call it
// on the UI thread.
func (s *sessionStore) Update(fn func(*session)) {
	next := s.cur.clone()
	fn(&next)
	next.tidy()
	if slices.Equal(next.Recents, s.cur.Recents) && slices.Equal(next.Windows, s.cur.Windows) {
		return
	}
	s.cur = next
	if s.path == "" {
		return
	}
	snap := next.clone()
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		if seq != s.seq {
			return // a newer session is waiting to be written
		}
		s.err = writeSession(s.path, snap)
	}()
}

// Flush waits for the writes in progress, as the application quits.
func (s *sessionStore) Flush() { s.wg.Wait() }

func writeSession(path string, s session) error {
	if s.Recents == nil {
		s.Recents = []string{}
	}
	if s.Windows == nil {
		s.Windows = []sessionWindow{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// sharedSessionStore is the session file of the user, read once for every
// window of the process.
var (
	sessionMu     sync.Mutex
	sharedSession *sessionStore
)

// userSession is the session of the process, in the user data directory.
func userSession() *sessionStore {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if sharedSession == nil {
		sharedSession = newSessionStore(userDataPath("session.json"))
	}
	return sharedSession
}
