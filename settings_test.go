package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/egoist/mygo"
)

func TestSettingsDefaultsAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "settings.json")
	s := newSettingsStore(path)
	if !s.GitHubAvatars() {
		t.Fatal("GitHub avatars should be on by default")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("loading wrote the file")
	}
	if err := s.Update(func(s *Settings) { s.GitHubAvatars = false }); err != nil {
		t.Fatal(err)
	}
	if s.GitHubAvatars() {
		t.Fatal("update not applied")
	}
	if got := newSettingsStore(path); got.GitHubAvatars() {
		t.Fatal("update not saved")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	if err := s.Update(func(s *Settings) { s.GitHubAvatars = true }); err != nil {
		t.Fatal(err)
	}
	if got := newSettingsStore(path); !got.GitHubAvatars() {
		t.Fatal("second update not saved")
	}
}

func TestSettingsKeepDefaultsForMissingKeysAndBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"empty object": `{}`,
		"other keys":   `{"somethingNew": 3}`,
		"corrupt":      `{"githubAvatars": tr`,
		"wrong type":   `{"githubAvatars": "yes"}`,
		"empty file":   ``,
	}
	for name, content := range cases {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if !newSettingsStore(path).GitHubAvatars() {
			t.Errorf("%s: lost the default", name)
		}
	}
	path := filepath.Join(dir, "off.json")
	if err := os.WriteFile(path, []byte(`{"githubAvatars": false, "somethingNew": 3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if newSettingsStore(path).GitHubAvatars() {
		t.Error("an explicit false was ignored")
	}
	if !newSettingsStore(filepath.Join(dir, "missing.json")).GitHubAvatars() {
		t.Error("a missing file should give the defaults")
	}
}

func TestSettingsInMemoryAndConcurrent(t *testing.T) {
	s := newSettingsStore("")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.Update(func(s *Settings) { s.GitHubAvatars = i%2 == 0 }) }()
		go func() { defer wg.Done(); _ = s.GitHubAvatars() }()
	}
	wg.Wait()
}

func TestSettingsUpdateKeepsTheChangeWhenSavingFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSettingsStore(filepath.Join(blocker, "settings.json")) // its directory is a file
	if err := s.Update(func(s *Settings) { s.GitHubAvatars = false }); err == nil {
		t.Fatal("expected an error")
	}
	if s.GitHubAvatars() {
		t.Fatal("the change was dropped with the failed save")
	}
}

func TestGitHubAvatarsMenuItem(t *testing.T) {
	s := newSettingsStore(filepath.Join(t.TempDir(), "settings.json"))
	redraws := 0
	item := githubAvatarsMenuItem(s, func() { redraws++ })
	if item.Label != "GitHub Avatars" || item.Type != mygo.MenuItemCheckbox || !item.Checked {
		t.Fatalf("item = %+v", item)
	}

	// The menu toggles the check, then calls Click.
	item.Checked = false
	item.Click(item, nil)
	if s.GitHubAvatars() || redraws != 1 {
		t.Fatalf("after unchecking: on=%v redraws=%d", s.GitHubAvatars(), redraws)
	}
	if newSettingsStore(s.path).GitHubAvatars() {
		t.Fatal("not saved")
	}
	item.Checked = true
	item.Click(item, nil)
	if !s.GitHubAvatars() || redraws != 2 {
		t.Fatalf("after checking: on=%v redraws=%d", s.GitHubAvatars(), redraws)
	}

	// It starts out as the setting is.
	_ = s.Update(func(s *Settings) { s.GitHubAvatars = false })
	if githubAvatarsMenuItem(s, nil).Checked {
		t.Fatal("the item shows checked for an off setting")
	}
}

func TestAppMenuHasTheSwitchInTheViewMenu(t *testing.T) {
	menu := newAppMenu(newSettingsStore(""), nil)
	item := menu.ItemByID("view.githubAvatars")
	if item == nil {
		t.Fatal("no GitHub Avatars item")
	}
	var view *mygo.MenuItem
	for _, it := range menu.Items() {
		if it.Role == mygo.RoleViewMenu {
			view = it
		}
	}
	if view == nil {
		t.Fatal("no View menu")
	}
	found := false
	roles := map[mygo.MenuRole]bool{}
	for _, it := range view.Submenu {
		found = found || it == item
		roles[it.Role] = true
	}
	if !found {
		t.Fatal("the item is not in the View menu")
	}
	for _, r := range []mygo.MenuRole{mygo.RoleReload, mygo.RoleToggleDevTools, mygo.RoleZoomIn, mygo.RoleToggleFullScreen} {
		if !roles[r] {
			t.Errorf("the View menu lost its %s item", r)
		}
	}
	top := map[mygo.MenuRole]bool{}
	for _, it := range menu.Items() {
		top[it.Role] = true
	}
	for _, r := range []mygo.MenuRole{mygo.RoleAppMenu, mygo.RoleFileMenu, mygo.RoleEditMenu, mygo.RoleViewMenu, mygo.RoleWindowMenu} {
		if !top[r] {
			t.Errorf("the menu bar lost its %s", r)
		}
	}
}
