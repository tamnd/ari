package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitRepo is the fold's working-tree window backed by real git and the
// filesystem. It answers the three questions the invalidation pass asks: where
// is Head, what changed since a commit, and does a file still hash to what its
// anchor stored. Every call is bounded by a short timeout so a slow or wedged
// git never stalls a fold, and a failed call reports ok=false so the pass leaves
// the row alone rather than demoting on a git error.
type gitRepo struct {
	root string
}

// newGitRepo returns a repo rooted at the project root, or nil when there is no
// git working tree, in which case the consolidator runs without an invalidation
// pass and memories evaporate only on their ttl clock.
func newGitRepo(root string) *gitRepo {
	if root == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	return &gitRepo{root: root}
}

func (g *gitRepo) git(args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", g.root}, args...)...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// Head is the current commit hash.
func (g *gitRepo) Head() (string, bool) {
	return g.git("rev-parse", "HEAD")
}

// ChangedSince lists the files touched between commit and HEAD, both committed
// diffs and the uncommitted working-tree state, so a memory demotes the moment
// its file is edited, not only once the edit is committed.
func (g *gitRepo) ChangedSince(commit string) (map[string]bool, bool) {
	committed, ok := g.git("diff", "--name-only", commit, "HEAD")
	if !ok {
		return nil, false // commit not in history, fall back to the hash check
	}
	working, _ := g.git("diff", "--name-only", "HEAD")
	files := map[string]bool{}
	for _, block := range []string{committed, working} {
		for line := range strings.SplitSeq(block, "\n") {
			if p := strings.TrimSpace(line); p != "" {
				files[p] = true
			}
		}
	}
	return files, true
}

// HashFile is the sha256 of a path under the root now, matching the hash a
// memory anchor stored at write time. ok is false when the file is gone, which
// the pass reads as the anchor no longer holding.
func (g *gitRepo) HashFile(path string) (string, bool) {
	f, err := os.Open(filepath.Join(g.root, path))
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, bufio.NewReader(f)); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}
