// Package perflog is the line format a test build of Arbor uses to report its
// perf spans to the scenario runner (e2e): one JSON object per finished
// operation. It carries what perf.Event carries and nothing more, so it holds
// the same closed set of names and numbers: no paths, repository, branch or
// file names, commit text or error text.
package perflog

import (
	"arbor/perf"
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Phase is the time spent in one phase of an operation.
type Phase struct {
	Phase string  `json:"phase"`
	Ms    float64 `json:"ms"`
}

// Record is one finished operation.
type Record struct {
	Op      string  `json:"op"`
	Result  string  `json:"result"`
	Size    string  `json:"size"`
	StartNs int64   `json:"startNs"` // Unix time the operation began
	Ms      float64 `json:"ms"`
	Phases  []Phase `json:"phases,omitempty"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// FromEvent converts a perf event; every string comes from a closed enumeration.
func FromEvent(e perf.Event) Record {
	r := Record{Op: e.Op.String(), Result: e.Result.String(), Size: e.Size.String(), StartNs: e.Start.UnixNano(), Ms: ms(e.Dur)}
	for _, p := range e.Phases[:e.NPhases] {
		r.Phases = append(r.Phases, Phase{p.Phase.String(), ms(p.Dur)})
	}
	return r
}

// Install makes the process write every perf event to the file at path, one
// JSON line each. The sink only hands the event to a writer goroutine, as perf
// requires. It returns a function that flushes and stops the writer.
func Install(path string) (stop func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	ch := make(chan perf.Event, 4096)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := bufio.NewWriter(f)
		enc := json.NewEncoder(w)
		for e := range ch {
			enc.Encode(FromEvent(e))
			if len(ch) == 0 {
				w.Flush() // the runner polls the file, so lines must not sit in a buffer
			}
		}
		w.Flush()
		f.Close()
	}()
	perf.SetSink(func(e perf.Event) {
		select {
		case ch <- e:
		default: // a full queue drops the event rather than block the UI thread
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			perf.SetSink(nil)
			close(ch)
			<-done
		})
	}, nil
}

// Read parses the lines of a log; a line cut off by a concurrent write is skipped.
func Read(r io.Reader) []Record {
	var out []Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec Record
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Op != "" {
			out = append(out, rec)
		}
	}
	return out
}

// ReadFile parses the log at path; a missing file is an empty log.
func ReadFile(path string) []Record {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return Read(f)
}
