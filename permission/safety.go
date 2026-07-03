package permission

import (
	"path/filepath"
	"strings"
)

// The safety floor is stage 5. It is a pure function of the call's
// mutation targets and the resolved paths; it reads no rules and no
// mode, which is what makes it unbypassable. There is no setting, no
// flag, and no hook output that turns it off (doc 05 section 7).

// Paths are the resolved locations the safety floor protects, plus the
// workspace root the auto-edit mode scopes itself to.
type Paths struct {
	Root         string // workspace root, for in-tree checks
	Nest         string // the project nest, .ari/ under the root
	GlobalNest   string // the global nest, ~/.ari
	Home         string // the user's home, for shell rc files
	AriBinary    string // the running ari executable, symlinks resolved
	GlobalConfig string // the global config file
}

// Verdict is the safety floor's answer for one call.
type Verdict struct {
	Blocked bool
	Rule    string // which check fired: nest, vcs, shellrc, ari-self
	Path    string // the offending target
	Message string // the model-facing sentence
}

// checkTargets runs the floor over one call's mutation targets. The
// first protected target blocks; a call with no protected targets
// passes.
func (p Paths) checkTargets(targets []string) Verdict {
	for _, t := range targets {
		t = expandHome(t, p.Home)
		if within(t, p.Nest) || within(t, p.GlobalNest) {
			return blocked("nest", t, "writing into ari's nest is never allowed")
		}
		if withinVCSInternals(t) {
			return blocked("vcs", t, "editing VCS internals like .git is never allowed")
		}
		if isShellRC(t, p.Home) {
			return blocked("shellrc", t, "writing shell startup files is never allowed")
		}
		if (p.AriBinary != "" && t == p.AriBinary) || (p.GlobalConfig != "" && t == p.GlobalConfig) {
			return blocked("ari-self", t, "modifying the ari binary or its config is never allowed")
		}
	}
	return Verdict{}
}

func blocked(rule, path, message string) Verdict {
	return Verdict{Blocked: true, Rule: rule, Path: path, Message: message}
}

// within reports whether path is root or inside it. An empty root
// protects nothing.
func within(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// withinVCSInternals reports whether any segment of the path is a VCS
// state directory, so both .git/config and .git itself count.
func withinVCSInternals(path string) bool {
	for seg := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if seg == ".git" || seg == ".hg" || seg == ".svn" {
			return true
		}
	}
	return false
}

// shellRCNames are the startup files a write to which is persistence,
// not code editing (doc 05 section 7).
var shellRCNames = map[string]bool{
	".bashrc": true, ".zshrc": true, ".profile": true, ".zprofile": true,
	".bash_profile": true, ".bash_login": true, ".zshenv": true, ".kshrc": true,
}

// isShellRC reports whether the path is a shell startup file in the
// user's home directory.
func isShellRC(path, home string) bool {
	if home == "" || !shellRCNames[filepath.Base(path)] {
		return false
	}
	return filepath.Dir(path) == filepath.Clean(home)
}

// expandHome resolves a leading ~ so rm ~/.zshrc is seen for what it
// is.
func expandHome(path, home string) string {
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
