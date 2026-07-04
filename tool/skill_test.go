package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/skill"
)

// stubSkill builds a project skill whose body lives in a real temp file, since
// the skill package reads bodies from disk on demand.
func stubSkill(t *testing.T, name, body string, allowed []string) *skill.Skill {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".md")
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &skill.Skill{
		Name:         name,
		Scope:        skill.ScopeProject,
		Path:         path,
		AllowedTools: allowed,
	}
}

// depsFor wires a skill tool over a fixed set, recording any grants.
func depsFor(skills ...*skill.Skill) (SkillDeps, *[]string) {
	byName := map[string]*skill.Skill{}
	var names []string
	for _, s := range skills {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	var granted []string
	return SkillDeps{
		Lookup:  func(n string) (*skill.Skill, bool) { s, ok := byName[n]; return s, ok },
		Names:   func() []string { return names },
		Matcher: func(*skill.Skill) skill.Matcher { return func(string) bool { return true } },
		Inline:  func(_ context.Context, cmd string) (string, error) { return "ran: " + cmd, nil },
		Grant:   func(_ string, rules []string) { granted = append(granted, rules...) },
		Trusted: true,
	}, &granted
}

func callSkill(t *testing.T, tool Tool, args string) (*Result, error) {
	t.Helper()
	raw := json.RawMessage(args)
	if err := tool.ValidateInput(context.Background(), raw, nil); err != nil {
		return nil, err
	}
	return tool.Call(context.Background(), raw, nil, nil)
}

// TestSkillToolLoadsBody: invoking a known skill injects its wrapped body and
// grants its allowed-tools.
func TestSkillToolLoadsBody(t *testing.T) {
	deps, granted := depsFor(stubSkill(t, "deploy", "Ship $ARGUMENTS.", []string{"sh(git *)"}))
	tl := NewSkill(deps)
	res, err := callSkill(t, tl, `{"name":"deploy","args":"staging"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Model, `<skill-invocation name="deploy">`) || !strings.Contains(res.Model, "Ship staging.") {
		t.Errorf("body not injected: %q", res.Model)
	}
	if len(*granted) != 1 || (*granted)[0] != "sh(git *)" {
		t.Errorf("grants = %v", *granted)
	}
	d, ok := res.Display.(SkillDisplay)
	if !ok || d.Name != "deploy" || d.Already {
		t.Errorf("display = %+v", res.Display)
	}
}

// TestSkillToolReinvocationGuard: a second invocation injects nothing and tells
// the model the instructions are already loaded.
func TestSkillToolReinvocationGuard(t *testing.T) {
	deps, granted := depsFor(stubSkill(t, "deploy", "Ship it.", []string{"sh(git *)"}))
	tl := NewSkill(deps)
	if _, err := callSkill(t, tl, `{"name":"deploy"}`); err != nil {
		t.Fatal(err)
	}
	res, err := callSkill(t, tl, `{"name":"deploy"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Model, "<skill-invocation") {
		t.Errorf("re-invocation injected a body: %q", res.Model)
	}
	if !strings.Contains(res.Model, "already loaded") {
		t.Errorf("no already-loaded notice: %q", res.Model)
	}
	if d, ok := res.Display.(SkillDisplay); !ok || !d.Already {
		t.Errorf("display did not mark re-invocation: %+v", res.Display)
	}
	// The guard also means the grant fires exactly once.
	if len(*granted) != 1 {
		t.Errorf("grant fired %d times, want 1", len(*granted))
	}
}

// TestSkillToolUnknownName: an unknown name fails validation with a near-match
// suggestion rather than injecting anything.
func TestSkillToolUnknownName(t *testing.T) {
	deps, _ := depsFor(stubSkill(t, "deploy", "x", nil))
	tl := NewSkill(deps)
	err := tl.ValidateInput(context.Background(), json.RawMessage(`{"name":"dep"}`), nil)
	if err == nil {
		t.Fatal("expected an error for an unknown skill")
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("no suggestion: %v", err)
	}
}

// TestSkillToolMissingName: a blank name is a validation error.
func TestSkillToolMissingName(t *testing.T) {
	deps, _ := depsFor(stubSkill(t, "deploy", "x", nil))
	tl := NewSkill(deps)
	if err := tl.ValidateInput(context.Background(), json.RawMessage(`{"name":"  "}`), nil); err == nil {
		t.Error("expected an error for a blank name")
	}
}

// TestSkillToolPreApproved: loading a body is not a gated action, so the tool
// pre-approves itself and leaves the dangerous inline path to the pipeline.
func TestSkillToolPreApproved(t *testing.T) {
	deps, _ := depsFor(stubSkill(t, "deploy", "x", nil))
	tl := NewSkill(deps)
	if !tl.CheckPermissions(context.Background(), nil, nil).IsAllow() {
		t.Error("CheckPermissions should pre-approve the skill tool")
	}
}
