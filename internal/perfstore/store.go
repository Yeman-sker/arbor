package perfstore

import (
	"arbor/internal/perflog"
	"arbor/perf"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of the store: how long and how much it keeps.
const (
	DefaultMaxAge   = 365 * 24 * time.Hour // the climb is read across releases
	DefaultMaxBytes = 100 << 20
	// DefaultQueue is how many events wait for the writer. An event is about
	// 140 bytes, so the queue is half a megabyte, and a burst beyond it is
	// dropped and counted rather than slowing the UI thread.
	DefaultQueue = 4096
	// maxBatch is how many queued events one write carries.
	maxBatch = 512
)

// Options are the settings of a Store.
type Options struct {
	// Version is the app version written on every line.
	Version string
	// QueueLen bounds the events waiting for the writer; DefaultQueue when 0.
	QueueLen int
	// MaxAge and MaxBytes bound the logs kept: whole days older than MaxAge are
	// deleted, and then the oldest days until the rest fits MaxBytes. 0 takes
	// the defaults.
	MaxAge   time.Duration
	MaxBytes int64
	// Now tells the time retention counts from; time.Now when nil.
	Now func() time.Time
	// RetentionEvery is how often the writer trims the logs besides when it
	// starts and when a day begins; 10 minutes when 0.
	RetentionEvery time.Duration

	// beforeWrite, for tests, runs before each write to disk.
	beforeWrite func()
}

type request struct {
	quit bool
	done chan struct{}
}

// Store records perf events into the logs of one directory. One Store belongs
// to the process and every window feeds it; Open starts its writer.
type Store struct {
	dir string
	opt Options

	ch   chan perf.Event
	ctl  chan request
	done chan struct{}

	closed  atomic.Bool
	dropped atomic.Uint64 // events lost since the writer last said so
	lost    atomic.Uint64 // events lost since Open
	written atomic.Uint64
	failed  atomic.Uint64 // events lost to a disk that could not be written

	closeOnce sync.Once
}

// Open starts a store that writes into dir, which is created on the first
// event. It returns at once: the writer does the file work.
func Open(dir string, opt Options) *Store {
	if opt.QueueLen <= 0 {
		opt.QueueLen = DefaultQueue
	}
	if opt.MaxAge <= 0 {
		opt.MaxAge = DefaultMaxAge
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = DefaultMaxBytes
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.RetentionEvery <= 0 {
		opt.RetentionEvery = 10 * time.Minute
	}
	opt.Version = SafeVersion(opt.Version)
	s := &Store{
		dir: dir, opt: opt,
		ch:   make(chan perf.Event, opt.QueueLen),
		ctl:  make(chan request),
		done: make(chan struct{}),
	}
	go s.run()
	return s
}

// Dir is the directory of the logs.
func (s *Store) Dir() string { return s.dir }

// Sink is what perf.SetSink takes. It runs on the caller's goroutine, often the
// UI thread, and only copies the event into the queue: it does not block, lock
// or allocate. When the queue is full the event is dropped and counted.
func (s *Store) Sink() perf.Sink { return s.record }

func (s *Store) record(e perf.Event) {
	if s.closed.Load() {
		return
	}
	select {
	case s.ch <- e:
	default:
		s.dropped.Add(1)
		s.lost.Add(1)
	}
}

// Stats counts what the store did since it was opened.
type Stats struct {
	Written uint64 // events on disk
	Dropped uint64 // events lost because the queue was full
	Failed  uint64 // events lost because the disk could not be written
}

// Stats returns the counters.
func (s *Store) Stats() Stats {
	return Stats{Written: s.written.Load(), Dropped: s.lost.Load(), Failed: s.failed.Load()}
}

// Flush returns once every event queued before the call is on disk.
func (s *Store) Flush() {
	if s.closed.Load() {
		return
	}
	req := request{done: make(chan struct{})}
	select {
	case s.ctl <- req:
		<-req.done
	case <-s.done:
	}
}

// Close writes what is queued and stops the writer. The sink may still be
// called afterwards (a span that began earlier): it then does nothing. It is
// safe to call more than once.
func (s *Store) Close() {
	s.closeOnce.Do(func() {
		req := request{quit: true, done: make(chan struct{})}
		s.ctl <- req
		<-req.done
		s.closed.Store(true)
		<-s.done
	})
}

// writer is the state of the background goroutine.
type writer struct {
	s    *Store
	file *os.File
	day  string // the day of the open file
	buf  []byte
	n    int // events in buf
	seen time.Time
}

func (s *Store) run() {
	defer close(s.done)
	w := &writer{s: s}
	tick := time.NewTicker(s.opt.RetentionEvery)
	defer tick.Stop()
	w.trim()
	for {
		select {
		case e := <-s.ch:
			w.add(e)
			w.drain(maxBatch - 1)
			w.write()
		case <-tick.C:
			w.trim()
		case req := <-s.ctl:
			w.drain(1 << 30)
			w.write()
			if req.quit {
				w.close()
				close(req.done)
				return
			}
			close(req.done)
		}
	}
}

// drain adds the queued events, up to max more.
func (w *writer) drain(max int) {
	for i := 0; i < max; i++ {
		select {
		case e := <-w.s.ch:
			w.add(e)
		default:
			return
		}
	}
}

// add encodes e into the batch, moving to a new file when its day is a later
// one than the file's.
func (w *writer) add(e perf.Event) {
	ent := FromRecord(perflog.FromEvent(e), w.s.opt.Version)
	day := ent.T.Format("2006-01-02")
	if day > w.day {
		w.write()
		w.rotate(day)
	}
	w.buf = ent.AppendJSON(w.buf)
	w.buf = append(w.buf, '\n')
	w.n++
}

// rotate makes the file of day the one written to.
func (w *writer) rotate(day string) {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	w.day = day
	w.trim()
}

// open readies the file of the current day.
func (w *writer) open() error {
	if w.file != nil {
		return nil
	}
	if w.day == "" {
		w.day = w.s.opt.Now().UTC().Format("2006-01-02")
	}
	if err := os.MkdirAll(w.s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.s.dir, "perf-"+w.day+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w.file = f
	return nil
}

// write puts the batch on disk with one write, preceded by a line that says how
// many events the queue lost since the last one.
func (w *writer) write() {
	if d := w.s.dropped.Swap(0); d > 0 {
		line := append([]byte(`{"t":"`), time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)...)
		line = append(line, `","dropped":`...)
		line = appendUint(line, d)
		line = append(line, `,"version":"`...)
		line = append(line, w.s.opt.Version...)
		line = append(line, "\"}\n"...)
		w.buf = append(w.buf, line...)
	}
	if len(w.buf) == 0 {
		return
	}
	if h := w.s.opt.beforeWrite; h != nil {
		h()
	}
	n := w.n
	var err error
	if err = w.open(); err == nil {
		_, err = w.file.Write(w.buf)
	}
	if err != nil {
		// A disk that cannot be written must not grow the process: the batch is
		// given up and counted, and the next one tries again.
		w.s.failed.Add(uint64(n))
		if w.file != nil {
			w.file.Close()
			w.file = nil
		}
	} else {
		w.s.written.Add(uint64(n))
	}
	w.buf = w.buf[:0]
	w.n = 0
}

func appendUint(dst []byte, v uint64) []byte {
	var tmp [20]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			break
		}
	}
	return append(dst, tmp[i:]...)
}

func (w *writer) close() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

// trim applies the retention rules to the directory: it never removes the file
// being written.
func (w *writer) trim() {
	keep := w.day
	if keep == "" {
		keep = w.s.opt.Now().UTC().Format("2006-01-02")
	}
	Trim(w.s.dir, w.s.opt.Now(), w.s.opt.MaxAge, w.s.opt.MaxBytes, keep)
}

// Trim deletes the logs of dir that are older than maxAge before now, and then
// the oldest of the rest until what remains is at most maxBytes. The file of
// the day keep (YYYY-MM-DD) is never removed, nor, to make room, the newest.
// It returns how many files it removed.
func Trim(dir string, now time.Time, maxAge time.Duration, maxBytes int64, keep string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	type logFile struct {
		path string
		day  time.Time
		size int64
	}
	var files []logFile
	var total int64
	for _, e := range entries {
		day, ok := logDay(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{filepath.Join(dir, e.Name()), day, info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].day.Before(files[j].day) })
	cutoff := now.UTC().Add(-maxAge).Truncate(24 * time.Hour)
	removed := 0
	for i, f := range files {
		if f.day.Format("2006-01-02") == keep {
			continue
		}
		// The newest log goes only when it is too old, not to make room.
		if f.day.Before(cutoff) || (total > maxBytes && i != len(files)-1) {
			if os.Remove(f.path) == nil {
				total -= f.size
				removed++
			}
		}
	}
	return removed
}

// Delete removes the whole directory of the logs.
func Delete(dir string) error {
	if dir == "" {
		return errors.New("perfstore: no directory")
	}
	return os.RemoveAll(dir)
}

// DirInfo describes the logs of a directory.
type DirInfo struct {
	Files int
	Bytes int64
	// First and Last are the days of the oldest and newest log.
	First, Last time.Time
}

// Info measures the logs of dir without reading them.
func Info(dir string) DirInfo {
	var d DirInfo
	files, _ := LogFiles(dir)
	for _, p := range files {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		day, _ := logDay(filepath.Base(p))
		d.Files++
		d.Bytes += fi.Size()
		if d.First.IsZero() || day.Before(d.First) {
			d.First = day
		}
		if day.After(d.Last) {
			d.Last = day
		}
	}
	return d
}
