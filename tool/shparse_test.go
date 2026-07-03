package tool

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

// TestCompoundBypassFixturesGolden pins the normalization every bypass
// attempt has to get through: compound splitting, wrapper and env
// stripping to a fixed point, and redirection stripping. The permission
// slice reuses these fixtures against its rule engine (doc 04 section
// 7.7, doc 05 section 5).
func TestCompoundBypassFixturesGolden(t *testing.T) {
	fixtures := []string{
		"ls && rm -rf /",
		"FOO=bar rm -rf /",
		"A=1 B=2 git push",
		"nohup git push",
		"timeout 30 git push",
		"timeout -k 5 30 git push",
		"nice -n 10 make",
		"env A=1 git push",
		"env FOO=bar nohup nice -n 5 rm -rf /tmp/x",
		`echo "a && b"`,
		"echo 'x; rm -rf /'",
		"cat f | grep x",
		"echo hi > /etc/passwd",
		"git push 2>&1 | tee log",
		"time go test ./...",
		"a; b; c",
		"x || y",
		"make >> build.log 2>&1",
	}
	var b strings.Builder
	for _, f := range fixtures {
		var quoted []string
		for _, sub := range newShMatcher(f).subcommands {
			quoted = append(quoted, fmt.Sprintf("%q", sub))
		}
		fmt.Fprintf(&b, "%-45s => [%s]\n", f, strings.Join(quoted, ", "))
	}
	eval.Golden(t, "sh_bypass", b.String())
}

// TestPrefixRuleNeverMatchesACompoundAsAUnit is the security rule from
// doc 04 section 7.7: chaining a second command onto an allowed first
// one must not widen the rule.
func TestPrefixRuleNeverMatchesACompoundAsAUnit(t *testing.T) {
	ls := Pattern{Tool: "sh", Content: "ls:*", Source: "sh(ls:*)"}
	if newShMatcher("ls && rm -rf /").Matches(ls) {
		t.Error("ls:* must not cover ls && rm -rf /")
	}
	if !newShMatcher("ls -la").Matches(ls) {
		t.Error("ls:* must cover ls -la")
	}
	if !newShMatcher("ls").Matches(ls) {
		t.Error("ls:* must cover a bare ls")
	}
}

// TestPrefixEndsAtAWordBoundary: sh(git:*) matches git status, never
// github-cli (doc 04 section 7.7).
func TestPrefixEndsAtAWordBoundary(t *testing.T) {
	git := Pattern{Tool: "sh", Content: "git:*", Source: "sh(git:*)"}
	if !newShMatcher("git status").Matches(git) {
		t.Error("git:* must cover git status")
	}
	if newShMatcher("github-cli status").Matches(git) {
		t.Error("git:* must not cover github-cli")
	}
	commit := Pattern{Tool: "sh", Content: "git commit:*", Source: "sh(git commit:*)"}
	if !newShMatcher("git commit -m x").Matches(commit) {
		t.Error("git commit:* must cover git commit -m x")
	}
	if newShMatcher("git commitx").Matches(commit) {
		t.Error("git commit:* must not cover git commitx")
	}
}

// TestEnvAssignmentCannotSmuggleADeniedCommand: after normalization the
// rm is visible to whatever rule governs rm, which is the whole point
// of the fixed-point strip (doc 04 section 7.7).
func TestEnvAssignmentCannotSmuggleADeniedCommand(t *testing.T) {
	m := newShMatcher("FOO=bar rm -rf /")
	if len(m.subcommands) != 1 || m.subcommands[0] != "rm -rf /" {
		t.Fatalf("subcommands = %v, want [rm -rf /]", m.subcommands)
	}
	rm := Pattern{Tool: "sh", Content: "rm:*", Source: "sh(rm:*)"}
	if !m.Matches(rm) {
		t.Error("the normalized rm must be visible to an rm rule")
	}
}

func TestQuotedOperatorsDoNotSplit(t *testing.T) {
	m := newShMatcher(`echo "a && b; c"`)
	if len(m.subcommands) != 1 {
		t.Errorf("subcommands = %v, want one", m.subcommands)
	}
}

func TestEmptyCommandMatchesNothing(t *testing.T) {
	any := Pattern{Tool: "sh", Content: "", Source: "sh"}
	if newShMatcher("").Matches(any) {
		t.Error("an empty command must match nothing, the conservative direction")
	}
}

func TestReadOnlyClassification(t *testing.T) {
	cases := []struct {
		command  string
		readOnly bool
	}{
		{"ls -la", true},
		{"git status", true},
		{"git log --oneline", true},
		{"cat a | grep b", true},
		{"go vet ./...", true},
		{"rm -rf /tmp/x", false},
		{"ls && rm x", false},
		{"git push", false},
		{"go build ./...", false},
		{"unknowncmd --version", false},
		{"", false},
		{"FOO=bar ls", true},
		{"timeout 5 git status", true},
	}
	for _, c := range cases {
		if got := shIsReadOnly(c.command); got != c.readOnly {
			t.Errorf("shIsReadOnly(%q) = %v, want %v", c.command, got, c.readOnly)
		}
	}
}

func TestDestructiveClassification(t *testing.T) {
	cases := []struct {
		command     string
		destructive bool
	}{
		{"rm -rf /tmp/x", true},
		{"ls && rm x", true},
		{"FOO=bar rm x", true},
		{"git push --force", true},
		{"git push -f origin main", true},
		{"git reset --hard HEAD~1", true},
		{"git clean -fd", true},
		{"git push", false},
		{"ls", false},
		{"echo rm", false},
	}
	for _, c := range cases {
		if got := shIsDestructive(c.command); got != c.destructive {
			t.Errorf("shIsDestructive(%q) = %v, want %v", c.command, got, c.destructive)
		}
	}
}
