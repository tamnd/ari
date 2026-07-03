package tool

import (
	"path/filepath"
	"strings"
)

// This file is the exported face of the command parser for the
// permission pipeline (doc 05 section 5). The pipeline evaluates a
// compound command per subcommand, so it needs the split, the per-sub
// classification, and a conservative account of what each subcommand
// could write.

// ShSub is one subcommand of a compound command line. Raw keeps the
// text as written, redirections included, because the safety floor
// cares about a > target the rule matcher strips. Norm is the
// wrapper-stripped, env-stripped, redirection-stripped form the rule
// language matches.
type ShSub struct {
	Raw  string
	Norm string
}

// ShSplit splits a command line into its subcommands, each carrying
// its raw and normalized form. An empty or unparseable command returns
// nil, and the callers treat nil as "matches nothing".
func ShSplit(command string) []ShSub {
	var subs []ShSub
	for _, part := range splitCompound(command) {
		subs = append(subs, ShSub{Raw: part, Norm: normalizeSubcommand(part)})
	}
	return subs
}

// ReadOnly reports whether this one subcommand is known to only
// observe. A redirection makes any subcommand a writer, so the raw
// form is checked for one before the normalized form is classified.
func (s ShSub) ReadOnly() bool {
	if len(redirectionTargets(shWords(s.Raw))) > 0 {
		return false
	}
	return shIsReadOnly(s.Norm)
}

// MutationTargets returns the paths this subcommand could write,
// resolved against cwd, computed conservatively: redirection targets
// always count, and for a subcommand that is not known read-only every
// non-flag argument counts as a candidate. A harmless word resolves to
// a harmless path; the safety floor only acts on the candidates that
// land inside a protected area (doc 05 section 7).
func (s ShSub) MutationTargets(cwd string) []string {
	rawWords := shWords(s.Raw)
	candidates := redirectionTargets(rawWords)
	if !shIsReadOnly(s.Norm) {
		words := shWords(s.Norm)
		if len(words) > 1 {
			candidates = append(candidates, words[1:]...)
		}
	}
	var targets []string
	for _, c := range candidates {
		c = unquote(c)
		if c == "" || strings.HasPrefix(c, "-") {
			continue
		}
		if !filepath.IsAbs(c) && !strings.HasPrefix(c, "~/") && c != "~" {
			c = filepath.Join(cwd, c)
		}
		targets = append(targets, filepath.Clean(c))
	}
	return targets
}

// redirectionTargets pulls the output-redirection targets out of a raw
// word list: the word after a bare > or >>, and the attached form
// >file. Input redirections read, so they are not targets.
func redirectionTargets(words []string) []string {
	var targets []string
	next := false
	for _, w := range words {
		trimmed := strings.TrimLeft(w, "0123456789")
		switch {
		case next:
			targets = append(targets, w)
			next = false
		case trimmed == ">" || trimmed == ">>":
			next = true
		case strings.HasPrefix(trimmed, ">"):
			t := strings.TrimLeft(trimmed, ">")
			if t != "" && !strings.HasPrefix(t, "&") {
				targets = append(targets, t)
			}
		}
	}
	return targets
}

// unquote strips one layer of surrounding quotes so a target written
// as ">'file'" resolves as file.
func unquote(w string) string {
	if len(w) >= 2 {
		if (w[0] == '\'' && w[len(w)-1] == '\'') || (w[0] == '"' && w[len(w)-1] == '"') {
			return w[1 : len(w)-1]
		}
	}
	return w
}

// ResolveMutationPath resolves a write target the way the write tool
// does: symlinks resolved through the nearest existing ancestor, so a
// symlink pointing into a protected area cannot hide the area from the
// safety floor.
func ResolveMutationPath(path string) string {
	resolved, err := resolveForWrite(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}
