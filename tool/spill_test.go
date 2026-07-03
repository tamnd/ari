package tool

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// cappedTool is a stub with a small cap so spill tests stay readable.
type cappedTool struct {
	Base
}

func (cappedTool) Name() string   { return "capped" }
func (cappedTool) Schema() Schema { return Schema{Name: "capped"} }
func (cappedTool) ValidateInput(context.Context, json.RawMessage, *ToolContext) error {
	return nil
}
func (cappedTool) Call(context.Context, json.RawMessage, *ToolContext, ProgressFunc) (*Result, error) {
	return nil, nil
}
func (cappedTool) MaxResultSize() int { return 1000 }

// TestOversizedResultSpillsWithHeadHeavyPreview is the end-to-end DoD
// check: the full text lands on disk under the nest, the inline preview
// keeps three quarters head and one quarter tail, the path is readable,
// and the sweep cleans it all up (doc 04 section 3.2).
func TestOversizedResultSpillsWithHeadHeavyPreview(t *testing.T) {
	tc := testContext(t)
	head := "HEAD-MARKER " + strings.Repeat("h", 738)
	middle := strings.Repeat("m", 3000)
	tail := strings.Repeat("t", 238) + " TAIL-MARKER"
	full := head + middle + tail

	r := &Result{Model: full}
	got, err := ApplyResultBudget(r, cappedTool{}, tc)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}

	// Head-heavy: the first 750 bytes survive inline, the last 250 too.
	if !strings.HasPrefix(got, full[:750]) {
		t.Error("the preview must open with the first three quarters of the budget")
	}
	if !strings.HasSuffix(got, full[len(full)-250:]) {
		t.Error("the preview must close with the last quarter of the budget")
	}
	if !strings.Contains(got, "4000 bytes total, truncated. Full output at ") {
		t.Errorf("the preview must say what was cut and where it lives:\n%s", got)
	}

	// The pointer is a real file holding the whole result.
	start := strings.Index(got, "Full output at ") + len("Full output at ")
	end := strings.Index(got[start:], ". Read it with")
	if end < 0 {
		t.Fatalf("the preview must tell the model how to follow the pointer:\n%s", got)
	}
	path := got[start : start+end]
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the spill path must be readable: %v", err)
	}
	if string(spilled) != full {
		t.Error("the spilled file must carry the full result")
	}

	// Session end sweeps the spill.
	if err := tc.Spill.(*DiskSpill).Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("sweep must remove the spilled file")
	}
}

func TestAResultUnderTheCapPassesThrough(t *testing.T) {
	tc := testContext(t)
	r := &Result{Model: "short output"}
	got, err := ApplyResultBudget(r, cappedTool{}, tc)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if got != "short output" {
		t.Errorf("got %q", got)
	}
}

// TestUIDisplayNeverReachesTheModel proves the two render paths never
// merge: whatever a tool puts in Display stays out of the model-facing
// string, capped or not (doc 04 sections 2.1 and 11).
func TestUIDisplayNeverReachesTheModel(t *testing.T) {
	tc := testContext(t)
	const marker = "UI-ONLY-MARKER-a2c39f"

	small := &Result{Model: "model text", Display: map[string]string{"rich": marker}}
	got, err := ApplyResultBudget(small, cappedTool{}, tc)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if strings.Contains(got, marker) {
		t.Error("the UI display leaked into the model string")
	}

	big := &Result{Model: strings.Repeat("x", 5000), Display: map[string]string{"rich": marker}}
	got, err = ApplyResultBudget(big, cappedTool{}, tc)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if strings.Contains(got, marker) {
		t.Error("the UI display leaked into a spilled preview")
	}
}
