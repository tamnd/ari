package colony

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// A trail is what the colony remembers about an ant's fitness on a class of
// task: a Beta belief the queen's Thompson sampler routes on (doc 06 sections
// 4.2, 4.4, D13). Trails are written at task end with the outcome and its
// token cost, and read only by the router. The belief evaporates on the same
// clock the memory store uses, so a stale ant drifts back toward the prior and
// gets re-explored rather than trusted or written off forever.

// errNoTrail is the internal not-found signal; the store turns it into the
// beta(1,1) prior, because an ant with no history is not an error, it is the
// uninformative belief we start from.
var errNoTrail = errors.New("colony: no trail for that ant and class")

// Trail is one ant's decayed fitness record on one task class.
type Trail struct {
	Ant      string
	Class    TaskClass
	Success  float64
	Failure  float64
	Tokens   int64
	WallMS   int64
	N        int64
	Centroid []float32
	Updated  time.Time
}

// Outcome is what task end records against a trail: which ant ran which class,
// whether it succeeded, what it cost, and the task embedding whose running
// mean becomes the trail centroid.
type Outcome struct {
	Ant     string
	Class   TaskClass
	Success bool
	Tokens  int64
	WallMS  int64
	Embed   []float32
}

// TrailStore is the router's fitness memory. It is written at task end and
// read only by the router (doc 06 section 4.4): nothing else in the colony
// reads a trail, because a trail is a routing belief, not a fact about the
// world.
type TrailStore interface {
	// Update folds one outcome into a trail, decaying the prior counts to now
	// before adding the new one, so the stored counts are always as of their
	// updated_at.
	Update(ctx context.Context, o Outcome) error
	// Sample draws a Thompson sample from the decayed Beta belief: high when
	// the ant is good or unknown on this class, low when it has failed here.
	Sample(ctx context.Context, ant string, class TaskClass) (float64, error)
	// Load returns the decayed trail, or the beta(1,1) prior when the ant has
	// no history on the class.
	Load(ctx context.Context, ant string, class TaskClass) (Trail, error)
}

// sqliteTrailStore is the colony.db-backed TrailStore.
type sqliteTrailStore struct {
	db           substrate
	halfLifeDays float64
	now          func() time.Time
	mu           sync.Mutex
	rng          *rand.Rand
}

// NewTrailStore wires a trail store. halfLifeDays is the shared evaporation
// clock (pass the memory store's RecencyHalfLifeDays so the two cannot skew).
// rng is the sampler's randomness, injectable so a test is deterministic; nil
// seeds one from the clock.
func NewTrailStore(db substrate, halfLifeDays float64, now func() time.Time, rng *rand.Rand) TrailStore {
	if now == nil {
		now = time.Now
	}
	if rng == nil {
		seed := uint64(now().UnixNano())
		rng = rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	}
	return &sqliteTrailStore{db: db, halfLifeDays: halfLifeDays, now: now, rng: rng}
}

// Update runs at task end. It decays the ant's existing counts to now, folds
// in the new outcome, updates the running-mean centroid, and writes the row.
// Doc 06 puts this in the same transaction that closes the ledger (slice 14)
// so an outcome and its cost land together; here it is one write on its own.
func (s *sqliteTrailStore) Update(ctx context.Context, o Outcome) error {
	now := s.now()
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		var success, failure float64
		var tokens, wall, n, updated int64
		var centroid []byte
		row := tx.QueryRowContext(ctx,
			`SELECT success, failure, tokens, wall_ms, n, updated_at, centroid FROM trails WHERE ant = ? AND class = ?`,
			o.Ant, o.Class)
		err := row.Scan(&success, &failure, &tokens, &wall, &n, &updated, &centroid)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			success, failure, tokens, wall, n, centroid = 0, 0, 0, 0, 0, nil
		case err != nil:
			return err
		default:
			age := time.Duration(now.Unix()-updated) * time.Second
			success = decayed(success, age, s.halfLifeDays)
			failure = decayed(failure, age, s.halfLifeDays)
		}

		if o.Success {
			success++
		} else {
			failure++
		}
		tokens += o.Tokens
		wall += o.WallMS
		n++
		vec := updateCentroid(decodeVec(centroid), o.Embed, n)

		_, werr := tx.ExecContext(ctx,
			`INSERT INTO trails (ant, class, centroid, success, failure, tokens, wall_ms, n, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(ant, class) DO UPDATE SET
			   centroid = excluded.centroid, success = excluded.success,
			   failure = excluded.failure, tokens = excluded.tokens,
			   wall_ms = excluded.wall_ms, n = excluded.n, updated_at = excluded.updated_at`,
			o.Ant, o.Class, encodeVec(vec), success, failure, tokens, wall, n, now.Unix())
		return werr
	})
}

// Load reads a trail and decays its counts from updated_at to now, so the
// decay is applied lazily in the read the sampler needs anyway: no sweep job,
// no background goroutine.
func (s *sqliteTrailStore) Load(ctx context.Context, ant string, class TaskClass) (Trail, error) {
	t := Trail{Ant: ant, Class: class}
	var updated int64
	var centroid []byte
	err := s.db.Read(ctx, func(db *sql.DB) error {
		row := db.QueryRowContext(ctx,
			`SELECT success, failure, tokens, wall_ms, n, updated_at, centroid FROM trails WHERE ant = ? AND class = ?`,
			ant, class)
		e := row.Scan(&t.Success, &t.Failure, &t.Tokens, &t.WallMS, &t.N, &updated, &centroid)
		if errors.Is(e, sql.ErrNoRows) {
			return errNoTrail
		}
		return e
	})
	if errors.Is(err, errNoTrail) {
		return Trail{Ant: ant, Class: class}, nil
	}
	if err != nil {
		return Trail{}, err
	}
	t.Updated = time.Unix(updated, 0)
	t.Centroid = decodeVec(centroid)
	age := s.now().Sub(t.Updated)
	t.Success = decayed(t.Success, age, s.halfLifeDays)
	t.Failure = decayed(t.Failure, age, s.halfLifeDays)
	return t, nil
}

// Sample draws one Thompson sample from the decayed belief.
func (s *sqliteTrailStore) Sample(ctx context.Context, ant string, class TaskClass) (float64, error) {
	t, err := s.Load(ctx, ant, class)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return betaSample(s.rng, t.Success, t.Failure), nil
}

// decayed applies the half-life to a count over an age: a count untouched for
// one half-life is worth half as much. Counts are continuous, so a fading
// ant's success never rounds to a hard zero, it drifts smoothly back toward
// the beta(1,1) prior (doc 06 section 4.4, D13).
func decayed(count float64, age time.Duration, halfLifeDays float64) float64 {
	if count == 0 || age <= 0 || halfLifeDays <= 0 {
		return count
	}
	days := age.Hours() / 24
	return count * math.Pow(0.5, days/halfLifeDays)
}

// betaSample draws from beta(success+1, failure+1), the posterior over an
// ant's success rate under a uniform prior. An ant with no history draws from
// beta(1,1), which is uniform on [0,1] and can draw high, so a new ant gets
// explored.
func betaSample(rng *rand.Rand, success, failure float64) float64 {
	x := gammaSample(rng, success+1)
	y := gammaSample(rng, failure+1)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// gammaSample draws from Gamma(shape, 1) by Marsaglia and Tsang's method. The
// shape here is always at least one (a Beta count plus one), so the sub-one
// boost is defensive.
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		u := rng.Float64()
		return gammaSample(rng, shape+1) * math.Pow(u, 1/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// updateCentroid folds a task embedding into a trail's running-mean centroid.
// It is written on every outcome in M3 and read by M4's over-breadth fork
// test; a nil embedding or a dimension change leaves the mean adopting what it
// can rather than erroring.
func updateCentroid(old, embed []float32, n int64) []float32 {
	if len(embed) == 0 {
		return old
	}
	if len(old) != len(embed) || n <= 1 {
		return append([]float32(nil), embed...)
	}
	out := make([]float32, len(old))
	inv := float32(1) / float32(n)
	for i := range old {
		out[i] = old[i] + (embed[i]-old[i])*inv
	}
	return out
}
