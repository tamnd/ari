package memory

import (
	"fmt"
	"strconv"
	"strings"
)

// PortableAnchor is one anchor in the exported form: a kind, a ref, and the
// content hash the memory was true at, empty when the anchor carries none.
type PortableAnchor struct {
	Kind string
	Ref  string
	Hash string
}

// PortableRow is one memory in the human-editable export form. It carries the
// stable id in an HTML comment so import can match a row back to the store, the
// label and body a developer actually reads and edits, and the anchors,
// importance, and provenance that round-trip through the file. From is the
// rendered provenance tail; it is written on export and ignored on import,
// because provenance is the store's to state, not the human's to rewrite.
type PortableRow struct {
	ID         string
	Kind       string
	Namespace  string
	Label      string
	Body       string
	Anchors    []PortableAnchor
	Importance int
	From       string
}

// exportPreamble tells a developer opening the file what is safe to change and
// what import keys on. It is not a memory block: it has no ari:memory marker, so
// the parser skips it.
const exportPreamble = `<!-- ari memory export. Edit the headings and body text. Keep the
     ari:memory comments so import can match rows by id. Delete a whole
     block to archive that memory. -->
`

// RenderPortable renders rows to the round-trippable markdown export. Each row
// becomes one block: a stable id in an HTML comment, the label as a heading, the
// body as prose, then the anchors, importance, and provenance as a short list.
// The output is deterministic, so the same rows render the same bytes and a
// re-export with no edits is a no-op diff.
func RenderPortable(rows []PortableRow) string {
	var b strings.Builder
	b.WriteString(exportPreamble)
	for _, r := range rows {
		b.WriteByte('\n')
		fmt.Fprintf(&b, "<!-- ari:memory id=%s kind=%s ns=%s -->\n", r.ID, r.Kind, r.Namespace)
		fmt.Fprintf(&b, "## %s\n\n", r.Label)
		b.WriteString(strings.TrimRight(r.Body, "\n"))
		b.WriteString("\n\n")
		for _, a := range r.Anchors {
			if a.Hash != "" {
				fmt.Fprintf(&b, "- anchor %s: %s @ %s\n", a.Kind, a.Ref, a.Hash)
			} else {
				fmt.Fprintf(&b, "- anchor %s: %s\n", a.Kind, a.Ref)
			}
		}
		fmt.Fprintf(&b, "- importance: %d\n", r.Importance)
		if r.From != "" {
			fmt.Fprintf(&b, "- from: %s\n", r.From)
		}
	}
	return b.String()
}

// rawBlock accumulates one block's lines as the parser walks the file, before
// the trailing metadata is peeled off the body.
type rawBlock struct {
	id, kind, ns string
	label        string
	awaitHeading bool // a comment was seen; the next heading is this block's label
	lines        []string
}

// ParsePortable reads the export markdown back into rows. A block with an
// ari:memory comment carries its id; a block a human added with only a heading
// has an empty id, which import turns into a fresh row. Parsing is lenient by
// design: a developer edits this by hand, so a missing hash or a dropped from
// line is not an error, it is just less to round-trip.
func ParsePortable(md string) ([]PortableRow, error) {
	var rows []PortableRow
	var cur *rawBlock
	flush := func() {
		if cur != nil {
			rows = append(rows, cur.finish())
			cur = nil
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if id, kind, ns, ok := parseMemoryComment(line); ok {
			flush()
			cur = &rawBlock{id: id, kind: kind, ns: ns, awaitHeading: true}
			continue
		}
		if h, ok := strings.CutPrefix(line, "## "); ok {
			if cur != nil && cur.awaitHeading && cur.label == "" {
				cur.label = strings.TrimSpace(h)
				cur.awaitHeading = false
				continue
			}
			flush()
			cur = &rawBlock{label: strings.TrimSpace(h)}
			continue
		}
		if cur == nil {
			continue // preamble or stray text before the first block
		}
		if cur.awaitHeading && cur.label == "" {
			if strings.TrimSpace(line) == "" {
				continue // a gap between the comment and its heading
			}
			cur.awaitHeading = false // a comment whose heading a human removed
		}
		cur.lines = append(cur.lines, line)
	}
	flush()
	return rows, nil
}

// finish splits a raw block into a row: the trailing run of anchor, importance,
// and from lines is the metadata, everything above it is the body. Peeling from
// the end keeps a body that itself contains a bullet list intact, because only
// the contiguous metadata at the very bottom is consumed.
func (b *rawBlock) finish() PortableRow {
	end := len(b.lines)
	var anchors []PortableAnchor
	importance := 0
	from := ""
	for end > 0 {
		l := strings.TrimSpace(b.lines[end-1])
		if l == "" {
			end--
			continue
		}
		if a, ok := parseAnchorLine(l); ok {
			anchors = append(anchors, a)
			end--
			continue
		}
		if n, ok := parseImportanceLine(l); ok {
			importance = n
			end--
			continue
		}
		if f, ok := parseFromLine(l); ok {
			from = f
			end--
			continue
		}
		break
	}
	// Anchors were collected bottom-up; restore export order.
	for i, j := 0, len(anchors)-1; i < j; i, j = i+1, j-1 {
		anchors[i], anchors[j] = anchors[j], anchors[i]
	}
	return PortableRow{
		ID:         b.id,
		Kind:       b.kind,
		Namespace:  b.ns,
		Label:      b.label,
		Body:       strings.TrimSpace(strings.Join(b.lines[:end], "\n")),
		Anchors:    anchors,
		Importance: importance,
		From:       from,
	}
}

// parseMemoryComment reads an "<!-- ari:memory id=... kind=... ns=... -->" line
// into its fields. Any line that is not exactly that shape is not a block
// marker, so the preamble and ordinary HTML comments pass through untouched.
func parseMemoryComment(line string) (id, kind, ns string, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "<!--") || !strings.HasSuffix(s, "-->") {
		return "", "", "", false
	}
	inner := strings.TrimSpace(s[4 : len(s)-3])
	rest, isMarker := strings.CutPrefix(inner, "ari:memory")
	if !isMarker {
		return "", "", "", false
	}
	for _, tok := range strings.Fields(rest) {
		k, v, found := strings.Cut(tok, "=")
		if !found {
			continue
		}
		switch k {
		case "id":
			id = v
		case "kind":
			kind = v
		case "ns":
			ns = v
		}
	}
	return id, kind, ns, true
}

// parseAnchorLine reads "- anchor <kind>: <ref>[ @ <hash>]".
func parseAnchorLine(l string) (PortableAnchor, bool) {
	rest, ok := strings.CutPrefix(l, "- anchor ")
	if !ok {
		return PortableAnchor{}, false
	}
	kind, tail, ok := strings.Cut(rest, ":")
	if !ok {
		return PortableAnchor{}, false
	}
	ref := strings.TrimSpace(tail)
	hash := ""
	if i := strings.LastIndex(ref, " @ "); i >= 0 {
		hash = strings.TrimSpace(ref[i+3:])
		ref = strings.TrimSpace(ref[:i])
	}
	return PortableAnchor{Kind: strings.TrimSpace(kind), Ref: ref, Hash: hash}, true
}

// parseImportanceLine reads "- importance: <n>".
func parseImportanceLine(l string) (int, bool) {
	v, ok := strings.CutPrefix(l, "- importance:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseFromLine reads "- from: <provenance>".
func parseFromLine(l string) (string, bool) {
	v, ok := strings.CutPrefix(l, "- from:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// ImportPlan is the diff of an edited export against live memory: the rows whose
// text a human changed, the blocks a human added, and the ids a human removed by
// deleting their block. It is pure data, computed by Reconcile and applied by
// the command, so the diff logic is testable without a store.
type ImportPlan struct {
	Update  []PortableRow // an existing id whose label or body changed
	Insert  []PortableRow // a block with no matching live id
	Archive []string      // a live id absent from the edited file
}

// Reconcile diffs the edited rows against the live rows by id. An edited body or
// label updates the row and (at apply time) marks it read_only, because a human
// edit is the highest-provenance input and the consolidator must not rewrite it
// (D11). A block with no id, or with an id that names no live row, becomes a
// fresh human-authored row so the words are never lost. A live id the file no
// longer mentions is archived. An unchanged block is left alone.
func Reconcile(live, edited []PortableRow) ImportPlan {
	liveByID := make(map[string]PortableRow, len(live))
	for _, r := range live {
		liveByID[r.ID] = r
	}
	var plan ImportPlan
	matched := make(map[string]bool, len(edited))
	for _, e := range edited {
		if e.ID == "" {
			plan.Insert = append(plan.Insert, e)
			continue
		}
		lr, ok := liveByID[e.ID]
		if !ok {
			e.ID = "" // a dangling id: keep the words, drop the stale handle
			plan.Insert = append(plan.Insert, e)
			continue
		}
		matched[e.ID] = true
		if strings.TrimSpace(lr.Body) != strings.TrimSpace(e.Body) ||
			strings.TrimSpace(lr.Label) != strings.TrimSpace(e.Label) {
			plan.Update = append(plan.Update, e)
		}
	}
	for _, r := range live {
		if !matched[r.ID] {
			plan.Archive = append(plan.Archive, r.ID)
		}
	}
	return plan
}
