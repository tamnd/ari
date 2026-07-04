package colony

import (
	"context"
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	memsqlite "github.com/tamnd/ari/memory/sqlite"
)

// testClock is an advanceable clock so a test can put a gap between an update
// and the read that decays it.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

// openTrails brings up a migrated colony.db and a trail store over it with a
// pinned clock and a seeded sampler, so both the decay and the draws are
// deterministic.
func openTrails(t *testing.T) (*sqliteTrailStore, *testClock) {
	t.Helper()
	ctx := context.Background()
	db, err := memsqlite.Open(filepath.Join(t.TempDir(), "colony.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Start(ctx); err != nil {
		t.Fatalf("start db: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	rng := rand.New(rand.NewPCG(42, 99))
	s := NewTrailStore(db, memsqlite.RecencyHalfLifeDays, clk.now, rng).(*sqliteTrailStore)
	return s, clk
}

// sampleStats draws n samples and returns their mean and max.
func sampleStats(t *testing.T, s *sqliteTrailStore, ant string, class TaskClass, n int) (mean, max float64) {
	t.Helper()
	ctx := context.Background()
	for range n {
		v, err := s.Sample(ctx, ant, class)
		if err != nil {
			t.Fatalf("sample: %v", err)
		}
		mean += v
		if v > max {
			max = v
		}
	}
	return mean / float64(n), max
}

// TestNoHistorySamplesUniform is the DoD that an ant with no history on a
// class samples from beta(1,1): mean near a half and able to draw high.
func TestNoHistorySamplesUniform(t *testing.T) {
	s, _ := openTrails(t)
	mean, max := sampleStats(t, s, "fresh", ClassEdit, 2000)
	if math.Abs(mean-0.5) > 0.05 {
		t.Errorf("beta(1,1) mean = %.3f, want near 0.5", mean)
	}
	if max < 0.9 {
		t.Errorf("beta(1,1) max draw = %.3f, want a high draw to be possible", max)
	}
}

// TestSuccessTightensTowardOne and its failure twin are the DoD that repeated
// outcomes move the belief: many successes cluster the samples near one.
func TestSuccessTightensTowardOne(t *testing.T) {
	s, _ := openTrails(t)
	ctx := context.Background()
	for range 40 {
		if err := s.Update(ctx, Outcome{Ant: "good", Class: ClassEdit, Success: true, Tokens: 1000}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	mean, _ := sampleStats(t, s, "good", ClassEdit, 2000)
	if mean < 0.9 {
		t.Errorf("mean after 40 successes = %.3f, want > 0.9", mean)
	}
}

// TestFailureTightensTowardZero is the DoD twin: many failures cluster the
// samples near zero.
func TestFailureTightensTowardZero(t *testing.T) {
	s, _ := openTrails(t)
	ctx := context.Background()
	for range 40 {
		if err := s.Update(ctx, Outcome{Ant: "bad", Class: ClassEdit, Success: false, Tokens: 1000}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	mean, _ := sampleStats(t, s, "bad", ClassEdit, 2000)
	if mean > 0.1 {
		t.Errorf("mean after 40 failures = %.3f, want < 0.1", mean)
	}
}

// TestStaleTrailDecaysBackToPrior is the DoD that a trail unused for a long
// time drifts back toward the prior so the ant gets re-explored: a success
// heavy trail, read many half-lives later, samples near a half again and can
// draw high.
func TestStaleTrailDecaysBackToPrior(t *testing.T) {
	s, clk := openTrails(t)
	ctx := context.Background()
	for range 40 {
		if err := s.Update(ctx, Outcome{Ant: "faded", Class: ClassEdit, Success: true, Tokens: 1000}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	freshMean, _ := sampleStats(t, s, "faded", ClassEdit, 2000)

	// Jump forward many half-lives.
	clk.t = clk.t.Add(time.Duration(memsqlite.RecencyHalfLifeDays*20) * 24 * time.Hour)
	staleMean, staleMax := sampleStats(t, s, "faded", ClassEdit, 2000)

	if freshMean < 0.9 {
		t.Fatalf("fresh mean = %.3f, want the trail to have been confident first", freshMean)
	}
	if math.Abs(staleMean-0.5) > 0.05 {
		t.Errorf("stale mean = %.3f, want the belief back near the 0.5 prior", staleMean)
	}
	if staleMax < 0.9 {
		t.Errorf("stale max draw = %.3f, want a re-explore draw to be possible again", staleMax)
	}
}

// TestFadingCountNeverRoundsToZero is the DoD that counts are floats: a decayed
// success stays strictly positive rather than snapping to a hard zero that
// would look like a failure.
func TestFadingCountNeverRoundsToZero(t *testing.T) {
	s, clk := openTrails(t)
	ctx := context.Background()
	for range 10 {
		if err := s.Update(ctx, Outcome{Ant: "slow", Class: ClassSurvey, Success: true, Tokens: 500}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	clk.t = clk.t.Add(time.Duration(memsqlite.RecencyHalfLifeDays*8) * 24 * time.Hour)
	tr, err := s.Load(ctx, "slow", ClassSurvey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if tr.Success <= 0 {
		t.Errorf("decayed success = %v, want strictly positive (floats, not a hard zero)", tr.Success)
	}
	if tr.Success >= 1 {
		t.Errorf("decayed success = %v, want much less than the raw 10 after 8 half-lives", tr.Success)
	}
}

// TestHalfLifeSharedWithMemory is the DoD that the trail clock and the memory
// clock are one number: decaying a count over exactly one memory half-life
// halves it.
func TestHalfLifeSharedWithMemory(t *testing.T) {
	oneHalfLife := time.Duration(memsqlite.RecencyHalfLifeDays) * 24 * time.Hour
	got := decayed(10, oneHalfLife, memsqlite.RecencyHalfLifeDays)
	if math.Abs(got-5) > 1e-9 {
		t.Errorf("a count decayed over one memory half-life = %v, want 5", got)
	}
}

// TestUpdateRecordsCostAndCount checks the columns the budget projection reads:
// the lifetime token total and the raw task count both accumulate.
func TestUpdateRecordsCostAndCount(t *testing.T) {
	s, _ := openTrails(t)
	ctx := context.Background()
	for i := range 3 {
		ok := i != 1
		if err := s.Update(ctx, Outcome{Ant: "w", Class: ClassEdit, Success: ok, Tokens: 100, WallMS: 50}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	tr, err := s.Load(ctx, "w", ClassEdit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if tr.N != 3 {
		t.Errorf("n = %d, want 3", tr.N)
	}
	if tr.Tokens != 300 {
		t.Errorf("tokens = %d, want 300", tr.Tokens)
	}
	if tr.WallMS != 150 {
		t.Errorf("wall_ms = %d, want 150", tr.WallMS)
	}
}

// TestCentroidIsRunningMean checks the centroid the M4 fork test will read is
// the mean of the task embeddings, written on every outcome.
func TestCentroidIsRunningMean(t *testing.T) {
	s, _ := openTrails(t)
	ctx := context.Background()
	embeds := [][]float32{{0, 0}, {2, 4}, {4, 8}}
	for _, e := range embeds {
		if err := s.Update(ctx, Outcome{Ant: "c", Class: ClassEdit, Success: true, Embed: e}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	tr, err := s.Load(ctx, "c", ClassEdit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []float32{2, 4}
	if len(tr.Centroid) != 2 || math.Abs(float64(tr.Centroid[0]-want[0])) > 1e-5 || math.Abs(float64(tr.Centroid[1]-want[1])) > 1e-5 {
		t.Errorf("centroid = %v, want the running mean %v", tr.Centroid, want)
	}
}
