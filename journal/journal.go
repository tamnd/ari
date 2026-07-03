// Package journal is the append-only event log of everything the colony
// does: the durable spine under the D2 event stream and the source for
// replay (doc 01 section 8). The log leads and the stream follows: an event
// reaches subscribers only after the writer has sequenced and written it,
// so any "I saw up to N" is a valid resume cursor.
package journal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/ari/event"
)

// DefaultRotateBytes is the size a journal file grows to before the writer
// rolls to the next index (doc 01 section 8.3).
const DefaultRotateBytes = 8 << 20

// Sink receives each event after it is durably written, in Seq order. The
// colony points this at the bus; the journal stays upstream of any lossy
// fan-out (doc 01 section 8.2).
type Sink func(event.Event)

// Journal is the append-only event log. Append stamps Seq and Time under a
// lock and enqueues; one writer goroutine drains the queue in order, writes
// each line, then hands the event to the sink.
type Journal struct {
	dir    string
	rotate int64
	sink   Sink

	mu      sync.Mutex
	nextSeq uint64
	queue   chan event.Event

	started bool
	done    chan struct{}

	file  *os.File
	w     *bufio.Writer
	size  int64
	index int
}

// Open prepares a journal in dir, scanning existing files so Seq continues
// gap-free across restarts. No goroutine starts until Start.
func Open(dir string, sink Sink) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	j := &Journal{dir: dir, rotate: DefaultRotateBytes, sink: sink, nextSeq: 1, queue: make(chan event.Event, 256), done: make(chan struct{})}
	files, err := j.files()
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		last := files[len(files)-1]
		j.index = indexOf(last)
		seq, err := lastSeq(filepath.Join(dir, last))
		if err != nil {
			return nil, err
		}
		j.nextSeq = seq + 1
	}
	return j, nil
}

// Start brings the writer goroutine up. Separate from Open so the colony
// controls goroutine lifetimes (doc 01 section 4.1).
func (j *Journal) Start(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.started {
		return nil
	}
	if err := j.openFile(); err != nil {
		return err
	}
	j.started = true
	go j.run()
	return nil
}

// Append stamps Seq and Time and enqueues the event for durable logging.
// It returns the stamped event after it is queued, not after it is on
// disk; ordering is the single writer's, durability its flush policy.
// Between Close and the process end the stamp still happens but nothing
// is logged or fanned out; the colony stops producing before it closes.
func (j *Journal) Append(e event.Event) event.Event {
	j.mu.Lock()
	e.Seq = j.nextSeq
	j.nextSeq++
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	// Enqueue under the lock so queue order always matches Seq order and
	// a concurrent Close cannot close the queue mid-send.
	if j.started {
		j.queue <- e
	}
	j.mu.Unlock()
	return e
}

// Close stops the writer, flushes, and closes the file. Idempotent.
func (j *Journal) Close() error {
	j.mu.Lock()
	if !j.started {
		j.mu.Unlock()
		return nil
	}
	j.started = false
	close(j.queue)
	j.mu.Unlock()
	<-j.done
	return nil
}

func (j *Journal) run() {
	defer close(j.done)
	for e := range j.queue {
		if err := j.write(e); err != nil {
			// A journal write failure must not stall the colony; the
			// stream stays live and the gap is visible in the log itself.
			fmt.Fprintf(os.Stderr, "ari: journal write: %v\n", err)
		}
		if j.sink != nil {
			j.sink(e)
		}
	}
	if j.w != nil {
		j.w.Flush()
	}
	if j.file != nil {
		j.file.Close()
	}
}

func (j *Journal) write(e event.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if j.size+int64(len(data))+1 > j.rotate && j.size > 0 {
		if err := j.roll(); err != nil {
			return err
		}
	}
	if _, err := j.w.Write(data); err != nil {
		return err
	}
	if err := j.w.WriteByte('\n'); err != nil {
		return err
	}
	j.size += int64(len(data)) + 1
	return j.w.Flush()
}

func (j *Journal) roll() error {
	j.w.Flush()
	j.file.Close()
	j.index++
	j.size = 0
	return j.openFile()
}

func (j *Journal) openFile() error {
	if j.index == 0 {
		j.index = 1
	}
	path := filepath.Join(j.dir, fileName(j.index))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	j.file, j.w, j.size = f, bufio.NewWriter(f), st.Size()
	return nil
}

// Since returns events with Seq greater than after, for replay and for a
// resuming client. It reads the log files, never the queue or the bus.
func (j *Journal) Since(ctx context.Context, after uint64) ([]event.Event, error) {
	files, err := j.files()
	if err != nil {
		return nil, err
	}
	var out []event.Event
	for _, name := range files {
		evs, err := readFile(filepath.Join(j.dir, name), after)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	return out, nil
}

// Cursor is the last Seq assigned, the resume point a fresh subscriber's
// hello carries.
func (j *Journal) Cursor() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.nextSeq - 1
}

func (j *Journal) files() ([]string, error) {
	des, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, de := range des {
		if !de.IsDir() && strings.HasPrefix(de.Name(), "events-") && strings.HasSuffix(de.Name(), ".jsonl") {
			out = append(out, de.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func fileName(index int) string {
	return fmt.Sprintf("events-%05d.jsonl", index)
}

func indexOf(name string) int {
	var i int
	fmt.Sscanf(name, "events-%05d.jsonl", &i)
	return i
}

func lastSeq(path string) (uint64, error) {
	evs, err := readFile(path, 0)
	if err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}
	return evs[len(evs)-1].Seq, nil
}

func readFile(path string, after uint64) ([]event.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e event.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("journal %s: bad line: %w", path, err)
		}
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
