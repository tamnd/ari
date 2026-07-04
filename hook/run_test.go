package hook

import (
	"context"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, command string, timeout time.Duration) Result {
	t.Helper()
	c := Command{Event: PostToolUse, Command: command, Timeout: timeout}
	return runCommand(context.Background(), c, []byte(`{"event":"PostToolUse"}`), nil)
}

func TestRunExitZeroPlainText(t *testing.T) {
	res := run(t, "echo formatted", time.Second)
	if res.ExitCode != 0 || res.Blocking || res.NonBlockingError {
		t.Fatalf("plain exit 0: %+v", res)
	}
	if res.Output != nil {
		t.Fatalf("plain text should not parse as control: %+v", res.Output)
	}
}

func TestRunExitZeroControl(t *testing.T) {
	res := run(t, `echo '{"additionalContext":"note"}'`, time.Second)
	if res.ExitCode != 0 || res.Output == nil {
		t.Fatalf("control exit 0: %+v", res)
	}
	if res.Output.AdditionalContext != "note" {
		t.Fatalf("context = %q", res.Output.AdditionalContext)
	}
}

func TestRunExitTwoBlocks(t *testing.T) {
	res := run(t, "echo nope >&2; exit 2", time.Second)
	if !res.Blocking {
		t.Fatalf("exit 2 should block: %+v", res)
	}
	if res.Message == "" {
		t.Fatal("block should carry the stderr message")
	}
}

func TestRunOtherExitNonBlocking(t *testing.T) {
	res := run(t, "echo broke >&2; exit 1", time.Second)
	if res.Blocking {
		t.Fatal("exit 1 must not block")
	}
	if !res.NonBlockingError {
		t.Fatalf("exit 1 should be a non-blocking error: %+v", res)
	}
}

// TestRunRedactsSecretsFromStdout proves a hook that echoes a secret env
// value cannot leak it into the transcript: the raw stdout carries the marker
// naming the variable, never the value (D16).
func TestRunRedactsSecretsFromStdout(t *testing.T) {
	t.Setenv("MY_API_TOKEN", "sk-live-abcdef123456")
	res := run(t, `echo "the key is $MY_API_TOKEN"`, time.Second)
	if strings.Contains(res.Stdout, "sk-live-abcdef123456") {
		t.Fatalf("secret value leaked into stdout: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "<redacted:MY_API_TOKEN>") {
		t.Fatalf("stdout missing the redaction marker: %q", res.Stdout)
	}
}

// TestRunRedactsSecretsFromContext proves the scrub runs before parsing, so a
// secret smuggled through additionalContext is redacted before it can reach
// model context (D16).
func TestRunRedactsSecretsFromContext(t *testing.T) {
	t.Setenv("DEPLOY_SECRET", "hunter2hunter2")
	res := run(t, `printf '{"additionalContext":"token %s"}' "$DEPLOY_SECRET"`, time.Second)
	if res.Output == nil {
		t.Fatalf("control output did not parse: %+v", res)
	}
	if strings.Contains(res.Output.AdditionalContext, "hunter2hunter2") {
		t.Fatalf("secret leaked into additionalContext: %q", res.Output.AdditionalContext)
	}
	if !strings.Contains(res.Output.AdditionalContext, "<redacted:DEPLOY_SECRET>") {
		t.Fatalf("additionalContext missing the redaction marker: %q", res.Output.AdditionalContext)
	}
}

func TestRunTimeoutIsNonBlocking(t *testing.T) {
	// A hook that runs past its timeout is killed and degrades to a warning,
	// never a silent allow and never a block it did not reach.
	res := run(t, "sleep 5", 50*time.Millisecond)
	if res.Blocking {
		t.Fatal("a timed-out hook must not block")
	}
	if !res.NonBlockingError {
		t.Fatalf("timeout should be a non-blocking error: %+v", res)
	}
}
