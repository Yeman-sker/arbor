package app

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// longJSONLines makes n lines of JSON objects of keys keys each, as minified or
// generated JSON is: keys 120 makes lines of about 7 KB.
func longJSONLines(n, keys int) string {
	r := rand.New(rand.NewSource(1))
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("{")
		for k := 0; k < keys; k++ {
			fmt.Fprintf(&b, `"key_%d_%d":{"id":%d,"name":"value %d","tags":["a","b"]},`, i, k, k, r.Intn(99999))
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func describeTokens(line string, toks []editor.Token) string {
	var b strings.Builder
	from := 0
	for _, t := range toks {
		end := min(max(t.End, from), len(line))
		fmt.Fprintf(&b, "[%d %q]", t.Kind, line[from:end])
		from = end
	}
	return b.String()
}

func TestLongLinesStayPlain(t *testing.T) {
	tok := languageFor("x.json").Tokenizer
	long := strings.TrimSuffix(longJSONLines(1, 200), "\n")
	if len(long) <= maxTokenizedLine {
		t.Fatalf("the line is %d bytes, not long", len(long))
	}
	_, inComment := tok.Tokenize("/* open", 0, nil)
	toks, state := tok.Tokenize(long, inComment, nil)
	if len(toks) != 1 || toks[0] != (editor.Token{End: len(long), Kind: editor.TokenText}) || state != inComment {
		t.Errorf("a long line in a comment: %v, state %d; want it plain, still in the comment", toks, state)
	}
	short := `"k": 1`
	toks, _ = tok.Tokenize(short, 0, nil)
	if toks[0].Kind != editor.TokenProperty {
		t.Errorf("a short line: %s, want a key", describeTokens(short, toks))
	}
}

func TestLanguageForKeepsMyGosLanguage(t *testing.T) {
	for _, path := range []string{"a.json", "b.go", "c.ts", "README.md", "Makefile"} {
		base, got := editor.LanguageFor(path), languageFor(path)
		if base == nil {
			continue
		}
		if got != languageFor(path) {
			t.Errorf("%s: another language each time, which highlights the text again", path)
		}
		if got.Name != base.Name || got.LineComment != base.LineComment || len(got.Brackets) != len(base.Brackets) {
			t.Errorf("%s: %q, want MyGo's %q with the same comments and brackets", path, got.Name, base.Name)
		}
	}
	if languageFor("notes.unknown-extension") != nil {
		t.Error("a file of no language has one")
	}
	// Short lines color as MyGo colors them.
	line := `func main() { fmt.Println("hi") } // x`
	want, _ := editor.LanguageFor("a.go").Tokenizer.Tokenize(line, 0, nil)
	got, _ := languageFor("a.go").Tokenizer.Tokenize(line, 0, nil)
	if w, g := describeTokens(line, want), describeTokens(line, got); w != g {
		t.Errorf("Go:\nMyGo: %s\nours: %s", w, g)
	}
}

// TestPacedHighlighterKeepsBudget gives the highlighter lines just short enough
// to color, in a language whose tokenizer is MyGo's. MyGo's highlighter looks at
// the clock every 64 lines, which here is half a megabyte: 5.5 ms for a 4 ms
// budget with JavaScript's tokenizer, 630 ms with MyGo's JSON one. Ours must
// stay near the budget, and color as MyGo's does when done.
func TestPacedHighlighterKeepsBudget(t *testing.T) {
	text := longJSONLines(400, 140) // about 8 KB a line
	buf := editor.NewBuffer(text)
	lang := languageFor("x.js")
	hl := lang.Highlighter(buf.Snapshot(), func() {})
	const budget = 4 * time.Millisecond
	var worst time.Duration
	calls := 0
	for {
		calls++
		start := time.Now()
		done := hl.Highlight(399, budget)
		worst = max(worst, time.Since(start))
		if done {
			break
		}
		if calls > 10000 {
			t.Fatal("the highlighter never finished")
		}
	}
	if worst > budget+4*time.Millisecond {
		t.Errorf("a pass took %v of a %v budget", worst, budget)
	}
	if calls < 2 {
		t.Errorf("3 MB highlighted in one pass of %v: the test's text is too short to tell", budget)
	}
	ref := editor.NewTokenHighlighter(longLineTokenizer{editor.LanguageFor("x.js").Tokenizer}, buf.Snapshot())
	for !ref.Highlight(399, time.Second) {
	}
	snap := buf.Snapshot()
	for _, i := range []int{0, 1, 63, 64, 200, 399} {
		line := snap.Line(i)
		want, _ := ref.Tokens(i, line, nil)
		got, ok := hl.Tokens(i, line, nil)
		if !ok || describeTokens(line, want) != describeTokens(line, got) {
			t.Errorf("line %d colors otherwise than MyGo's highlighter colors it (final %v)", i, ok)
		}
	}
}

// TestLongJSONScrollsWithinAFrame scrolls an editor showing long JSON lines, as
// the Files view and the diff show them: in ARB-12 such frames took 300 to 575
// ms in the app. The tester draws on the CPU, so the bound is loose.
func TestLongJSONScrollsWithinAFrame(t *testing.T) {
	st := &editor.State{Buffer: editor.NewBuffer(""), ReadOnly: true}
	st.Buffer.SetLanguage(languageFor("x.json"))
	st.Buffer.Reset(longJSONLines(800, 120))
	tt := ui.NewTester(func(c *ui.Context) { editor.View(c, st).Grow(1).FillWidth() }, 1000, 780)
	tt.Frame()
	var worst time.Duration
	for i := 0; i < 30; i++ {
		start := time.Now()
		tt.Scroll(500, 400, 0, 40)
		tt.Frame()
		worst = max(worst, time.Since(start))
	}
	if worst > 16*time.Millisecond {
		t.Errorf("the slowest scroll took %v", worst)
	}
}
