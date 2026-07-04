package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// hookFn adapts a fixed verdict to the pipeline's HookFunc seam.
func hookFn(v HookVerdict) HookFunc {
	return func(context.Context, Call) (HookVerdict, bool) { return v, true }
}

// TestHookAllowStillClearsTheSafetyFloor is the DoD invariant: a PreToolUse
// hook that allows a call cannot approve a write the safety floor forbids. A
// hook allow lands at stage 7 for a clean path but is denied by the floor at
// stage 5 for a protected one, exactly like a tool that pre-approves itself.
func TestHookAllowStillClearsTheSafetyFloor(t *testing.T) {
	ctx := context.Background()

	clean := newPipeline(t, ModeAsk, nil, nil, nil)
	clean.Hook = hookFn(HookVerdict{Behavior: Allow})
	if d := clean.Decide(ctx, writeCall(clean, filepath.Join(clean.Paths.Root, "a.txt"), "x")); d.Behavior != Allow {
		t.Fatalf("clean path: got %s, want the hook allow to stand", d.Behavior)
	}

	protected := newPipeline(t, ModeAsk, nil, nil, nil)
	protected.Hook = hookFn(HookVerdict{Behavior: Allow})
	d := protected.Decide(ctx, writeCall(protected, filepath.Join(protected.Paths.Root, ".git", "config"), "x"))
	if d.Behavior != Deny || d.Reason.Kind != KindSafety {
		t.Fatalf("protected path: got %s/%s, want the safety deny", d.Behavior, d.Reason.Kind)
	}
}

// TestHookCannotWidenACall pins the never-widen invariant against a hook: an
// updatedInput that adds a flag or a subcommand is refused with a deny, fail
// closed, the same rule a resolver faces.
func TestHookCannotWidenACall(t *testing.T) {
	ctx := context.Background()

	t.Run("a widening flag is refused", func(t *testing.T) {
		p := newPipeline(t, ModeFullAuto, nil, nil, nil)
		wider, _ := json.Marshal(map[string]any{"command": "git push --force"})
		p.Hook = hookFn(HookVerdict{Behavior: Allow, UpdatedInput: wider})
		d := p.Decide(ctx, shCall(p, "git push"))
		if d.Behavior != Deny || d.Reason.Kind != KindHook {
			t.Fatalf("got %s/%s, want the hook deny for widening", d.Behavior, d.Reason.Kind)
		}
	})

	t.Run("a smuggled second subcommand is refused", func(t *testing.T) {
		p := newPipeline(t, ModeFullAuto, nil, nil, nil)
		wider, _ := json.Marshal(map[string]any{"command": "git push && rm -rf /"})
		p.Hook = hookFn(HookVerdict{Behavior: Allow, UpdatedInput: wider})
		d := p.Decide(ctx, shCall(p, "git push"))
		if d.Behavior != Deny {
			t.Fatalf("got %s, want deny for a smuggled subcommand", d.Behavior)
		}
	})

	t.Run("a narrowing hook allow is honored", func(t *testing.T) {
		p := newPipeline(t, ModeAsk, nil, nil, nil)
		narrower, _ := json.Marshal(map[string]any{"command": "git push"})
		p.Hook = hookFn(HookVerdict{Behavior: Allow, UpdatedInput: narrower})
		d := p.Decide(ctx, shCall(p, "git push --force"))
		if d.Behavior != Allow {
			t.Fatalf("got %s, want the narrowed allow", d.Behavior)
		}
		if shCommand(d.UpdatedInput) != "git push" {
			t.Fatalf("updated input = %q, want the narrowed command", shCommand(d.UpdatedInput))
		}
	})
}

// TestHookDenyIsFinal proves a hook deny beats an allow rule and a permissive
// mode.
func TestHookDenyIsFinal(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t, ModeFullAuto, nil, nil, []string{"sh(ls:*)"})
	p.Hook = hookFn(HookVerdict{Behavior: Deny, Message: "policy forbids ls here"})
	d := p.Decide(ctx, shCall(p, "ls"))
	if d.Behavior != Deny || d.Reason.Kind != KindHook {
		t.Fatalf("got %s/%s, want the hook deny", d.Behavior, d.Reason.Kind)
	}
	if d.Message != "policy forbids ls here" {
		t.Fatalf("message = %q, want the hook's reason", d.Message)
	}
}

// TestHookAskForcesAPrompt proves a hook ask forces approval even under
// full-auto, and that a hook ask still cannot ask its way past a safety deny.
func TestHookAskForcesAPrompt(t *testing.T) {
	ctx := context.Background()

	t.Run("a hook ask beats full-auto", func(t *testing.T) {
		p := newPipeline(t, ModeFullAuto, nil, nil, nil)
		p.Hook = hookFn(HookVerdict{Behavior: Ask, Message: "confirm this one"})
		d := p.Decide(ctx, shCall(p, "rm -rf /tmp/scratch"))
		if d.Behavior != Ask || d.Reason.Kind != KindHook {
			t.Fatalf("got %s/%s, want the hook ask over the mode allow", d.Behavior, d.Reason.Kind)
		}
	})

	t.Run("the floor still beats a hook ask", func(t *testing.T) {
		p := newPipeline(t, ModeFullAuto, nil, nil, nil)
		p.Hook = hookFn(HookVerdict{Behavior: Ask})
		d := p.Decide(ctx, writeCall(p, filepath.Join(p.Paths.Root, ".git", "config"), "x"))
		if d.Behavior != Deny || d.Reason.Kind != KindSafety {
			t.Fatalf("got %s/%s, want the safety deny ahead of the hook ask", d.Behavior, d.Reason.Kind)
		}
	})
}

// TestHookContextRidesTheDecision proves a hook that only contributes context
// leaves the decision otherwise untouched and surfaces the context.
func TestHookContextRidesTheDecision(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t, ModeAsk, nil, nil, []string{"sh(go test:*)"})
	p.Hook = hookFn(HookVerdict{Context: "the last run took nine minutes"})
	d := p.Decide(ctx, shCall(p, "go test ./..."))
	if d.Behavior != Allow || d.Reason.Stage != StageAllow {
		t.Fatalf("got %s/%s, want the rule allow unchanged", d.Behavior, d.Reason.Stage)
	}
	if d.HookContext != "the last run took nine minutes" {
		t.Fatalf("hook context = %q, want it surfaced", d.HookContext)
	}
}

// TestHookAbstainsIsANoop proves a hook that returns ok=false leaves the
// decision exactly as if there were no hook.
func TestHookAbstainsIsANoop(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t, ModeAsk, nil, nil, []string{"sh(go test:*)"})
	p.Hook = func(context.Context, Call) (HookVerdict, bool) { return HookVerdict{}, false }
	d := p.Decide(ctx, shCall(p, "go test ./..."))
	if d.Behavior != Allow || d.HookContext != "" {
		t.Fatalf("got %s (ctx %q), want the plain rule allow", d.Behavior, d.HookContext)
	}
}
