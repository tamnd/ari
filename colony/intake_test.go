package colony

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bagEmbedder embeds a request as a bag of words hashed into a small fixed
// vector, so two phrasings that share their content words land on the same
// vector regardless of order or filler. It is the property intake needs: an
// embedding stable across two phrasings of the same task.
type bagEmbedder struct{}

func (bagEmbedder) Configured() bool { return true }
func (bagEmbedder) Model() string    { return "bag-v1" }
func (bagEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	const dim = 16
	vec := make([]float32, dim)
	for word := range strings.FieldsSeq(strings.ToLower(text)) {
		word = strings.Trim(word, ".,:;!?()")
		if word == "" || stop[word] {
			continue
		}
		vec[fnv(word)%dim] += 1
	}
	return vec, nil
}

// countingClassifier records how many times the cheap tier was asked, so the
// cascade's "exactly one call, only when the rules abstain" contract is
// testable.
type countingClassifier struct {
	calls int
	out   TaskClass
}

func (c *countingClassifier) Classify(_ context.Context, _ string) (TaskClass, error) {
	c.calls++
	return c.out, nil
}

// TestUnambiguousClassifiesByRules is the DoD that a clear request settles on
// the rule stage with no model call.
func TestUnambiguousClassifiesByRules(t *testing.T) {
	cases := map[string]TaskClass{
		"add a migration for the users table":           ClassMigrate,
		"gofmt the whole tree":                          ClassMechanical,
		"write a table test for the parser":             ClassTest,
		"the build panics on startup, why":              ClassDebug,
		"what does the queen router do":                 ClassSurvey,
		"research the trade-off between b-tree and lsm": ClassResearch,
		"implement the fan-out gate":                    ClassEdit,
	}
	for text, want := range cases {
		cc := &countingClassifier{out: ClassGeneral}
		in := NewIntake("intake", bagEmbedder{}, cc, nil, fixedNow)
		b, err := in.Foreground(context.Background(), "s1", "t1", text, Budget{Tokens: 4000})
		if err != nil {
			t.Fatalf("intake %q: %v", text, err)
		}
		if b.Class != want {
			t.Errorf("%q classified %s, want %s", text, b.Class, want)
		}
		if cc.calls != 0 {
			t.Errorf("%q made %d model calls, want 0 (rules should settle it)", text, cc.calls)
		}
	}
}

// TestAmbiguousTriggersOneCheapCall is the DoD that an ambiguous request makes
// exactly one cheap-tier call whose output must be one token from the
// controlled vocabulary.
func TestAmbiguousTriggersOneCheapCall(t *testing.T) {
	cc := &countingClassifier{out: ClassResearch}
	in := NewIntake("intake", bagEmbedder{}, cc, nil, fixedNow)
	b, err := in.Foreground(context.Background(), "s1", "t1", "look into the thing we talked about", Budget{Tokens: 4000})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if cc.calls != 1 {
		t.Errorf("cheap-tier calls = %d, want exactly 1", cc.calls)
	}
	if b.Class != ClassResearch {
		t.Errorf("class = %s, want research (the cheap tier's answer)", b.Class)
	}

	// A cheap tier that answers outside the vocabulary is an abstention.
	cc2 := &countingClassifier{out: TaskClass("frobnicate")}
	in2 := NewIntake("intake", bagEmbedder{}, cc2, nil, fixedNow)
	b2, err := in2.Foreground(context.Background(), "s1", "t1", "look into the thing", Budget{Tokens: 4000})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if b2.Class != ClassGeneral {
		t.Errorf("off-vocabulary answer should fall to general, got %s", b2.Class)
	}
}

// TestNilClassifierFallsToGeneral is the DoD tail: with no cheap tier wired,
// an ambiguous request is general without a model call, and general is a
// routable class.
func TestNilClassifierFallsToGeneral(t *testing.T) {
	in := NewIntake("intake", bagEmbedder{}, nil, nil, fixedNow)
	b, err := in.Foreground(context.Background(), "s1", "t1", "look into the thing", Budget{Tokens: 4000})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if b.Class != ClassGeneral {
		t.Errorf("class = %s, want general", b.Class)
	}
}

// TestEmbeddingStableAcrossPhrasings is the DoD that two phrasings of the same
// task land near each other: cosine within tolerance.
func TestEmbeddingStableAcrossPhrasings(t *testing.T) {
	in := NewIntake("intake", bagEmbedder{}, nil, nil, fixedNow)
	a, err := in.Foreground(context.Background(), "s1", "t1", "add a migration for the users table", Budget{Tokens: 4000})
	if err != nil {
		t.Fatalf("intake a: %v", err)
	}
	b, err := in.Foreground(context.Background(), "s1", "t2", "for the users table, please add a migration", Budget{Tokens: 4000})
	if err != nil {
		t.Fatalf("intake b: %v", err)
	}
	if cos := cosine(a.Embed, b.Embed); cos < 0.99 {
		t.Errorf("two phrasings embedded cosine %.3f, want >= 0.99", cos)
	}
	if a.EmbedModel != "bag-v1" {
		t.Errorf("embed model = %q, want bag-v1", a.EmbedModel)
	}
}

// TestAnchorsForRealFiles is the DoD that a request naming real files gets
// file anchors, and a named path that does not exist is dropped.
func TestAnchorsForRealFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "colony"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "colony", "board.go"), []byte("package colony\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	in := NewIntake("intake", bagEmbedder{}, nil, DirResolver(root), fixedNow)
	b, err := in.Foreground(context.Background(), "s1", "t1", "read colony/board.go and also colony/ghost.go then fix it", Budget{Tokens: 4000})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	var files []string
	for _, a := range b.Anchors {
		if a.Kind == AnchorFile {
			files = append(files, a.Value)
		}
	}
	if len(files) != 1 || files[0] != "colony/board.go" {
		t.Errorf("file anchors = %v, want [colony/board.go]; a nonexistent path must be dropped", files)
	}
}

// TestBlackboardBriefInheritsParent is the DoD that a blackboard-born brief
// carries its parent and a sliced budget, and inherits the parent's trust.
func TestBlackboardBriefInheritsParent(t *testing.T) {
	in := NewIntake("intake", bagEmbedder{}, nil, nil, fixedNow)
	parent, err := in.Foreground(context.Background(), "s1", "root", "implement the fan-out gate", Budget{Tokens: 4000, ToolCalls: 40})
	if err != nil {
		t.Fatalf("parent intake: %v", err)
	}
	parent.Labels = Labels{"untrusted"}

	child, err := in.Blackboard(context.Background(), "sub", "add a test for the gate", parent)
	if err != nil {
		t.Fatalf("child intake: %v", err)
	}
	if child.Origin != OriginBlackboard {
		t.Errorf("child origin = %s, want blackboard", child.Origin)
	}
	if child.Parent != "root" {
		t.Errorf("child parent = %q, want root", child.Parent)
	}
	if child.Budget.Tokens != 2000 || child.Budget.ToolCalls != 20 {
		t.Errorf("child budget = %+v, want half the parent's", child.Budget)
	}
	if len(child.Labels) != 1 || child.Labels[0] != "untrusted" {
		t.Errorf("child labels = %v, want the parent's untrusted label", child.Labels)
	}
}

// --- test helpers ---

var stop = map[string]bool{
	"a": true, "an": true, "the": true, "for": true, "please": true,
	"and": true, "then": true, "also": true, "to": true, "of": true,
}

func fixedNow() time.Time { return time.Unix(1_700_000_000, 0) }

func fnv(s string) uint32 {
	h := uint32(2166136261)
	for i := range len(s) {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
