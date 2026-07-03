package core

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/tamnd/ari/bus"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/journal"
	"github.com/tamnd/ari/nest"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/session/jsonl"
	"github.com/tamnd/ari/version"
)

// Colony is the headless core: one project, one kernel, the population.
// Every client (TUI, one-shot, json stream, serve) drives it through this
// type (doc 01 section 4.1).
type Colony struct {
	nest    nest.Nest
	config  *config.Config
	bus     *bus.Bus
	journal *journal.Journal
	store   session.Store
	runner  TurnRunner
	flags   config.FlagOverrides

	mu       sync.Mutex
	started  bool
	sessions map[SessionID]*sessionState

	ctx      context.Context
	stop     context.CancelFunc
	turns    sync.WaitGroup
	pumps    sync.WaitGroup
	closing  sync.Once
	closeErr error
}

// sessionState serializes turns per session: one runs, the rest queue.
type sessionState struct {
	busy   bool
	queue  []queuedTurn
	cancel context.CancelFunc // the running turn's
}

type queuedTurn struct {
	id  TurnID
	req SubmitRequest
}

// TurnRunner executes one turn. The agent loop installs the real one in a
// later slice; until then the default is honest about what is missing, and
// tests install scripted runners so the command-and-stream shape is
// exercised without a model (D23).
type TurnRunner interface {
	RunTurn(ctx context.Context, t *TurnHandle) error
}

// TurnHandle is what a runner gets: identity, the prompt, the store for
// transcript writes, and an emit path into the journal and the stream.
type TurnHandle struct {
	Session SessionID
	Turn    TurnID
	Request SubmitRequest
	Store   session.Store

	colony *Colony
}

// Emit stamps the event with the turn's session and id, journals it, and
// fans it out. Seq is assigned by the journal's single writer.
func (t *TurnHandle) Emit(typ event.Type, payload any) error {
	return t.colony.emit(typ, string(t.Session), string(t.Turn), payload)
}

// Option configures Open.
type Option func(*Colony)

// WithRunner installs the turn runner. The loop slice makes this the
// default; tests use scripted runners.
func WithRunner(r TurnRunner) Option {
	return func(c *Colony) { c.runner = r }
}

// WithFlags threads command-line overrides into config loading.
func WithFlags(f config.FlagOverrides) Option {
	return func(c *Colony) { c.flags = f }
}

// WithConfig injects a preloaded config, skipping the file load. Tests use
// this to run against a temp nest with no config on disk.
func WithConfig(cfg *config.Config) Option {
	return func(c *Colony) { c.config = cfg }
}

// notYetRunner is the honest default until the loop slice lands (D24).
type notYetRunner struct{}

func (notYetRunner) RunTurn(ctx context.Context, t *TurnHandle) error {
	return Errf(ErrInternal, "the agent loop is not built yet; it arrives with a later slice")
}

// Open constructs a Colony for the project rooted at dir. It resolves the
// nest, loads config, opens the stores, and wires the kernel. It starts no
// goroutine that outlives the caller until Start (doc 01 section 4.1).
func Open(ctx context.Context, dir string, opts ...Option) (*Colony, error) {
	n, err := nest.Resolve(dir)
	if err != nil {
		return nil, Wrap(ErrNest, err, "resolving the nest")
	}
	if err := n.EnsureGlobal(); err != nil {
		return nil, Wrap(ErrNest, err, "preparing the global nest")
	}
	c := &Colony{
		nest:     n,
		bus:      bus.New(),
		runner:   notYetRunner{},
		sessions: make(map[SessionID]*sessionState),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.config == nil {
		cfg, err := config.Load(n, c.flags)
		if err != nil {
			return nil, Wrap(ErrConfig, err, "loading config")
		}
		c.config = cfg
	}
	store, err := jsonl.New(n.SessionsDir())
	if err != nil {
		return nil, Wrap(ErrNest, err, "opening the session store")
	}
	c.store = store
	j, err := journal.Open(n.JournalDir(), func(e event.Event) { c.bus.Publish(e) })
	if err != nil {
		return nil, Wrap(ErrNest, err, "opening the journal")
	}
	c.journal = j
	c.ctx, c.stop = context.WithCancel(context.Background())
	return c, nil
}

// Config exposes the loaded config read-only, for doctor and the clients.
func (c *Colony) Config() *config.Config { return c.config }

// Nest exposes the resolved paths read-only.
func (c *Colony) Nest() nest.Nest { return c.nest }

// Start brings the background goroutines up: the journal writer and the
// bus fan-out. Separate from Open so a client subscribes before any event
// can be produced (doc 01 section 4.1).
func (c *Colony) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	if err := c.journal.Start(ctx); err != nil {
		return Wrap(ErrNest, err, "starting the journal")
	}
	c.started = true
	return nil
}

// Close stops background goroutines, flushes the journal, and closes the
// stores. It is idempotent and safe to call from a signal handler.
func (c *Colony) Close() error {
	c.closing.Do(func() {
		c.stop()       // cancels every running turn and pump
		c.turns.Wait() // turns finish emitting before the journal stops
		c.closeErr = c.journal.Close()
		c.pumps.Wait()
	})
	return c.closeErr
}

func (c *Colony) emit(typ event.Type, sessionID, turnID string, payload any) error {
	e, err := event.New(typ, sessionID, turnID, payload)
	if err != nil {
		return Wrap(ErrInternal, err, "encoding an event")
	}
	c.journal.Append(e)
	return nil
}

// NewSession creates or forks a session and announces it on the stream.
func (c *Colony) NewSession(ctx context.Context, req NewSessionRequest) (SessionID, error) {
	id, err := c.store.Create(ctx, req.Parent, session.SessionMeta{Title: req.Title, AtEntry: req.AtTurn})
	if err != nil {
		return "", Wrap(ErrNest, err, "creating the session")
	}
	if req.Parent != "" {
		_ = c.emit(event.TypeSessionForked, string(id), "", event.SessionForked{ID: string(id), Parent: string(req.Parent), AtTurn: req.AtTurn})
	} else {
		_ = c.emit(event.TypeSessionCreated, string(id), "", event.SessionCreated{ID: string(id), Title: req.Title, Root: c.nest.Root})
	}
	return id, nil
}

// ListSessions returns summaries, newest first.
func (c *Colony) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	out, err := c.store.List(ctx)
	if err != nil {
		return nil, Wrap(ErrNest, err, "listing sessions")
	}
	return out, nil
}

// Load rebuilds a session transcript for resume. Narrow read access per
// doc 01 section 4.2: a value, never a kernel handle.
func (c *Colony) Load(ctx context.Context, s SessionID) (session.Transcript, error) {
	t, err := c.store.Load(ctx, s)
	if err != nil {
		return session.Transcript{}, Wrap(ErrNest, err, "loading the session")
	}
	return t, nil
}

// Submit enqueues a user turn and returns its TurnID. The answer arrives
// only as events (doc 01 section 4.2).
func (c *Colony) Submit(ctx context.Context, req SubmitRequest) (TurnID, error) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return "", Errf(ErrInternal, "the colony is not started")
	}
	c.mu.Unlock()

	// The user entry lands in the transcript before the turn runs, so a
	// crash mid-turn never loses what the user typed.
	body, err := json.Marshal(map[string]string{"text": req.Text})
	if err != nil {
		return "", Wrap(ErrInternal, err, "encoding the user turn")
	}
	entry := session.Entry{
		ID:      session.NewID(),
		Type:    session.EntryUser,
		Time:    time.Now().UTC(),
		Body:    body,
		Version: version.Version,
	}
	if err := c.store.Append(ctx, req.Session, entry); err != nil {
		return "", Wrap(ErrNest, err, "appending the user turn")
	}

	id := TurnID(session.NewID())
	c.mu.Lock()
	st := c.sessions[req.Session]
	if st == nil {
		st = &sessionState{}
		c.sessions[req.Session] = st
	}
	if st.busy {
		st.queue = append(st.queue, queuedTurn{id: id, req: req})
		c.mu.Unlock()
		return id, nil
	}
	st.busy = true
	c.mu.Unlock()
	c.startTurn(id, req)
	return id, nil
}

func (c *Colony) startTurn(id TurnID, req SubmitRequest) {
	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	c.sessions[req.Session].cancel = cancel
	c.mu.Unlock()

	c.turns.Add(1)
	go func() {
		defer c.turns.Done()
		defer cancel()
		h := &TurnHandle{Session: req.Session, Turn: id, Request: req, Store: c.store, colony: c}
		_ = h.Emit(event.TypeTurnStarted, event.TurnStarted{ID: string(id), Ant: workerAnt, Prompt: req.Text})
		err := c.runner.RunTurn(ctx, h)
		fin := event.TurnFinished{ID: string(id), Reason: "done"}
		switch {
		case ctx.Err() != nil:
			fin.Reason = "canceled"
		case err != nil:
			fin.Reason = "error"
			info := Info(err)
			fin.Error = info.Message
			_ = h.Emit(event.TypeError, info)
		}
		_ = h.Emit(event.TypeTurnFinished, fin)
		c.finishTurn(req.Session)
	}()
}

// workerAnt names the ant on turn.started; the router owns this from the
// loop slice on, and M0 has exactly one worker.
const workerAnt = "worker"

func (c *Colony) finishTurn(s SessionID) {
	c.mu.Lock()
	st := c.sessions[s]
	st.cancel = nil
	if len(st.queue) == 0 {
		st.busy = false
		c.mu.Unlock()
		return
	}
	next := st.queue[0]
	st.queue = st.queue[1:]
	c.mu.Unlock()
	if c.ctx.Err() != nil {
		return // closing; queued turns die with the colony
	}
	c.startTurn(next.id, next.req)
}

// Cancel aborts the running turn on a session via context cancellation.
func (c *Colony) Cancel(ctx context.Context, s SessionID) error {
	c.mu.Lock()
	st := c.sessions[s]
	var cancel context.CancelFunc
	if st != nil {
		cancel = st.cancel
	}
	c.mu.Unlock()
	if cancel == nil {
		return Errf(ErrInternal, "no turn is running on session %s", s)
	}
	cancel()
	return nil
}

// Respond answers an outstanding permission request. The pipeline lands in
// a later slice; until then no request can be outstanding, and the answer
// is the honest one.
func (c *Colony) Respond(ctx context.Context, req RespondRequest) error {
	return Errf(ErrPermission, "no outstanding request %s on session %s", req.RequestID, req.Session)
}

// Subscription is one client's view of the event stream. Read C, watch
// Done, call Cancel when finished. C is never closed; Done signals the end
// (the bus contract).
type Subscription struct {
	C      <-chan event.Event
	sub    *bus.Sub
	cancel context.CancelFunc
}

// Done reports termination: the subscription was canceled or the colony
// closed.
func (s *Subscription) Done() <-chan struct{} { return s.sub.Done() }

// Cancel detaches from the stream.
func (s *Subscription) Cancel() {
	s.cancel()
	s.sub.Cancel()
}

// Events subscribes to the stream. The first event is a hello carrying the
// schema version and the resume cursor (doc 01 section 4.2). Session
// filtering happens here; type filtering rides the bus.
func (c *Colony) Events(ctx context.Context, filter EventFilter) (*Subscription, error) {
	inner := c.bus.Subscribe(bus.MustDeliver, 64, filter.Types...)
	out := make(chan event.Event, 64)
	pumpCtx, cancel := context.WithCancel(c.ctx)
	s := &Subscription{C: out, sub: inner, cancel: cancel}

	hello, err := event.New(event.TypeHello, "", "", event.Hello{
		Schema: event.SchemaMajor,
		Cursor: c.journal.Cursor(),
		Server: "ari/" + version.Version,
	})
	if err != nil {
		cancel()
		inner.Cancel()
		return nil, Wrap(ErrInternal, err, "encoding hello")
	}

	c.pumps.Add(1)
	go func() {
		defer c.pumps.Done()
		defer inner.Cancel()
		send := func(e event.Event) bool {
			select {
			case out <- e:
				return true
			case <-pumpCtx.Done():
				return false
			case <-ctx.Done():
				return false
			}
		}
		if !send(hello) {
			return
		}
		for {
			select {
			case e := <-inner.C:
				if filter.Session != "" && e.Session != string(filter.Session) {
					continue
				}
				if !send(e) {
					return
				}
			case <-inner.Done():
				return
			case <-pumpCtx.Done():
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return s, nil
}
