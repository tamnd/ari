// Package nest owns ari's data directories: the global nest at ~/.ari and
// the project nest at .ari/ in the repo. It is the only package that
// knows the layout; nothing else hardcodes a path, which is what lets a
// test point the whole nest at a temp dir.
package nest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Nest is the resolved pair of data directories for one run.
type Nest struct {
	// Global is the per-user nest, ~/.ari unless ARI_HOME overrides it.
	Global string
	// Root is the project root: the git repo root when there is one,
	// otherwise the working directory the run started in.
	Root string
	// Key is the sanitized project key that names this project's state
	// under Global/projects.
	Key string
}

// Resolve computes the nest for cwd. The project root is the enclosing
// git repo root, or cwd itself outside a repo; worktrees of one repo
// share a key because the key derives from the repo root path.
func Resolve(cwd string) (Nest, error) {
	global := os.Getenv("ARI_HOME")
	if global == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Nest{}, err
		}
		global = filepath.Join(home, ".ari")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return Nest{}, err
	}
	root := gitRoot(abs)
	if root == "" {
		root = abs
	}
	return Nest{Global: global, Root: root, Key: Key(root)}, nil
}

// gitRoot walks up from dir looking for a .git entry (a directory in a
// normal clone, a file in a worktree; a worktree's .git file still means
// the walk stops here, and Key uses the main repo root only when the
// worktree shares it, so a plain worktree gets its own key by path).
func gitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Key sanitizes a project root path into a directory name: every
// non-alphanumeric byte becomes a dash, and a path too long to be a
// comfortable directory name is truncated with a hash suffix so distinct
// paths never collide.
func Key(root string) string {
	var b strings.Builder
	for _, r := range root {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	key := b.String()
	const maxLen = 100
	if len(key) > maxLen {
		sum := sha256.Sum256([]byte(root))
		key = key[:maxLen-9] + "-" + hex.EncodeToString(sum[:4])
	}
	return key
}

// Global-nest paths.

// GlobalConfig is ~/.ari/config.toml.
func (n Nest) GlobalConfig() string { return filepath.Join(n.Global, "config.toml") }

// AuthDir holds provider credentials, 0600, never in model context.
func (n Nest) AuthDir() string { return filepath.Join(n.Global, "auth") }

// TrustFile holds the per-workspace hook trust decisions, keyed by workspace
// root path. It lives in the global nest so trust is remembered across
// sessions and is never committed into a repo (doc 05 section 12, D16).
func (n Nest) TrustFile() string { return filepath.Join(n.Global, "trust.json") }

// ProjectStateDir is this project's per-user state under the global nest:
// sessions, the colony database, the journal. It lives here and not in
// the repo so nothing tempts anyone to commit it.
func (n Nest) ProjectStateDir() string {
	return filepath.Join(n.Global, "projects", n.Key)
}

// SessionsDir holds the JSONL transcripts for this project.
func (n Nest) SessionsDir() string { return filepath.Join(n.ProjectStateDir(), "sessions") }

// ColonyDB is the one SQLite file for this project (M2 creates it).
func (n Nest) ColonyDB() string { return filepath.Join(n.ProjectStateDir(), "colony.db") }

// JournalDir holds the append-only event log.
func (n Nest) JournalDir() string { return filepath.Join(n.ProjectStateDir(), "journal") }

// Project-nest paths: the small, committable part.

// ProjectDir is <root>/.ari.
func (n Nest) ProjectDir() string { return filepath.Join(n.Root, ".ari") }

// ProjectConfig is <root>/.ari/config.toml, committable.
func (n Nest) ProjectConfig() string { return filepath.Join(n.ProjectDir(), "config.toml") }

// LocalConfig is <root>/.ari/local.toml, gitignored, per-user overrides.
func (n Nest) LocalConfig() string { return filepath.Join(n.ProjectDir(), "local.toml") }

// AntsDir is <root>/.ari/ants, where each ant keeps its card.json and
// SKILL.md. It sits in the committable project nest, not the state dir,
// because a card is a git artifact a human reads and edits (doc 06 2.3).
func (n Nest) AntsDir() string { return filepath.Join(n.ProjectDir(), "ants") }

// ARIMD is <root>/ARI.md, the project memory (D21).
func (n Nest) ARIMD() string { return filepath.Join(n.Root, "ARI.md") }

// EnsureGlobal creates the global-nest directories with tight modes. The
// auth directory is 0700 because it holds credential references (D16).
func (n Nest) EnsureGlobal() error {
	if err := os.MkdirAll(n.Global, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(n.AuthDir(), 0o700); err != nil {
		return err
	}
	return os.MkdirAll(n.SessionsDir(), 0o755)
}
