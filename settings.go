package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo"
)

// Settings are the user's preferences, kept in settings.json in the user data
// directory. A key that is missing keeps its default, so a setting can be added
// without migrating the file.
type Settings struct {
	// GitHubAvatars shows GitHub profile pictures in commit cards, for
	// repositories whose origin is on GitHub. Off, nothing is fetched and
	// the cards show initials.
	GitHubAvatars bool `json:"githubAvatars"`
	// PerformanceDiagnostics records how long operations take, on this computer
	// only, and shows the Performance window. It stays off until the user allows it.
	PerformanceDiagnostics bool `json:"performanceDiagnostics"`
}

func defaultSettings() Settings {
	return Settings{GitHubAvatars: true}
}

// settingsStore is the settings of the process, safe for use from any
// goroutine.
type settingsStore struct {
	mu   sync.RWMutex
	path string // "" keeps them in memory only
	cur  Settings
}

// newSettingsStore loads the settings from path. A missing or unreadable file
// gives the defaults, and is replaced by the next Update.
func newSettingsStore(path string) *settingsStore {
	s := &settingsStore{path: path, cur: defaultSettings()}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	loaded := defaultSettings()
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s
	}
	s.cur = loaded
	return s
}

// Get returns a copy of the settings.
func (s *settingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// GitHubAvatars reports whether commit cards may show GitHub avatars.
func (s *settingsStore) GitHubAvatars() bool { return s.Get().GitHubAvatars }

// Update changes the settings with fn and saves them. The change holds in
// memory even when saving fails.
func (s *settingsStore) Update(fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	fn(&next)
	s.cur = next
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(data, '\n'))
}

// writeFileAtomic writes data to path through a temporary file, so that a
// reader never sees half of it.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

var (
	sharedMu            sync.Mutex
	sharedSettingsStore *settingsStore
	sharedAvatarCache   *avatarCache
)

// userDataPath is a path inside the application's user data directory,
// which this creates. It is "" when there is no such directory.
func userDataPath(elem ...string) string {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(append([]string{dir}, elem...)...)
}

// sharedSettings is the settings file of the user, loaded once for every app
// of the process.
func sharedSettings() *settingsStore {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedSettingsStore == nil {
		sharedSettingsStore = newSettingsStore(userDataPath("settings.json"))
	}
	return sharedSettingsStore
}

// newAppMenu is the application menu: the standard one, with the settings
// that are switches in its View menu. changed asks the windows to redraw.
// fileItems, when there are any, are the items of the File menu in place of
// the standard one's.
func newAppMenu(settings *settingsStore, changed func(), fileItems ...*mygo.MenuItem) *mygo.Menu {
	return newAppMenuWith(settings, changed, fileItems, nil)
}

// newAppMenuWith is newAppMenu with viewItems added at the end of the View menu.
func newAppMenuWith(settings *settingsStore, changed func(), fileItems, viewItems []*mygo.MenuItem) *mygo.Menu {
	view := []*mygo.MenuItem{
		{Role: mygo.RoleReload},
		{Role: mygo.RoleForceReload},
		{Role: mygo.RoleToggleDevTools},
		mygo.Separator(),
		{Role: mygo.RoleResetZoom},
		{Role: mygo.RoleZoomIn},
		{Role: mygo.RoleZoomOut},
		mygo.Separator(),
		{Role: mygo.RoleToggleFullScreen},
		mygo.Separator(),
		githubAvatarsMenuItem(settings, changed),
	}
	view = append(view, viewItems...)
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Role: mygo.RoleFileMenu, Submenu: fileItems},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu, Submenu: view},
		{Role: mygo.RoleWindowMenu},
	})
}

// githubAvatarsMenuItem is the "GitHub Avatars" switch of the View menu.
func githubAvatarsMenuItem(settings *settingsStore, changed func()) *mygo.MenuItem {
	return &mygo.MenuItem{
		ID:      "view.githubAvatars",
		Label:   "GitHub Avatars",
		Type:    mygo.MenuItemCheckbox,
		Checked: settings.GitHubAvatars(),
		ToolTip: "Show GitHub profile pictures in commit cards",
		Click: func(it *mygo.MenuItem, _ *mygo.Window) {
			// The menu toggled the check before calling.
			on := it.IsChecked()
			_ = settings.Update(func(s *Settings) { s.GitHubAvatars = on })
			if changed != nil {
				changed()
			}
		},
	}
}
