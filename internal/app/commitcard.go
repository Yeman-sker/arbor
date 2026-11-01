package app

import (
	"arbor/internal/git"
	"arbor/perf"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// The hover card of a graph row: who made the commit and when, its message,
// refs and size. It is built from what the history query already loaded; the
// size and the avatars arrive later from background jobs, and nothing in the
// card's render path runs Git, reads the disk or waits for the network.

const (
	cardWidth       = 380
	cardSideGap     = 10 // between the row and the card beside it
	cardEdgeGap     = 8  // the card keeps this far from the window's edge
	cardBodyLines   = 10
	cardMaxRefs     = 6
	cardMaxBodySize = 6000
	cardMaxStats    = 2048
)

// commitStat is how much a commit changed.
type commitStat struct {
	Files   int
	Added   int
	Deleted int
}

type statState uint8

const (
	statLoading statState = iota + 1
	statReady
	statFailed
)

type statEntry struct {
	state statState
	stat  commitStat
}

type remoteState uint8

const (
	remoteLoading remoteState = iota + 1
	remoteGitHub
	remoteOther
)

// cardRemote is what is known of the origin remote of the repository at root.
type cardRemote struct {
	root  string
	state remoteState
	repo  githubRepo
}

// commitCards is the per-app state of the hover cards. The zero value works:
// it shows initials and loads sizes with Git. Only the UI thread touches it;
// background jobs hand their results over through the window.
type commitCards struct {
	// Wiring. A nil settings means the defaults, a nil avatars cache means
	// no avatars, and the other functions default to the real thing; tests
	// replace them.
	settings   *settingsStore
	avatars    *avatarCache
	now        func() time.Time
	statLoad   func(root, hash string) (commitStat, error)
	remoteLoad func(root string) string

	stats  map[string]statEntry
	remote cardRemote
	// shown is the commit whose card the last frame showed, for the
	// performance span of the card's first frame.
	shown string
	// deferred are the jobs of an app without a window, which has no thread
	// to run them on: drain runs them.
	deferred []func() func()
	// stopped is set when the window closes: the jobs that have not started
	// do not, and what the others find is dropped.
	stopped atomic.Bool
}

// useUserServices connects the app to the user's settings file and the
// avatar cache in the user data directory.
func (a *app) useUserServices() {
	a.cards.settings = sharedSettings()
	a.cards.avatars = sharedAvatars()
}

func (cc *commitCards) clock() time.Time {
	if cc.now != nil {
		return cc.now()
	}
	return time.Now()
}

func (cc *commitCards) avatarsEnabled() bool {
	return cc.avatars != nil && (cc.settings == nil || cc.settings.GitHubAvatars())
}

// spawn runs job off the UI thread and publishes the function it returns on
// the UI thread, through post (the window's Update). Without a window the job
// waits for drain. A closed window starts no job, and drops the result of one
// that was running: Window.Update runs what it is given whether the window is
// there or not.
func (cc *commitCards) spawn(post func(func()), job func() func()) {
	if post == nil {
		cc.deferred = append(cc.deferred, job)
		return
	}
	go func() {
		if cc.stopped.Load() {
			return
		}
		publish := job()
		if publish == nil {
			return
		}
		post(func() {
			if !cc.stopped.Load() {
				publish()
			}
		})
	}()
}

// stop ends the jobs of the cards, for a window that has closed.
func (cc *commitCards) stop() { cc.stopped.Store(true) }

// drain runs the jobs of an app without a window, and publishes what they
// return, as the window would.
func (cc *commitCards) drain() {
	for len(cc.deferred) > 0 {
		job := cc.deferred[0]
		cc.deferred = cc.deferred[1:]
		if publish := job(); publish != nil {
			publish()
		}
	}
}

// stat returns the size of a commit once it has loaded, and starts loading it
// the first time it is asked for. Commits never change, so it loads once.
func (cc *commitCards) stat(post func(func()), root, hash string) (commitStat, bool) {
	key := root + "\x00" + hash
	if e, ok := cc.stats[key]; ok {
		return e.stat, e.state == statReady
	}
	if cc.stats == nil || len(cc.stats) >= cardMaxStats {
		cc.stats = make(map[string]statEntry)
	}
	cc.stats[key] = statEntry{state: statLoading}
	load := cc.statLoad
	if load == nil {
		load = loadCommitStat
	}
	cc.spawn(post, func() func() {
		st, err := load(root, hash)
		return func() {
			if err != nil {
				cc.stats[key] = statEntry{state: statFailed}
				return
			}
			cc.stats[key] = statEntry{state: statReady, stat: st}
		}
	})
	return commitStat{}, false
}

// githubRepo returns the GitHub repository the origin remote of the
// repository at root points to, once looked up, and starts looking it up the
// first time it is asked for.
func (cc *commitCards) githubRepo(post func(func()), root string) (githubRepo, bool) {
	if cc.remote.root != root || cc.remote.state == 0 {
		cc.remote = cardRemote{root: root, state: remoteLoading}
		load := cc.remoteLoad
		if load == nil {
			load = loadOriginURL
		}
		cc.spawn(post, func() func() {
			repo, ok := parseGitHubRemote(load(root))
			return func() {
				if cc.remote.root != root {
					return
				}
				if ok {
					cc.remote = cardRemote{root: root, state: remoteGitHub, repo: repo}
				} else {
					cc.remote = cardRemote{root: root, state: remoteOther}
				}
			}
		})
	}
	return cc.remote.repo, cc.remote.state == remoteGitHub
}

// noteShown reports whether the card of hash is new in this frame.
func (cc *commitCards) noteShown(hash string) bool {
	if cc.shown == hash {
		return false
	}
	cc.shown = hash
	return true
}

func (cc *commitCards) noteHidden(hash string) {
	if cc.shown == hash {
		cc.shown = ""
	}
}

// loadCommitStat counts the files and lines a commit changed against its
// first parent, which for a merge is what it brought in. numstat rather than
// shortstat: Git translates the latter's summary line.
func loadCommitStat(root, hash string) (commitStat, error) {
	if !isHexHash(hash) {
		return commitStat{}, fmt.Errorf("not a commit hash")
	}
	out, err := git.Run(root, "show", "--first-parent", "--numstat", "--format=", hash)
	if err != nil {
		return commitStat{}, err
	}
	return parseNumstat(out), nil
}

func parseNumstat(out string) commitStat {
	var st commitStat
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(parts) < 3 {
			continue
		}
		st.Files++
		// Binary files show "-" for both counts.
		if n, err := strconv.Atoi(parts[0]); err == nil {
			st.Added += n
		}
		if n, err := strconv.Atoi(parts[1]); err == nil {
			st.Deleted += n
		}
	}
	return st
}

func isHexHash(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func loadOriginURL(root string) string {
	out, err := git.Run(root, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// commitCard shows the hover card of cm beside its graph row.
func (a *app) commitCard(c *ui.Context, t *ui.Theme, row ui.Element, cm git.Commit) {
	// The old tip also described the row to assistive technology; the card
	// itself is not read.
	row.Description(cm.ShortHash + " · " + cm.Subject)
	shown := false
	ui.TooltipBase(c, row, func(tip ui.Element) {
		shown = true
		a.commitCardContent(c, t, row, tip, cm)
	})
	if !shown {
		a.cards.noteHidden(cm.Hash)
	}
}

func (a *app) commitCardContent(c *ui.Context, t *ui.Theme, row, tip ui.Element, cm git.Commit) {
	cc := &a.cards
	if cc.noteShown(cm.Hash) {
		// Ends when the card, with everything that is not loaded later, is
		// painted.
		a.paints.Arm(perf.Begin(perf.OpCommitCard), perf.ResultOK)
	}
	probe := a.paints.Pending()

	root := ""
	if a.repo != nil {
		root = a.repo.Root
	}
	stat, haveStat := cc.stat(a.live.post, root, cm.Hash)

	shadow := ui.RGBA(0, 0, 0, 0.14)
	if t.Dark {
		shadow = ui.RGBA(0, 0, 0, 0.45)
	}
	if w, _ := c.Size(); a.sidebarW+cardSideGap+cardWidth+cardEdgeGap <= w {
		tip.AttachTo(row, ui.AnchorTopRight, ui.AnchorTopLeft).Margin(0, 0, 0, cardSideGap)
	} else {
		// A wide sidebar in a narrow window leaves no room beside the row,
		// and the window would cut the card off: go under the row.
		tip.AttachTo(row, ui.AnchorBottomLeft, ui.AnchorTopLeft).Margin(4, 0, 0, 0)
	}
	tip.Width(cardWidth).Radius(10).Background(t.Surface).Border(1, t.Border).
		Shadow(0, 10, 28, 0, shadow).TextColor(t.Text).FontSize(13)
	tip.Children(func() {
		ui.Column(c).Padding(14, 16, 12, 16).Gap(10).Children(func() {
			a.cardHeader(c, t, cm)
			ui.Text(c, cm.Subject).FontSize(14).FontWeight(500).LineHeight(1.35).MaxLines(4)
			if body := reflowBody(cm.Body); body != "" {
				ui.Text(c, body).FontSize(12.5).TextColor(t.Text.Alpha(0.78)).LineHeight(1.5).MaxLines(cardBodyLines)
			}
			a.cardCoAuthors(c, t, cm)
			cardRefPills(c, t, cm.Refs)
		})
		ui.Row(c).Padding(8, 16).Gap(10).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, IconGitCommit).Size(12, 12).Shrink(0).TextColor(t.TextMuted)
			ui.Text(c, cm.ShortHash).Font("monospace").FontSize(11.5).TextColor(t.TextMuted).SingleLine()
			ui.Spacer(c)
			if haveStat {
				cardStatText(c, t, stat)
			}
		})
		if probe {
			ui.Box(c).Absolute().Width(1).Height(1).Draw(func(*ui.Painter, ui.Rect) { a.paints.Painted() })
		}
	})
}

func (a *app) cardHeader(c *ui.Context, t *ui.Theme, cm git.Commit) {
	name := cm.Author
	if name == "" {
		name = cm.Email
	}
	if name == "" {
		name = "Unknown author"
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Avatar(c, name, a.cardAvatar(cm.Email, cm.Hash, true)).Size(32, 32).Shrink(0)
		ui.Column(c).Gap(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, name).FontSize(13).FontWeight(500).SingleLine()
			ui.Text(c, commitTimeLine(a.cards.clock(), cm)).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
		})
	})
}

func (a *app) cardCoAuthors(c *ui.Context, t *ui.Theme, cm git.Commit) {
	cos := visibleCoAuthors(cm)
	if len(cos) == 0 {
		return
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(2).Shrink(0).Children(func() {
			for _, co := range cos[:min(len(cos), 3)] {
				ui.Avatar(c, co.Name, a.cardAvatar(co.Email, "", false)).Size(18, 18).Shrink(0)
			}
		})
		ui.Text(c, coAuthorsLabel(cos)).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
	})
}

// cardAvatar returns the GitHub avatar of a person when it has loaded; nil
// shows initials. sha is the commit to ask the GitHub API about, for an author
// whose address does not name their account.
func (a *app) cardAvatar(email, sha string, author bool) *ui.Bitmap {
	cc := &a.cards
	if email == "" || !cc.avatarsEnabled() || a.repo == nil {
		return nil
	}
	repo, ok := cc.githubRepo(a.live.post, a.repo.Root)
	if !ok {
		return nil
	}
	req := avatarRequest{Email: email}
	if author {
		req.Owner, req.Repo, req.SHA = repo.Owner, repo.Name, sha
	}
	return cc.avatars.Lookup(req)
}

func cardRefPills(c *ui.Context, t *ui.Theme, raw []string) {
	refs := cardRefs(raw)
	if len(refs) == 0 {
		return
	}
	ui.Row(c).Gap(4).Wrap().Children(func() {
		for i, r := range refs {
			if i == cardMaxRefs {
				ui.Row(c).Padding(1.5, 6).Radius(4).Background(t.TextMuted.Alpha(0.13)).Children(func() {
					ui.Textf(c, "+%d", len(refs)-cardMaxRefs).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
				})
				break
			}
			col, icon := t.Accent, IconGitBranch
			if r.Tag {
				col, icon = t.Warning, IconTag
			}
			ui.Row(c).Gap(3).AlignItems(ui.Center).Padding(1.5, 6).Radius(4).MaxWidth(cardWidth - 32).
				Background(col.Alpha(0.13)).Children(func() {
				ui.Icon(c, icon).Size(10, 10).Shrink(0).TextColor(col)
				ui.Text(c, r.Name).FontSize(11).FontWeight(500).TextColor(col).SingleLine().Shrink(1).MinWidth(0)
			})
		}
	})
}

// cardStatText shows "N files +A −D"; counts of zero are left out.
func cardStatText(c *ui.Context, t *ui.Theme, st commitStat) {
	ui.Text(c, filesLabel(st.Files)).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
	if st.Added > 0 {
		ui.Text(c, "+"+commaInt(st.Added)).Font("monospace").FontSize(11.5).TextColor(t.Success).SingleLine()
	}
	if st.Deleted > 0 {
		ui.Text(c, "−"+commaInt(st.Deleted)).Font("monospace").FontSize(11.5).TextColor(t.Danger).SingleLine()
	}
}

func filesLabel(n int) string {
	switch n {
	case 0:
		return "no changes"
	case 1:
		return "1 file"
	}
	return commaInt(n) + " files"
}

func commaInt(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// commitTimeLine is "2 days ago · Oct 29, 2026 at 14:02", in English whatever
// the locale and in the viewer's time zone.
func commitTimeLine(now time.Time, cm git.Commit) string {
	if cm.Time.IsZero() {
		return cm.Date
	}
	return relativeTime(now, cm.Time) + " · " + cm.Time.Local().Format("Jan 2, 2006 at 15:04")
}

// relativeTime says how long before now t was, rounded to the nearest unit
// rather than to calendar days: 46 hours are 2 days, not "yesterday".
func relativeTime(now, t time.Time) string {
	d := now.Sub(t)
	if d < 45*time.Second {
		return "just now"
	}
	ago := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(n) + " " + unit + "s ago"
	}
	round := func(x float64) int { return max(int(math.Round(x)), 1) }
	if m := round(d.Minutes()); m < 60 {
		return ago(m, "minute")
	}
	if h := round(d.Hours()); h < 24 {
		return ago(h, "hour")
	}
	days := d.Hours() / 24
	if n := round(days); n < 30 {
		return ago(n, "day")
	}
	if n := round(days / 30.4375); n < 12 {
		return ago(n, "month")
	}
	return ago(round(days/365.25), "year")
}

type cardRef struct {
	Name string
	Tag  bool
}

// cardRefs turns the decorations Git prints ("HEAD -> main", "tag: v1") into
// names for pills. Remote HEADs are noise and left out.
func cardRefs(raw []string) []cardRef {
	var out []cardRef
	seen := map[cardRef]bool{}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		var ref cardRef
		switch {
		case strings.HasPrefix(r, "tag: "):
			ref = cardRef{Name: strings.TrimSpace(strings.TrimPrefix(r, "tag: ")), Tag: true}
		case strings.HasPrefix(r, "HEAD -> "):
			ref = cardRef{Name: strings.TrimSpace(strings.TrimPrefix(r, "HEAD -> "))}
		default:
			ref = cardRef{Name: r}
		}
		if ref.Name == "" || strings.HasSuffix(ref.Name, "/HEAD") || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// visibleCoAuthors are the co-authors other than the author.
func visibleCoAuthors(cm git.Commit) []git.CoAuthor {
	var out []git.CoAuthor
	for _, co := range cm.CoAuthors {
		if co.Email != "" && strings.EqualFold(co.Email, cm.Email) {
			continue
		}
		if co.Name == "" {
			continue
		}
		out = append(out, co)
	}
	return out
}

// coAuthorsLabel is "with A", "with A and B", "with A, B and C" and, past
// three, "with A, B and 2 others".
func coAuthorsLabel(cos []git.CoAuthor) string {
	names := make([]string, 0, 3)
	for _, co := range cos[:min(len(cos), 3)] {
		names = append(names, co.Name)
	}
	switch {
	case len(cos) == 1:
		return "with " + names[0]
	case len(cos) == 2:
		return "with " + names[0] + " and " + names[1]
	case len(cos) == 3:
		return "with " + names[0] + ", " + names[1] + " and " + names[2]
	}
	return fmt.Sprintf("with %s, %s and %d others", names[0], names[1], len(cos)-2)
}

var listItemRe = regexp.MustCompile(`^\s*(?:[-*+•]|\d{1,3}[.)])\s+\S`)

// reflowBody joins the lines a message was wrapped at into paragraphs: a
// single newline becomes a space and a blank line stays a paragraph break.
// Lines that start a list item, indented lines and fenced code keep their
// line breaks.
func reflowBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if len(body) > cardMaxBodySize {
		i := cardMaxBodySize
		for i > 0 && !utf8.RuneStart(body[i]) {
			i--
		}
		body = body[:i]
	}
	var (
		paras    []string
		cur      strings.Builder
		have     bool
		prevKeep bool
		inFence  bool
	)
	flush := func() {
		if have {
			paras = append(paras, cur.String())
		}
		cur.Reset()
		have, prevKeep = false, false
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "" {
			if !inFence {
				flush()
			} else if have {
				cur.WriteByte('\n')
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		fence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		keep := inFence || fence || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "  ") || listItemRe.MatchString(line)
		if fence {
			inFence = !inFence
		}
		text := strings.TrimLeft(line, " \t")
		if keep {
			text = strings.ReplaceAll(line, "\t", "    ")
		}
		switch {
		case !have:
			cur.WriteString(text)
		case keep || prevKeep:
			cur.WriteByte('\n')
			cur.WriteString(text)
		default:
			cur.WriteByte(' ')
			cur.WriteString(text)
		}
		have, prevKeep = true, keep
	}
	flush()
	return strings.Join(paras, "\n\n")
}
