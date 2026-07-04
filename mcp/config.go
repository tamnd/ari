// Package mcp is the Model Context Protocol client bridge (doc 13, D20).
// It is a bridge, not a core dependency: a configured server's tools are
// available to the model, but they are deferred behind a search-and-load
// step so turn one never carries their schemas, and every MCP tool call
// still runs through the permission pipeline like any other. MCP output is
// untrusted content and its text never drives shell injection (D20).
package mcp

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// ServerSpec is one configured MCP server. Only the stdio transport is
// proven in M1; the transport field is a discriminator so sse and http are
// a later adapter (N4) rather than a schema change.
type ServerSpec struct {
	Transport string            `toml:"transport"`
	Command   string            `toml:"command"`
	Args      []string          `toml:"args"`
	Env       map[string]string `toml:"env"`
}

// file is the on-disk shape of one mcp.toml layer.
type file struct {
	Servers map[string]ServerSpec `toml:"servers"`
	Deny    []string              `toml:"deny"`
}

// Config is the merged MCP configuration for a session: the servers to
// connect to and the denylist that hides a server or a tool no matter which
// layer allowed it.
type Config struct {
	// Servers is keyed by the short server name that prefixes its tools,
	// so a server named "sqlite" exposes "sqlite__query".
	Servers map[string]ServerSpec
	// Deny is the union of every layer's deny patterns. A pattern is a tool
	// name, exact ("sqlite__query") or a server glob ("sqlite__*"), and a
	// denied tool is never registered, so the model never sees it. The
	// union is the never-lose-a-deny discipline the permission pipeline
	// uses for its own rules (doc 05 section 3): a higher layer cannot
	// un-deny what a lower layer denied.
	Deny []string
}

// Options names the roots the discovery walk consults.
type Options struct {
	// Root is the project root; the walk stops here.
	Root string
	// Cwd is the working directory; the walk starts here and climbs to Root,
	// so a nested .ari/mcp.toml shadows an outer one.
	Cwd string
	// GlobalDir is the user's global ari directory, the lowest-precedence
	// layer, consulted before any project layer.
	GlobalDir string
}

// Discover loads and merges mcp.toml across the global layer and the
// project chain from Root down to Cwd. A missing file is not an error; a
// malformed one is, so a typo surfaces instead of silently dropping a
// server. Later layers override a server of the same name and add to the
// denylist; they can never shrink it.
func Discover(opts Options) (Config, error) {
	cfg := Config{Servers: map[string]ServerSpec{}}

	var paths []string
	if opts.GlobalDir != "" {
		paths = append(paths, filepath.Join(opts.GlobalDir, "mcp.toml"))
	}
	for _, dir := range dirsRootToCwd(opts.Root, opts.Cwd) {
		paths = append(paths, filepath.Join(dir, ".ari", "mcp.toml"))
	}

	seenDeny := map[string]bool{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Config{}, fmt.Errorf("reading %s: %w", p, err)
		}
		var f file
		if _, err := toml.Decode(string(data), &f); err != nil {
			return Config{}, fmt.Errorf("parsing %s: %w", p, err)
		}
		maps.Copy(cfg.Servers, f.Servers)
		for _, d := range f.Deny {
			if !seenDeny[d] {
				seenDeny[d] = true
				cfg.Deny = append(cfg.Deny, d)
			}
		}
	}
	sort.Strings(cfg.Deny)
	return cfg, nil
}

// Denied reports whether a namespaced tool name is denied by any layer's
// denylist. A pattern is either the exact tool name or a server glob with a
// trailing "*", so "sqlite__*" denies every tool from the sqlite server.
func (c Config) Denied(toolName string) bool {
	for _, pat := range c.Deny {
		if prefix, ok := cutStar(pat); ok {
			if len(toolName) >= len(prefix) && toolName[:len(prefix)] == prefix {
				return true
			}
			continue
		}
		if pat == toolName {
			return true
		}
	}
	return false
}

// cutStar splits a trailing "*" glob into its literal prefix.
func cutStar(pat string) (string, bool) {
	if pat != "" && pat[len(pat)-1] == '*' {
		return pat[:len(pat)-1], true
	}
	return "", false
}

// dirsRootToCwd returns the directories from Root down to Cwd inclusive, so
// the caller reads outer layers before inner ones and a nested config
// shadows its parent. It mirrors the skill and memory discovery walk so
// every .ari surface climbs the tree the same way.
func dirsRootToCwd(root, cwd string) []string {
	if root == "" {
		if cwd == "" {
			return nil
		}
		return []string{filepath.Clean(cwd)}
	}
	root = filepath.Clean(root)
	if cwd == "" {
		return []string{root}
	}
	cwd = filepath.Clean(cwd)
	if root == cwd || !within(root, cwd) {
		return []string{root}
	}
	var up []string
	for d := cwd; ; {
		up = append(up, d)
		if d == root {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up
}

// within reports whether p is root or a descendant of it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !hasDotDotPrefix(rel))
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}
