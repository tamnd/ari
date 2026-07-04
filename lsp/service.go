package lsp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// defaultTouchTimeout bounds how long a touch waits for the server to
// publish diagnostics before returning what it has. It is what turns a
// slow or silent server from a hang into a zero-diagnostics result, the
// R1 mitigation made concrete (plan 02 slice 5).
const defaultTouchTimeout = 3 * time.Second

// errBroken marks an adapter that failed to start; a broken adapter is
// not respawned on the next edit.
var errBroken = errors.New("lsp: server is broken")

// dialer opens a connection to a freshly launched server for an adapter
// rooted at root. The default dialer execs the adapter's binary; tests
// inject a fake so the spawn logic runs without a real server.
type dialer func(ctx context.Context, a Adapter, root string) (*rpcConn, io.Closer, error)

// State is a server's lifecycle for the sidebar (slice 6): it is
// spawning, ready, or broken.
type State string

const (
	StateSpawning State = "spawning"
	StateReady    State = "ready"
	StateBroken   State = "broken"
)

// Status is one server's state and diagnostic counts, the projection the
// sidebar renders.
type Status struct {
	Name     string
	State    State
	Errors   int
	Warnings int
}

// Service is the LSPClient implementation. It resolves a path to an
// adapter, spawns that adapter's server lazily on first touch, guards
// against a double-spawn, and quarantines a server that failed to start
// so it is not respawned every edit.
type Service struct {
	enabled bool
	reg     *Registry
	root    string
	dial    dialer
	timeout time.Duration

	mu       sync.Mutex
	cond     *sync.Cond
	servers  map[string]*server
	spawning map[string]bool
	broken   map[string]bool
}

// Options configures a Service.
type Options struct {
	// Enabled gates the whole client; false makes every call a no-op.
	Enabled bool
	// Root is the workspace root sent to the server on initialize.
	Root string
	// Registry resolves paths to adapters; nil means DefaultRegistry.
	Registry *Registry
	// Timeout bounds the per-touch diagnostics wait; zero means the default.
	Timeout time.Duration
	// dialer opens a server connection; nil means the exec dialer.
	dialer dialer
}

// New builds a Service. When Enabled is false it still returns a usable
// client whose operations are no-ops, so callers never branch on nil.
func New(o Options) *Service {
	reg := o.Registry
	if reg == nil {
		reg = DefaultRegistry()
	}
	to := o.Timeout
	if to <= 0 {
		to = defaultTouchTimeout
	}
	d := o.dialer
	if d == nil {
		d = execDialer
	}
	s := &Service{
		enabled:  o.Enabled,
		reg:      reg,
		root:     o.Root,
		dial:     d,
		timeout:  to,
		servers:  map[string]*server{},
		spawning: map[string]bool{},
		broken:   map[string]bool{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Touch implements LSPClient. A disabled client, an unmatched path, or a
// server that will not start all yield nil: the edit proceeds and simply
// gets no diagnostics.
func (s *Service) Touch(ctx context.Context, path string, kind TouchKind) error {
	if !s.enabled {
		return nil
	}
	a, ok := s.reg.forPath(path)
	if !ok {
		return nil
	}
	srv, err := s.server(ctx, a)
	if err != nil {
		return nil // a broken or absent server is zero diagnostics, never a failed edit
	}
	tctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return srv.touch(tctx, path, kind)
}

// Diagnostics implements LSPClient.
func (s *Service) Diagnostics(path string) []Diagnostic {
	if !s.enabled {
		return nil
	}
	a, ok := s.reg.forPath(path)
	if !ok {
		return nil
	}
	s.mu.Lock()
	srv := s.servers[a.Name]
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.diagnostics(path)
}

// Status reports every known server's state and counts for the sidebar.
func (s *Service) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Status
	for _, a := range s.reg.adapters {
		st := Status{Name: a.Name}
		switch {
		case s.broken[a.Name]:
			st.State = StateBroken
		case s.spawning[a.Name]:
			st.State = StateSpawning
		case s.servers[a.Name] != nil:
			st.State = StateReady
			for _, ds := range s.servers[a.Name].allDiagnostics() {
				for _, d := range ds {
					switch d.Severity {
					case "error":
						st.Errors++
					case "warning":
						st.Warnings++
					}
				}
			}
		default:
			continue // an adapter never touched has no status to show
		}
		out = append(out, st)
	}
	return out
}

// Shutdown closes every running server. It is safe to call more than once.
func (s *Service) Shutdown() {
	s.mu.Lock()
	servers := s.servers
	s.servers = map[string]*server{}
	s.mu.Unlock()
	for _, srv := range servers {
		_ = srv.close()
	}
}

// server returns the running server for an adapter, spawning it on first
// use. A concurrent second caller waits on the spawn rather than starting
// a second server, and a failed spawn quarantines the adapter.
func (s *Service) server(ctx context.Context, a Adapter) (*server, error) {
	s.mu.Lock()
	for {
		if s.broken[a.Name] {
			s.mu.Unlock()
			return nil, errBroken
		}
		if srv := s.servers[a.Name]; srv != nil {
			s.mu.Unlock()
			return srv, nil
		}
		if s.spawning[a.Name] {
			s.cond.Wait() // another caller is spawning; wait for it to finish
			continue
		}
		break
	}
	s.spawning[a.Name] = true
	s.mu.Unlock()

	srv, err := s.spawn(ctx, a)

	s.mu.Lock()
	delete(s.spawning, a.Name)
	if err != nil {
		s.broken[a.Name] = true
	} else {
		s.servers[a.Name] = srv
	}
	s.cond.Broadcast()
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return srv, nil
}

// spawn dials a server, roots it, and runs the handshake. A failure at
// any step returns an error the caller turns into a broken entry.
func (s *Service) spawn(ctx context.Context, a Adapter) (*server, error) {
	root := s.workspaceRoot(a)
	conn, closer, err := s.dial(ctx, a, root)
	if err != nil {
		return nil, err
	}
	// Bind the closer into the conn so server.close tears down the process.
	conn.closer = closer
	srv := newServer(a, conn, root)
	if err := srv.initialize(ctx); err != nil {
		_ = srv.close()
		return nil, err
	}
	return srv, nil
}

// workspaceRoot walks up from the service root looking for one of the
// adapter's root markers, falling back to the service root when none is
// found, so gopls anchors on the module rather than a random directory.
func (s *Service) workspaceRoot(a Adapter) string {
	if len(a.RootMarkers) == 0 {
		return s.root
	}
	for dir := s.root; dir != ""; {
		for _, marker := range a.RootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return s.root
}
