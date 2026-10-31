// Package fixture generates the repositories every Arbor performance number is
// measured on: the Go benchmarks, the real-app scenarios and the baselines.
//
// THE FIXTURES ARE A MEASURING STICK. DO NOT CHANGE THEM TO MAKE A NUMBER LOOK
// BETTER. For a given Version the repositories are byte-for-byte reproducible:
// the generator uses no clock, no random seed and no machine state, and every
// Spec pins the object id of main and a digest of all refs. Build fails if
// either differs, and TestPinned fails with it. Changing a generator, a Spec or
// a pin therefore changes every number ever recorded, so it needs its own
// reviewed commit that bumps Version, re-pins the hashes and re-records the
// baselines. Optimization work must never touch this package.
package fixture

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Version identifies the generator. Bump it, with the pins below, when the
// fixtures must change; baselines recorded for another version do not compare.
const Version = 1

// Spec describes one fixture repository.
type Spec struct {
	Name     string
	Tops     int // top-level directories
	Subs     int // sub-directories in each
	Per      int // files in each sub-directory; Files = Tops*Subs*Per
	Branches int // feature branches, each one commit off main
	Commits  int // commits on main

	// WantMain is the object id of main, and WantRefs the sha256 of every ref
	// and its object id, as `git for-each-ref` prints them.
	WantMain string
	WantRefs string
}

// Files is the number of tracked files at main.
func (s Spec) Files() int { return s.Tops * s.Subs * s.Per }

// Small and Large are the two repository sizes of the benchmarks.
var (
	Small = Spec{
		Name: "small", Tops: 5, Subs: 5, Per: 8, Branches: 200, Commits: 450,
		WantMain: "bed7c4a2cfc31911507ee6c74868bf908e15c410",
		WantRefs: "b5084a169c5e91b1784c6c521794dfddc683a9cdd88baa1576c715023d24d817",
	}
	Large = Spec{
		Name: "large", Tops: 20, Subs: 25, Per: 40, Branches: 200, Commits: 450,
		WantMain: "fe378fe96f0213bd903e59cb87919077563e7875",
		WantRefs: "24f40d5c1d87747bd5546a718e0cdb904feb89be7003f4411295edc7a414122b",
	}
)

// Dirty working-tree state applied by Dirty, in file indexes of the spec.
var (
	dirtyUnstaged = []int{3, 17, 41, 77}
	dirtyStaged   = []int{5, 29}
)

const (
	baseTime  = 1_700_000_000
	author    = "Arbor Perf"
	authorMsg = "perf@arbor.invalid"
)

var authors = []string{"Ada Perf", "Grace Perf", "Linus Perf", "Barbara Perf", "Ken Perf"}

var verbs = []string{"add", "fix", "refactor", "update", "tune", "split", "drop", "rename", "cache", "guard"}

// Path is the repository-relative path of file idx. File names are unique
// across the repository so UI scenarios can find a file by its name alone.
func (s Spec) Path(idx int) string {
	per := s.Subs * s.Per
	top, rest := idx/per, idx%per
	sub := rest / s.Per
	return fmt.Sprintf("pkg%02d/mod%02d/f%05d%s", top, sub, idx, ext(idx))
}

func ext(idx int) string {
	switch {
	case idx%10 == 9:
		return ".md"
	case idx%10 == 8:
		return ".json"
	}
	return ".go"
}

// Branch returns the name of feature branch k.
func Branch(k int) string { return fmt.Sprintf("feature/b%03d", k) }

// DirtyPaths lists the files Dirty changes: unstaged, staged, untracked.
func (s Spec) DirtyPaths() (unstaged, staged, untracked []string) {
	for _, i := range dirtyUnstaged {
		unstaged = append(unstaged, s.Path(i))
	}
	for _, i := range dirtyStaged {
		staged = append(staged, s.Path(i))
	}
	return unstaged, staged, []string{"untracked/new_000.go", "untracked/new_001.go"}
}

// mix is splitmix64: a fixed, portable, seedless source of numbers.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// content is the text of file idx at revision rev. About a ninth of its lines
// depend on the revision, so consecutive revisions differ in a few hunks.
func content(idx, rev int) []byte {
	var b bytes.Buffer
	n := 20 + int(mix(uint64(idx))%30)
	switch ext(idx) {
	case ".md":
		fmt.Fprintf(&b, "# Module %d\n\n", idx)
	case ".json":
		b.WriteString("{\n")
	default:
		fmt.Fprintf(&b, "package pkg%02d\n\nimport \"fmt\"\n\n", idx%97)
	}
	for j := 0; j < n; j++ {
		salt := 0
		if (j+rev)%9 == 0 {
			salt = rev
		}
		h := mix(uint64(idx)<<24 ^ uint64(j)<<8 ^ uint64(salt))
		switch ext(idx) {
		case ".md":
			fmt.Fprintf(&b, "- item %d.%d is %x and uses revision %d\n", idx, j, h&0xffffff, salt)
		case ".json":
			fmt.Fprintf(&b, "  \"k%d_%d\": {\"v\": %d, \"rev\": %d},\n", idx, j, h%100000, salt)
		default:
			fmt.Fprintf(&b, "func F%d_%d(x int) int { return x*%d + %d } // rev %d\n", idx, j, h%97+1, h%1013, salt)
		}
	}
	switch ext(idx) {
	case ".json":
		b.WriteString("  \"end\": true\n}\n")
	case ".go":
		fmt.Fprintf(&b, "\nfunc Print%d() { fmt.Println(F%d_0(%d)) }\n", idx, idx, rev)
	}
	return b.Bytes()
}

// Content returns the committed text of file idx at revision rev, which
// scenarios use to write expected edits.
func Content(idx, rev int) []byte { return content(idx, rev) }

// touched lists the distinct files main commit i changes.
func (s Spec) touched(i int) []int {
	n := 2 + i%6
	seen := map[int]bool{}
	var out []int
	for j := 0; len(out) < n && j < 4*n; j++ {
		f := int(mix(uint64(i)*7919+uint64(j)) % uint64(s.Files()))
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func subject(i int) string {
	return fmt.Sprintf("%s pkg%02d module %d (#%d)", verbs[i%len(verbs)], i%17, int(mix(uint64(i))%1000), i)
}

type stream struct {
	w   *bufio.Writer
	err error
}

func (s *stream) printf(format string, a ...any) {
	if s.err == nil {
		_, s.err = fmt.Fprintf(s.w, format, a...)
	}
}

func (s *stream) data(b []byte) {
	s.printf("data %d\n", len(b))
	if s.err == nil {
		_, s.err = s.w.Write(b)
	}
	s.printf("\n")
}

func (s *stream) commit(ref string, mark int, who int, when int, msg string, from, merge int, files map[string][]byte, order []string) {
	s.printf("commit %s\nmark :%d\n", ref, mark)
	s.printf("author %s <%s> %d +0000\n", authors[who%len(authors)], authMail(who), when)
	s.printf("committer %s <%s> %d +0000\n", author, authorMsg, when)
	s.data([]byte(msg))
	if from != 0 {
		s.printf("from :%d\n", from)
	}
	if merge != 0 {
		s.printf("merge :%d\n", merge)
	}
	for _, p := range order {
		s.printf("M 100644 inline %s\n", p)
		s.data(files[p])
	}
	s.printf("\n")
}

func authMail(who int) string {
	return strings.ToLower(strings.Fields(authors[who%len(authors)])[0]) + "@arbor.invalid"
}

// Mark numbers: main commit i is mark i; branch k is mark 1000+k.
func (s Spec) writeHistory(w io.Writer) error {
	st := &stream{w: bufio.NewWriterSize(w, 1<<20)}

	files := map[string][]byte{}
	var order []string
	for i := 0; i < s.Files(); i++ {
		p := s.Path(i)
		files[p] = content(i, 0)
		order = append(order, p)
	}
	st.commit("refs/heads/main", 1, 0, baseTime, "Initial import of the performance fixture\n", 0, 0, files, order)

	for i := 2; i <= s.Commits; i++ {
		when := baseTime + i*3600
		files, order = map[string][]byte{}, order[:0]
		for _, f := range s.touched(i) {
			p := s.Path(f)
			files[p] = content(f, i)
			order = append(order, p)
		}
		merge := 0
		// Every 20th commit from 40 on merges the branch that forked at i-38.
		if i%20 == 0 && i >= 40 {
			if k := (i - 40) / 2; k < s.Branches {
				merge = 1000 + k
			}
		}
		msg := subject(i) + "\n\nGenerated for the Arbor performance fixture.\n"
		if merge != 0 {
			msg = fmt.Sprintf("Merge %s into main\n", Branch(merge-1000))
		}
		st.commit("refs/heads/main", i, i, when, msg, 0, merge, files, order)

		// Branch k forks off main commit 2+2k, so it exists before any merge of it.
		if i%2 == 0 {
			if k := (i - 2) / 2; k < s.Branches {
				f := s.touched(i + 1000)[0]
				p := s.Path(f)
				bf := map[string][]byte{p: content(f, 1000+k)}
				st.commit("refs/heads/"+Branch(k), 1000+k, k, when+60, subject(1000+k)+"\n", i, 0, bf, []string{p})
			}
		}
	}
	st.printf("done\n")
	if st.err != nil {
		return st.err
	}
	return st.w.Flush()
}

func gitEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		switch {
		case strings.HasPrefix(e, "GIT_DIR="), strings.HasPrefix(e, "GIT_WORK_TREE="),
			strings.HasPrefix(e, "GIT_INDEX_FILE="), strings.HasPrefix(e, "GIT_CONFIG"):
			continue
		}
		env = append(env, e)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
}

func git(dir string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Build creates the repository in the empty directory dir, with main checked
// out and clean, and verifies it against the spec's pins.
func Build(dir string, s Spec) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q", "--object-format=sha1", "-b", "main"},
		{"config", "user.name", author},
		{"config", "user.email", authorMsg},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
		{"config", "maintenance.auto", "false"},
		{"config", "core.fsmonitor", "false"},
	} {
		if _, err := git(dir, nil, args...); err != nil {
			return err
		}
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(s.writeHistory(pw)) }()
	if _, err := git(dir, pr, "fast-import", "--quiet", "--done"); err != nil {
		pr.Close()
		return err
	}
	// The working tree is written here and indexed with read-tree: git's own
	// checkout of 20000 files takes tens of seconds on macOS.
	rev := make([]int, s.Files())
	for i := 2; i <= s.Commits; i++ {
		for _, f := range s.touched(i) {
			rev[f] = i
		}
	}
	for i, r := range rev {
		full := filepath.Join(dir, filepath.FromSlash(s.Path(i)))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, content(i, r), 0o644); err != nil {
			return err
		}
	}
	if _, err := git(dir, nil, "read-tree", "main"); err != nil {
		return err
	}
	if _, err := git(dir, nil, "update-index", "-q", "--refresh"); err != nil {
		return err
	}
	return s.verify(dir)
}

func (s Spec) verify(dir string) error {
	head, err := git(dir, nil, "rev-parse", "main")
	if err != nil {
		return err
	}
	refs, err := git(dir, nil, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(refs))
	gotMain, gotRefs := strings.TrimSpace(head), hex.EncodeToString(sum[:])
	if gotMain != s.WantMain || gotRefs != s.WantRefs {
		return fmt.Errorf("fixture %q (version %d) differs from its pins: main %s want %s, refs %s want %s",
			s.Name, Version, gotMain, s.WantMain, gotRefs, s.WantRefs)
	}
	return nil
}

// Pins returns what verify compares with, for re-pinning after a deliberate change.
func (s Spec) Pins(dir string) (mainID, refsDigest string, err error) {
	head, err := git(dir, nil, "rev-parse", "main")
	if err != nil {
		return "", "", err
	}
	refs, err := git(dir, nil, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(refs))
	return strings.TrimSpace(head), hex.EncodeToString(sum[:]), nil
}

// Dirty adds the fixed working-tree state of a repository in use: four
// modified files, two modified and staged, and two untracked.
func (s Spec) Dirty(dir string) error {
	write := func(p string, b []byte) error {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, b, 0o644)
	}
	for _, i := range dirtyUnstaged {
		if err := write(s.Path(i), content(i, 9999)); err != nil {
			return err
		}
	}
	for _, i := range dirtyStaged {
		if err := write(s.Path(i), content(i, 9998)); err != nil {
			return err
		}
		if _, err := git(dir, nil, "add", "--", s.Path(i)); err != nil {
			return err
		}
	}
	for n, p := range []string{"untracked/new_000.go", "untracked/new_001.go"} {
		if err := write(p, []byte("package untracked\n\nfunc New"+strconv.Itoa(n)+"() {}\n")); err != nil {
			return err
		}
	}
	_, err := git(dir, nil, "update-index", "-q", "--refresh")
	return err
}

// BuildDirty is Build followed by Dirty.
func BuildDirty(dir string, s Spec) error {
	if err := Build(dir, s); err != nil {
		return err
	}
	return s.Dirty(dir)
}

// Copy duplicates a fixture, for scenarios that change the repository.
func Copy(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
		return nil
	})
}
