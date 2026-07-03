package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	shMaxResult      = 30_000
	shDefaultTimeout = 2 * time.Minute
	shMaxTimeout     = 10 * time.Minute
	shKillGrace      = 500 * time.Millisecond
)

type shArgs struct {
	Command     string `json:"command"`           // the command line, required
	Timeout     int    `json:"timeout,omitempty"` // ms, capped by the max
	Background  bool   `json:"run_in_background,omitempty"`
	Description string `json:"description,omitempty"` // one-line human summary
}

// ShDisplay is the typed data the UI renders for a shell result: the
// command, its output for the scrollable pane, and how it ended.
type ShDisplay struct {
	Command     string
	Description string
	Output      string
	ExitCode    int
	Killed      bool
	Background  string // the shell id when run in the background
}

// shTool runs a command through the platform shell. Each call is a
// fresh shell; the only state threaded between calls is the cwd, held
// on the ToolContext where the model and the code both see it (doc 04
// section 7.2).
type shTool struct {
	Base
	bg *bgShells
}

// NewSh builds the sh tool with its own background-shell registry, one
// per session because the registry lives as long as the tool value.
func NewSh() Tool { return shTool{bg: newBgShells()} }

func (shTool) Name() string { return "sh" }

func (shTool) Schema() Schema {
	return Schema{
		Name:        "sh",
		Description: "Run a shell command; cwd persists between calls, shell state does not.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "The command line to run."},
				"timeout": {"type": "integer", "description": "Timeout in milliseconds, capped by the configured maximum."},
				"run_in_background": {"type": "boolean", "description": "Run detached and return a shell id immediately."},
				"description": {"type": "string", "description": "One-line human summary of what this command does."}
			},
			"required": ["command"]
		}`),
	}
}

func (shTool) MaxResultSize() int { return shMaxResult }

func (shTool) IsReadOnly(a json.RawMessage) bool        { return shArgsReadOnly(a) }
func (shTool) IsConcurrencySafe(a json.RawMessage) bool { return shArgsReadOnly(a) }
func (shTool) IsDestructive(a json.RawMessage) bool {
	var args shArgs
	if json.Unmarshal(a, &args) != nil {
		return false
	}
	return shIsDestructive(args.Command)
}

// shArgsReadOnly decodes and classifies; a failed parse is not
// read-only, the conservative direction (doc 04 section 7.8).
func shArgsReadOnly(a json.RawMessage) bool {
	var args shArgs
	if json.Unmarshal(a, &args) != nil {
		return false
	}
	if args.Background {
		return false
	}
	return shIsReadOnly(args.Command)
}

func (shTool) MatchPrefix(a json.RawMessage) PrefixMatcher {
	var args shArgs
	if json.Unmarshal(a, &args) != nil {
		return shPrefixMatcher{}
	}
	return newShMatcher(args.Command)
}

func (shTool) ValidateInput(_ context.Context, raw json.RawMessage, _ *ToolContext) error {
	var a shArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return fmt.Errorf("command is required")
	}
	return nil
}

func (s shTool) Call(ctx context.Context, raw json.RawMessage, tc *ToolContext, onProgress ProgressFunc) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var a shArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}

	if a.Background {
		return s.startBackground(ctx, a, tc)
	}

	timeout := shDefaultTimeout
	if a.Timeout > 0 {
		timeout = min(time.Duration(a.Timeout)*time.Millisecond, shMaxTimeout)
	}

	script := withCwdSentinel(a.Command)
	cmd := shellCommand(script)
	cmd.Dir = tc.Cwd
	if tc.Sandbox != nil {
		wrapped, err := tc.Sandbox.Wrap(ctx, cmd, SandboxSpec{Cwd: tc.Cwd})
		if err != nil {
			return nil, fmt.Errorf("sandbox refused the command: %v", err)
		}
		cmd = wrapped
	}

	var buf outputBuffer
	buf.onProgress = onProgress
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	killed, cancelled, runErr := runWithTimeout(ctx, cmd, timeout)

	output, newCwd := extractCwd(buf.String())
	if newCwd != "" && newCwd != tc.Cwd {
		if info, err := os.Stat(newCwd); err == nil && info.IsDir() {
			tc.Cwd = newCwd
		}
	}
	output = redactSecrets(output, os.Environ())

	if cancelled {
		// The loop records the abort in the transcript; the error carries
		// the partial story (doc 01 section 6.6).
		return nil, fmt.Errorf("command was cancelled and its process group killed; partial output:\n%s", output)
	}

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode = exitErr.ExitCode()
		} else if !killed {
			return nil, fmt.Errorf("command did not run: %v", runErr)
		}
	}

	model := output
	switch {
	case killed:
		model = fmt.Sprintf("%s\ncommand timed out after %d ms and was killed; run it in the background or narrow it", output, timeout.Milliseconds())
	case exitCode != 0:
		model = fmt.Sprintf("%s\nexit code %d", output, exitCode)
	}
	if model == "" {
		model = "(no output)"
	}
	return &Result{
		Model: model,
		Display: ShDisplay{
			Command:     a.Command,
			Description: a.Description,
			Output:      output,
			ExitCode:    exitCode,
			Killed:      killed,
		},
	}, nil
}

// runWithTimeout starts the command in its own process group and waits,
// killing the whole group on timeout or context cancel so a shell that
// spawned a build does not orphan the build (doc 04 section 7.3).
func runWithTimeout(ctx context.Context, cmd *exec.Cmd, d time.Duration) (killed, cancelled bool, err error) {
	tctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	setpgid(cmd)
	if err := cmd.Start(); err != nil {
		return false, false, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-tctx.Done():
		killGroup(cmd, shKillGrace)
		<-done
		return ctx.Err() == nil, ctx.Err() != nil, tctx.Err()
	case err := <-done:
		return false, false, err
	}
}

// startBackground launches the command detached, registers it, and
// returns the shell id immediately. Output accumulates in the job's
// buffer; the loop surfaces the exit (doc 04 section 7.4).
func (s shTool) startBackground(ctx context.Context, a shArgs, tc *ToolContext) (*Result, error) {
	cmd := shellCommand(a.Command)
	cmd.Dir = tc.Cwd
	if tc.Sandbox != nil {
		wrapped, err := tc.Sandbox.Wrap(ctx, cmd, SandboxSpec{Cwd: tc.Cwd})
		if err != nil {
			return nil, fmt.Errorf("sandbox refused the command: %v", err)
		}
		cmd = wrapped
	}
	job, err := s.bg.start(cmd)
	if err != nil {
		return nil, fmt.Errorf("command did not start: %v", err)
	}
	return &Result{
		Model:   fmt.Sprintf("started background shell %s; its output and exit will be reported, do not poll or sleep waiting on it", job.ID),
		Display: ShDisplay{Command: a.Command, Description: a.Description, Background: job.ID},
	}, nil
}

// Background returns the session's background-shell registry, for the
// loop to surface output and exits.
func (s shTool) Background() *bgShells { return s.bg }

// bgShells is the per-session registry of detached shells.
type bgShells struct {
	mu   sync.Mutex
	n    int
	jobs map[string]*bgJob
}

type bgJob struct {
	ID   string
	Done chan struct{}

	mu   sync.Mutex
	buf  bytes.Buffer
	exit int
	cmd  *exec.Cmd
}

func newBgShells() *bgShells { return &bgShells{jobs: map[string]*bgJob{}} }

func (b *bgShells) start(cmd *exec.Cmd) (*bgJob, error) {
	b.mu.Lock()
	b.n++
	job := &bgJob{ID: fmt.Sprintf("bg%d", b.n), Done: make(chan struct{}), cmd: cmd}
	b.mu.Unlock()

	cmd.Stdout = bgWriter{job}
	cmd.Stderr = bgWriter{job}
	setpgid(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.jobs[job.ID] = job
	b.mu.Unlock()
	go func() {
		err := cmd.Wait()
		job.mu.Lock()
		if exitErr, ok := err.(*exec.ExitError); ok {
			job.exit = exitErr.ExitCode()
		}
		job.mu.Unlock()
		close(job.Done)
	}()
	return job, nil
}

func (b *bgShells) get(id string) *bgJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.jobs[id]
}

// Output returns what the job has written so far, secrets redacted.
func (j *bgJob) Output() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return redactSecrets(j.buf.String(), os.Environ())
}

// ExitCode is only meaningful after Done is closed.
func (j *bgJob) ExitCode() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.exit
}

// Kill terminates the job's whole process group.
func (j *bgJob) Kill() { killGroup(j.cmd, shKillGrace) }

type bgWriter struct{ job *bgJob }

func (w bgWriter) Write(p []byte) (int, error) {
	w.job.mu.Lock()
	defer w.job.mu.Unlock()
	return w.job.buf.Write(p)
}

// outputBuffer collects combined output and streams progress chunks.
type outputBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	onProgress ProgressFunc
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	if b.onProgress != nil {
		b.onProgress(string(p))
	}
	return n, err
}

func (b *outputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// secretNamePattern flags environment variables whose value must never
// reach model context (D16). The name is what an error or a redaction
// marker may mention; the value never.
var secretNamePattern = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH)`)

// redactSecrets replaces the value of every secret-bearing environment
// variable that appears in the output with a marker naming the variable,
// so a sh env or an echoed key never lands in the transcript (doc 04
// section 7.5, D16).
func redactSecrets(output string, environ []string) string {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || len(value) < 6 || !secretNamePattern.MatchString(name) {
			continue
		}
		output = strings.ReplaceAll(output, value, "<redacted:"+name+">")
	}
	return output
}
