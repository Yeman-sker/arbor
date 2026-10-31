// Package perfstore keeps what Arbor measured about itself (ARB-5): a log of
// finished operations in JSON Lines under the user data directory, written in
// the background, rotated by day and trimmed by age and size.
//
// A line is the closed set of names and numbers that package perf records and
// nothing else: operation, phases, result and size bucket come from fixed
// enumerations, and the app version from the build. The encoder writes only
// "safe tokens" (letters, digits and . _ + -), so even a mistake elsewhere could
// not put a path, a name or an error message into a file. Nothing in this
// package or its callers talks to a network: see TestNoPackageOfThePerfSystemImportsTheNetwork.
package perfstore

import (
	"arbor/internal/perflog"
	"math"
	"strconv"
	"time"
)

// PhaseMs is the time one phase of an operation took.
type PhaseMs struct {
	Name string
	Ms   float64
}

// Entry is one finished operation, as a line of the log.
type Entry struct {
	// T is when the operation began, to the second, in UTC.
	T      time.Time
	Op     string
	Ms     float64
	Phases []PhaseMs
	// Result is ok, error or cancelled; Size small, medium, large or unknown.
	Result  string
	Size    string
	Version string
}

// Result names, as perf writes them.
const (
	ResultOK        = "ok"
	ResultError     = "error"
	ResultCancelled = "cancelled"
)

// FromRecord is the entry for a record of package perflog, which turns perf
// events into names: the store and the e2e runner share that conversion.
func FromRecord(r perflog.Record, version string) Entry {
	e := Entry{
		T: time.Unix(0, r.StartNs).UTC().Truncate(time.Second), Op: r.Op, Ms: r.Ms,
		Result: r.Result, Size: r.Size, Version: version,
	}
	if len(r.Phases) > 0 {
		e.Phases = make([]PhaseMs, len(r.Phases))
		for i, p := range r.Phases {
			e.Phases[i] = PhaseMs{p.Phase, p.Ms}
		}
	}
	return e
}

// safeToken is s when it is made of letters, digits and . _ + - and short,
// else "_": the one gate between a string and a file.
func safeToken(s string) string {
	if s == "" {
		return "_"
	}
	if len(s) > 40 {
		return "_"
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '+', c == '-':
		default:
			return "_"
		}
	}
	return s
}

// round3 keeps a duration to a microsecond, which is all a line needs.
func round3(ms float64) float64 {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 {
		return 0
	}
	return math.Round(ms*1000) / 1000
}

func appendMs(dst []byte, ms float64) []byte {
	return strconv.AppendFloat(dst, round3(ms), 'f', -1, 64)
}

// AppendJSON appends the line of e, without the newline:
//
//	{"t":"2026-10-31T12:00:00Z","op":"switch_branch","ms":612,"phases":{"git_checkout":480,"load_repo":95,"apply":21},"result":"ok","size":"large","version":"0.2.0"}
//
// A phase that was marked twice is written once, with the sum of both.
func (e Entry) AppendJSON(dst []byte) []byte {
	dst = append(dst, `{"t":"`...)
	dst = e.T.UTC().Truncate(time.Second).AppendFormat(dst, time.RFC3339)
	dst = append(dst, `","op":"`...)
	dst = append(dst, safeToken(e.Op)...)
	dst = append(dst, `","ms":`...)
	dst = appendMs(dst, e.Ms)
	if len(e.Phases) > 0 {
		dst = append(dst, `,"phases":{`...)
		first := true
		for i, p := range e.Phases {
			dup := false
			sum := p.Ms
			for j := 0; j < i; j++ {
				if e.Phases[j].Name == p.Name {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			for j := i + 1; j < len(e.Phases); j++ {
				if e.Phases[j].Name == p.Name {
					sum += e.Phases[j].Ms
				}
			}
			if !first {
				dst = append(dst, ',')
			}
			first = false
			dst = append(dst, '"')
			dst = append(dst, safeToken(p.Name)...)
			dst = append(dst, `":`...)
			dst = appendMs(dst, sum)
		}
		dst = append(dst, '}')
	}
	dst = append(dst, `,"result":"`...)
	dst = append(dst, safeToken(e.Result)...)
	dst = append(dst, `","size":"`...)
	dst = append(dst, safeToken(e.Size)...)
	dst = append(dst, `","version":"`...)
	dst = append(dst, safeToken(e.Version)...)
	dst = append(dst, `"}`...)
	return dst
}

// SafeVersion is the form of an app version that the log accepts.
func SafeVersion(v string) string {
	if v == "" {
		return "dev"
	}
	return safeToken(v)
}
