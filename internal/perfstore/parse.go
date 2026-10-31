package perfstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lineKind says what a line of a log is.
type lineKind int

const (
	lineBad lineKind = iota
	lineEvent
	lineDropped
)

// interner shares the strings of a log: a few dozen names repeat on every line.
type interner map[string]string

func (in interner) get(b []byte) string {
	if s, ok := in[string(b)]; ok {
		return s
	}
	s := string(b)
	in[s] = s
	return s
}

// raw is what the fast parser reads out of a line.
type raw struct {
	t, op, result, size, version []byte
	ms                           float64
	hasMs                        bool
	phases                       []rawPhase
	dropped                      float64
	hasDropped                   bool
}

type rawPhase struct {
	name []byte
	ms   float64
}

type scanner struct {
	b []byte
	i int
}

func (s *scanner) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

var errSlow = errors.New("needs the general parser")

// str reads a string without escapes; an escape sends the line to encoding/json.
func (s *scanner) str() ([]byte, error) {
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		return nil, errors.New("not a string")
	}
	s.i++
	start := s.i
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case '"':
			v := s.b[start:s.i]
			s.i++
			return v, nil
		case '\\':
			return nil, errSlow
		}
		s.i++
	}
	return nil, errors.New("unterminated string")
}

func (s *scanner) number() (float64, error) {
	start := s.i
	for s.i < len(s.b) {
		c := s.b[s.i]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			s.i++
			continue
		}
		break
	}
	if start == s.i {
		return 0, errors.New("not a number")
	}
	return strconv.ParseFloat(string(s.b[start:s.i]), 64)
}

// skip passes over a value the reader has no use for.
func (s *scanner) skip() error {
	s.ws()
	if s.i >= len(s.b) {
		return errors.New("no value")
	}
	switch c := s.b[s.i]; {
	case c == '"':
		_, err := s.str()
		return err
	case c == '{' || c == '[':
		depth := 0
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case '"':
				if _, err := s.str(); err != nil {
					return err
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					s.i++
					return nil
				}
			}
			s.i++
		}
		return errors.New("unterminated value")
	default:
		for s.i < len(s.b) && s.b[s.i] != ',' && s.b[s.i] != '}' && s.b[s.i] != ']' {
			s.i++
		}
		return nil
	}
}

// parseRaw reads one object, in any key order, ignoring keys it does not know.
func parseRaw(line []byte, r *raw) error {
	s := scanner{b: line}
	s.ws()
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return errors.New("not an object")
	}
	s.i++
	for {
		s.ws()
		if s.i >= len(s.b) {
			return errors.New("unterminated object")
		}
		if s.b[s.i] == '}' {
			return nil
		}
		if s.b[s.i] == ',' {
			s.i++
			continue
		}
		key, err := s.str()
		if err != nil {
			return err
		}
		s.ws()
		if s.i >= len(s.b) || s.b[s.i] != ':' {
			return errors.New("no colon")
		}
		s.i++
		s.ws()
		switch string(key) {
		case "t", "op", "result", "size", "version":
			v, err := s.str()
			if err != nil {
				return err
			}
			switch string(key) {
			case "t":
				r.t = v
			case "op":
				r.op = v
			case "result":
				r.result = v
			case "size":
				r.size = v
			default:
				r.version = v
			}
		case "ms", "dropped":
			v, err := s.number()
			if err != nil {
				return err
			}
			if string(key) == "ms" {
				r.ms, r.hasMs = v, true
			} else {
				r.dropped, r.hasDropped = v, true
			}
		case "phases":
			if s.i >= len(s.b) || s.b[s.i] != '{' {
				if err := s.skip(); err != nil {
					return err
				}
				continue
			}
			s.i++
			for {
				s.ws()
				if s.i >= len(s.b) {
					return errors.New("unterminated phases")
				}
				if s.b[s.i] == '}' {
					s.i++
					break
				}
				if s.b[s.i] == ',' {
					s.i++
					continue
				}
				name, err := s.str()
				if err != nil {
					return err
				}
				s.ws()
				if s.i >= len(s.b) || s.b[s.i] != ':' {
					return errors.New("no colon in phases")
				}
				s.i++
				s.ws()
				ms, err := s.number()
				if err != nil {
					return err
				}
				r.phases = append(r.phases, rawPhase{name, ms})
			}
		default:
			if err := s.skip(); err != nil {
				return err
			}
		}
	}
}

// slowLine is the general parser's view of a line, for lines with escapes.
type slowLine struct {
	T       string             `json:"t"`
	Op      string             `json:"op"`
	Ms      *float64           `json:"ms"`
	Phases  map[string]float64 `json:"phases"`
	Result  string             `json:"result"`
	Size    string             `json:"size"`
	Version string             `json:"version"`
	Dropped *float64           `json:"dropped"`
}

func (l slowLine) raw() raw {
	r := raw{t: []byte(l.T), op: []byte(l.Op), result: []byte(l.Result), size: []byte(l.Size), version: []byte(l.Version)}
	if l.Ms != nil {
		r.ms, r.hasMs = *l.Ms, true
	}
	if l.Dropped != nil {
		r.dropped, r.hasDropped = *l.Dropped, true
	}
	names := make([]string, 0, len(l.Phases))
	for n := range l.Phases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r.phases = append(r.phases, rawPhase{[]byte(n), l.Phases[n]})
	}
	return r
}

// maxMs is the longest duration a line may claim: eleven days is a corrupt line.
const maxMs = 1e9

var minTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ParseLine reads one line of a log. ok is false for a line that is cut off,
// garbled or not an event; dropped is the count a line of lost events carries.
// Every string is cleaned to a safe token, so what a file says cannot reach a
// screen or a report as anything else.
func ParseLine(line []byte) (e Entry, dropped uint64, ok bool) {
	return parseLine(line, interner{})
}

func parseLine(line []byte, in interner) (Entry, uint64, bool) {
	e, d, k := parseKind(line, in)
	return e, d, k != lineBad
}

func parseKind(line []byte, in interner) (Entry, uint64, lineKind) {
	var r raw
	if err := parseRaw(line, &r); err != nil {
		if !errors.Is(err, errSlow) {
			return Entry{}, 0, lineBad
		}
		var sl slowLine
		if json.Unmarshal(line, &sl) != nil {
			return Entry{}, 0, lineBad
		}
		r = sl.raw()
	}
	if r.hasDropped && len(r.op) == 0 {
		if r.dropped < 0 || r.dropped > 1e12 || math.IsNaN(r.dropped) {
			return Entry{}, 0, lineBad
		}
		return Entry{}, uint64(r.dropped), lineDropped
	}
	if len(r.op) == 0 || !r.hasMs || math.IsNaN(r.ms) || r.ms < 0 || r.ms > maxMs {
		return Entry{}, 0, lineBad
	}
	t, err := time.Parse(time.RFC3339, string(r.t))
	if err != nil || t.Before(minTime) {
		return Entry{}, 0, lineBad
	}
	e := Entry{
		T: t.UTC(), Op: in.get([]byte(safeToken(string(r.op)))), Ms: r.ms,
		Result: in.get([]byte(safeToken(string(r.result)))), Size: in.get([]byte(safeToken(string(r.size)))),
		Version: in.get([]byte(safeToken(string(r.version)))),
	}
	if len(r.phases) > 0 {
		e.Phases = make([]PhaseMs, 0, len(r.phases))
		for _, p := range r.phases {
			if math.IsNaN(p.ms) || p.ms < 0 || p.ms > maxMs {
				continue
			}
			e.Phases = append(e.Phases, PhaseMs{in.get([]byte(safeToken(string(p.name)))), p.ms})
		}
	}
	return e, 0, lineEvent
}

// ReadStats says what a read found.
type ReadStats struct {
	Files   int
	Events  int
	Corrupt int
	// Dropped is the number of events the writers say they lost to a full
	// queue; it is only known for the days a log recorded it.
	Dropped uint64
}

// ReadLines reads the lines of one log and calls visit with each event. A line
// that is garbled or cut off (as the last of a file may be after a crash) is
// counted and skipped; the rest is read.
func ReadLines(r io.Reader, in interner, visit func(Entry), st *ReadStats) error {
	if in == nil {
		in = interner{}
	}
	br := bufio.NewReaderSize(r, 256<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if trimmed := trimEOL(line); len(trimmed) > 0 {
				switch e, d, k := parseKind(trimmed, in); k {
				case lineEvent:
					st.Events++
					visit(e)
				case lineDropped:
					st.Dropped += d
				default:
					st.Corrupt++
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

// LogName is the name of the log file of a day.
func LogName(day time.Time) string {
	return "perf-" + day.UTC().Format("2006-01-02") + ".jsonl"
}

// logDay is the day a log file name stands for.
func logDay(name string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, "perf-")
	if !ok {
		return time.Time{}, false
	}
	rest, ok = strings.CutSuffix(rest, ".jsonl")
	if !ok {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", rest)
	return d, err == nil
}

// LogFiles lists the log files of dir, oldest first. A directory that is not
// there has none.
func LogFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if _, ok := logDay(e.Name()); ok && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := range names {
		names[i] = filepath.Join(dir, names[i])
	}
	return names, nil
}

// ReadDir reads every log of dir, oldest first, calling visit with each event.
// A directory that is not there is an empty log.
func ReadDir(dir string, visit func(Entry)) (ReadStats, error) {
	var st ReadStats
	files, err := LogFiles(dir)
	if err != nil {
		return st, err
	}
	in := interner{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue // removed meanwhile, as retention does
		}
		st.Files++
		err = ReadLines(f, in, visit, &st)
		f.Close()
		if err != nil {
			return st, err
		}
	}
	return st, nil
}
