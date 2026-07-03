//go:build unix

package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tamnd/ari/kernel/eval"
)

func callSh(t *testing.T, tc *ToolContext, tool Tool, args string) (*Result, error) {
	t.Helper()
	if err := tool.ValidateInput(context.Background(), json.RawMessage(args), tc); err != nil {
		return nil, err
	}
	return tool.Call(context.Background(), json.RawMessage(args), tc, nil)
}

func TestLsIsReadOnlyAndRmIsDestructive(t *testing.T) {
	s := NewSh()
	ls := json.RawMessage(`{"command":"ls -la"}`)
	rm := json.RawMessage(`{"command":"rm -rf /tmp/x"}`)

	if !s.IsReadOnly(ls) || !s.IsConcurrencySafe(ls) {
		t.Error("ls must be read-only and concurrency-safe, decided from the arguments")
	}
	if s.IsDestructive(ls) {
		t.Error("ls is not destructive")
	}
	if s.IsReadOnly(rm) || s.IsConcurrencySafe(rm) {
		t.Error("rm must be neither read-only nor concurrency-safe")
	}
	if !s.IsDestructive(rm) {
		t.Error("rm is destructive")
	}
}

// TestCwdPersistsButShellStateDoesNot is the honesty contract of doc 04
// section 7.2: the cwd threads between calls, exported variables and
// functions do not, because each call is a fresh shell.
func TestCwdPersistsButShellStateDoesNot(t *testing.T) {
	tc := testContext(t)
	s := NewSh()
	sub := filepath.Join(tc.Cwd, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := callSh(t, tc, s, `{"command":"cd subdir; export ARI_LEAK=leaked; ari_fn() { echo fn; }"}`); err != nil {
		t.Fatalf("first call: %v", err)
	}

	res, err := callSh(t, tc, s, `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("pwd after cd: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}
	gotCwd, err := filepath.EvalSymlinks(strings.TrimSpace(res.Model))
	if err != nil {
		t.Fatalf("pwd output %q did not resolve: %v", strings.TrimSpace(res.Model), err)
	}
	if gotCwd != wantCwd {
		t.Errorf("cwd after a cd = %q, want %q", gotCwd, wantCwd)
	}

	res, err = callSh(t, tc, s, `{"command":"echo \"[${ARI_LEAK}]\""}`)
	if err != nil {
		t.Fatalf("env probe: %v", err)
	}
	if !strings.HasPrefix(res.Model, "[]") {
		t.Errorf("an exported variable leaked across calls: %q", res.Model)
	}

	res, err = callSh(t, tc, s, `{"command":"ari_fn 2>/dev/null || echo no-fn"}`)
	if err != nil {
		t.Fatalf("function probe: %v", err)
	}
	if !strings.Contains(res.Model, "no-fn") {
		t.Errorf("a shell function leaked across calls: %q", res.Model)
	}
}

func TestNonZeroExitReportsTheCode(t *testing.T) {
	tc := testContext(t)
	res, err := callSh(t, tc, NewSh(), `{"command":"echo before-failure; exit 3"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Model, "before-failure") {
		t.Errorf("output lost: %q", res.Model)
	}
	if !strings.HasSuffix(res.Model, "exit code 3") {
		t.Errorf("model = %q, want the exit code line last", res.Model)
	}
	display := res.Display.(ShDisplay)
	if display.ExitCode != 3 {
		t.Errorf("display exit = %d, want 3", display.ExitCode)
	}
}

// waitForFile polls until the file exists and is non-empty.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the probe file never appeared")
	return ""
}

// waitForDeath polls until the pid is gone.
func waitForDeath(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("pid %d survived the group kill", pid)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// TestTimeoutKillsTheWholeProcessGroup: the shell spawned a child; the
// timeout must reach the child, not orphan it (doc 04 section 7.3).
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	tc := testContext(t)
	res, err := callSh(t, tc, NewSh(),
		`{"command":"sleep 30 & echo $!; wait","timeout":400}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Model, "command timed out after 400 ms and was killed; run it in the background or narrow it") {
		t.Errorf("model = %q, want the timeout note", res.Model)
	}
	display := res.Display.(ShDisplay)
	if !display.Killed {
		t.Error("display must record the kill")
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(display.Output), "%d", &pid); err != nil {
		t.Fatalf("no child pid in output %q: %v", display.Output, err)
	}
	waitForDeath(t, pid)
}

// TestCancelKillsTheProcessGroup is the sibling-abort shape: a context
// cancel is a group kill and the call reports the abort (doc 01 section
// 6.6).
func TestCancelKillsTheProcessGroup(t *testing.T) {
	tc := testContext(t)
	pidFile := filepath.Join(tc.Cwd, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewSh()
	args := json.RawMessage(fmt.Sprintf(`{"command":"sleep 30 & echo $! > %s; wait"}`, pidFile))
	if err := s.ValidateInput(ctx, args, tc); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := s.Call(ctx, args, tc, nil)
		errCh <- err
	}()

	pidText := waitForFile(t, pidFile)
	var pid int
	if _, err := fmt.Sscanf(pidText, "%d", &pid); err != nil {
		t.Fatalf("pid file %q: %v", pidText, err)
	}
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("a cancelled command must return the abort as an error for the transcript")
	}
	if !strings.Contains(err.Error(), "command was cancelled and its process group killed") {
		t.Errorf("abort = %q", err.Error())
	}
	waitForDeath(t, pid)
}

// TestSecretsAreRedactedFromShOutput is D16 at the sh boundary: an
// echoed secret-bearing variable never reaches the transcript.
func TestSecretsAreRedactedFromShOutput(t *testing.T) {
	const secret = "hunter2-secret-value-8842"
	t.Setenv("ARI_TEST_API_TOKEN", secret)
	tc := testContext(t)

	res, err := callSh(t, tc, NewSh(), `{"command":"echo token=$ARI_TEST_API_TOKEN"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if strings.Contains(res.Model, secret) {
		t.Fatal("the secret value reached the model result")
	}
	if !strings.Contains(res.Model, "token=<redacted:ARI_TEST_API_TOKEN>") {
		t.Errorf("model = %q, want the redaction marker naming the variable", res.Model)
	}
}

// TestShOutputSpillsAtItsCap: a long build log spills head-heavy with a
// readable path (doc 04 section 3.2).
func TestShOutputSpillsAtItsCap(t *testing.T) {
	tc := testContext(t)
	s := NewSh()
	res, err := callSh(t, tc, s, `{"command":"i=0; while [ $i -lt 2000 ]; do echo line-$i-aaaaaaaaaaaaaaaaaaaaaa; i=$((i+1)); done"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Model) <= shMaxResult {
		t.Fatalf("the fixture must overflow the cap, got %d bytes", len(res.Model))
	}
	got, err := ApplyResultBudget(res, s, tc)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if !strings.Contains(got, "truncated. Full output at ") {
		t.Error("an oversized sh result must spill with a pointer")
	}
	if !strings.Contains(got, "line-0-") {
		t.Error("the head of the output must survive in the preview")
	}
}

func TestBackgroundShellReturnsAnIdAndBuffersOutput(t *testing.T) {
	tc := testContext(t)
	s := NewSh().(shTool)

	res, err := callSh(t, tc, s, `{"command":"echo bg-output-marker","run_in_background":true}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Model, "started background shell bg1") {
		t.Errorf("model = %q, want the shell id", res.Model)
	}
	if !strings.Contains(res.Model, "do not poll or sleep") {
		t.Errorf("model = %q, want the no-polling guidance", res.Model)
	}
	job := s.Background().get("bg1")
	if job == nil {
		t.Fatal("the job must be registered under its id")
	}
	select {
	case <-job.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("the background job never finished")
	}
	if !strings.Contains(job.Output(), "bg-output-marker") {
		t.Errorf("job output = %q", job.Output())
	}
	if job.ExitCode() != 0 {
		t.Errorf("exit = %d", job.ExitCode())
	}
}

func TestBackgroundIsNeverConcurrencySafe(t *testing.T) {
	s := NewSh()
	bg := json.RawMessage(`{"command":"ls","run_in_background":true}`)
	if s.IsReadOnly(bg) || s.IsConcurrencySafe(bg) {
		t.Error("a background launch mutates session state and is not read-only")
	}
}

func TestEmptyCommandIsAValidationError(t *testing.T) {
	tc := testContext(t)
	_, err := callSh(t, tc, NewSh(), `{"command":"  "}`)
	if err == nil || err.Error() != "command is required" {
		t.Errorf("err = %v, want command is required", err)
	}
}

// TestShResultRendererGolden pins the model-facing shape: output first,
// exit code line last, wide lines untouched because wrapping is the
// TUI's job (doc 02 section 4.3).
func TestShResultRendererGolden(t *testing.T) {
	tc := testContext(t)
	res, err := callSh(t, tc, NewSh(),
		`{"command":"printf 'short line\nthis is a deliberately wide line that keeps going and going and going and going so the TUI pane has something to scroll horizontally\n'; exit 3"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	eval.Golden(t, "sh_result", res.Model)
}
