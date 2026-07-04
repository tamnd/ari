package hook

import (
	"testing"
	"time"
)

func TestSpecBuildDefaults(t *testing.T) {
	c, err := Spec{Command: "echo hi"}.Build(PostToolUse, "project")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.Event != PostToolUse || c.Layer != "project" {
		t.Fatalf("event/layer not set: %+v", c)
	}
	if c.Timeout != defaultToolTimeout {
		t.Fatalf("tool timeout = %v, want %v", c.Timeout, defaultToolTimeout)
	}
	fast, err := Spec{Command: "echo hi"}.Build(SessionStart, "user")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if fast.Timeout != defaultFastTimeout {
		t.Fatalf("fast timeout = %v, want %v", fast.Timeout, defaultFastTimeout)
	}
}

func TestSpecBuildEmptyCommand(t *testing.T) {
	if _, err := (Spec{Command: "   "}).Build(PreToolUse, "user"); err == nil {
		t.Fatal("empty command should error")
	}
}

func TestSpecBuildBadTimeout(t *testing.T) {
	if _, err := (Spec{Command: "x", Timeout: "soon"}).Build(PreToolUse, "user"); err == nil {
		t.Fatal("bad timeout should error")
	}
	if _, err := (Spec{Command: "x", Timeout: "-1s"}).Build(PreToolUse, "user"); err == nil {
		t.Fatal("non-positive timeout should error")
	}
	c, err := (Spec{Command: "x", Timeout: "3s"}).Build(PreToolUse, "user")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.Timeout != 3*time.Second {
		t.Fatalf("timeout = %v, want 3s", c.Timeout)
	}
}

func TestSpecBuildBadMatcher(t *testing.T) {
	if _, err := (Spec{Command: "x", Matcher: "("}).Build(PreToolUse, "user"); err == nil {
		t.Fatal("uncompilable regex matcher should error")
	}
}

func TestMatcherApplies(t *testing.T) {
	cases := []struct {
		matcher string
		tool    string
		want    bool
	}{
		{"", "read", true},
		{"*", "anything", true},
		{"write", "write", true},
		{"write", "read", false},
		{"write|edit", "edit", true},
		{"write|edit", "sh", false},
		{"^wr.*", "write", true},
		{"^wr.*", "read", false},
	}
	for _, tc := range cases {
		c, err := Spec{Command: "x", Matcher: tc.matcher}.Build(PreToolUse, "user")
		if err != nil {
			t.Fatalf("Build(%q): %v", tc.matcher, err)
		}
		if got := c.Applies(tc.tool); got != tc.want {
			t.Errorf("matcher %q tool %q: Applies=%v want %v", tc.matcher, tc.tool, got, tc.want)
		}
	}
}

func TestNonToolEventIgnoresMatcher(t *testing.T) {
	// A matcher on a non-tool event is meaningless; the hook always applies.
	c, err := Spec{Command: "x", Matcher: "write"}.Build(UserPromptSubmit, "user")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !c.Applies("") {
		t.Fatal("non-tool event should always apply")
	}
}
