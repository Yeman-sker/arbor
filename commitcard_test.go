package main

import (
	"arbor/perf"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// cardNow is the clock of the card tests: two days after the rich commit.
var cardNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

const (
	subjectRich   = "WIVYN-55: render themed Mermaid diagrams with Pele and seed Pele Vine"
	subjectPlain  = "fix: attach diagnostics when settings fail to load"
	subjectOldest = "chore: seed the repository"
)

const richMessage = `WIVYN-55: render themed Mermaid diagrams with Pele and seed Pele Vine

Mermaid fences in chat now render inline through the Pele
engine, themed from the active design tokens so diagrams
follow light and dark mode.

The Pele Vine ships seeded:
- new homes get the capability
- without an install step
  (indented continuation)

Signed-off-by: Ada Lovelace <ada@example.com>
Co-Authored-By: Claude <noreply@anthropic.com>
`

const richBody = "Mermaid fences in chat now render inline through the Pele engine, themed from the active design tokens so diagrams follow light and dark mode.\n\n" +
	"The Pele Vine ships seeded:\n- new homes get the capability\n- without an install step\n  (indented continuation)"

// commitSpec is a commit of a test repository.
type commitSpec struct {
	file   string
	msg    string
	date   string // author date, ISO 8601
	author string // "Name <email>", empty for Liu
	tag    string
	branch string // created at this commit
}

const defaultAuthor = "Liu <liu@example.com>"

// testGit runs Git in dir as a fixed identity, without the user's hooks and
// signing, and fails the test on error.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=Liu", "-c", "user.email=liu@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(dir, ".no-hooks"),
	}, args...)
	out, err := runGit(dir, full...)
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

// cardRepo is three commits: an old one without a body, one with a long
// message, trailers, a tag and a branch, and a plain one on top.
func cardRepo(t *testing.T) (dir string, hashes map[string]string) {
	t.Helper()
	dir, hs := makeRepo(t, []commitSpec{
		{file: "a.txt", msg: subjectOldest + "\n", date: "2026-10-27T10:00:00+00:00"},
		{file: "b.txt", msg: richMessage, date: "2026-10-29T14:02:00+00:00", tag: "v0.4.0", branch: "via/wivyn-55"},
		{file: "c.txt", msg: subjectPlain + "\n", date: "2026-10-31T11:30:00+00:00"},
	})
	return dir, map[string]string{"oldest": hs[0], "rich": hs[1], "plain": hs[2]}
}

// cardApp opens the Git view of dir in a tester, with the clock fixed and the
// settings in memory.
func cardApp(t *testing.T, dir string, w, h int) (*app, *ui.Tester) {
	t.Helper()
	a := newApp(dir)
	if a.repo == nil {
		t.Fatalf("repository did not load: %s", a.repoErr)
	}
	a.activeMode = modeGit
	a.cards.now = func() time.Time { return cardNow }
	a.cards.settings = newSettingsStore("")
	tt := ui.NewTester(a.view, w, h)
	tt.Frame()
	return a, tt
}

// hover rests the pointer on the row showing subject for long enough to show
// its card, and returns the row's text box.
func hover(t *testing.T, tt *ui.Tester, subject string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(subject)
	if !ok {
		t.Fatalf("no row shows %q: %v", subject, tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Advance(800 * time.Millisecond)
	tt.Frame()
	return r
}

func leave(tt *ui.Tester) {
	tt.Move(900, 700)
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
}

func count(tt *ui.Tester, text string) int {
	return strings.Count("\x00"+strings.Join(tt.Texts(), "\x00")+"\x00", "\x00"+text+"\x00")
}

func TestCommitCardAppearsAfterDelay(t *testing.T) {
	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)

	if count(tt, "Liu") != 0 {
		t.Fatalf("card showing before hover: %v", tt.Texts())
	}
	r, _ := tt.Find(subjectRich)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if count(tt, "Liu") != 0 {
		t.Fatalf("card showed before the 0.6s delay")
	}
	tt.Advance(500 * time.Millisecond)
	tt.Frame()

	timeLine := "2 days ago · " + time.Date(2026, 10, 29, 14, 2, 0, 0, time.UTC).Local().Format("Jan 2, 2006 at 15:04")
	for _, want := range []string{"Liu", timeLine, "v0.4.0", "via/wivyn-55", "with Claude", richBody} {
		if !slices.Contains(tt.Texts(), want) {
			t.Errorf("card has no %q in %v", want, tt.Texts())
		}
	}
	if got := count(tt, subjectRich); got != 2 {
		t.Errorf("subject shown %d times, want the row's and the card's", got)
	}
	if !hasTextContaining(tt, a.repo.Commits[1].ShortHash) {
		t.Errorf("card has no short hash %q", a.repo.Commits[1].ShortHash)
	}
}

func TestCommitCardStripsTrailers(t *testing.T) {
	dir, _ := cardRepo(t)
	_, tt := cardApp(t, dir, 1280, 800)
	hover(t, tt, subjectRich)
	for _, noise := range []string{"Signed-off-by", "Co-Authored-By", "Co-authored-by", "noreply@anthropic.com", "ada@example.com"} {
		if hasTextContaining(tt, noise) {
			t.Errorf("card shows %q: %v", noise, tt.Texts())
		}
	}
}

func TestCommitCardDisappearsOnLeave(t *testing.T) {
	dir, _ := cardRepo(t)
	_, tt := cardApp(t, dir, 1280, 800)
	hover(t, tt, subjectRich)
	if count(tt, "Liu") == 0 {
		t.Fatalf("no card after hover")
	}
	leave(tt)
	if count(tt, "Liu") != 0 || count(tt, subjectRich) != 1 {
		t.Fatalf("card still showing after the pointer left: %v", tt.Texts())
	}
}

func TestCommitCardPassesPointerThrough(t *testing.T) {
	dir, hashes := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	r := hover(t, tt, subjectRich)
	// The card covers the area right of the row; the row is still under the
	// pointer, and a click on it expands the commit.
	author, ok := tt.Find("Liu")
	if !ok || author.X < r.X+r.W {
		t.Fatalf("card is not beside the row: row=%+v author=%+v", r, author)
	}
	if err := tt.Click(subjectRich); err != nil {
		// the first match is the row's
		t.Fatal(err)
	}
	if a.expandedCommit != hashes["rich"] {
		t.Fatalf("click on the row did not expand the commit: %q", a.expandedCommit)
	}
}

func TestCommitCardWithoutBody(t *testing.T) {
	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	hover(t, tt, subjectOldest)
	if count(tt, "Liu") == 0 || count(tt, subjectOldest) != 2 {
		t.Fatalf("card did not show: %v", tt.Texts())
	}
	if slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "with ") }) {
		t.Errorf("co-author row without co-authors: %v", tt.Texts())
	}
	if hasTextContaining(tt, richBody[:20]) {
		t.Errorf("another commit's body shows")
	}
	a.cards.drain()
	tt.Frame()
	if !slices.Contains(tt.Texts(), "1 file") || !slices.Contains(tt.Texts(), "+2") {
		t.Errorf("footer missing its size: %v", tt.Texts())
	}
}

func TestCommitCardFlipsAboveInShortWindow(t *testing.T) {
	dir, _ := cardRepo(t)

	_, tall := cardApp(t, dir, 1280, 800)
	rowTall := hover(t, tall, subjectRich)
	authorTall, ok := tall.Find("Liu")
	if !ok {
		t.Fatal("no card in the tall window")
	}
	if authorTall.Y < rowTall.Y-20 {
		t.Errorf("tall window: card above its row: row=%+v author=%+v", rowTall, authorTall)
	}

	_, short := cardApp(t, dir, 1280, 620)
	rowShort := hover(t, short, subjectRich)
	authorShort, ok := short.Find("Liu")
	if !ok {
		t.Fatal("no card in the short window")
	}
	if authorShort.Y > rowShort.Y-100 {
		t.Errorf("short window: card did not flip above its row: row=%+v author=%+v", rowShort, authorShort)
	}
	if authorShort.Y < 0 {
		t.Errorf("short window: card above the window: %+v", authorShort)
	}
}

func TestCommitCardStaysInsideNarrowWindow(t *testing.T) {
	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 820, 800)
	a.sidebarW = 540
	tt.Frame()
	hover(t, tt, subjectRich)
	a.cards.drain()
	tt.Frame()
	author, ok := tt.Find("Liu")
	if !ok {
		t.Fatal("no card")
	}
	files, ok := tt.Find("1 file")
	if !ok {
		t.Fatalf("no footer: %v", tt.Texts())
	}
	if author.X < 0 || files.X+files.W > 820 {
		t.Errorf("card leaves the window: author=%+v files=%+v", author, files)
	}
}

func TestCommitCardRendersWithoutGitOrDisk(t *testing.T) {
	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	statCalls, remoteCalls := 0, 0
	a.cards.statLoad = func(root, hash string) (commitStat, error) {
		statCalls++
		return commitStat{Files: 3, Added: 1200, Deleted: 7}, nil
	}
	a.cards.remoteLoad = func(string) string { remoteCalls++; return "" }

	hover(t, tt, subjectRich)
	tt.Frame()
	if statCalls != 0 || remoteCalls != 0 {
		t.Fatalf("frames ran Git: stat=%d remote=%d", statCalls, remoteCalls)
	}
	if hasTextContaining(tt, "files") || hasTextContaining(tt, "file") {
		t.Fatalf("footer shows a size before it loaded: %v", tt.Texts())
	}
	if !hasTextContaining(tt, a.repo.Commits[1].ShortHash) {
		t.Fatalf("footer lost its hash")
	}

	a.cards.drain()
	tt.Frame()
	for _, want := range []string{"3 files", "+1,200", "−7"} {
		if !slices.Contains(tt.Texts(), want) {
			t.Errorf("footer has no %q in %v", want, tt.Texts())
		}
	}
}

func TestCommitCardStatLoadsOncePerCommit(t *testing.T) {
	dir, hashes := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	var loaded []string
	a.cards.statLoad = func(root, hash string) (commitStat, error) {
		loaded = append(loaded, hash)
		return loadCommitStat(root, hash)
	}

	for range 3 {
		hover(t, tt, subjectRich)
		a.cards.drain()
		tt.Frame()
		leave(tt)
	}
	if len(loaded) != 1 || loaded[0] != hashes["rich"] {
		t.Fatalf("loaded %v, want the rich commit once", loaded)
	}
	hover(t, tt, subjectPlain)
	a.cards.drain()
	hover(t, tt, subjectRich)
	a.cards.drain()
	if len(loaded) != 2 || loaded[1] != hashes["plain"] {
		t.Fatalf("loaded %v, want the rich and the plain commit once each", loaded)
	}
}

func TestCommitCardFailedStatKeepsFooterToTheHash(t *testing.T) {
	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	calls := 0
	a.cards.statLoad = func(root, hash string) (commitStat, error) {
		calls++
		return commitStat{}, os.ErrNotExist
	}
	hover(t, tt, subjectRich)
	a.cards.drain()
	tt.Frame()
	leave(tt)
	hover(t, tt, subjectRich)
	a.cards.drain()
	if calls != 1 || hasTextContaining(tt, "file") {
		t.Fatalf("calls=%d texts=%v", calls, tt.Texts())
	}
}

func TestCommitCardPerfSpan(t *testing.T) {
	var got []perf.Event
	perf.SetSink(func(e perf.Event) { got = append(got, e) })
	defer perf.SetSink(nil)

	dir, _ := cardRepo(t)
	a, tt := cardApp(t, dir, 1280, 800)
	count := func() int {
		n := 0
		for _, e := range got {
			if e.Op == perf.OpCommitCard {
				if e.Result != perf.ResultOK {
					t.Errorf("result = %v", e.Result)
				}
				n++
			}
		}
		return n
	}
	if count() != 0 {
		t.Fatal("span before any card")
	}
	hover(t, tt, subjectRich)
	if count() != 1 {
		t.Fatalf("spans after the card showed = %d, want 1: %+v", count(), got)
	}
	if a.paints.Pending() {
		t.Fatal("span still pending after the card painted")
	}
	// More frames of the same card are not new cards.
	a.cards.drain()
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	tt.Frame()
	if count() != 1 {
		t.Fatalf("spans after more frames = %d, want 1", count())
	}
	leave(tt)
	hover(t, tt, subjectRich)
	if count() != 2 {
		t.Fatalf("spans after showing again = %d, want 2", count())
	}
	leave(tt)
	hover(t, tt, subjectPlain)
	if count() != 3 {
		t.Fatalf("spans for another commit = %d, want 3", count())
	}
}

func TestCommitCardShowsManyRefsAndCoAuthors(t *testing.T) {
	dir, _ := makeRepo(t, []commitSpec{
		{file: "a.txt", msg: "feat: release\n\nbody\n\nCo-authored-by: A One <a@example.com>\nCo-authored-by: B Two <b@example.com>\nCo-authored-by: C Three <c@example.com>\nCo-authored-by: D Four <d@example.com>\n", date: "2026-10-31T11:00:00+00:00"},
	})
	for _, name := range []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"} {
		testGit(t, dir, "branch", name)
	}
	testGit(t, dir, "tag", "v9")
	_, tt := cardApp(t, dir, 1280, 800)
	hover(t, tt, "feat: release")
	if !slices.Contains(tt.Texts(), "with A One, B Two and 2 others") {
		t.Errorf("co-authors label missing: %v", tt.Texts())
	}
	// main, v9 and r1 to r8 are ten refs; six get a pill of their own.
	if !slices.Contains(tt.Texts(), "+4") {
		t.Errorf("pills past the sixth should collapse into +4: %v", tt.Texts())
	}
}

func TestReflowBody(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"blank only", "\n\n  \n", ""},
		{"joins wrapped lines", "one\ntwo\nthree", "one two three"},
		{"paragraphs", "one\ntwo\n\nthree\nfour", "one two\n\nthree four"},
		{"collapses blank runs", "a\n\n\n\nb", "a\n\nb"},
		{"crlf", "a\r\nb\r\n\r\nc", "a b\n\nc"},
		{"trailing spaces", "a  \nb \t", "a b"},
		{"bullets keep breaks", "intro text\n- one\n- two\n* three\n+ four\nnext", "intro text\n- one\n- two\n* three\n+ four\nnext"},
		{"numbered", "steps\n1. first\n2) second\n10. tenth", "steps\n1. first\n2) second\n10. tenth"},
		{"bullet then wrapped prose", "- item that\nwraps here", "- item that\nwraps here"},
		{"indented code", "run:\n    go test\n\tgo vet\nthen", "run:\n    go test\n    go vet\nthen"},
		{"fenced code", "text\n```\na\n\nb\n```\nafter", "text\n```\na\n\nb\n```\nafter"},
		{"dash is not a bullet", "a\n-b\n--c", "a -b --c"},
		{"star emphasis is not a bullet", "a\n*b* c", "a *b* c"},
		{"unicode", "日本語の\n本文です。\n\n- 項目", "日本語の 本文です。\n\n- 項目"},
		{"indent of the first line is code", "   a\nb", "   a\nb"},
	}
	for _, c := range cases {
		if got := reflowBody(c.in); got != c.want {
			t.Errorf("%s: reflowBody(%q)\n got %q\nwant %q", c.name, c.in, got, c.want)
		}
	}
	long := strings.Repeat("é", cardMaxBodySize)
	if got := reflowBody(long); len(got) > cardMaxBodySize || !strings.HasPrefix(long, got) {
		t.Errorf("long body cut to %d bytes, not at a rune boundary or not a prefix", len(got))
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{-time.Hour, "just now"},
		{0, "just now"},
		{44 * time.Second, "just now"},
		{45 * time.Second, "1 minute ago"},
		{89 * time.Second, "1 minute ago"},
		{90 * time.Second, "2 minutes ago"},
		{30 * time.Minute, "30 minutes ago"},
		{59 * time.Minute, "59 minutes ago"},
		{59*time.Minute + 40*time.Second, "1 hour ago"},
		{90 * time.Minute, "2 hours ago"},
		{5 * time.Hour, "5 hours ago"},
		{23*time.Hour + 40*time.Minute, "1 day ago"},
		{30 * time.Hour, "1 day ago"},
		{36 * time.Hour, "2 days ago"},
		{46 * time.Hour, "2 days ago"},
		{4 * 24 * time.Hour, "4 days ago"},
		{29*24*time.Hour + 14*time.Hour, "1 month ago"},
		{45 * 24 * time.Hour, "1 month ago"},
		{47 * 24 * time.Hour, "2 months ago"},
		{200 * 24 * time.Hour, "7 months ago"},
		{353 * 24 * time.Hour, "1 year ago"},
		{400 * 24 * time.Hour, "1 year ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	}
	for _, c := range cases {
		if got := relativeTime(now, now.Add(-c.ago)); got != c.want {
			t.Errorf("%v ago: %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestCommitTimeLineIsEnglishInAnyLocale(t *testing.T) {
	cm := Commit{Date: "2026-10-29", Time: time.Date(2026, 10, 29, 14, 2, 0, 0, time.UTC)}
	got := commitTimeLine(cardNow, cm)
	if want := "2 days ago · " + cm.Time.Local().Format("Jan 2, 2006 at 15:04"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// In the viewer's zone, and in English whatever the locale.
	saved := time.Local
	time.Local = time.FixedZone("test", 2*3600)
	defer func() { time.Local = saved }()
	if got, want := commitTimeLine(cardNow, cm), "2 days ago · Oct 29, 2026 at 16:02"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := commitTimeLine(cardNow, Commit{Date: "2026-10-29"}); got != "2026-10-29" {
		t.Errorf("without a time: %q", got)
	}
}

func TestCardRefs(t *testing.T) {
	got := cardRefs([]string{"HEAD -> main", "tag: v1", "origin/main", "origin/HEAD", "tag: v1", "HEAD", "feature/x", ""})
	want := []cardRef{{"main", false}, {"v1", true}, {"origin/main", false}, {"HEAD", false}, {"feature/x", false}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got := cardRefs(nil); len(got) != 0 {
		t.Errorf("no refs: %+v", got)
	}
}

func TestFilesLabelAndCommaInt(t *testing.T) {
	for n, want := range map[int]string{0: "no changes", 1: "1 file", 2: "2 files", 1234: "1,234 files"} {
		if got := filesLabel(n); got != want {
			t.Errorf("filesLabel(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 12345: "12,345", 1234567: "1,234,567"} {
		if got := commaInt(n); got != want {
			t.Errorf("commaInt(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestParseNumstat(t *testing.T) {
	out := "3\t1\ta.go\n-\t-\timg.png\n10\t0\tdir/{old => new}/b.go\n\n"
	if got, want := parseNumstat(out), (commitStat{Files: 3, Added: 13, Deleted: 1}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := parseNumstat(""); got != (commitStat{}) {
		t.Errorf("empty: %+v", got)
	}
}

func TestLoadCommitStat(t *testing.T) {
	dir, hashes := cardRepo(t)
	st, err := loadCommitStat(dir, hashes["rich"])
	if err != nil || st != (commitStat{Files: 1, Added: 2}) {
		t.Fatalf("stat = %+v, %v", st, err)
	}
	if _, err := loadCommitStat(dir, "--output=/tmp/x"); err == nil {
		t.Fatal("an option passed for a hash")
	}

	// A merge counts what it brought into the branch it was made on.
	testGit(t, dir, "checkout", "-b", "side", hashes["oldest"])
	commitFile(t, dir, commitSpec{file: "side1.txt", msg: "side one\n", date: "2026-10-30T10:00:00+00:00"})
	commitFile(t, dir, commitSpec{file: "side2.txt", msg: "side two\n", date: "2026-10-30T11:00:00+00:00"})
	testGit(t, dir, "checkout", "main")
	testGit(t, dir, "merge", "--no-ff", "-m", "Merge side", "side")
	merge := testGit(t, dir, "rev-parse", "HEAD")
	st, err = loadCommitStat(dir, merge)
	if err != nil || st != (commitStat{Files: 2, Added: 4}) {
		t.Fatalf("merge stat = %+v, %v", st, err)
	}
	// An empty commit changes nothing.
	testGit(t, dir, "commit", "--allow-empty", "-m", "empty")
	st, err = loadCommitStat(dir, testGit(t, dir, "rev-parse", "HEAD"))
	if err != nil || st != (commitStat{}) {
		t.Fatalf("empty commit stat = %+v, %v", st, err)
	}
}
