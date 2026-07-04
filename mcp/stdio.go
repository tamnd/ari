package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Connect spawns a stdio server per its spec, wires a duplex stream to its
// stdin and stdout, performs the MCP handshake, and returns a ready client.
// Only the stdio transport is supported in M1; any other transport is a
// configuration error rather than a silent skip, so a user who wrote
// transport = "http" learns it is not wired yet.
func Connect(ctx context.Context, spec ServerSpec) (*Client, error) {
	if spec.Transport != "" && spec.Transport != "stdio" {
		return nil, fmt.Errorf("transport %q is not supported in this release; use stdio", spec.Transport)
	}
	if spec.Command == "" {
		return nil, fmt.Errorf("server has no command")
	}

	rwc, err := startStdio(ctx, spec)
	if err != nil {
		return nil, err
	}
	c := newClient(rwc)
	if err := c.Initialize(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initializing server: %w", err)
	}
	return c, nil
}

// startStdio launches the child process and returns a stream that writes to
// its stdin and reads from its stdout. Closing the stream kills the process
// so it cannot outlive the session.
func startStdio(ctx context.Context, spec ServerSpec) (io.ReadWriteCloser, error) {
	// The process lifetime is the session, not the turn: it is decoupled
	// from ctx so a finished turn does not kill a server the next turn
	// needs, and it is ended only by Close (or by the OS on ari exit).
	cmd := exec.CommandContext(context.WithoutCancel(ctx), spec.Command, spec.Args...)
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// The server's own logs go to the parent's stderr, never onto stdout,
	// so a log line cannot be mistaken for a protocol message.
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("wiring server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("wiring server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting server %q: %w", spec.Command, err)
	}
	return &procStream{cmd: cmd, in: stdin, out: stdout}, nil
}

// procStream is the duplex stream over a child process: reads pull from its
// stdout, writes push to its stdin, and Close ends the process.
type procStream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (p *procStream) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *procStream) Write(b []byte) (int, error) { return p.in.Write(b) }

// Close ends the process: it closes stdin so a well-behaved server exits,
// kills the process if it lingers, and reaps it so no zombie is left.
func (p *procStream) Close() error {
	_ = p.in.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
	return nil
}
