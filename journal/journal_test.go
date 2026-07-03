package journal

import (
	"context"
	"sync"
	"testing"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

func ev(t *testing.T, text string) event.Event {
	t.Helper()
	e, err := event.New(event.TypeLog, "s1", "", event.Log{Level: "info", Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// collector is a test sink recording delivery order.
type collector struct {
	mu   sync.Mutex
	seqs []uint64
	got  chan struct{}
	want int
}

func newCollector(want int) *collector {
	return &collector{got: make(chan struct{}), want: want}
}

func (c *collector) sink(e event.Event) {
	c.mu.Lock()
	c.seqs = append(c.seqs, e.Seq)
	if len(c.seqs) == c.want {
		close(c.got)
	}
	c.mu.Unlock()
}

func TestSeqIsTotalAndGapFree(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	col := newCollector(50)
	j, err := Open(dir, col.sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Start(ctx); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 5 {
				j.Append(ev(t, "concurrent"))
			}
		})
	}
	wg.Wait()
	<-col.got
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// The sink saw every event in Seq order with no gap, even under
	// concurrent appenders: the single writer is the choke point.
	for i, s := range col.seqs {
		if s != uint64(i+1) {
			t.Fatalf("sink order broken at %d: seq %d", i, s)
		}
	}
	if j.Cursor() != 50 {
		t.Errorf("cursor = %d, want 50", j.Cursor())
	}
}

func TestSeqContinuesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	col := newCollector(3)
	j, err := Open(dir, col.sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		j.Append(ev(t, "before"))
	}
	<-col.got
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	j2, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e := j2.Append(ev(t, "after"))
	if e.Seq != 4 {
		t.Errorf("restart seq = %d, want 4", e.Seq)
	}
	if err := j2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSinceReadsAcrossRotation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	j, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.rotate = 256 // force rotation every few events
	if err := j.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		j.Append(ev(t, "rotating event with enough text to cross the tiny cap"))
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	files, err := j.files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("expected rotation, got %d file(s)", len(files))
	}

	evs, err := j.Since(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 15 {
		t.Fatalf("Since(5) returned %d events, want 15", len(evs))
	}
	for i, e := range evs {
		if e.Seq != uint64(6+i) {
			t.Errorf("event %d has seq %d, want %d", i, e.Seq, 6+i)
		}
	}
}

func TestAppendAfterCloseDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	j, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	e := j.Append(ev(t, "late"))
	if e.Seq == 0 {
		t.Error("a late append still stamps, it just is not logged")
	}
	if err := j.Close(); err != nil {
		t.Errorf("Close must be idempotent: %v", err)
	}
}

func TestNoLeaksAfterClose(t *testing.T) {
	ctx := context.Background()
	j, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Start(ctx); err != nil {
		t.Fatal(err)
	}
	j.Append(ev(t, "one"))
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	eval.NoLeaks(t)
}
