package fold

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tamnd/ari/memory/sqlite"
)

// mergeJaccard is how much two observations must overlap in words before the
// fold treats them as the same note and merges them. Set low enough to catch
// reworded repeats, high enough not to blur two distinct notes into one.
const mergeJaccard = 0.5

// reflectionImportance is the write-time weight a fold-synthesized reflection
// carries. It sits above an observation because a lesson drawn from several
// observations is a stronger signal than any one of them, but the fold only
// writes it when the observations actually share an anchor and the model draws
// a lesson, so the weight cannot be gamed by repetition.
const reflectionImportance = 7

// Summarizer is the cheap-tier model the fold leans on to combine notes and
// draw lessons. It returns the text and the tokens it spent so the fold can
// account its cost against the budget the ship gate checks. The fold treats an
// empty return as "no lesson here" and writes nothing, so the model selects
// and phrases but never invents a memory out of nothing.
type Summarizer interface {
	Summarize(ctx context.Context, prompt string) (text string, tokens int, err error)
}

// Consolidator is the single writer of live memory (D12). It reads the pending
// candidates a namespace has accumulated, merges the repeats, draws reflections
// where the evidence supports one, and writes the survivors in one fold. At
// most one fold runs at a time across the whole colony, so two triggers (an
// idle tick and a session ending together) never fold the same candidates
// twice.
type Consolidator struct {
	store   *sqlite.Store
	sum     Summarizer
	emit    func(FoldReport)
	repo    Repo
	clock   func() time.Time
	running atomic.Bool
}

// Option configures a consolidator at construction. Options keep New's required
// arguments to the store, the model, and the emitter, so a caller that has no
// working tree to check against (a test, an import job) builds one without one.
type Option func(*Consolidator)

// WithRepo gives the consolidator a working tree so the fold's invalidation pass
// can demote memories whose anchored files changed. Without it the pass is a
// no-op and memories only evaporate on their ttl clock.
func WithRepo(r Repo) Option {
	return func(c *Consolidator) { c.repo = r }
}

// WithClock overrides the wall clock, so a test can advance time and watch a
// memory decay on its ttl class.
func WithClock(now func() time.Time) Option {
	return func(c *Consolidator) { c.clock = now }
}

// New builds a consolidator over a store and a cheap-tier summarizer. emit, if
// set, is called once per folded namespace with the fold's report so the loop
// can put a memory.folded event on the bus; pass nil to fold silently.
func New(store *sqlite.Store, sum Summarizer, emit func(FoldReport), opts ...Option) *Consolidator {
	c := &Consolidator{store: store, sum: sum, emit: emit, clock: time.Now}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Fold runs one consolidation cycle over every namespace with pending
// candidates and returns a report per namespace. If a fold is already running
// it returns nil with no error, the at-most-one guarantee: a second trigger
// while a fold is in flight is a no-op, not a queued second pass.
func (c *Consolidator) Fold(ctx context.Context) ([]FoldReport, error) {
	if !c.running.CompareAndSwap(false, true) {
		return nil, nil
	}
	defer c.running.Store(false)

	nss, err := c.store.PendingNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var reports []FoldReport
	for _, ns := range nss {
		r, err := c.FoldNamespace(ctx, ns)
		if err != nil {
			return reports, err
		}
		reports = append(reports, r)
		if c.emit != nil {
			c.emit(r)
		}
	}
	return reports, nil
}

// FoldNamespace folds one namespace's pending candidates into live memory and
// returns the report. It is the body Fold loops over; it takes no lock, so the
// caller (Fold, or a test) owns the at-most-one guarantee. An empty namespace
// returns a zero report without touching the model, so a fold triggered on a
// quiet colony spends nothing.
func (c *Consolidator) FoldNamespace(ctx context.Context, ns string) (FoldReport, error) {
	cands, ids, err := c.store.PendingCandidates(ctx, ns, 0)
	if err != nil {
		return FoldReport{}, err
	}
	report := FoldReport{Namespace: ns, Candidates: len(cands)}
	if len(cands) == 0 {
		return report, nil
	}

	anchorsByID, evidenceByID, err := c.store.CandidateDetails(ctx, ids)
	if err != nil {
		return report, err
	}
	for i := range cands {
		cands[i].Anchors = anchorsByID[ids[i]]
		cands[i].Evidence = evidenceByID[ids[i]]
	}

	// Split the intake: observations get clustered and merged, reflections the
	// ant already authored are carried through with their evidence intact.
	var obs, refl []sqlite.Candidate
	for _, cd := range cands {
		if cd.Kind == sqlite.KindReflection {
			refl = append(refl, cd)
		} else {
			obs = append(obs, cd)
		}
	}

	var merged []sqlite.Folded
	for _, group := range clusterObservations(obs) {
		f, tokens, err := c.mergeGroup(ctx, ns, obs, group)
		if err != nil {
			return report, err
		}
		report.TokensCheap += tokens
		merged = append(merged, f)
	}

	// Reflect: where several merged observations share an anchor, ask the cheap
	// tier for the lesson and record it resting on exactly those rows. The model
	// draws the lesson; the evidence is the rows themselves, not the model's
	// word, so a reflection can never float free of what it rests on.
	reflections, tokens, err := c.reflect(ctx, ns, merged)
	if err != nil {
		return report, err
	}
	report.TokensCheap += tokens
	merged = append(merged, reflections...)

	// Carry the ant-authored reflections through, each still citing the live
	// observations it named.
	for _, cd := range refl {
		merged = append(merged, sqlite.Folded{
			Memory: sqlite.Memory{
				Namespace: ns, Kind: sqlite.KindReflection,
				Label: label(cd.Body), Body: cd.Body, Importance: cd.Importance,
				TTLClass:  sqlite.TTLNormal,
				SourceAnt: cd.Source.Ant, SourceTask: cd.Source.Task, AnchorCommit: cd.Source.Commit,
			},
			Anchors:     cd.Anchors,
			EvidenceIDs: cd.Evidence,
		})
	}

	report.Merged = len(merged)
	for _, f := range merged {
		if f.Memory.Kind == sqlite.KindReflection {
			report.Reflections++
		}
	}

	if err := c.store.CommitFold(ctx, merged, ids, c.clock()); err != nil {
		return report, err
	}

	// The fold is also where memory is invalidated: with the survivors written,
	// check which anchored rows the working tree moved out from under and demote
	// them. The diff runs once per distinct anchor_commit, not once per row.
	demoted, err := c.demoteStale(ctx, ns)
	if err != nil {
		return report, err
	}
	for _, d := range demoted {
		if d.Stale {
			report.Demoted++
		}
	}
	return report, nil
}

// mergeGroup turns one cluster of observation candidates into one live row. A
// singleton is written as-is with no model call. A group of repeats is handed
// to the cheap tier to combine into a single statement, its importance set to
// the strongest single signal in the group, not their sum, so repeating a note
// cannot lift its rank (the poisoning defense). Anchors are the union of the
// group's, copied from the candidates rather than authored by the model.
func (c *Consolidator) mergeGroup(ctx context.Context, ns string, obs []sqlite.Candidate, group []int) (sqlite.Folded, int, error) {
	first := obs[group[0]]
	body := first.Body
	importance := first.Importance
	tokens := 0

	if len(group) > 1 {
		var b strings.Builder
		b.WriteString("Combine these notes a worker recorded into one clear statement. Keep only what they agree on and add nothing that is not already present.\n\n")
		for _, i := range group {
			b.WriteString("- ")
			b.WriteString(obs[i].Body)
			b.WriteString("\n")
			if obs[i].Importance > importance {
				importance = obs[i].Importance
			}
		}
		text, spent, err := c.sum.Summarize(ctx, b.String())
		tokens = spent
		if err != nil {
			return sqlite.Folded{}, tokens, err
		}
		if t := strings.TrimSpace(text); t != "" {
			body = t
		}
	}

	anchors := unionAnchors(obs, group)
	return sqlite.Folded{
		Memory: sqlite.Memory{
			Namespace: ns, Kind: sqlite.KindObservation,
			Label: label(body), Body: body, Importance: importance,
			TTLClass:  sqlite.TTLNormal,
			SourceAnt: first.Source.Ant, SourceTask: first.Source.Task, AnchorCommit: first.Source.Commit,
		},
		Anchors: anchors,
	}, tokens, nil
}

// reflect draws reflections from the merged observations. It groups them by a
// shared anchor and, for any group of two or more, asks the cheap tier for the
// lesson. An empty answer is taken as "no lesson" and skipped, so the fold
// never writes a reflection the model could not actually draw. The evidence is
// the indices of the merged rows in the batch, wired to their ids at commit.
func (c *Consolidator) reflect(ctx context.Context, ns string, merged []sqlite.Folded) ([]sqlite.Folded, int, error) {
	byAnchor := map[string][]int{}
	for i, f := range merged {
		for _, a := range f.Anchors {
			key := a.Kind + ":" + strings.ToLower(a.Ref)
			byAnchor[key] = append(byAnchor[key], i)
		}
	}

	keys := make([]string, 0, len(byAnchor))
	for k := range byAnchor {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tokens := 0
	var out []sqlite.Folded
	seen := map[string]bool{}
	for _, k := range keys {
		idxs := byAnchor[k]
		if len(idxs) < 2 {
			continue
		}
		sig := signature(idxs)
		if seen[sig] {
			continue // a second anchor over the same rows teaches the same lesson
		}
		seen[sig] = true

		var b strings.Builder
		b.WriteString("State in one sentence the general lesson these related notes teach. If they teach no lesson beyond themselves, reply with nothing.\n\n")
		for _, i := range idxs {
			b.WriteString("- ")
			b.WriteString(merged[i].Memory.Body)
			b.WriteString("\n")
		}
		text, spent, err := c.sum.Summarize(ctx, b.String())
		tokens += spent
		if err != nil {
			return out, tokens, err
		}
		lesson := strings.TrimSpace(text)
		if lesson == "" {
			continue
		}
		out = append(out, sqlite.Folded{
			Memory: sqlite.Memory{
				Namespace: ns, Kind: sqlite.KindReflection,
				Label: label(lesson), Body: lesson, Importance: reflectionImportance,
				TTLClass: sqlite.TTLNormal,
			},
			Anchors:  unionFoldedAnchors(merged, idxs),
			Evidence: idxs,
		})
	}
	return out, tokens, nil
}

// clusterObservations groups observation candidates that say the same thing.
// Two candidates join a group when their bodies overlap past mergeJaccard,
// unioned transitively so a chain of near-duplicates lands in one group.
// Overlap is measured on wording, not anchor: two distinct notes about the
// same file are not duplicates, they are separate facts that a later reflection
// may tie together, so a shared anchor never merges them here.  Groups come
// back in a stable order (by first member) so a fold is deterministic given
// its input.
func clusterObservations(obs []sqlite.Candidate) [][]int {
	n := len(obs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }

	words := make([]map[string]bool, n)
	for i := range obs {
		words[i] = wordSet(obs[i].Body)
	}
	for i := range n {
		for j := i + 1; j < n; j++ {
			if jaccard(words[i], words[j]) >= mergeJaccard {
				union(i, j)
			}
		}
	}

	groups := map[int][]int{}
	for i := range n {
		r := find(i)
		groups[r] = append(groups[r], i)
	}
	roots := make([]int, 0, len(groups))
	for r := range groups {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(a, b int) bool { return groups[roots[a]][0] < groups[roots[b]][0] })
	out := make([][]int, 0, len(groups))
	for _, r := range roots {
		out = append(out, groups[r])
	}
	return out
}

// unionAnchors collects the distinct anchors across a group of candidates,
// preserving first-seen order so the merged row's anchors are deterministic.
func unionAnchors(obs []sqlite.Candidate, group []int) []sqlite.Anchor {
	seen := map[string]bool{}
	var out []sqlite.Anchor
	for _, i := range group {
		for _, a := range obs[i].Anchors {
			k := a.Kind + ":" + strings.ToLower(a.Ref)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, a)
		}
	}
	return out
}

// unionFoldedAnchors is unionAnchors over already-folded rows, for the anchors
// a reflection inherits from the observations it rests on.
func unionFoldedAnchors(merged []sqlite.Folded, idxs []int) []sqlite.Anchor {
	seen := map[string]bool{}
	var out []sqlite.Anchor
	for _, i := range idxs {
		for _, a := range merged[i].Anchors {
			k := a.Kind + ":" + strings.ToLower(a.Ref)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, a)
		}
	}
	return out
}

// wordSet lowercases a body and returns its set of words of three letters or
// more, the tokens the Jaccard overlap is measured on. Short words carry little
// signal and are dropped so "the" and "a" do not merge unrelated notes.
func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	notWord := func(r rune) bool {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		return !isWord
	}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), notWord) {
		if len(f) >= 3 {
			set[f] = true
		}
	}
	return set
}

// jaccard is the overlap of two word sets, the intersection over the union,
// zero when either is empty so an empty body never merges with anything.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// signature is a stable key for a set of row indices, so two anchors covering
// the same rows do not each spawn the same reflection.
func signature(idxs []int) string {
	s := append([]int(nil), idxs...)
	sort.Ints(s)
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ",")
}

// label is a short title for a memory, the first line trimmed to a readable
// length, so a row has a handle without the model authoring one.
func label(body string) string {
	line := body
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	const max = 72
	if len(line) > max {
		line = strings.TrimSpace(line[:max])
	}
	return line
}
