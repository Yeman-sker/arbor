package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// existingDirs makes n directories in a temporary directory.
func existingDirs(t *testing.T, n int) []string {
	t.Helper()
	root := t.TempDir()
	var dirs []string
	for i := 0; i < n; i++ {
		d := filepath.Join(root, fmt.Sprintf("repo-%02d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	return dirs
}

func TestSessionRoundTrip(t *testing.T) {
	dirs := existingDirs(t, 4)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")

	s := newSessionStore(path)
	s.Update(func(ss *session) {
		ss.addRecent(dirs[0])
		ss.addRecent(dirs[1])
		ss.addRecent(dirs[2])
		ss.Windows = []sessionWindow{{Slot: 2, Repo: dirs[1]}, {Slot: 1, Repo: dirs[0]}}
	})
	s.Flush()
	if s.err != nil {
		t.Fatal(s.err)
	}

	again := newSessionStore(path).Get()
	if want := []string{dirs[2], dirs[1], dirs[0]}; !slices.Equal(again.Recents, want) {
		t.Fatalf("recents = %v, want %v", again.Recents, want)
	}
	if want := []sessionWindow{{1, dirs[0]}, {2, dirs[1]}}; !slices.Equal(again.Windows, want) {
		t.Fatalf("windows = %+v, want %+v", again.Windows, want)
	}

	// The file is plain JSON, with those two keys, and nothing is left beside
	// it from the atomic write.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 2 || raw["recents"] == nil || raw["windows"] == nil {
		t.Fatalf("session.json = %s (%v)", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "session.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("files in the directory: %v", names)
	}
}

func TestSessionLastWriteWins(t *testing.T) {
	dirs := existingDirs(t, 3)
	path := filepath.Join(t.TempDir(), "session.json")
	s := newSessionStore(path)
	for i := 0; i < 50; i++ {
		s.Update(func(ss *session) { ss.addRecent(dirs[i%3]) })
	}
	s.Flush()
	want := s.Get()
	if got := readSession(path); !slices.Equal(got.Recents, want.Recents) {
		t.Fatalf("on disk %v, in memory %v", got.Recents, want.Recents)
	}
	// An update that changes nothing writes nothing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Update(func(ss *session) { ss.addRecent(want.Recents[0]) })
	s.Flush()
	if _, err := os.Stat(path); err == nil {
		t.Fatal("an unchanged session was written again")
	}
}

func TestSessionSkipsMissingDirectories(t *testing.T) {
	dirs := existingDirs(t, 2)
	gone := filepath.Join(t.TempDir(), "deleted")
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.json")
	data, _ := json.Marshal(session{
		Recents: []string{gone, dirs[0], file, dirs[1]},
		Windows: []sessionWindow{{1, gone}, {2, dirs[1]}, {3, file}},
	})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got := newSessionStore(path).Get()
	if want := []string{dirs[0], dirs[1]}; !slices.Equal(got.Recents, want) {
		t.Fatalf("recents = %v, want %v", got.Recents, want)
	}
	if want := []sessionWindow{{2, dirs[1]}}; !slices.Equal(got.Windows, want) {
		t.Fatalf("windows = %+v, want %+v", got.Windows, want)
	}
}

func TestSessionToleratesMissingAndCorruptFiles(t *testing.T) {
	dirs := existingDirs(t, 1)
	good, _ := json.Marshal(session{Recents: []string{dirs[0]}})
	for name, content := range map[string]string{
		"missing":         "",
		"empty":           "",
		"garbage":         "\x00\xffnot json at all",
		"truncated":       string(good[:len(good)/2]),
		"an array":        `[1, 2, 3]`,
		"null":            `null`,
		"wrong types":     `{"recents": 5, "windows": "x"}`,
		"nested nonsense": `{"recents": [[1], {"a": 2}], "windows": [[], null, 3]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			if name != "missing" {
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s := newSessionStore(path)
			if got := s.Get(); len(got.Recents) != 0 || len(got.Windows) != 0 {
				t.Fatalf("session = %+v", got)
			}
			// The next change replaces the file by a good one.
			s.Update(func(ss *session) { ss.addRecent(dirs[0]) })
			s.Flush()
			if got := newSessionStore(path).Get(); !slices.Equal(got.Recents, []string{dirs[0]}) {
				t.Fatalf("after a change: %+v", got)
			}
		})
	}

	// What is fine is kept when another part is not.
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"recents": ["`+dirs[0]+`"], "windows": 7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSession(path); !slices.Equal(got.Recents, []string{dirs[0]}) || len(got.Windows) != 0 {
		t.Fatalf("partial = %+v", got)
	}
}

func TestSessionTidy(t *testing.T) {
	s := session{
		Recents: []string{"/a", "relative/path", "/b", "/a", "", "/c/../c", "/d", "/e", "/f", "/g", "/h", "/i", "/j", "/k", "/l"},
		Windows: []sessionWindow{
			{3, "/x"}, {0, "/zero"}, {-1, "/neg"}, {maxSessionSlot + 1, "/huge"},
			{1, "/y"}, {1, "/again-slot-1"}, {2, "/y"}, {4, ""}, {5, "rel"}, {6, "/z/"},
		},
	}
	s.tidy()
	if want := []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h", "/i", "/j"}; !slices.Equal(s.Recents, want) {
		t.Fatalf("recents = %v", s.Recents)
	}
	want := []sessionWindow{{1, "/y"}, {3, "/x"}, {6, "/z"}}
	if !slices.Equal(s.Windows, want) {
		t.Fatalf("windows = %+v, want %+v", s.Windows, want)
	}
}

func TestRecentsAreOrderedByUseAndCapped(t *testing.T) {
	var s session
	for i := 0; i < 13; i++ {
		s.addRecent(fmt.Sprintf("/repos/r%02d", i))
	}
	if len(s.Recents) != maxRecents || maxRecents != 10 {
		t.Fatalf("%d recents (cap %d)", len(s.Recents), maxRecents)
	}
	if s.Recents[0] != "/repos/r12" || s.Recents[9] != "/repos/r03" {
		t.Fatalf("recents = %v: newest first, the oldest dropped", s.Recents)
	}
	// Using one again moves it to the front, once.
	s.addRecent("/repos/r05")
	s.addRecent("/repos/r05/")
	if s.Recents[0] != "/repos/r05" || s.Recents[1] != "/repos/r12" || len(s.Recents) != 10 {
		t.Fatalf("recents = %v", s.Recents)
	}
	if n := strings.Count(strings.Join(s.Recents, " "), "/repos/r05"); n != 1 {
		t.Fatalf("r05 listed %d times", n)
	}
	// One that is dropped can come back; relative paths and nothing are not kept.
	s.addRecent("")
	s.addRecent("repos/relative")
	s.addRecent("/repos/r00")
	if s.Recents[0] != "/repos/r00" || len(s.Recents) != 10 || slices.Contains(s.Recents, "repos/relative") {
		t.Fatalf("recents = %v", s.Recents)
	}
	s.forget("/repos/r12")
	if slices.Contains(s.Recents, "/repos/r12") || len(s.Recents) != 9 {
		t.Fatalf("after forget: %v", s.Recents)
	}
}

func TestSessionIsNotSettings(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	sessionPath := filepath.Join(dir, "session.json")
	st := newSettingsStore(settingsPath)
	if err := st.Update(func(s *Settings) { s.GitHubAvatars = false }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	dirs := existingDirs(t, 2)
	s := newSessionStore(sessionPath)
	s.Update(func(ss *session) { ss.addRecent(dirs[0]); ss.Windows = []sessionWindow{{1, dirs[1]}} })
	s.Flush()
	after, _ := os.ReadFile(settingsPath)
	if string(before) != string(after) {
		t.Fatalf("settings.json changed: %s -> %s", before, after)
	}
	if strings.Contains(string(after), "recents") || strings.Contains(string(after), "windows") {
		t.Fatalf("session data in settings.json: %s", after)
	}
	// And a broken session file does not touch the settings either.
	if err := os.WriteFile(sessionPath, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = newSessionStore(sessionPath)
	if newSettingsStore(settingsPath).GitHubAvatars() {
		t.Fatal("the setting was lost")
	}
}

func TestSessionStoreInMemoryWritesNothing(t *testing.T) {
	dirs := existingDirs(t, 1)
	s := newSessionStore("")
	s.Update(func(ss *session) { ss.addRecent(dirs[0]) })
	s.Flush()
	if got := s.Get().Recents; !slices.Equal(got, []string{dirs[0]}) {
		t.Fatalf("recents = %v", got)
	}
	// Get hands out a copy.
	got := s.Get()
	got.Recents[0] = "/changed"
	if s.Get().Recents[0] != dirs[0] {
		t.Fatal("Get shares its slices")
	}
}

func TestSessionWriteErrorKeepsTheSessionInMemory(t *testing.T) {
	dirs := existingDirs(t, 1)
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// session.json cannot be made inside a file.
	s := newSessionStore(filepath.Join(blocker, "session.json"))
	s.Update(func(ss *session) { ss.addRecent(dirs[0]) })
	s.Flush()
	if s.err == nil {
		t.Fatal("expected a write error")
	}
	if got := s.Get().Recents; !slices.Equal(got, []string{dirs[0]}) {
		t.Fatalf("the change was dropped with the failed save: %v", got)
	}
}
