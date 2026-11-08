package app

import (
	"sync"
	"time"

	"github.com/egoist/mygo/ui/editor"
)

// Syntax highlighting that keeps scrolling smooth in files with long lines
// (ARB-12). The editor highlights the lines it is about to show within a frame's
// budget, but MyGo's highlighter lets a file whose lines run to kilobytes, as
// minified or generated JSON does, overrun that budget many times over:
//
//   - it looks at the clock only every 64 lines, which for long lines can be
//     megabytes later;
//   - it tokenizes lines however long, to know the state the next one starts in,
//     although lines over 10,000 bytes show plain anyway.
//
// languageFor gives the editors MyGo's language with lines too long to color left
// plain, and a highlighter that hands the work over in pieces small enough for the
// budget to hold. Both go once MyGo's highlighter does this itself (the change is
// proposed upstream); MyGo's JSON tokenizer, quadratic in a line's keys before
// v0.8.4, is linear since.

// maxTokenizedLine is the longest line given colors, MyGo's own limit for the
// lines it shows: past it a line shows plain whatever the tokenizer says.
const maxTokenizedLine = 10000

// highlightChunkBytes is how much text the highlighter is asked to tokenize
// before the budget is looked at again: well under a millisecond of lexing.
const highlightChunkBytes = 16 << 10

// smoothLanguages are the languages languageFor made, by MyGo's language. The
// same one must come back each time: a buffer given another language highlights
// its text again from the start.
var smoothLanguages sync.Map // *editor.Language -> *editor.Language

// languageFor returns the language of a file for the editors to highlight it
// with: MyGo's, changed as described above. A language MyGo highlights with a
// highlighter of its own, or not at all, is MyGo's as it is.
func languageFor(path string) *editor.Language {
	base := editor.LanguageFor(path)
	if base == nil || base.Highlighter != nil || base.Tokenizer == nil {
		return base
	}
	if l, ok := smoothLanguages.Load(base); ok {
		return l.(*editor.Language)
	}
	tok := longLineTokenizer{base.Tokenizer}
	l := *base
	l.Tokenizer = tok
	l.Highlighter = func(text editor.Snapshot, _ func()) editor.Highlighter {
		return &pacedHighlighter{inner: editor.NewTokenHighlighter(tok, text), text: text}
	}
	got, _ := smoothLanguages.LoadOrStore(base, &l)
	return got.(*editor.Language)
}

// longLineTokenizer leaves lines longer than maxTokenizedLine plain, and the state
// the next line starts in as this one started: a block comment or string open
// before such a line is still open after it.
type longLineTokenizer struct{ editor.Tokenizer }

func (t longLineTokenizer) Tokenize(line string, state uint64, tokens []editor.Token) ([]editor.Token, uint64) {
	if len(line) > maxTokenizedLine {
		return append(tokens, editor.Token{End: len(line), Kind: editor.TokenText}), state
	}
	return t.Tokenizer.Tokenize(line, state, tokens)
}

// pacedHighlighter is MyGo's highlighter given the lines to tokenize a piece at a
// time, so that the budget of a frame is looked at every highlightChunkBytes
// rather than every 64 lines, however long they are.
type pacedHighlighter struct {
	inner editor.Highlighter
	text  editor.Snapshot
	// ready is how many lines from the top were highlighted with no edit since:
	// those the inner highlighter is not given a piece at a time. It only paces
	// the work; what is right is the inner highlighter's to know.
	ready int
}

func (h *pacedHighlighter) Edit(old, new editor.Snapshot, changes []editor.Change) {
	h.inner.Edit(old, new, changes)
	h.text = new
	for _, c := range changes {
		h.ready = min(h.ready, new.LineOf(c.NewStart))
	}
}

func (h *pacedHighlighter) Highlight(last int, budget time.Duration) bool {
	start := time.Now()
	last = min(last, h.text.LineCount()-1)
	for h.ready <= last {
		left := budget - time.Since(start)
		if left <= 0 {
			return false
		}
		upTo, bytes := h.ready, 0
		for upTo < last && bytes < highlightChunkBytes {
			bytes += min(h.text.LineEnd(upTo)-h.text.LineStart(upTo), maxTokenizedLine)
			upTo++
		}
		if !h.inner.Highlight(upTo, left) {
			return false
		}
		h.ready = upTo + 1
	}
	// Every line up to last is highlighted: this only says so.
	return h.inner.Highlight(last, budget)
}

func (h *pacedHighlighter) Tokens(i int, text string, dst []editor.Token) ([]editor.Token, bool) {
	return h.inner.Tokens(i, text, dst)
}
