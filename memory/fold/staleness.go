package fold

import (
	"context"

	"github.com/tamnd/ari/memory/sqlite"
)

// Repo is the fold's read-only window onto the working tree, the source of
// truth for whether a file anchor still holds. It is an interface so the
// consolidator does not shell out itself and a test can drive the pass with a
// scripted tree. All three methods return ok=false when the answer is unknown
// (no repo, no such commit, unreadable file) so the pass can degrade to leaving
// a row alone rather than demoting on a failed git call.
type Repo interface {
	// Head is the current commit, the point the anchors are checked against.
	Head() (commit string, ok bool)
	// ChangedSince lists the paths touched between commit and Head, the set a
	// `git diff --name-only commit..HEAD` returns. ok is false when the commit is
	// not in history, in which case the pass falls back to the hash comparison.
	ChangedSince(commit string) (files map[string]bool, ok bool)
	// HashFile is the content hash of a path in the working tree now, compared
	// against the hash stored on the anchor. ok is false when the file is gone.
	HashFile(path string) (hash string, ok bool)
}

// Demotion records one row the invalidation pass moved, for the report and the
// tests. Stale reports the flag's new state, so a demote and a restore are told
// apart by their direction.
type Demotion struct {
	ID    string
	Stale bool
}

// demoteStale is the fold's invalidation pass. For every anchored live row in a
// namespace it asks whether the files under it moved: either the row's
// anchor_commit is behind Head and one of its files is in the changed set, or a
// file's content no longer hashes to what the anchor stored. A row whose files
// moved is marked stale (a reversible demotion recall weights at half); a stale
// row whose files match again has the flag cleared and its verified_at advanced
// to Head. The `git diff --name-only` for a given anchor_commit is computed once
// and cached, so the diff runs once per distinct commit in the fold, not once
// per row.
func (c *Consolidator) demoteStale(ctx context.Context, ns string) ([]Demotion, error) {
	if c.repo == nil {
		return nil, nil
	}
	head, ok := c.repo.Head()
	if !ok {
		return nil, nil // no working tree to check against, leave every row as is
	}
	rows, err := c.store.StaleRows(ctx, ns)
	if err != nil {
		return nil, err
	}

	changedCache := map[string]map[string]bool{}
	changedFor := func(commit string) map[string]bool {
		if commit == "" {
			return nil // no baseline commit, the hash comparison stands alone
		}
		if v, ok := changedCache[commit]; ok {
			return v
		}
		files, ok := c.repo.ChangedSince(commit)
		if !ok {
			files = nil
		}
		changedCache[commit] = files
		return files
	}

	var moved []Demotion
	for _, r := range rows {
		touched := c.anchorMoved(r, changedFor(r.AnchorCommit))
		switch {
		case touched && !r.Stale:
			if err := c.store.SetStale(ctx, r.ID, true, ""); err != nil {
				return moved, err
			}
			moved = append(moved, Demotion{ID: r.ID, Stale: true})
		case !touched && r.Stale:
			if err := c.store.SetStale(ctx, r.ID, false, head); err != nil {
				return moved, err
			}
			moved = append(moved, Demotion{ID: r.ID, Stale: false})
		}
	}
	return moved, nil
}

// anchorMoved reports whether any file anchor of a row no longer holds: it is in
// the commit's changed set, its content hash drifted, or the file is gone. A row
// with no file anchor never moves here; it lives on its ttl clock instead.
func (c *Consolidator) anchorMoved(r sqlite.StaleRow, changed map[string]bool) bool {
	for _, f := range r.Files {
		if changed[f.Ref] {
			return true
		}
		cur, ok := c.repo.HashFile(f.Ref)
		if !ok {
			return true // the file the memory anchored to is gone
		}
		if f.Hash != "" && cur != f.Hash {
			return true
		}
	}
	return false
}
