package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/ari/ant"
	"github.com/tamnd/ari/memory"
	memsqlite "github.com/tamnd/ari/memory/sqlite"
	"github.com/tamnd/ari/nest"
)

// humanRowImportance is the weight a block a human typed by hand carries when
// import has no importance line to read. A hand-authored note is a strong
// signal, so it lands above an observation but below a pinned rule.
const humanRowImportance = 8

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Export and import colony memory as editable markdown",
	Long: `Read and edit a namespace's memory as markdown.

ari memory export renders a namespace to markdown you can open in your
editor. ari memory import reads it back: an edited body updates the row
and marks it read only, a block you add becomes a new memory, and a
block you delete archives its row. A read-only row is the highest
provenance there is, so the consolidator never rewrites it.`,
}

var memoryExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Render a namespace's memory to editable markdown",
	RunE: func(c *cobra.Command, args []string) error {
		ns, _ := c.Flags().GetString("namespace")
		out, _ := c.Flags().GetString("out")
		return runMemoryExport(c.Context(), c.OutOrStdout(), ns, out)
	},
}

var memoryImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Read an edited memory export back into the namespace",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		ns, _ := c.Flags().GetString("namespace")
		return runMemoryImport(c.Context(), c.OutOrStdout(), ns, args[0])
	},
}

func init() {
	def := ant.WorkerCard().State.Namespace
	memoryExportCmd.Flags().String("namespace", def, "the memory namespace to export")
	memoryExportCmd.Flags().String("out", "", "write to this file instead of stdout")
	memoryImportCmd.Flags().String("namespace", def, "the memory namespace to import into")
	memoryCmd.AddCommand(memoryExportCmd, memoryImportCmd)
	rootCmd.AddCommand(memoryCmd)
}

// openMemory resolves the nest the same way every command does and opens the
// project's colony.db, so export and import target the same file the loop
// writes. The caller closes the returned store.
func openMemory(ctx context.Context) (*memsqlite.Store, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, coded(3, err)
	}
	n, err := nest.Resolve(cwd)
	if err != nil {
		return nil, coded(3, err)
	}
	st, err := memsqlite.Open(n.ColonyDB())
	if err != nil {
		return nil, coded(3, err)
	}
	if err := st.Start(ctx); err != nil {
		_ = st.Close()
		return nil, coded(3, err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, coded(3, err)
	}
	return st, nil
}

// runMemoryExport renders a namespace to the round-trippable markdown and
// writes it to a file or stdout.
func runMemoryExport(ctx context.Context, stdout io.Writer, ns, outPath string) error {
	st, err := openMemory(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	rows, err := st.ExportRows(ctx, ns)
	if err != nil {
		return coded(1, err)
	}
	md := memory.RenderPortable(toPortable(rows))
	if outPath == "" {
		_, err := io.WriteString(stdout, md)
		return err
	}
	if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
		return coded(1, err)
	}
	_, err = fmt.Fprintf(stdout, "exported %d memories to %s\n", len(rows), outPath)
	return err
}

// runMemoryImport reads an edited export, diffs it against live memory, and
// applies the plan: edited rows updated and marked read_only, new blocks added
// as human rows, deleted blocks archived. It reports what it did, never the
// contents, so the summary is safe in a log.
func runMemoryImport(ctx context.Context, stdout io.Writer, ns, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return coded(3, err)
	}
	edited, err := memory.ParsePortable(string(data))
	if err != nil {
		return coded(1, err)
	}

	st, err := openMemory(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	liveRows, err := st.ExportRows(ctx, ns)
	if err != nil {
		return coded(1, err)
	}
	plan := memory.Reconcile(toPortable(liveRows), edited)

	updated := 0
	for _, r := range plan.Update {
		ok, err := st.UpdateMemoryText(ctx, ns, r.ID, r.Label, r.Body)
		if err != nil {
			return coded(1, err)
		}
		if ok {
			updated++
		}
	}
	added := 0
	for _, r := range plan.Insert {
		if err := st.InsertMemory(ctx, humanRow(ns, r), toAnchors(r.Anchors), nil); err != nil {
			return coded(1, err)
		}
		added++
	}
	archived := 0
	for _, id := range plan.Archive {
		if _, ok, err := st.ArchiveMemory(ctx, ns, id); err != nil {
			return coded(1, err)
		} else if ok {
			archived++
		}
	}
	_, err = fmt.Fprintf(stdout, "imported %s: %d updated, %d added, %d archived\n", ns, updated, added, archived)
	return err
}

// humanRow builds a fresh read_only memory from a block a human added. It is an
// observation regardless of what the block claimed, because a hand-typed note
// with no evidence cannot be a reflection, and it carries the highest
// provenance the store knows: human, read_only, so no fold ever rewrites it.
func humanRow(ns string, r memory.PortableRow) memsqlite.Memory {
	now := time.Now()
	imp := r.Importance
	if imp <= 0 {
		imp = humanRowImportance
	}
	return memsqlite.Memory{
		ID:         memsqlite.NewID(now),
		Namespace:  ns,
		Kind:       memsqlite.KindObservation,
		Label:      humanLabel(r),
		Body:       r.Body,
		Importance: imp,
		CreatedAt:  now.Unix(),
		AccessedAt: now.Unix(),
		SourceAnt:  "human",
		TTLClass:   memsqlite.TTLNormal,
		ReadOnly:   true,
	}
}

// humanLabel is the block's heading, or the first line of the body when a human
// added a block with no heading.
func humanLabel(r memory.PortableRow) string {
	if r.Label != "" {
		return r.Label
	}
	line := r.Body
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// toPortable maps the store's export rows to the render form, folding the
// provenance and the read_only and verified flags into a single from line.
func toPortable(rows []memsqlite.ExportRow) []memory.PortableRow {
	out := make([]memory.PortableRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, memory.PortableRow{
			ID:         r.ID,
			Kind:       r.Kind,
			Namespace:  r.Namespace,
			Label:      r.Label,
			Body:       r.Body,
			Anchors:    toPortableAnchors(r.Anchors),
			Importance: r.Importance,
			From:       fromLine(r),
		})
	}
	return out
}

// fromLine renders a row's provenance: who recorded it and, when it matters, a
// status tag so a developer reading the file sees which rows a human pinned and
// which the machine has confirmed.
func fromLine(r memsqlite.ExportRow) string {
	base := strings.TrimSpace(r.SourceAnt + " " + r.SourceTask)
	switch {
	case r.ReadOnly:
		return strings.TrimSpace(base + ", human")
	case r.Verified:
		return strings.TrimSpace(base + ", verified")
	default:
		return base
	}
}

func toPortableAnchors(anchors []memsqlite.Anchor) []memory.PortableAnchor {
	out := make([]memory.PortableAnchor, 0, len(anchors))
	for _, a := range anchors {
		out = append(out, memory.PortableAnchor{Kind: a.Kind, Ref: a.Ref, Hash: a.FileHash})
	}
	return out
}

func toAnchors(anchors []memory.PortableAnchor) []memsqlite.Anchor {
	out := make([]memsqlite.Anchor, 0, len(anchors))
	for _, a := range anchors {
		out = append(out, memsqlite.Anchor{Kind: a.Kind, Ref: a.Ref, FileHash: a.Hash})
	}
	return out
}
