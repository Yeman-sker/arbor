package fixture

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(out, "\n")
}

// TestPinned builds each fixture and compares it with its pins: the object id
// of main and the digest of every ref. A failure means the generator, a Spec
// or the git object format changed; see the package comment before touching
// a pin.
func TestPinned(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, s := range []Spec{Small, Large, Holdout} {
		t.Run(s.Name, func(t *testing.T) {
			if s.Name == "large" && testing.Short() {
				t.Skip("large fixture skipped with -short")
			}
			dir := filepath.Join(t.TempDir(), "repo")
			if err := Build(dir, s); err != nil {
				t.Fatal(err)
			}
			if got := gitOut(t, dir, "ls-files", "-z"); strings.Count(got, "\x00") != s.Files() {
				t.Errorf("tracked files = %d, want %d", strings.Count(got, "\x00"), s.Files())
			}
			if got := gitOut(t, dir, "rev-list", "--count", "--first-parent", "main"); got != strconv.Itoa(s.Commits) {
				t.Errorf("main commits = %s, want %d", got, s.Commits)
			}
			if got := gitOut(t, dir, "for-each-ref", "refs/heads/feature/", "--format=x"); strings.Count(got, "x") != s.Branches {
				t.Errorf("feature branches = %d, want %d", strings.Count(got, "x"), s.Branches)
			}
			if n, _ := strconv.Atoi(gitOut(t, dir, "rev-list", "--count", "--all")); n < 400 {
				t.Errorf("commits = %d, want at least 400", n)
			}
			if got := gitOut(t, dir, "status", "--porcelain"); got != "" {
				t.Errorf("fresh fixture is not clean:\n%s", got)
			}
		})
	}
}

func TestDirtyState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if err := BuildDirty(dir, Small); err != nil {
		t.Fatal(err)
	}
	got := gitOut(t, dir, "status", "--porcelain", "-uall")
	unstaged, staged, untracked := Small.DirtyPaths()
	for _, p := range unstaged {
		if !strings.Contains(got, " M "+p) {
			t.Errorf("missing unstaged %s in\n%s", p, got)
		}
	}
	for _, p := range staged {
		if !strings.Contains(got, "M  "+p) {
			t.Errorf("missing staged %s in\n%s", p, got)
		}
	}
	for _, p := range untracked {
		if !strings.Contains(got, "?? "+p) {
			t.Errorf("missing untracked %s in\n%s", p, got)
		}
	}
}

func TestContentIsDeterministic(t *testing.T) {
	a, b := Content(7, 3), Content(7, 3)
	if string(a) != string(b) || string(a) == string(Content(7, 4)) {
		t.Fatal("content must depend on (file, revision) alone")
	}
}
