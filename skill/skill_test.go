package skill

import (
	"os"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

// mapReader turns an in-memory file map into a read function so discovery and
// Body run without touching disk.
func mapReader(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if data, ok := files[path]; ok {
			return []byte(data), nil
		}
		return nil, os.ErrNotExist
	}
}

// TestFrontmatterFieldsParsed: every documented field lands where it belongs.
func TestFrontmatterFieldsParsed(t *testing.T) {
	body := `---
name: deploy
description: Ship the current branch to staging
allowed-tools: [sh(git *), read]
argument-hint: <target>
arguments:
  - target
  - reason?
disable-model-invocation: true
model: opus
context: fresh
---
Run the deploy for $target because $reason.`
	front, md, err := splitFrontmatter(body)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	fm, err := parseFrontmatter(front)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s, err := fromFrontmatter(fm, md, "fallback")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if s.Name != "deploy" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Description != "Ship the current branch to staging" {
		t.Errorf("description = %q", s.Description)
	}
	if strings.Join(s.AllowedTools, "|") != "sh(git *)|read" {
		t.Errorf("allowed-tools = %v", s.AllowedTools)
	}
	if s.ArgumentHint != "<target>" {
		t.Errorf("argument-hint = %q", s.ArgumentHint)
	}
	if len(s.Arguments) != 2 || s.Arguments[0] != (ArgSpec{Name: "target", Required: true}) || s.Arguments[1] != (ArgSpec{Name: "reason", Required: false}) {
		t.Errorf("arguments = %+v", s.Arguments)
	}
	if !s.ModelHidden {
		t.Error("disable-model-invocation did not set ModelHidden")
	}
	if s.Model != "opus" || s.Context != "fresh" {
		t.Errorf("model=%q context=%q", s.Model, s.Context)
	}
}

// TestDescriptionFallbackAndCap: a missing description falls back to the first
// body line, and an over-long one is truncated with a marker.
func TestDescriptionFallbackAndCap(t *testing.T) {
	fm, _ := parseFrontmatter("name: x")
	s, err := fromFrontmatter(fm, "# heading\n\nThe first real line.\n", "x")
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "The first real line." {
		t.Errorf("fallback description = %q", s.Description)
	}

	long := strings.Repeat("a", 400)
	fm2, _ := parseFrontmatter("name: y\ndescription: " + long)
	s2, _ := fromFrontmatter(fm2, "", "y")
	if len([]rune(s2.Description)) > descriptionCap {
		t.Errorf("description not capped: %d runes", len([]rune(s2.Description)))
	}
	if !strings.HasSuffix(s2.Description, "…") {
		t.Error("capped description missing the ellipsis marker")
	}
}

// TestInvalidNameIsWarning: a bad name fails to build, which discovery turns
// into a warning rather than a crash.
func TestInvalidNameIsWarning(t *testing.T) {
	fm, _ := parseFrontmatter("name: Bad_Name")
	if _, err := fromFrontmatter(fm, "", "Bad_Name"); err == nil {
		t.Error("expected an error for an invalid name")
	}
}

// TestUnclosedFrontmatterErrors: an opened but unclosed fence is an error, not
// a silent swallow of the whole file.
func TestUnclosedFrontmatterErrors(t *testing.T) {
	if _, _, err := splitFrontmatter("---\nname: x\nno close here\n"); err == nil {
		t.Error("expected an error for unclosed frontmatter")
	}
}

// TestNoFrontmatterIsAllBody: a plain markdown command template declares
// nothing and is all body.
func TestNoFrontmatterIsAllBody(t *testing.T) {
	front, body, err := splitFrontmatter("Just a template with $ARGUMENTS.\n")
	if err != nil {
		t.Fatal(err)
	}
	if front != "" || !strings.Contains(body, "$ARGUMENTS") {
		t.Errorf("front=%q body=%q", front, body)
	}
}
