package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/tool"
)

// Pipeline evaluates every tool call through the eight ordered stages.
// One value serves a whole session; it is safe for concurrent use when
// its fields are not mutated after construction. The one sanctioned
// mutation is AddAllow, which "allow for session" rides and which takes
// the rules lock the evaluator reads under.
type Pipeline struct {
	Rules Rules
	Mode  Mode
	Paths Paths

	// Resolver answers an Ask. Nil means the Ask stands as the decision
	// and the caller owns resolution; the interactive dialog and the
	// headless auto-deny are both resolvers wired by later slices.
	Resolver Resolver

	// Journal, when set, receives the permission.requested and
	// permission.resolved pair every decision emits (doc 05 section 15).
	Journal tool.Journal

	// Hook, when set, is the seam to the PreToolUse hook runner. It runs
	// once per call before the eight stages and steers the decision: it
	// can deny, narrow the input and allow, or force an ask. The ant wires
	// it; the permission package never imports hook (doc 05 section 3).
	Hook HookFunc

	reqID   atomic.Uint64
	rulesMu sync.RWMutex // guards Rules; parallel reads decide concurrently
}

// HookVerdict is a PreToolUse hook's steer on a call. Behavior is Allow,
// Deny, or Ask; the empty string means the hook ran but did not steer the
// permission decision, only contributed context.
type HookVerdict struct {
	Behavior     Behavior
	UpdatedInput json.RawMessage // narrows the call; a widening input is refused
	Message      string
	Context      string // additionalContext to surface to the model
}

// HookFunc is the pipeline's seam to the PreToolUse hook runner. ok=false
// means the hook abstained: no hook matched, or the workspace is not
// trusted so no hook ran at all. The ant wires this to hook.Runner.
type HookFunc func(ctx context.Context, call Call) (HookVerdict, bool)

// hookState carries a PreToolUse hook's steer into the eight stages. A
// hook allow becomes a pre-approval the safety floor still vets, exactly
// like a tool that approves itself at stage 3; a hook ask is honored
// after the floor so a hook can never ask its way past a safety deny.
type hookState struct {
	preApproved  bool
	forceAsk     bool
	askMsg       string
	context      string
	updatedInput json.RawMessage
}

// AddAllow appends session-layer allow rules, the durable half of an
// "allow for session" answer. Clip forces the append to reallocate so a
// concurrently taken snapshot never sees its backing array written.
func (p *Pipeline) AddAllow(rules ...Rule) {
	p.rulesMu.Lock()
	defer p.rulesMu.Unlock()
	p.Rules.Allow = append(slices.Clip(p.Rules.Allow), rules...)
}

// ruleset snapshots the rules for one evaluation.
func (p *Pipeline) ruleset() Rules {
	p.rulesMu.RLock()
	defer p.rulesMu.RUnlock()
	return p.Rules
}

// Call is one tool invocation under decision.
type Call struct {
	Tool    tool.Tool
	Input   json.RawMessage
	TC      *tool.ToolContext // handed to the tool's CheckPermissions
	CallID  string            // the model's tool-call id, for the event stream
	Session string
	Turn    string
	Cwd     string // where sh-relative paths resolve, for the safety floor
}

// Request is what a resolver sees when the pipeline asks for an answer.
type Request struct {
	ID          string
	Call        Call
	Reason      Reason
	Message     string
	Consequence event.Consequence
	Suggestions []string
}

// Resolution is a resolver's answer to an Ask. Behavior must be Allow
// or Deny; UpdatedInput may narrow the call and never widens it.
type Resolution struct {
	Behavior     Behavior
	UpdatedInput json.RawMessage
	Message      string

	// Kind, when set on a deny, names which machinery answered instead
	// of a person; the headless auto-deny sets KindHeadless so clients
	// and exit codes can tell it apart from a user's no.
	Kind ReasonKind
}

// Resolver answers an Ask. Returning ok=false abstains, leaving the
// Ask standing as the decision.
type Resolver interface {
	Resolve(ctx context.Context, req *Request) (Resolution, bool)
}

// ResolverFunc adapts a function to the Resolver interface.
type ResolverFunc func(ctx context.Context, req *Request) (Resolution, bool)

func (f ResolverFunc) Resolve(ctx context.Context, req *Request) (Resolution, bool) {
	return f(ctx, req)
}

// unit is what the eight stages actually evaluate: the whole call for
// most tools, one subcommand of it for compound sh. shSub is nil for
// non-sh units.
type unit struct {
	call  Call
	name  string
	shSub *tool.ShSub
}

// Decide runs the pipeline for one tool call and returns the Decision.
// A compound sh call is decided per subcommand and combined most
// restrictive wins; everything else is one unit (doc 05 section 5.1).
// Every decision emits a permission.requested and permission.resolved
// pair so the journal can reconstruct why any call ran.
func (p *Pipeline) Decide(ctx context.Context, call Call) Decision {
	d := p.decide(ctx, call)
	req := p.buildRequest(call, d)
	p.emitRequested(call, req)

	if d.Behavior == Ask && p.Resolver != nil {
		if res, ok := p.Resolver.Resolve(ctx, req); ok {
			d = p.applyResolution(call, d, res)
		}
	}
	p.emitResolved(call, req.ID, d)
	return d
}

// decide picks the evaluation shape: per-subcommand for compound sh,
// one unit otherwise. The PreToolUse hook runs first, once for the whole
// call, and its steer rides the stages as hookState.
func (p *Pipeline) decide(ctx context.Context, call Call) Decision {
	call, hs, denied, denyDec := p.applyHook(ctx, call)
	if denied {
		return denyDec
	}

	d := p.decideStages(ctx, call, hs)
	if hs.context != "" {
		d.HookContext = hs.context
	}
	// A hook that narrowed the input rides that narrowing onto the
	// decision so the tool runs the narrowed call, not the original. A
	// later resolver narrowing on an Ask wins over this.
	if len(hs.updatedInput) > 0 && d.Behavior == Allow && len(d.UpdatedInput) == 0 {
		d.UpdatedInput = hs.updatedInput
	}
	return d
}

// decideStages runs the eight stages over the right evaluation shape.
func (p *Pipeline) decideStages(ctx context.Context, call Call, hs hookState) Decision {
	name := call.Tool.Name()
	if name != "sh" {
		return p.evaluate(ctx, unit{call: call, name: name}, hs)
	}
	subs := tool.ShSplit(shCommand(call.Input))
	switch len(subs) {
	case 0:
		// Nothing parseable to match; the safe default is to ask.
		return Decision{
			Behavior: Ask,
			Reason:   Reason{Kind: KindDefault, Stage: StageDefault},
			Message:  "the command could not be parsed, so it needs approval",
		}
	case 1:
		return p.evaluate(ctx, unit{call: call, name: name, shSub: &subs[0]}, hs)
	}
	return p.evalShell(ctx, call, subs, hs)
}

// applyHook runs the PreToolUse hook and folds its verdict into a
// hookState. A deny is final and returns a Decision directly. A narrowing
// updatedInput is applied to the call; a widening one is refused with a
// deny, fail closed, the same invariant a resolver faces (doc 05 section
// 3, D15). Allow and ask ride the stages so the safety floor still vets
// them: a hook can never approve a write the floor forbids.
func (p *Pipeline) applyHook(ctx context.Context, call Call) (Call, hookState, bool, Decision) {
	if p.Hook == nil {
		return call, hookState{}, false, Decision{}
	}
	v, ok := p.Hook(ctx, call)
	if !ok {
		return call, hookState{}, false, Decision{}
	}
	if v.Behavior == Deny {
		msg := v.Message
		if msg == "" {
			msg = "a hook denied this call"
		}
		return call, hookState{}, true, Decision{
			Behavior: Deny,
			Reason:   Reason{Kind: KindHook, Stage: StageHook},
			Message:  msg,
		}
	}
	if len(v.UpdatedInput) > 0 {
		if !narrows(call.Tool.Name(), call.Input, v.UpdatedInput) {
			return call, hookState{}, true, Decision{
				Behavior: Deny,
				Reason:   Reason{Kind: KindHook, Stage: StageHook, Details: "a hook returned an updated input that widens the call; a hook may only narrow"},
				Message:  "the hook widened the call, so it was refused",
			}
		}
		call.Input = v.UpdatedInput
	}
	hs := hookState{context: v.Context, updatedInput: v.UpdatedInput}
	switch v.Behavior {
	case Allow:
		hs.preApproved = true
	case Ask:
		hs.forceAsk = true
		hs.askMsg = v.Message
	}
	return call, hs, false, Decision{}
}

// evalShell decides a compound sh call by deciding each subcommand and
// combining: any deny denies the whole call, else any ask asks, else
// allow. Sub records every subcommand's own reason so the prompt can
// highlight the piece that triggered the stop.
func (p *Pipeline) evalShell(ctx context.Context, call Call, subs []tool.ShSub, hs hookState) Decision {
	reasons := make([]SubReason, 0, len(subs))
	worst := Decision{Behavior: Allow}
	worstSub := ""
	for i := range subs {
		d := p.evaluate(ctx, unit{call: call, name: "sh", shSub: &subs[i]}, hs)
		reasons = append(reasons, SubReason{
			Command:  subs[i].Norm,
			Behavior: d.Behavior,
			Reason:   d.Reason,
		})
		if moreRestrictive(d.Behavior, worst.Behavior) {
			worst = d
			worstSub = subs[i].Norm
		}
	}
	worst.Reason = Reason{
		Kind:    KindSubcmd,
		Stage:   worst.Reason.Stage,
		Rule:    worst.Reason.Rule,
		Mode:    worst.Reason.Mode,
		Sub:     reasons,
		Details: worstSub,
	}
	if worst.Behavior != Allow && worstSub != "" {
		worst.Message = fmt.Sprintf("the subcommand %q is why: %s", worstSub, worst.Message)
	}
	return worst
}

// evaluate is the whole pipeline for one unit: eight stages, one early
// return per stage, no recursion, so the order on the page is the
// order at runtime (D15, doc 05 section 3).
func (p *Pipeline) evaluate(ctx context.Context, u unit, hs hookState) Decision {
	// A hook allow enters as a pre-approval, exactly like a tool that
	// approves itself at stage 3: it clears at stage 7 but the safety
	// floor at stage 5 still vets it.
	toolPreApproved := hs.preApproved
	rules := p.ruleset()

	// Stage 1: deny rules. A matching deny is final and nothing below
	// can lift it.
	if r, ok := p.matchRules(rules.Deny, u, anyContent); ok {
		return Decision{
			Behavior: Deny,
			Reason:   Reason{Kind: KindRule, Stage: StageDeny, Rule: r.Pattern.Source},
			Message:  fmt.Sprintf("denied by the rule %s", r.Pattern.Source),
		}
	}

	// Stage 2: tool-wide ask. "ask before any sh at all" lives here,
	// above any narrower allow rule.
	if r, ok := p.matchRules(rules.Ask, u, toolWideOnly); ok {
		return askDecision(
			Reason{Kind: KindRule, Stage: StageToolAsk, Rule: r.Pattern.Source},
			fmt.Sprintf("the rule %s asks before every %s call", r.Pattern.Source, u.name),
		)
	}

	// Stage 3: the tool's own CheckPermissions. An allow here does not
	// short-circuit; it sets a pre-approval the safety floor still vets.
	switch res := u.call.Tool.CheckPermissions(ctx, u.call.Input, u.call.TC); {
	case res.IsDeny():
		return Decision{
			Behavior: Deny,
			Reason:   Reason{Kind: KindTool, Stage: StageToolCheck},
			Message:  res.Message(),
		}
	case res.IsAsk():
		return askDecision(Reason{Kind: KindTool, Stage: StageToolCheck}, res.Message())
	case res.IsAllow():
		toolPreApproved = true
	}

	// Stage 4: content-ask rules, e.g. sh(npm publish:*) or
	// write(**/*.pem), honored even in a permissive mode.
	if r, ok := p.matchRules(rules.Ask, u, contentOnly); ok {
		return askDecision(
			Reason{Kind: KindContent, Stage: StageContentAsk, Rule: r.Pattern.Source},
			fmt.Sprintf("the rule %s asks about this call", r.Pattern.Source),
		)
	}

	// Stage 5: safety checks, immune to every bypass: modes, allow
	// rules, and the pre-approval above all sit below this floor.
	if v := p.Paths.checkTargets(p.mutationTargets(u)); v.Blocked {
		return Decision{
			Behavior: Deny,
			Reason:   Reason{Kind: KindSafety, Stage: StageSafety, Rule: v.Rule, Details: v.Path},
			Message:  v.Message,
		}
	}

	// A PreToolUse hook that asked is honored here, below the floor so a
	// hook can never ask its way past a safety deny, and above the modes
	// so even full-auto stops to ask when a hook says to.
	if hs.forceAsk {
		msg := hs.askMsg
		if msg == "" {
			msg = "a hook asked for approval before this call"
		}
		return askDecision(Reason{Kind: KindHook, Stage: StageHook}, msg)
	}

	// Stage 6: mode transforms, last among the deciders, so no early
	// allow skips them and they cannot skip the floor.
	if d, decided := p.transformMode(u); decided {
		return d
	}

	// Stage 7: allow rules, including a tool that pre-approved itself
	// at stage 3 and cleared the floor.
	if toolPreApproved {
		return Decision{Behavior: Allow, Reason: Reason{Kind: KindTool, Stage: StageToolCheck}}
	}
	if r, ok := p.matchRules(rules.Allow, u, anyContent); ok {
		return Decision{Behavior: Allow, Reason: Reason{Kind: KindRule, Stage: StageAllow, Rule: r.Pattern.Source}}
	}

	// Stage 8: nothing decided. The safe default is to ask a human.
	return askDecision(Reason{Kind: KindDefault, Stage: StageDefault}, "no rule covers this call")
}

func askDecision(reason Reason, message string) Decision {
	return Decision{Behavior: Ask, Reason: reason, Message: message}
}

// ruleFilter narrows which rules a stage considers: stage 2 takes only
// tool-wide asks, stage 4 only content asks, stages 1 and 7 take both.
type ruleFilter int

const (
	anyContent ruleFilter = iota
	toolWideOnly
	contentOnly
)

// matchRules finds the first rule in the list that covers this unit.
func (p *Pipeline) matchRules(rules []Rule, u unit, filter ruleFilter) (Rule, bool) {
	for _, r := range rules {
		if !r.appliesTo(u.name) {
			continue
		}
		if filter == toolWideOnly && !r.toolWide() {
			continue
		}
		if filter == contentOnly && r.toolWide() {
			continue
		}
		if u.shSub != nil {
			// One subcommand, already normalized; a prefix or wildcard
			// rule never sees the compound as a unit (doc 05 section 5).
			if r.Pattern.CoversSubcommand(u.shSub.Norm) {
				return r, true
			}
			continue
		}
		if r.toolWide() || u.call.Tool.MatchPrefix(u.call.Input).Matches(r.Pattern) {
			return r, true
		}
	}
	return Rule{}, false
}

// mutationTargets is the conservative account of what this unit would
// write, for the safety floor: the resolved file path for write and
// edit, the per-subcommand candidates for sh, nothing for tools the
// pipeline cannot see into.
func (p *Pipeline) mutationTargets(u unit) []string {
	switch u.name {
	case "write", "edit":
		var a struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(u.call.Input, &a) != nil || a.FilePath == "" {
			return nil
		}
		return []string{tool.ResolveMutationPath(a.FilePath)}
	case "sh":
		if u.shSub == nil {
			return nil
		}
		return u.shSub.MutationTargets(u.call.Cwd)
	}
	return nil
}

// transformMode is stage 6 (doc 05 section 6). It returns decided=false
// to let stages 7 and 8 run.
func (p *Pipeline) transformMode(u unit) (Decision, bool) {
	switch p.Mode {
	case ModePlan:
		// Plan is read-only: any unit that is not read-only is denied,
		// and because this is stage 6 no allow rule can re-enable it.
		if !p.unitReadOnly(u) {
			return Decision{
				Behavior: Deny,
				Reason:   Reason{Kind: KindMode, Stage: StageMode, Mode: ModePlan},
				Message:  "plan mode is read-only, so this " + u.name + " call is not run",
			}, true
		}
		return Decision{}, false

	case ModeAutoEdit:
		// Auto-edit approves in-tree edits and writes; everything else
		// falls through, so sh still asks unless a rule allows it.
		if p.isInTreeMutation(u) {
			return Decision{
				Behavior: Allow,
				Reason:   Reason{Kind: KindMode, Stage: StageMode, Mode: ModeAutoEdit},
			}, true
		}
		return Decision{}, false

	case ModeFullAuto:
		// Full-auto approves anything that reached this stage, which is
		// anything the safety floor already cleared.
		return Decision{
			Behavior: Allow,
			Reason:   Reason{Kind: KindMode, Stage: StageMode, Mode: ModeFullAuto},
		}, true
	}
	// ModeAsk and the zero value transform nothing.
	return Decision{}, false
}

// unitReadOnly answers plan mode's question for one unit.
func (p *Pipeline) unitReadOnly(u unit) bool {
	if u.shSub != nil {
		return u.shSub.ReadOnly()
	}
	return u.call.Tool.IsReadOnly(u.call.Input)
}

// isInTreeMutation reports whether the unit is an edit or write whose
// target sits inside the workspace root, the shape auto-edit trusts.
func (p *Pipeline) isInTreeMutation(u unit) bool {
	if u.name != "write" && u.name != "edit" {
		return false
	}
	targets := p.mutationTargets(u)
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if !within(t, p.Paths.Root) {
			return false
		}
	}
	return true
}

// applyResolution folds a resolver's answer into the decision. An
// UpdatedInput must narrow the call; a resolution that widens is
// refused and the call is denied, fail closed (doc 05 section 2).
func (p *Pipeline) applyResolution(call Call, d Decision, res Resolution) Decision {
	if res.Behavior == Deny {
		d.Behavior = Deny
		if res.Message != "" {
			d.Message = res.Message
		}
		if res.Kind != "" {
			d.Reason.Kind = res.Kind
		}
		return d
	}
	if len(res.UpdatedInput) > 0 && !narrows(call.Tool.Name(), call.Input, res.UpdatedInput) {
		d.Behavior = Deny
		d.Reason.Details = "the resolver returned an updated input that widens the call; a resolution may only narrow"
		d.Message = "the updated input widens the call, so it was refused"
		return d
	}
	d.Behavior = Allow
	d.UpdatedInput = res.UpdatedInput
	d.Message = ""
	return d
}

// narrows reports whether updated is a narrowing of the original
// input. For sh, every updated subcommand must drop words from one of
// the original subcommands, first word intact; for every other tool
// only the identical input passes, the conservative direction.
func narrows(toolName string, original, updated json.RawMessage) bool {
	if jsonEqual(original, updated) {
		return true
	}
	if toolName != "sh" {
		return false
	}
	origSubs := tool.ShSplit(shCommand(original))
	updSubs := tool.ShSplit(shCommand(updated))
	if len(updSubs) == 0 || len(updSubs) > len(origSubs) {
		return false
	}
	used := make([]bool, len(origSubs))
	for _, u := range updSubs {
		found := false
		for i, o := range origSubs {
			if !used[i] && dropsWords(o.Norm, u.Norm) {
				used[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// dropsWords reports whether narrowed keeps the first word of original
// and is an order-preserving subsequence of its words, the shape of
// dropping a flag.
func dropsWords(original, narrowed string) bool {
	ow := strings.Fields(original)
	nw := strings.Fields(narrowed)
	if len(nw) == 0 || len(nw) > len(ow) || nw[0] != ow[0] {
		return false
	}
	i := 0
	for _, w := range ow {
		if i < len(nw) && nw[i] == w {
			i++
		}
	}
	return i == len(nw)
}

func jsonEqual(a, b json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
}

// buildRequest assembles the ask payload: the rendered consequence,
// the reason, and the rule suggestions the UI may offer. Suggestions
// are proposals; nothing here or downstream writes a rule without an
// explicit user action.
func (p *Pipeline) buildRequest(call Call, d Decision) *Request {
	return &Request{
		ID:          "perm" + strconv.FormatUint(p.reqID.Add(1), 10),
		Call:        call,
		Reason:      d.Reason,
		Message:     d.Message,
		Consequence: RenderConsequence(call.Tool.Name(), call.Input),
		Suggestions: suggestions(call, d),
	}
}

// suggestions proposes rule fragments after an Ask: a two-word prefix
// for sh (the worst subcommand of a compound), the exact URL host
// prefix for fetch, the directory for write and edit.
func suggestions(call Call, d Decision) []string {
	if d.Behavior != Ask {
		return nil
	}
	name := call.Tool.Name()
	switch name {
	case "sh":
		sub := d.Reason.Details // worst subcommand of a compound
		if sub == "" {
			if subs := tool.ShSplit(shCommand(call.Input)); len(subs) == 1 {
				sub = subs[0].Norm
			}
		}
		words := strings.Fields(sub)
		switch {
		case len(words) >= 2:
			return []string{fmt.Sprintf("sh(%s %s:*)", words[0], words[1])}
		case len(words) == 1:
			return []string{fmt.Sprintf("sh(%s:*)", words[0])}
		}
		return nil
	case "fetch":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(call.Input, &a) == nil && a.URL != "" {
			if i := strings.Index(a.URL, "://"); i > 0 {
				rest := a.URL[i+3:]
				host, _, _ := strings.Cut(rest, "/")
				return []string{fmt.Sprintf("fetch(%s://%s:*)", a.URL[:i], host)}
			}
		}
		return nil
	case "write", "edit":
		var a struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(call.Input, &a) == nil && a.FilePath != "" {
			dir := a.FilePath[:strings.LastIndexByte(a.FilePath, '/')+1]
			if dir != "" {
				return []string{fmt.Sprintf("%s(%s:*)", name, dir)}
			}
		}
		return nil
	}
	return []string{name}
}

// shCommand pulls the command string out of sh input.
func shCommand(input json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &a)
	return a.Command
}

// emitRequested publishes the permission.requested half of the pair.
func (p *Pipeline) emitRequested(call Call, req *Request) {
	if p.Journal == nil {
		return
	}
	e, err := event.New(event.TypePermissionRequested, call.Session, call.Turn, event.PermissionRequested{
		ID:          req.ID,
		Call:        call.CallID,
		Tool:        call.Tool.Name(),
		Consequence: req.Consequence,
		Suggestions: req.Suggestions,
		Mode:        string(p.Mode),
		Reason:      req.Message,
	})
	if err == nil {
		p.Journal.Append(e)
	}
}

// emitResolved publishes the permission.resolved half of the pair.
func (p *Pipeline) emitResolved(call Call, reqID string, d Decision) {
	if p.Journal == nil {
		return
	}
	e, err := event.New(event.TypePermissionResolved, call.Session, call.Turn, event.PermissionResolved{
		ID:       reqID,
		Behavior: string(d.Behavior),
		Stage:    string(d.Reason.Stage),
		Rule:     d.Reason.Rule,
		Kind:     string(d.Reason.Kind),
		Reason:   d.Message,
	})
	if err == nil {
		p.Journal.Append(e)
	}
}
