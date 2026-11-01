package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitSpec is a commit of a test repository.
type commitSpec struct {
	file   string
	msg    string
	date   string // author date, ISO 8601
	author string // "Name <email>", empty for Liu
	tag    string
	branch string // created at this commit
}

// testGit runs Git in dir as a fixed identity, without the user's hooks and
// signing, and fails the test on error.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=Liu", "-c", "user.email=liu@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(dir, ".no-hooks"),
	}, args...)
	out, err := Run(dir, full...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// makeRepo commits specs in order, each adding a file, and returns their
// hashes.
func makeRepo(t *testing.T, specs []commitSpec) (dir string, hashes []string) {
	t.Helper()
	dir = t.TempDir()
	testGit(t, dir, "init", "-b", "main")
	for _, s := range specs {
		hashes = append(hashes, commitFile(t, dir, s))
		if s.tag != "" {
			testGit(t, dir, "tag", s.tag)
		}
		if s.branch != "" {
			testGit(t, dir, "branch", s.branch)
		}
	}
	return dir, hashes
}

func commitFile(t *testing.T, dir string, s commitSpec) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, s.file), []byte(s.file+"\nline 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgFile := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(msgFile, []byte(s.msg), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", s.file)
	args := []string{"commit", "-F", msgFile, "--date=" + s.date}
	if s.author != "" {
		args = append(args, "--author="+s.author)
	}
	testGit(t, dir, args...)
	return testGit(t, dir, "rev-parse", "HEAD")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Run(dir, args...)
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
