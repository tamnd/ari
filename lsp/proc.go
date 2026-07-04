package lsp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// execDialer is the production dialer: it discovers the adapter's binary
// on PATH and launches it, wiring the process's stdio to the JSON-RPC
// connection. A binary that is not on PATH returns an error, which the
// service turns into a broken entry so the absent server degrades to zero
// diagnostics rather than failing the edit (plan 02 slice 5).
func execDialer(ctx context.Context, a Adapter, root string) (*rpcConn, io.Closer, error) {
	bin, err := exec.LookPath(a.Command)
	if err != nil {
		return nil, nil, fmt.Errorf("lsp: %s not found on PATH: %w", a.Command, err)
	}
	cmd := exec.Command(bin, a.Args...)
	cmd.Dir = root

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	conn := newConn(stdout, stdin, nil)
	return conn, &procCloser{cmd: cmd, stdin: stdin}, nil
}

// procCloser tears the server process down: close its stdin so it sees
// EOF, then wait so the child is reaped rather than orphaned.
type procCloser struct {
	cmd   *exec.Cmd
	stdin io.Closer
}

func (p *procCloser) Close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
	return nil
}
