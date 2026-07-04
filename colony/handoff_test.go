package colony

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sampleHeader is a valid header for a handoff of the given kind.
func sampleHeader(kind Kind) Header {
	return Header{ID: "01ABC", Kind: kind, From: "worker", TaskID: "t1"}
}

// TestHandoffsRoundTrip is the slice 4 DoD: every handoff type serializes to
// JSON and back unchanged, because the board stores them as rows and the
// wire carries them.
func TestHandoffsRoundTrip(t *testing.T) {
	cases := []Handoff{
		TaskBrief{Header: sampleHeader(KindTaskBrief), Goal: "fix the parser", Deliverable: KindPatch,
			Context: []ContextRef{{Path: "/x/y.go", Symbol: "Parse"}}, Budget: Budget{Tokens: 5000}},
		Finding{Header: sampleHeader(KindFinding), Summary: "the leak is in Open",
			Evidence: []Citation{{Path: "core/open.go", Lines: [2]int{10, 20}}}, Confidence: 0.8},
		Patch{Header: sampleHeader(KindPatch), Diff: "--- a\n+++ b\n", BaseRef: "abc123", Verified: true},
		Verdict{Header: sampleHeader(KindVerdict), Subject: "01FIND", Pass: true, Reasons: []string{"tests pass"}, Stakes: StakesNormal},
		Question{Header: sampleHeader(KindQuestion), Ask: "which config wins?", Blocking: true},
	}
	for _, h := range cases {
		if err := h.Validate(); err != nil {
			t.Errorf("%T should validate: %v", h, err)
			continue
		}
		data, err := json.Marshal(h)
		if err != nil {
			t.Errorf("marshal %T: %v", h, err)
			continue
		}
		out := reflect.New(reflect.TypeOf(h)).Interface()
		if err := json.Unmarshal(data, out); err != nil {
			t.Errorf("unmarshal %T: %v", h, err)
			continue
		}
		got := reflect.ValueOf(out).Elem().Interface()
		if !reflect.DeepEqual(got, h) {
			t.Errorf("%T did not round-trip:\n got %+v\nwant %+v", h, got, h)
		}
	}
}

// TestFindingNeedsAnchor is the DoD that matches the memory rule: a Finding
// with no citation fails validation, because an unsourced claim has no
// provenance.
func TestFindingNeedsAnchor(t *testing.T) {
	f := Finding{Header: sampleHeader(KindFinding), Summary: "trust me"}
	if err := f.Validate(); err == nil {
		t.Fatal("a finding with no citation must fail validation")
	}
	f.Evidence = []Citation{{Path: ""}}
	if err := f.Validate(); err == nil {
		t.Error("a citation with no path must fail validation")
	}
	f.Evidence = []Citation{{Path: "a.go"}}
	if err := f.Validate(); err != nil {
		t.Errorf("a finding with a cited path must validate: %v", err)
	}
}

// TestHandoffBudgetEnforced pins doc 09 section 3.3: a handoff over its
// per-kind token budget is refused with an error naming the overage.
func TestHandoffBudgetEnforced(t *testing.T) {
	q := Question{Header: sampleHeader(KindQuestion), Ask: strings.Repeat("x", 4000)}
	err := q.Validate()
	if err == nil {
		t.Fatal("a question over its 300-token budget must be refused")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("the refusal must name the budget, got %q", err.Error())
	}
}

// TestHandoffRequiresIDAndKind pins the shared header contract.
func TestHandoffRequiresIDAndKind(t *testing.T) {
	noID := Finding{Header: Header{Kind: KindFinding}, Summary: "x", Evidence: []Citation{{Path: "a"}}}
	if err := noID.Validate(); err == nil {
		t.Error("a handoff with no id must fail validation")
	}
	wrongKind := Finding{Header: Header{ID: "1", Kind: KindPatch}, Summary: "x", Evidence: []Citation{{Path: "a"}}}
	if err := wrongKind.Validate(); err == nil {
		t.Error("a finding whose header kind is not finding must fail validation")
	}
}

// TestLabelsUnionIsMonotonic pins that combining label sets never drops a
// label and dedups and sorts, the join that makes trust impossible to shed.
func TestLabelsUnionIsMonotonic(t *testing.T) {
	got := Labels{"fetch", "mcp"}.Union(Labels{"mcp", "untrusted"})
	want := Labels{"fetch", "mcp", "untrusted"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
}

// TestColonyExposesNoTranscriptAPI is the slice 4 architecture test: the
// kernel must not import the session package, so no colony function can take
// an ant id and return that ant's transcript or episode. The only inter-ant
// currency is the five typed handoffs (doc 09 section 3.4, D3).
func TestColonyExposesNoTranscriptAPI(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == "github.com/tamnd/ari/session" {
				t.Errorf("%s imports the session package; the kernel must not carry transcripts between ants (D3)", name)
			}
		}
	}
}
