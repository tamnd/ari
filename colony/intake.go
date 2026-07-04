package colony

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Intake turns any request into a routed TaskBrief. A request is whatever
// came in: a line the user typed, a one-shot prompt, or a blackboard subtask
// row. The point of intake is to reduce free text to a typed brief once, so
// every downstream decision is arithmetic over the brief rather than parsing
// over the request again (doc 06 section 3).
type Intake struct {
	embed    Embedder
	classify Classifier
	resolve  AnchorResolver
	from     string
	now      func() time.Time
}

// Classifier is the cheap-tier fallback the rule stage falls through to. It
// is a classification, never a reasoning turn: one static prompt in, one
// token from the controlled vocabulary out (doc 06 section 3.1, D14, D17).
// The kernel depends only on this narrow shape, so the provider machinery
// stays outside it.
type Classifier interface {
	Classify(ctx context.Context, text string) (TaskClass, error)
}

// AnchorResolver checks candidate anchors against the repo and returns only
// what is real: a file that exists, a symbol grep finds, a commit that
// resolves. Resolution is best-effort and never blocks, so a candidate that
// does not resolve is dropped rather than guessed (doc 06 section 3.2).
type AnchorResolver interface {
	File(path string) bool
	Symbol(name string) bool
	Commit(hash string) bool
}

// AnchorKind names what an anchor points at.
type AnchorKind string

const (
	AnchorFile   AnchorKind = "file"
	AnchorSymbol AnchorKind = "symbol"
	AnchorCommit AnchorKind = "commit"
)

// Anchor is a concrete thing the request named that resolves in the repo.
// The fan-out gate tests independence by disjoint anchor sets, staleness
// reads them, and spawn seeding finds the nearest cluster from them, so
// paying a little to extract them at intake saves three later steps.
type Anchor struct {
	Kind  AnchorKind `json:"kind"`
	Value string     `json:"value"`
}

// The controlled vocabulary is small on purpose (doc 06 section 3.1): its
// only job is to be a cheap prefilter before the queen spends cosine, so a
// coarse class that occasionally lands on general beats a fine one that
// drifts across a session.
const (
	ClassEdit       TaskClass = "edit"
	ClassSurvey     TaskClass = "survey"
	ClassMigrate    TaskClass = "migrate"
	ClassDebug      TaskClass = "debug"
	ClassTest       TaskClass = "test"
	ClassResearch   TaskClass = "research"
	ClassMechanical TaskClass = "mechanical"
	ClassGeneral    TaskClass = "general"
)

// classVocab is the set the classifier's one-token output must land in; a
// token outside it is treated as an abstention and falls to general.
var classVocab = map[TaskClass]bool{
	ClassEdit: true, ClassSurvey: true, ClassMigrate: true, ClassDebug: true,
	ClassTest: true, ClassResearch: true, ClassMechanical: true, ClassGeneral: true,
}

// NewIntake wires an intake. The classifier may be nil, in which case the
// rule stage is the whole classifier and an ambiguous request falls to
// general without a model call. The resolver may be nil, in which case no
// anchor resolves. The clock is injectable for tests.
func NewIntake(from string, embed Embedder, classify Classifier, resolve AnchorResolver, now func() time.Time) *Intake {
	if resolve == nil {
		resolve = noopResolver{}
	}
	if now == nil {
		now = time.Now
	}
	return &Intake{embed: embed, classify: classify, resolve: resolve, from: from, now: now}
}

// Foreground turns a request that arrived in the foreground (a TUI line or a
// one-shot prompt) into a brief for the given session.
func (in *Intake) Foreground(ctx context.Context, sessionID, taskID, text string, budget Budget) (TaskBrief, error) {
	return in.build(ctx, sessionID, taskID, "", OriginForeground, text, budget, nil)
}

// Blackboard turns a subtask into a brief that inherits from its parent: the
// blackboard origin, the parent id, a sliced budget so a child cannot outspend
// its parent, and the parent's trust labels so a subtask spawned from
// untrusted content cannot quietly gain automation rights (doc 06 section 3.3,
// D16, D19).
func (in *Intake) Blackboard(ctx context.Context, taskID, text string, parent TaskBrief) (TaskBrief, error) {
	b, err := in.build(ctx, parent.SessionID, taskID, parent.TaskID, OriginBlackboard, text, sliceBudget(parent.Budget), parent.Labels)
	if err != nil {
		return TaskBrief{}, err
	}
	return b, nil
}

// build is the shared path both intakes run: classify, extract anchors,
// embed, and assemble the brief.
func (in *Intake) build(ctx context.Context, sessionID, taskID, parent string, origin Origin, text string, budget Budget, inherit Labels) (TaskBrief, error) {
	class := in.classCascade(ctx, text)
	anchors := in.anchors(text)
	vec, model, err := in.embedText(ctx, text, anchors)
	if err != nil {
		return TaskBrief{}, err
	}
	now := in.now()
	b := TaskBrief{
		Header: Header{
			ID:        newID(now),
			Kind:      KindTaskBrief,
			From:      in.from,
			TaskID:    taskID,
			SessionID: sessionID,
			Labels:    inherit.Union(nil),
			CreatedAt: now,
		},
		Goal:        text,
		Deliverable: deliverableFor(class),
		Budget:      budget,
		Origin:      origin,
		Parent:      parent,
		Class:       class,
		Anchors:     anchors,
		Embed:       vec,
		EmbedModel:  model,
	}
	if err := b.Validate(); err != nil {
		return TaskBrief{}, err
	}
	return b, nil
}

// classCascade is the two-stage classifier: rules over the text first, a
// single cheap-tier call only when the rules abstain, and general when even
// that abstains or errors. Most requests in a coding session are unambiguous
// enough that the rule stage settles them, which keeps intake free.
func (in *Intake) classCascade(ctx context.Context, text string) TaskClass {
	if c, ok := classifyByRules(text); ok {
		return c
	}
	if in.classify == nil {
		return ClassGeneral
	}
	c, err := in.classify.Classify(ctx, text)
	if err != nil || !classVocab[c] {
		return ClassGeneral
	}
	return c
}

// classifyByRules settles the unambiguous requests without a model call. The
// order matters: the more specific cues win, so a request that names both a
// migration and an edit verb classifies as a migration.
func classifyByRules(text string) (TaskClass, bool) {
	t := strings.ToLower(text)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(t, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("migration", "migrate", "schema change") || (strings.Contains(t, ".sql") && has("add", "create")):
		return ClassMigrate, true
	case has("gofmt", "reformat", "rename ", "bump ", "mechanical", "whitespace", "boilerplate"):
		return ClassMechanical, true
	case has("_test.go", "go test", "write a test", "add a test", "unit test", "table test"):
		return ClassTest, true
	case has("panic", "failing test", "stack trace", "regression", "why is", "why does", "bug:"):
		return ClassDebug, true
	case has("what does", "how does", "where is", "explain", "walk me through", "summarize"):
		return ClassSurvey, true
	case has("research", "investigate", "compare ", "evaluate ", "trade-off", "design doc"):
		return ClassResearch, true
	case has("add ", "implement", "change ", "refactor", "fix ", "edit ", "write ", "update ", "wire "):
		return ClassEdit, true
	}
	return "", false
}

// deliverableFor maps a class to the handoff a worker in that class produces:
// the classes that change files produce a patch, the ones that answer a
// question produce a finding.
func deliverableFor(class TaskClass) Kind {
	switch class {
	case ClassEdit, ClassMigrate, ClassTest, ClassMechanical, ClassDebug:
		return KindPatch
	default:
		return KindFinding
	}
}

// sliceBudget gives a subtask a conservative half of its parent's budget so a
// child can never outspend its parent. Doc 10 refines the arithmetic; intake
// only needs the brief to carry a slice, not the parent's whole allowance.
func sliceBudget(parent Budget) Budget {
	return Budget{
		Tokens:    parent.Tokens / 2,
		Deadline:  parent.Deadline,
		ToolCalls: parent.ToolCalls / 2,
	}
}

// embedText embeds the request plus a light dusting of anchor context. Light
// is the operative word: the whole conversation would make the vector drift
// every turn, but the text and its anchors keep two phrasings of the same
// task near each other (doc 06 section 3.2).
func (in *Intake) embedText(ctx context.Context, text string, anchors []Anchor) ([]float32, string, error) {
	if in.embed == nil || !in.embed.Configured() {
		return nil, "", nil
	}
	vec, err := in.embed.Embed(ctx, embedInput(text, anchors))
	if err != nil {
		return nil, "", err
	}
	return vec, in.embed.Model(), nil
}

// embedInput assembles the light context the embedding sees.
func embedInput(text string, anchors []Anchor) string {
	if len(anchors) == 0 {
		return text
	}
	parts := make([]string, 0, len(anchors))
	for _, a := range anchors {
		parts = append(parts, a.Value)
	}
	return text + " " + strings.Join(parts, " ")
}

var (
	commitRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	extRE    = regexp.MustCompile(`\.[a-zA-Z]{1,5}$`)
	symbolRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
)

// anchors extracts the concrete things the request named and keeps only the
// ones the resolver confirms are real. It never blocks: a candidate that does
// not resolve is simply dropped.
func (in *Intake) anchors(text string) []Anchor {
	var out []Anchor
	seen := map[string]bool{}
	for raw := range strings.FieldsSeq(text) {
		tok := strings.Trim(raw, ".,:;!?()[]{}\"'`")
		if tok == "" || seen[tok] {
			continue
		}
		switch {
		case looksLikePath(tok) && in.resolve.File(tok):
			out = append(out, Anchor{Kind: AnchorFile, Value: tok})
		case commitRE.MatchString(tok) && hasDigit(tok) && in.resolve.Commit(tok):
			out = append(out, Anchor{Kind: AnchorCommit, Value: tok})
		case strings.Contains(tok, ".") && symbolRE.MatchString(tok) && in.resolve.Symbol(tok):
			out = append(out, Anchor{Kind: AnchorSymbol, Value: tok})
		default:
			continue
		}
		seen[tok] = true
	}
	return out
}

// looksLikePath reports whether a token is shaped like a repo path: it has a
// separator or a file extension.
func looksLikePath(tok string) bool {
	return strings.Contains(tok, "/") || extRE.MatchString(tok)
}

// hasDigit keeps a hex-shaped English word (say "faded") from being read as a
// commit: a real short hash almost always carries a digit.
func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// noopResolver resolves nothing, the safe default when intake has no repo to
// check against.
type noopResolver struct{}

func (noopResolver) File(string) bool   { return false }
func (noopResolver) Symbol(string) bool { return false }
func (noopResolver) Commit(string) bool { return false }

// dirResolver resolves file anchors against a repo root by stat. Symbols and
// commits are left to a richer resolver; intake's file anchors are the ones
// the later steps most rely on.
type dirResolver struct{ root string }

// DirResolver resolves file anchors against files under root.
func DirResolver(root string) AnchorResolver { return dirResolver{root} }

func (d dirResolver) File(p string) bool {
	if p == "" || strings.Contains(p, "..") {
		return false
	}
	info, err := os.Stat(filepath.Join(d.root, filepath.Clean(p)))
	return err == nil && !info.IsDir()
}

func (dirResolver) Symbol(string) bool { return false }
func (dirResolver) Commit(string) bool { return false }
