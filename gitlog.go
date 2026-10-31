package main

import (
	"strings"
	"time"
)

// CoAuthor is a person named by a Co-authored-by trailer.
type CoAuthor struct {
	Name  string
	Email string
}

// commitLogFormat is the pretty format of the history query. Message bodies
// contain tabs and newlines, so fields end with the unit separator (0x1f) and
// records with the record separator (0x1e); the co-author trailers are joined
// with the group separator (0x1d).
//
// Fields, in order: hash, parents, short hash, author name, author email,
// short date, strict ISO date, refs, subject, body, the trailer block as
// written, and the Co-authored-by values.
const commitLogFormat = "%H%x1f%P%x1f%h%x1f%an%x1f%ae%x1f%ad%x1f%aI%x1f%D%x1f%s%x1f%b%x1f" +
	"%(trailers)%x1f%(trailers:key=Co-authored-by,valueonly,unfold,separator=%x1d)%x1e"

const commitLogFields = 12

// parseCommitLog parses the output of git log with commitLogFormat. Records
// that do not have every field are skipped.
func parseCommitLog(out string) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\r\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", commitLogFields)
		if len(f) < commitLogFields {
			continue
		}
		var parents []string
		if f[1] != "" {
			parents = strings.Fields(f[1])
		}
		var refs []string
		if f[7] != "" {
			for _, r := range strings.Split(f[7], ",") {
				if r = strings.TrimSpace(r); r != "" {
					refs = append(refs, r)
				}
			}
		}
		cm := Commit{
			Hash:      f[0],
			Parents:   parents,
			ShortHash: f[2],
			Author:    f[3],
			Email:     f[4],
			Date:      f[5],
			Refs:      refs,
			Subject:   f[8],
		}
		if ts, err := time.Parse(time.RFC3339, f[6]); err == nil {
			cm.Time = ts
		}
		cm.Body, cm.CoAuthors = splitBody(f[9], f[10], f[11])
		commits = append(commits, cm)
	}
	return commits
}

// knownTrailerKeys are the sign-off style trailers that are noise in a card:
// who else worked on a commit is shown as co-authors, the rest says nothing
// a reader of the history needs. Other trailers ("Fixes: #1") and prose that
// merely looks like one ("Note: ...") stay in the body.
var knownTrailerKeys = map[string]bool{
	"co-authored-by": true,
	"signed-off-by":  true,
	"reviewed-by":    true,
	"acked-by":       true,
	"tested-by":      true,
	"reported-by":    true,
	"suggested-by":   true,
	"helped-by":      true,
	"cc":             true,
	"change-id":      true,
}

// trailerKey returns the lower-case key of a "Key: value" line.
func trailerKey(line string) (key, value string, ok bool) {
	k, v, found := strings.Cut(line, ":")
	if !found || k == "" {
		return "", "", false
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return "", "", false
		}
	}
	return strings.ToLower(k), strings.TrimSpace(v), true
}

// splitBody returns the body without its trailers and the co-authors found.
// block is the trailer block git recognised in the message, coValues the
// Co-authored-by values it extracted, joined with the group separator.
func splitBody(body, block, coValues string) (string, []CoAuthor) {
	body = strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	block = strings.TrimRight(strings.ReplaceAll(block, "\r\n", "\n"), " \t\n")
	var co []CoAuthor
	add := func(v string) {
		ca := parseCoAuthor(v)
		if ca.Name == "" && ca.Email == "" {
			return
		}
		for _, have := range co {
			if strings.EqualFold(have.Email, ca.Email) && have.Name == ca.Name {
				return
			}
		}
		co = append(co, ca)
	}
	// An older Git leaves the placeholder as it is.
	if coValues != "" && !strings.HasPrefix(coValues, "%(") {
		for _, v := range strings.Split(coValues, "\x1d") {
			add(v)
		}
	}

	if block != "" && !strings.HasPrefix(block, "%(") && strings.HasSuffix(body, block) {
		for _, line := range strings.Split(block, "\n") {
			if key, _, ok := trailerKey(line); ok && knownTrailerKeys[key] {
				body = strings.TrimRight(strings.TrimSuffix(body, block), " \t\n")
				break
			}
		}
	}

	// Sign-off lines git did not take for a trailer block, such as one
	// glued to the last paragraph, still do not belong in the text.
	lines := strings.Split(body, "\n")
	end := len(lines)
	for end > 0 {
		line := strings.TrimSpace(lines[end-1])
		if line == "" {
			end--
			continue
		}
		key, value, ok := trailerKey(line)
		if !ok || !knownTrailerKeys[key] {
			break
		}
		if key == "co-authored-by" {
			add(value)
		}
		end--
	}
	return strings.Join(lines[:end], "\n"), co
}

// parseCoAuthor parses "Name <email>".
func parseCoAuthor(v string) CoAuthor {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, "<"); i >= 0 {
		if j := strings.Index(v[i:], ">"); j > 0 {
			name := strings.TrimSpace(v[:i])
			email := strings.TrimSpace(v[i+1 : i+j])
			if name == "" {
				name, _, _ = strings.Cut(email, "@")
			}
			return CoAuthor{Name: name, Email: email}
		}
	}
	return CoAuthor{Name: v}
}
