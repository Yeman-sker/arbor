package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// logRecord builds a record of the log format from its fields in order: hash,
// parents, short hash, author, email, short date, strict date, refs, subject,
// body, trailer block, co-author values.
func logRecord(f ...string) string {
	return strings.Join(f, "\x1f") + "\x1e"
}

func TestParseCommitLogFields(t *testing.T) {
	out := logRecord("aaaa", "bbbb cccc", "aaa", "Ada Lovelace", "ada@example.com", "2026-10-29", "2026-10-29T14:02:03+02:00",
		"HEAD -> main, tag: v1, origin/main", "feat: add\tthing", "line one\nline two\n", "", "")
	cms := parseCommitLog(out)
	if len(cms) != 1 {
		t.Fatalf("parsed %d commits, want 1", len(cms))
	}
	cm := cms[0]
	if cm.Hash != "aaaa" || cm.ShortHash != "aaa" || cm.Author != "Ada Lovelace" || cm.Email != "ada@example.com" || cm.Date != "2026-10-29" {
		t.Errorf("fields: %+v", cm)
	}
	if want := []string{"bbbb", "cccc"}; !reflect.DeepEqual(cm.Parents, want) {
		t.Errorf("parents = %v, want %v", cm.Parents, want)
	}
	if want := []string{"HEAD -> main", "tag: v1", "origin/main"}; !reflect.DeepEqual(cm.Refs, want) {
		t.Errorf("refs = %v, want %v", cm.Refs, want)
	}
	if cm.Subject != "feat: add\tthing" {
		t.Errorf("subject = %q: a tab belongs to it", cm.Subject)
	}
	if cm.Body != "line one\nline two" {
		t.Errorf("body = %q", cm.Body)
	}
	wantTime := time.Date(2026, 10, 29, 12, 2, 3, 0, time.UTC)
	if !cm.Time.Equal(wantTime) {
		t.Errorf("time = %v, want the instant %v", cm.Time, wantTime)
	}
	if _, off := cm.Time.Zone(); off != 2*3600 {
		t.Errorf("the author's offset was lost: %v", cm.Time)
	}
}

func TestParseCommitLogRecords(t *testing.T) {
	t.Run("separators and newlines between records", func(t *testing.T) {
		out := logRecord("h1", "", "h1", "A", "a@x", "d", "2026-01-01T00:00:00Z", "", "first", "", "", "") + "\n" +
			logRecord("h2", "h1", "h2", "B", "b@x", "d", "2026-01-02T00:00:00Z", "", "second", "", "", "") + "\n"
		cms := parseCommitLog(out)
		if len(cms) != 2 || cms[0].Hash != "h1" || cms[1].Hash != "h2" {
			t.Fatalf("got %+v", cms)
		}
		if cms[0].Parents != nil {
			t.Errorf("root commit has parents %v", cms[0].Parents)
		}
	})
	t.Run("empty body and empty output", func(t *testing.T) {
		if cms := parseCommitLog(""); len(cms) != 0 {
			t.Errorf("empty output: %+v", cms)
		}
		cms := parseCommitLog(logRecord("h", "", "h", "A", "a@x", "d", "2026-01-01T00:00:00Z", "", "s", "\n", "", ""))
		if len(cms) != 1 || cms[0].Body != "" || cms[0].CoAuthors != nil {
			t.Errorf("got %+v", cms)
		}
	})
	t.Run("tabs and newlines in the body", func(t *testing.T) {
		body := "col1\tcol2\n\n\tindented\n\nlast\n"
		cms := parseCommitLog(logRecord("h", "", "h", "A", "a@x", "d", "2026-01-01T00:00:00Z", "", "s", body, "", ""))
		if len(cms) != 1 || cms[0].Body != "col1\tcol2\n\n\tindented\n\nlast" {
			t.Errorf("got %+v", cms)
		}
	})
	t.Run("unicode", func(t *testing.T) {
		cms := parseCommitLog(logRecord("h", "", "h", "Zoë 李雷 🙂", "zoe@example.com", "d", "2026-01-01T00:00:00Z",
			"tag: 版本-1", "修复：café ☕ 日本語", "本文は\n日本語です 🎉\n", "", ""))
		cm := cms[0]
		if cm.Author != "Zoë 李雷 🙂" || cm.Subject != "修复：café ☕ 日本語" || cm.Body != "本文は\n日本語です 🎉" || cm.Refs[0] != "tag: 版本-1" {
			t.Errorf("got %+v", cm)
		}
	})
	t.Run("merge commit", func(t *testing.T) {
		cms := parseCommitLog(logRecord("m", "p1 p2", "m", "A", "a@x", "d", "2026-01-01T00:00:00Z", "", "Merge branch 'side'", "", "", ""))
		if len(cms) != 1 || len(cms[0].Parents) != 2 {
			t.Errorf("got %+v", cms)
		}
	})
	t.Run("bad date and short records", func(t *testing.T) {
		out := logRecord("h", "", "h", "A", "a@x", "d", "not a date", "", "s", "", "", "") + "h2\x1fonly\x1fthree\x1e"
		cms := parseCommitLog(out)
		if len(cms) != 1 || !cms[0].Time.IsZero() {
			t.Errorf("got %+v", cms)
		}
	})
}

func TestSplitBody(t *testing.T) {
	cases := []struct {
		name, body, block, co string
		wantBody              string
		wantCo                []CoAuthor
	}{
		{
			name:     "trailer block taken out",
			body:     "Text\n\nSigned-off-by: A <a@x>\nCo-Authored-By: Claude <noreply@anthropic.com>\n",
			block:    "Signed-off-by: A <a@x>\nCo-Authored-By: Claude <noreply@anthropic.com>\n",
			co:       "Claude <noreply@anthropic.com>",
			wantBody: "Text",
			wantCo:   []CoAuthor{{"Claude", "noreply@anthropic.com"}},
		},
		{
			name:     "only trailers",
			body:     "Co-authored-by: B <b@x>\n",
			block:    "Co-authored-by: B <b@x>\n",
			co:       "B <b@x>",
			wantBody: "",
			wantCo:   []CoAuthor{{"B", "b@x"}},
		},
		{
			name:     "block with a Fixes line goes whole when it has a sign-off",
			body:     "Text\n\nFixes: #12\nSigned-off-by: A <a@x>\n",
			block:    "Fixes: #12\nSigned-off-by: A <a@x>\n",
			wantBody: "Text",
		},
		{
			name:     "a block of unknown keys is prose",
			body:     "Text\n\nNote: this is not a trailer\n",
			block:    "Note: this is not a trailer\n",
			wantBody: "Text\n\nNote: this is not a trailer",
		},
		{
			name:     "Fixes alone stays",
			body:     "Text\n\nFixes: #12\n",
			block:    "Fixes: #12\n",
			wantBody: "Text\n\nFixes: #12",
		},
		{
			name:     "sign-off glued to the last paragraph",
			body:     "Text\nmore\nCo-authored-by: C <c@x>\nSigned-off-by: A <a@x>\n",
			wantBody: "Text\nmore",
			wantCo:   []CoAuthor{{"C", "c@x"}},
		},
		{
			name:     "several co-authors, once each",
			body:     "T\n\nCo-authored-by: A <a@x>\nCo-authored-by: B <b@x>\n",
			block:    "Co-authored-by: A <a@x>\nCo-authored-by: B <b@x>\n",
			co:       "A <a@x>\x1dB <b@x>",
			wantBody: "T",
			wantCo:   []CoAuthor{{"A", "a@x"}, {"B", "b@x"}},
		},
		{
			name:     "old Git leaves the placeholders",
			body:     "Text",
			block:    "%(trailers)",
			co:       "%(trailers:key=Co-authored-by,valueonly,unfold,separator=%x1d)",
			wantBody: "Text",
		},
		{
			name:     "crlf",
			body:     "Text\r\n\r\nSigned-off-by: A <a@x>\r\n",
			block:    "Signed-off-by: A <a@x>\r\n",
			wantBody: "Text",
		},
		{
			name:     "trailer in the middle stays",
			body:     "Intro\nSigned-off-by: A <a@x>\n\nconclusion",
			wantBody: "Intro\nSigned-off-by: A <a@x>\n\nconclusion",
		},
	}
	for _, c := range cases {
		body, co := splitBody(c.body, c.block, c.co)
		if body != c.wantBody {
			t.Errorf("%s: body %q, want %q", c.name, body, c.wantBody)
		}
		if !reflect.DeepEqual(co, c.wantCo) {
			t.Errorf("%s: co-authors %+v, want %+v", c.name, co, c.wantCo)
		}
	}
}

func TestParseCoAuthor(t *testing.T) {
	cases := map[string]CoAuthor{
		"Claude <noreply@anthropic.com>":               {"Claude", "noreply@anthropic.com"},
		"  Zoë  <12345+zoe@users.noreply.github.com> ": {"Zoë", "12345+zoe@users.noreply.github.com"},
		"<solo@example.com>":                           {"solo", "solo@example.com"},
		"Just A Name":                                  {"Just A Name", ""},
		"Odd <name> <real@example.com>":                {"Odd <name>", "real@example.com"},
		"":                                             {},
	}
	for in, want := range cases {
		if got := parseCoAuthor(in); got != want {
			t.Errorf("parseCoAuthor(%q) = %+v, want %+v", in, got, want)
		}
	}
}

// LoadRepo against a real repository: tabs and newlines in messages, unicode,
// an empty body, a root and a merge commit, trailers and co-authors.
func TestLoadRepoParsesRealHistory(t *testing.T) {
	dir, _ := makeRepo(t, []commitSpec{
		{file: "a.txt", msg: "chore: root\n", date: "2026-10-27T10:00:00+00:00"},
		{file: "b.txt", msg: "feat:\ttabbed subject\n\ncol1\tcol2\n\n\tindented line\n\nSecond paragraph\nwrapped.\n\nSigned-off-by: Ada <ada@example.com>\nCo-authored-by: Zoë Ångström <12345+zoe@users.noreply.github.com>\nCo-Authored-By: Claude <noreply@anthropic.com>\n",
			date: "2026-10-28T23:30:00-05:00", author: "Zoë 李雷 <zoe@example.com>"},
		{file: "c.txt", msg: "修复：café ☕\n\n日本語の本文 🎉\n", date: "2026-10-29T08:00:00+09:00", tag: "v1"},
	})
	testGit(t, dir, "checkout", "-q", "-b", "side", "HEAD~1")
	commitFile(t, dir, commitSpec{file: "s.txt", msg: "side work\n", date: "2026-10-30T10:00:00+00:00"})
	testGit(t, dir, "checkout", "-q", "main")
	testGit(t, dir, "merge", "--no-ff", "-m", "Merge branch 'side'", "side")

	snap, err := LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Commits) != 5 {
		t.Fatalf("%d commits, want 5", len(snap.Commits))
	}
	if len(snap.Graph) != 5 {
		t.Fatalf("graph has %d rows", len(snap.Graph))
	}
	by := map[string]Commit{}
	for _, cm := range snap.Commits {
		by[cm.Subject] = cm
	}

	merge := by["Merge branch 'side'"]
	if len(merge.Parents) != 2 || merge.Body != "" || len(merge.CoAuthors) != 0 {
		t.Errorf("merge: %+v", merge)
	}
	if root := by["chore: root"]; len(root.Parents) != 0 || root.Body != "" || root.Email != "liu@example.com" || root.Author != "Liu" {
		t.Errorf("root: %+v", root)
	}

	tabbed := by["feat:\ttabbed subject"]
	if tabbed.Author != "Zoë 李雷" || tabbed.Email != "zoe@example.com" {
		t.Errorf("author: %+v", tabbed)
	}
	if want := "col1\tcol2\n\n\tindented line\n\nSecond paragraph\nwrapped."; tabbed.Body != want {
		t.Errorf("body %q, want %q", tabbed.Body, want)
	}
	wantCo := []CoAuthor{{"Zoë Ångström", "12345+zoe@users.noreply.github.com"}, {"Claude", "noreply@anthropic.com"}}
	if !reflect.DeepEqual(tabbed.CoAuthors, wantCo) {
		t.Errorf("co-authors %+v, want %+v", tabbed.CoAuthors, wantCo)
	}
	if want := time.Date(2026, 10, 29, 4, 30, 0, 0, time.UTC); !tabbed.Time.Equal(want) {
		t.Errorf("time %v is not the instant %v", tabbed.Time, want)
	}
	if tabbed.Date != "2026-10-28" {
		t.Errorf("short date %q: the list still shows the author's own date", tabbed.Date)
	}

	jp := by["修复：café ☕"]
	if jp.Body != "日本語の本文 🎉" || len(jp.Refs) == 0 {
		t.Errorf("unicode commit: %+v", jp)
	}
	foundTag := false
	for _, r := range jp.Refs {
		foundTag = foundTag || r == "tag: v1"
	}
	if !foundTag {
		t.Errorf("refs %v lack the tag", jp.Refs)
	}
}
