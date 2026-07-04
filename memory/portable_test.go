package memory

import (
	"strings"
	"testing"
)

func sampleRows() []PortableRow {
	return []PortableRow{
		{
			ID: "01aa", Kind: "reflection", Namespace: "worker/main",
			Label: "never hand-edit gen/*.go",
			Body:  "Run `make gen` after any change to schema.go.\nA hand edit is lost on the next build.",
			Anchors: []PortableAnchor{
				{Kind: "file", Ref: "schema.go", Hash: "9c2e1a4"},
				{Kind: "file", Ref: "gen/model.go"},
			},
			Importance: 8,
			From:       "ant_worker task_1042, verified",
		},
		{
			ID: "01bb", Kind: "observation", Namespace: "worker/main",
			Label:      "reuse the shared transport",
			Body:       "One http.Transport for the whole process.",
			Importance: 6,
			From:       "ant_worker",
		},
	}
}

// TestPortableRoundTrip: render then parse returns the same rows, ids and all,
// which is the property import relies on to match a block back to its store row.
func TestPortableRoundTrip(t *testing.T) {
	rows := sampleRows()
	got, err := ParsePortable(RenderPortable(rows))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("parsed %d rows, want %d", len(got), len(rows))
	}
	for i, w := range rows {
		g := got[i]
		if g.ID != w.ID || g.Kind != w.Kind || g.Namespace != w.Namespace {
			t.Errorf("row %d header = %q/%q/%q, want %q/%q/%q", i, g.ID, g.Kind, g.Namespace, w.ID, w.Kind, w.Namespace)
		}
		if g.Label != w.Label {
			t.Errorf("row %d label = %q, want %q", i, g.Label, w.Label)
		}
		if g.Body != w.Body {
			t.Errorf("row %d body = %q, want %q", i, g.Body, w.Body)
		}
		if g.Importance != w.Importance {
			t.Errorf("row %d importance = %d, want %d", i, g.Importance, w.Importance)
		}
		if len(g.Anchors) != len(w.Anchors) {
			t.Fatalf("row %d anchors = %d, want %d", i, len(g.Anchors), len(w.Anchors))
		}
		for j, wa := range w.Anchors {
			if g.Anchors[j] != wa {
				t.Errorf("row %d anchor %d = %+v, want %+v", i, j, g.Anchors[j], wa)
			}
		}
	}
}

// TestParseHumanAddedBlock: a block a human typed with only a heading and no
// ari:memory comment parses to a row with an empty id, the signal Reconcile
// turns into a fresh insert.
func TestParseHumanAddedBlock(t *testing.T) {
	md := "## a note I typed\n\nremember to bump the version before tagging.\n"
	rows, err := ParsePortable(md)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("parsed %d rows, want 1", len(rows))
	}
	if rows[0].ID != "" {
		t.Errorf("id = %q, want empty for a human-added block", rows[0].ID)
	}
	if rows[0].Label != "a note I typed" || !strings.Contains(rows[0].Body, "bump the version") {
		t.Errorf("row = %+v, want the typed heading and body", rows[0])
	}
}

// TestParseKeepsBodyBulletList: a body that ends in its own bullet list is not
// mistaken for the metadata trailer, because only the anchor, importance, and
// from lines at the very bottom are peeled off.
func TestParseKeepsBodyBulletList(t *testing.T) {
	md := "<!-- ari:memory id=x kind=observation ns=n -->\n## steps\n\ndo this:\n\n- first\n- second\n\n- importance: 4\n"
	rows, err := ParsePortable(md)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("parsed %d rows, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Body, "- first") || !strings.Contains(rows[0].Body, "- second") {
		t.Errorf("body dropped its bullet list: %q", rows[0].Body)
	}
	if rows[0].Importance != 4 {
		t.Errorf("importance = %d, want 4", rows[0].Importance)
	}
}

// TestReconcileUpdate: an edited body on a row that still carries its id is an
// update, not an insert or an archive.
func TestReconcileUpdate(t *testing.T) {
	live := []PortableRow{{ID: "a", Label: "l", Body: "old body"}}
	edited := []PortableRow{{ID: "a", Label: "l", Body: "new body"}}
	plan := Reconcile(live, edited)
	if len(plan.Update) != 1 || plan.Update[0].Body != "new body" {
		t.Fatalf("update = %+v, want the edited row", plan.Update)
	}
	if len(plan.Insert) != 0 || len(plan.Archive) != 0 {
		t.Fatalf("plan should be update-only, got %+v", plan)
	}
}

// TestReconcileUnchangedIsNoop: a block whose text a human left alone produces
// no update, so import does not needlessly mark it read_only.
func TestReconcileUnchangedIsNoop(t *testing.T) {
	live := []PortableRow{{ID: "a", Label: "l", Body: "same\n"}}
	edited := []PortableRow{{ID: "a", Label: "l", Body: "  same  "}} // whitespace only
	plan := Reconcile(live, edited)
	if len(plan.Update)+len(plan.Insert)+len(plan.Archive) != 0 {
		t.Fatalf("unchanged block should be a no-op, got %+v", plan)
	}
}

// TestReconcileInsertAndArchive: a block with no id is an insert, and a live id
// the file no longer mentions is an archive.
func TestReconcileInsertAndArchive(t *testing.T) {
	live := []PortableRow{{ID: "keep", Body: "kept"}, {ID: "gone", Body: "removed"}}
	edited := []PortableRow{
		{ID: "keep", Body: "kept"},
		{ID: "", Label: "new", Body: "a fresh note"},
	}
	plan := Reconcile(live, edited)
	if len(plan.Insert) != 1 || plan.Insert[0].Body != "a fresh note" {
		t.Fatalf("insert = %+v, want the new block", plan.Insert)
	}
	if len(plan.Archive) != 1 || plan.Archive[0] != "gone" {
		t.Fatalf("archive = %+v, want [gone]", plan.Archive)
	}
	if len(plan.Update) != 0 {
		t.Fatalf("update = %+v, want none", plan.Update)
	}
}

// TestReconcileDanglingID: an id that names no live row keeps the human's words
// as a fresh insert rather than dropping them.
func TestReconcileDanglingID(t *testing.T) {
	live := []PortableRow{{ID: "real", Body: "x"}}
	edited := []PortableRow{{ID: "real", Body: "x"}, {ID: "ghost", Body: "orphan words"}}
	plan := Reconcile(live, edited)
	if len(plan.Insert) != 1 || plan.Insert[0].Body != "orphan words" || plan.Insert[0].ID != "" {
		t.Fatalf("insert = %+v, want the orphan promoted to a fresh row", plan.Insert)
	}
	if len(plan.Archive) != 0 {
		t.Fatalf("archive = %+v, want none: the real row is still present", plan.Archive)
	}
}
