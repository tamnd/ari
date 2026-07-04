package hook

import (
	"context"
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
