package skill

import (
	"context"
	"strings"
	"testing"
)

// mkSkill builds a project skill with an in-memory body for invocation tests.
func mkSkill(name, body string, allowed []string) *Skill {
	return &Skill{
		Name:         name,
		Scope:        ScopeProject,
		Path:         "/repo/.ari/skills/" + name + "/SKILL.md",
		AllowedTools: allowed,
		read:         func(string) ([]byte, error) { return []byte("---\nname: " + name + "\n---\n" + body), nil },
	}
}

// TestInvokeWrapsBody: a plain invocation wraps the substituted body in the
// invocation marker and rides the allowed-tools back as grants.
func TestInvokeWrapsBody(t *testing.T) {
	s := mkSkill("deploy", "Ship $ARGUMENTS now.", []string{"sh(git *)"})
	inv, err := s.Invoke(context.Background(), Request{Args: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inv.Message, `<skill-invocation name="deploy">`) {
		t.Errorf("missing marker: %q", inv.Message)
	}
	if !strings.Contains(inv.Message, "Ship staging now.") {
		t.Errorf("body not substituted: %q", inv.Message)
	}
	if !strings.HasSuffix(strings.TrimRight(inv.Message, "\n"), "</skill-invocation>") {
		t.Errorf("marker not closed: %q", inv.Message)
	}
	if len(inv.Grants) != 1 || inv.Grants[0] != "sh(git *)" {
		t.Errorf("grants = %v", inv.Grants)
	}
}

// TestInvokeInlineShellRuns: with every gate open, an inline command runs and
// its output is spliced fenced into the body.
func TestInvokeInlineShellRuns(t *testing.T) {
	s := mkSkill("status", "Branch is !`git branch --show-current` today.", []string{"sh(git *)"})
	req := Request{
		Trusted: true,
		Match:   func(string) bool { return true },
		Inline:  func(_ context.Context, cmd string) (string, error) { return "main", nil },
	}
	inv, err := s.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "$ git branch --show-current") || !strings.Contains(inv.Message, "main") {
		t.Errorf("inline output not spliced: %q", inv.Message)
	}
	if strings.Contains(inv.Message, "!`git") {
		t.Errorf("raw token left behind: %q", inv.Message)
	}
}

// TestInlineGateUntrusted: an untrusted session never runs inline shell; the
// token is left literal (gate three).
func TestInlineGateUntrusted(t *testing.T) {
	s := mkSkill("status", "Branch !`git branch`.", []string{"sh(git *)"})
	req := Request{
		Trusted: false,
		Match:   func(string) bool { return true },
		Inline: func(context.Context, string) (string, error) {
			t.Fatal("inline ran in an untrusted session")
			return "", nil
		},
	}
	inv, err := s.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "!`git branch`") {
		t.Errorf("token not left literal: %q", inv.Message)
	}
}

// TestInlineGateBuiltinScope: a builtin skill never runs inline shell even in
// a trusted session, matching the MCP-sourced rule (gate four).
func TestInlineGateBuiltinScope(t *testing.T) {
	s := mkSkill("status", "Branch !`git branch`.", []string{"sh(git *)"})
	s.Scope = ScopeBuiltin
	req := Request{
		Trusted: true,
		Match:   func(string) bool { return true },
		Inline: func(context.Context, string) (string, error) {
			t.Fatal("inline ran for a builtin skill")
			return "", nil
		},
	}
	inv, err := s.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "!`git branch`") {
		t.Errorf("token not left literal: %q", inv.Message)
	}
}

// TestInlineGateNotAllowed: a command outside allowed-tools stays literal and
// records a note (gate one).
func TestInlineGateNotAllowed(t *testing.T) {
	s := mkSkill("status", "Danger !`rm -rf /`.", []string{"sh(git *)"})
	req := Request{
		Trusted: true,
		Match:   func(cmd string) bool { return strings.HasPrefix(cmd, "git ") },
		Inline: func(context.Context, string) (string, error) {
			t.Fatal("inline ran for an unmatched command")
			return "", nil
		},
	}
	inv, err := s.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "!`rm -rf /`") {
		t.Errorf("unmatched token not left literal: %q", inv.Message)
	}
	if len(inv.Notes) == 0 || !strings.Contains(inv.Notes[0], "allowed-tools") {
		t.Errorf("no gate note: %v", inv.Notes)
	}
}

// TestInlineGateDeniedByPipeline: the runner denied the command, so the token
// stays literal and a note explains why (gate two, the pipeline).
func TestInlineGateDeniedByPipeline(t *testing.T) {
	s := mkSkill("status", "Try !`git push`.", []string{"sh(git *)"})
	req := Request{
		Trusted: true,
		Match:   func(string) bool { return true },
		Inline:  func(context.Context, string) (string, error) { return "", context.Canceled },
	}
	inv, err := s.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "!`git push`") {
		t.Errorf("denied token not left literal: %q", inv.Message)
	}
	if len(inv.Notes) == 0 || !strings.Contains(inv.Notes[0], "did not run") {
		t.Errorf("no denial note: %v", inv.Notes)
	}
}

// TestInlineNoAllowedToolsNoRun: a skill with no allowed-tools never runs
// inline shell, since gate one has nothing to match against.
func TestInlineNoAllowedToolsNoRun(t *testing.T) {
	s := mkSkill("note", "See !`git log`.", nil)
	req := Request{
		Trusted: true,
		Match:   func(string) bool { return true },
		Inline: func(context.Context, string) (string, error) {
			t.Fatal("inline ran without allowed-tools")
			return "", nil
		},
	}
	inv, err := s.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "!`git log`") {
		t.Errorf("token not left literal: %q", inv.Message)
	}
}

// TestInvokeContextForkNote: context:fork loads inline in M1 and records the
// deferral note rather than pretending sub-agents exist.
func TestInvokeContextForkNote(t *testing.T) {
	s := mkSkill("big", "Do a lot.", nil)
	s.Context = "fork"
	inv, err := s.Invoke(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Message, "Do a lot.") {
		t.Errorf("body missing: %q", inv.Message)
	}
	found := false
	for _, n := range inv.Notes {
		if strings.Contains(n, "context:fork") {
			found = true
		}
	}
	if !found {
		t.Errorf("no context:fork deferral note: %v", inv.Notes)
	}
}

// TestNamedArgsMapsDeclared: declared arguments map onto the positional
// argument fields for $name substitution.
func TestNamedArgsMapsDeclared(t *testing.T) {
	s := &Skill{Arguments: []ArgSpec{{Name: "target"}, {Name: "reason"}}}
	named := NamedArgs(s, "staging smoke")
	if named["target"] != "staging" || named["reason"] != "smoke" {
		t.Errorf("named = %v", named)
	}
	if NamedArgs(&Skill{}, "x") != nil {
		t.Error("a skill with no declared arguments should return nil")
	}
}

// TestNearMatches: a typo resolves to the closest discovered names.
func TestNearMatches(t *testing.T) {
	names := []string{"deploy", "deprecate", "test"}
	near := NearMatches(names, "dep")
	if len(near) != 2 {
		t.Errorf("near = %v", near)
	}
	if got := NearMatches(names, "zzz"); got != nil {
		t.Errorf("expected no matches, got %v", got)
	}
}
