package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempNest points the whole nest at a temp dir and chdirs into a temp project,
// so a command test writes a real colony.db without touching the user's ~/.ari.
func tempNest(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ARI_HOME", filepath.Join(home, ".ari"))
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(proj); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestMemoryImportExportCycle drives the whole human loop through the command
// wiring: a hand-written block imports as a read_only row, an edit updates it and
// keeps it read_only, and deleting the block archives it out of the export.
func TestMemoryImportExportCycle(t *testing.T) {
	tempNest(t)
	ctx := context.Background()
	ns := "worker/main"
	dir := t.TempDir()

	// 1. A block a human typed by hand, no id: import adds it as a new row.
	added := filepath.Join(dir, "add.md")
	if err := os.WriteFile(added, []byte("## bump the version before tagging\n\nrun `make release` only after the tag is pushed.\n"), 0o644); err != nil {
		t.Fatalf("write add.md: %v", err)
	}
	var b strings.Builder
	if err := runMemoryImport(ctx, &b, ns, added); err != nil {
		t.Fatalf("import add: %v", err)
	}
	if !strings.Contains(b.String(), "1 added") {
		t.Fatalf("import report = %q, want 1 added", b.String())
	}

	// 2. Export shows it, with an id comment and a human provenance tag.
	b.Reset()
	if err := runMemoryExport(ctx, &b, ns, ""); err != nil {
		t.Fatalf("export: %v", err)
	}
	md := b.String()
	if !strings.Contains(md, "bump the version before tagging") {
		t.Fatalf("export missing the added row:\n%s", md)
	}
	if !strings.Contains(md, "<!-- ari:memory id=") || !strings.Contains(md, "from: human, human") {
		t.Fatalf("export not round-trippable or not marked human:\n%s", md)
	}

	// 3. Edit the exported body and re-import: an update, still read_only.
	edited := strings.Replace(md, "run `make release` only after the tag is pushed.", "push the tag first, then run `make release`.", 1)
	editedPath := filepath.Join(dir, "edit.md")
	if err := os.WriteFile(editedPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("write edit.md: %v", err)
	}
	b.Reset()
	if err := runMemoryImport(ctx, &b, ns, editedPath); err != nil {
		t.Fatalf("import edit: %v", err)
	}
	if !strings.Contains(b.String(), "1 updated") {
		t.Fatalf("import report = %q, want 1 updated", b.String())
	}
	b.Reset()
	if err := runMemoryExport(ctx, &b, ns, ""); err != nil {
		t.Fatalf("export after edit: %v", err)
	}
	if !strings.Contains(b.String(), "push the tag first") {
		t.Fatalf("edit did not land:\n%s", b.String())
	}

	// 4. Delete the block (empty file) and import: the row is archived out.
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty.md: %v", err)
	}
	b.Reset()
	if err := runMemoryImport(ctx, &b, ns, empty); err != nil {
		t.Fatalf("import empty: %v", err)
	}
	if !strings.Contains(b.String(), "1 archived") {
		t.Fatalf("import report = %q, want 1 archived", b.String())
	}
	b.Reset()
	if err := runMemoryExport(ctx, &b, ns, ""); err != nil {
		t.Fatalf("export after archive: %v", err)
	}
	if strings.Contains(b.String(), "bump the version") || strings.Contains(b.String(), "push the tag first") {
		t.Fatalf("archived row still in export:\n%s", b.String())
	}
}

// TestMemoryExportToFile writes to a path and reports the count instead of
// dumping the markdown to stdout.
func TestMemoryExportToFile(t *testing.T) {
	tempNest(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "mem.md")
	var b strings.Builder
	if err := runMemoryExport(ctx, &b, "worker/main", out); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(b.String(), "exported 0 memories to "+out) {
		t.Fatalf("report = %q, want the count and path", b.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("export file not written: %v", err)
	}
}
