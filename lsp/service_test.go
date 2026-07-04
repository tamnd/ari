package lsp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// closerFunc adapts a function to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// fakeDialer returns a dialer backed by an in-memory server that answers
// the initialize handshake and, on every didOpen or didChange, publishes
// the given diagnostics for the touched document. It also counts how many
// times it was dialed, so a test can assert a server spawns once. When
// failStart is true the dial fails, standing in for a binary that will
// not launch.
func fakeDialer(t *testing.T, publish []Diagnostic, failStart bool) (dialer, *int64) {
	t.Helper()
	var calls int64
	d := func(ctx context.Context, a Adapter, root string) (*rpcConn, io.Closer, error) {
		atomic.AddInt64(&calls, 1)
		if failStart {
			return nil, nil, io.ErrUnexpectedEOF
		}
		clientR, serverW := io.Pipe() // server -> client
		serverR, clientW := io.Pipe() // client -> server
		client := newConn(clientR, clientW, nil)
		srv := newConn(serverR, serverW, nil)
		srv.setHandler(func(method string, params json.RawMessage) {
			if method != "textDocument/didOpen" && method != "textDocument/didChange" {
				return
			}
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(params, &p)
			_ = srv.notify("textDocument/publishDiagnostics", buildPublish(p.TextDocument.URI, publish))
		})
		t.Cleanup(func() { _ = srv.close() })
		return client, closerFunc(func() error { return nil }), nil
	}
	return d, &calls
}

// buildPublish renders our Diagnostic list into an LSP publishDiagnostics
// params object, converting back to 0-based positions and numeric
// severities so it exercises the real onNotify decoder.
func buildPublish(uri string, ds []Diagnostic) map[string]any {
	arr := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		sev := 2
		if d.Severity == "error" {
			sev = 1
		}
		arr = append(arr, map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": d.Line - 1, "character": d.Col - 1},
				"end":   map[string]any{"line": d.Line - 1, "character": d.Col},
			},
			"severity": sev,
			"message":  d.Message,
		})
	}
	return map[string]any{"uri": uri, "diagnostics": arr}
}

// writeGo drops a .go file so touch has something on disk to read.
func writeGo(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDisabledIsNoop: a client that config left off never dials and never
// reports, so a user who does not want a language server pays nothing.
func TestDisabledIsNoop(t *testing.T) {
	dial, calls := fakeDialer(t, nil, false)
	s := New(Options{Enabled: false, Root: t.TempDir(), dialer: dial})
	path := writeGo(t, t.TempDir(), "x.go")

	if err := s.Touch(context.Background(), path, TouchDocument); err != nil {
		t.Fatalf("Touch on a disabled client: %v", err)
	}
	if got := s.Diagnostics(path); got != nil {
		t.Errorf("disabled client reported %v, want none", got)
	}
	if *calls != 0 {
		t.Errorf("disabled client dialed %d times, want 0", *calls)
	}
}

// TestLazySpawnAndDiagnostics: the first touch of a matching file spawns
// the server, and the error it publishes comes back through Diagnostics.
func TestLazySpawnAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	want := []Diagnostic{{Line: 7, Col: 12, Severity: "error", Message: "undefined: time"}}
	dial, calls := fakeDialer(t, want, false)
	s := New(Options{Enabled: true, Root: root, dialer: dial})
	t.Cleanup(s.Shutdown)
	path := writeGo(t, root, "greeter.go")

	if err := s.Touch(context.Background(), path, TouchDocument); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got := s.Diagnostics(path)
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Diagnostics = %v, want %v", got, want)
	}
	if *calls != 1 {
		t.Errorf("dialed %d times, want 1", *calls)
	}
}

// TestNoDoubleSpawn: two touches racing on the first spawn start exactly
// one server; the spawning guard makes the second wait rather than launch
// a second process.
func TestNoDoubleSpawn(t *testing.T) {
	root := t.TempDir()
	dial, calls := fakeDialer(t, nil, false)
	s := New(Options{Enabled: true, Root: root, dialer: dial})
	t.Cleanup(s.Shutdown)
	a := writeGo(t, root, "a.go")
	b := writeGo(t, root, "b.go")

	var wg sync.WaitGroup
	for _, p := range []string{a, b} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			_ = s.Touch(context.Background(), path, TouchDocument)
		}(p)
	}
	wg.Wait()

	if *calls != 1 {
		t.Errorf("dialed %d times, want exactly 1 despite the race", *calls)
	}
}

// TestUnmatchedExtensionIgnored: a file no adapter owns is a no-op, never
// a spawn.
func TestUnmatchedExtensionIgnored(t *testing.T) {
	root := t.TempDir()
	dial, calls := fakeDialer(t, nil, false)
	s := New(Options{Enabled: true, Root: root, dialer: dial})
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(context.Background(), path, TouchDocument); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if *calls != 0 {
		t.Errorf("dialed %d times for an unmatched extension, want 0", *calls)
	}
}

// TestBrokenServerNotRespawned: a dial that fails quarantines the adapter,
// so the second edit does not pay the failed-start cost again.
func TestBrokenServerNotRespawned(t *testing.T) {
	root := t.TempDir()
	dial, calls := fakeDialer(t, nil, true)
	s := New(Options{Enabled: true, Root: root, dialer: dial})
	path := writeGo(t, root, "x.go")

	for i := range 3 {
		if err := s.Touch(context.Background(), path, TouchDocument); err != nil {
			t.Fatalf("Touch %d returned an error, want a graceful no-op: %v", i, err)
		}
	}
	if got := s.Diagnostics(path); got != nil {
		t.Errorf("a broken server reported %v, want none", got)
	}
	if *calls != 1 {
		t.Errorf("a broken adapter was dialed %d times, want 1", *calls)
	}
	st := s.Status()
	if len(st) != 1 || st[0].State != StateBroken {
		t.Errorf("status = %v, want one broken server", st)
	}
}

// TestAbsentBinaryDegrades exercises the real exec dialer: an adapter
// whose binary is not on PATH degrades to zero diagnostics, never a
// failed edit.
func TestAbsentBinaryDegrades(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(Adapter{
		Name: "ghost", Command: "ari-no-such-language-server-xyz",
		Extensions: []string{".zz"}, LanguageID: "zz",
	})
	s := New(Options{Enabled: true, Root: root, Registry: reg}) // default exec dialer
	path := filepath.Join(root, "x.zz")
	if err := os.WriteFile(path, []byte("z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch(context.Background(), path, TouchDocument); err != nil {
		t.Fatalf("Touch with an absent binary: %v", err)
	}
	if got := s.Diagnostics(path); got != nil {
		t.Errorf("absent binary reported %v, want none", got)
	}
}

// TestTouchTimeoutIsNotAnError: a server that never publishes leaves the
// touch to time out and return cleanly, the R1 mitigation: a silent
// server is zero diagnostics, not a hang.
func TestTouchTimeoutIsNotAnError(t *testing.T) {
	root := t.TempDir()
	// A dialer whose server answers initialize but never publishes.
	silent := func(ctx context.Context, a Adapter, root string) (*rpcConn, io.Closer, error) {
		clientR, serverW := io.Pipe()
		serverR, clientW := io.Pipe()
		client := newConn(clientR, clientW, nil)
		srv := newConn(serverR, serverW, nil)
		srv.setHandler(func(string, json.RawMessage) {})
		t.Cleanup(func() { _ = srv.close() })
		return client, closerFunc(func() error { return nil }), nil
	}
	s := New(Options{Enabled: true, Root: root, dialer: silent, Timeout: 100 * time.Millisecond})
	t.Cleanup(s.Shutdown)
	path := writeGo(t, root, "x.go")

	start := time.Now()
	if err := s.Touch(context.Background(), path, TouchDocument); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("touch returned in %v, expected it to wait out the timeout", elapsed)
	}
	if got := s.Diagnostics(path); got != nil {
		t.Errorf("silent server reported %v, want none", got)
	}
}
