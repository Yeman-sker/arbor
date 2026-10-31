package main

import (
	"arbor/internal/perflog"
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// buildApp compiles the e2e build of Arbor into dir and returns the binary. The
// build tag links the automation plugin and the span log; release builds have
// neither. -o keeps the binary out of the repository.
func buildApp(root, dir string) (string, error) {
	bin := filepath.Join(dir, "arbor-e2e")
	cmd := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build -tags e2e: %v\n%s", err, out)
	}
	return bin, nil
}

// Frame is one frame the engine logged (MYGO_FRAME_STATS=all).
type Frame struct {
	At time.Time
	N  int
	Ms float64
}

var frameLine = regexp.MustCompile(`mygo: frame (\d+) took ([0-9.]+) ms`)

// App is a running e2e build of Arbor.
type App struct {
	cmd     *exec.Cmd
	Client  *Client
	logPath string
	dir     string
	started time.Time

	mu     sync.Mutex
	frames []Frame
	tail   []string
	done   chan struct{}
}

// Launch starts the binary on a repository and waits for its window.
// extraEnv is added to the environment of the next launched apps.
var extraEnv []string

func Launch(bin, repo string) (*App, error) {
	dir, err := os.MkdirTemp("", "ae")
	if err != nil {
		return nil, err
	}
	a := &App{dir: dir, logPath: filepath.Join(dir, "spans.jsonl"), done: make(chan struct{})}
	sock := filepath.Join(dir, "s.sock")
	a.cmd = exec.Command(bin, repo)
	a.cmd.Env = append(append(os.Environ(), "MYGO_AUTOMATION="+sock, "ARBOR_PERF_LOG="+a.logPath, "MYGO_FRAME_STATS=all"), extraEnv...)
	stderr, err := a.cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	a.cmd.Stdout = io.Discard
	a.started = time.Now()
	if err := a.cmd.Start(); err != nil {
		return nil, err
	}
	go a.scan(stderr)
	go func() { a.cmd.Wait(); close(a.done) }()

	c, err := Dial(sock, 20*time.Second)
	if err != nil {
		a.Stop()
		return nil, fmt.Errorf("%v\napp output:\n%s", err, a.Output())
	}
	a.Client = c
	if _, err := c.Window(20 * time.Second); err != nil {
		a.Stop()
		return nil, fmt.Errorf("%v\napp output:\n%s", err, a.Output())
	}
	return a, nil
}

func (a *App) scan(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := frameLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			ms, _ := strconv.ParseFloat(m[2], 64)
			a.mu.Lock()
			a.frames = append(a.frames, Frame{At: time.Now(), N: n, Ms: ms})
			a.mu.Unlock()
			continue
		}
		a.mu.Lock()
		a.tail = append(a.tail, line)
		if len(a.tail) > 40 {
			a.tail = a.tail[1:]
		}
		a.mu.Unlock()
	}
}

// Output is the last lines the app printed apart from frame statistics.
func (a *App) Output() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.tail, "\n")
}

// FramesSince returns the frames logged at or after t.
func (a *App) FramesSince(t time.Time) []Frame {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Frame
	for _, f := range a.frames {
		if !f.At.Before(t) {
			out = append(out, f)
		}
	}
	return out
}

// Mark returns how many spans the app has reported so far.
func (a *App) Mark() int { return len(perflog.ReadFile(a.logPath)) }

// Spans returns the spans reported after mark.
func (a *App) Spans(mark int) []perflog.Record {
	all := perflog.ReadFile(a.logPath)
	if mark > len(all) {
		return nil
	}
	return all[mark:]
}

// Await waits for a span of op reported after mark.
func (a *App) Await(op string, mark int, timeout time.Duration) (perflog.Record, bool) {
	deadline := time.Now().Add(timeout)
	for {
		for _, r := range a.Spans(mark) {
			if r.Op == op {
				return r, true
			}
		}
		if time.Now().After(deadline) {
			return perflog.Record{}, false
		}
		time.Sleep(4 * time.Millisecond)
	}
}

// Usage is the process's resident memory in MB and the CPU time it has used.
func (a *App) Usage() (rssMB float64, cpu time.Duration, err error) {
	out, err := exec.Command("ps", "-o", "rss=,time=", "-p", strconv.Itoa(a.cmd.Process.Pid)).Output()
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return 0, 0, fmt.Errorf("ps printed %q", out)
	}
	kb, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, 0, err
	}
	cpu, err = parseCPUTime(f[1])
	return kb / 1024, cpu, err
}

// parseCPUTime reads ps's [[H:]M:]SS.ss.
func parseCPUTime(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	var secs float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, fmt.Errorf("cpu time %q: %v", s, err)
		}
		secs = secs*60 + v
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// Stop quits the app and removes its files.
func (a *App) Stop() {
	if a.Client != nil {
		a.Client.Close()
	}
	if a.cmd.Process != nil {
		a.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-a.done:
		case <-time.After(3 * time.Second):
			a.cmd.Process.Kill()
			<-a.done
		}
	}
	os.RemoveAll(a.dir)
}
