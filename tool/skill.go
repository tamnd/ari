package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/tamnd/ari/skill"
)

// skillMaxResult caps the invoked body: a skill body is instructions plus a
// little spliced inline output, not a data dump.
const skillMaxResult = 32000

type skillArgs struct {
	Name string `json:"name"`           // the skill to invoke
	Args string `json:"args,omitempty"` // an optional argument string
}

// SkillDisplay is the typed data the UI renders for an invocation: the name,
// whether it was a re-invocation, and any deferral or gate notes. Never sent
// to the model.
type SkillDisplay struct {
	Name    string
	Already bool
	Notes   []string
}

// SkillDeps wires the skill tool to the discovered set and to the host's
// permission-gated seams. The runner builds it per session, because the
// matcher and the inline runner both close over that session's permission
// pipeline (doc 13 section 2.5).
type SkillDeps struct {
	// Lookup resolves a discovered skill by name; the bool is false for an
	// unknown name.
	Lookup func(name string) (*skill.Skill, bool)
	// Names lists the discovered names, for near-match suggestions on a miss.
	Names func() []string
	// Matcher builds the allowed-tools gate for one skill, or nil when the
	// skill declares no allowed-tools. It carries the doc 05 normalization.
	Matcher func(s *skill.Skill) skill.Matcher
	// Inline runs one gated command through the session's sh path. Nil
	// disables inline execution entirely.
	Inline skill.InlineRunner
	// Grant adds a skill's allowed-tools to the session as always-allow rules,
	// tagged with the skill name for the doc 05 audit trail.
	Grant func(name string, rules []string)
	// Trusted is false for an untrusted-content session (D19): no skill runs
	// inline shell regardless of its scope.
	Trusted bool
}

// skillTool loads a discovered skill's body and injects it as a synthetic
// user message wrapped in an invocation marker. It is the seventh tool, core
// in that it ships in the binary but registered only when at least one
// model-visible skill was discovered (doc 13 section 2.5).
type skillTool struct {
	Base
	deps    SkillDeps
	mu      sync.Mutex
	invoked map[string]bool // names already loaded this session
}

// NewSkill builds the skill tool over the discovered set and the session's
// gated seams.
func NewSkill(deps SkillDeps) Tool {
	return &skillTool{deps: deps, invoked: map[string]bool{}}
}

func (*skillTool) Name() string { return "skill" }

func (*skillTool) Schema() Schema {
	return Schema{
		Name: "skill",
		Description: "Load a skill's instructions into the conversation. A matching skill is a blocking " +
			"requirement to consult before you answer. If a <skill-invocation> tag for the name is already " +
			"present, the instructions are already loaded: follow them instead of invoking again.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "The skill to load."},
				"args": {"type": "string", "description": "Optional arguments for the skill."}
			},
			"required": ["name"]
		}`),
	}
}

func (*skillTool) MaxResultSize() int { return skillMaxResult }

// CheckPermissions pre-approves the skill tool: loading an instruction body
// is not a gated action. The dangerous part, a skill's own inline shell, is
// gated separately inside Invoke and still faces the full pipeline (doc 13
// section 2.7), so the tool clears the safety floor with nothing to mutate.
func (*skillTool) CheckPermissions(context.Context, json.RawMessage, *ToolContext) PermissionResult {
	return AllowResult()
}

func (t *skillTool) ValidateInput(_ context.Context, raw json.RawMessage, _ *ToolContext) error {
	var a skillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if _, ok := t.deps.Lookup(a.Name); !ok {
		return fmt.Errorf("no skill named %q%s", a.Name, suggestion(t.deps.Names(), a.Name))
	}
	return nil
}

func (t *skillTool) Call(ctx context.Context, raw json.RawMessage, _ *ToolContext, _ ProgressFunc) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var a skillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	s, ok := t.deps.Lookup(a.Name)
	if !ok {
		return nil, fmt.Errorf("no skill named %q%s", a.Name, suggestion(t.deps.Names(), a.Name))
	}

	// The re-invocation guard, enforced mechanically as well as by the tool
	// description: a second invocation of a loaded name injects nothing and
	// tells the model the instructions are already present (doc 13 section
	// 2.5).
	t.mu.Lock()
	already := t.invoked[s.Name]
	t.mu.Unlock()
	if already {
		return &Result{
			Model:   fmt.Sprintf("skill %s is already loaded; follow the loaded instructions instead of invoking it again", s.Name),
			Display: SkillDisplay{Name: s.Name, Already: true},
		}, nil
	}

	req := skill.Request{
		Args:    a.Args,
		Named:   skill.NamedArgs(s, a.Args),
		Inline:  t.deps.Inline,
		Trusted: t.deps.Trusted,
	}
	if t.deps.Matcher != nil {
		req.Match = t.deps.Matcher(s)
	}
	inv, err := s.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}

	// allowed-tools become session allow rules so the skill's own commands
	// pass the pipeline without a prompt for each one; they narrow, never
	// widen (doc 13 section 2.5).
	if len(inv.Grants) > 0 && t.deps.Grant != nil {
		t.deps.Grant(s.Name, inv.Grants)
	}
	t.mu.Lock()
	t.invoked[s.Name] = true
	t.mu.Unlock()

	return &Result{
		Model:   inv.Message,
		Display: SkillDisplay{Name: s.Name, Notes: inv.Notes},
	}, nil
}

// suggestion renders the near-match tail of an unknown-name error.
func suggestion(names []string, target string) string {
	near := skill.NearMatches(names, target)
	if len(near) == 0 {
		return ""
	}
	return "; did you mean " + strings.Join(near, ", ") + "?"
}
