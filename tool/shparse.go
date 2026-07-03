package tool

import (
	"strings"
)

// This file owns turning a command line into the normalized subcommands
// the permission pipeline matches rules against (doc 04 section 7.7).
// This is where a bypass would live, so every strip runs to a fixed
// point and a failed parse reports the conservative answer.

// splitCompound splits a command line on ;, &&, ||, |, and newlines into
// its component commands, respecting single quotes, double quotes, and
// backslash escapes. A rule that allows ls must not allow ls && rm.
func splitCompound(command string) []string {
	var parts []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			parts = append(parts, s)
		}
		cur.Reset()
	}
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case escaped:
			cur.WriteByte(c)
			escaped = false
		case c == '\\' && !inSingle:
			cur.WriteByte(c)
			escaped = true
		case c == '\'' && !inDouble:
			cur.WriteByte(c)
			inSingle = !inSingle
		case c == '"' && !inSingle:
			cur.WriteByte(c)
			inDouble = !inDouble
		case inSingle || inDouble:
			cur.WriteByte(c)
		case c == ';' || c == '\n':
			flush()
		case c == '&' && i+1 < len(command) && command[i+1] == '&':
			flush()
			i++
		case c == '|':
			if i+1 < len(command) && command[i+1] == '|' {
				i++
			}
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return parts
}

// shWords splits one subcommand into words, respecting quotes. Quotes
// are kept on the word so reconstruction is faithful; classification
// only looks at unquoted leading words anyway.
func shWords(sub string) []string {
	var words []string
	var cur strings.Builder
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(sub); i++ {
		c := sub[i]
		switch {
		case escaped:
			cur.WriteByte(c)
			escaped = false
		case c == '\\' && !inSingle:
			cur.WriteByte(c)
			escaped = true
		case c == '\'' && !inDouble:
			cur.WriteByte(c)
			inSingle = !inSingle
		case c == '"' && !inSingle:
			cur.WriteByte(c)
			inDouble = !inDouble
		case (c == ' ' || c == '\t') && !inSingle && !inDouble:
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// normalizeSubcommand strips wrappers, leading environment assignments,
// and redirections to a fixed point, so FOO=bar nohup git push matches
// the same rules git push does and nothing smuggles past on a prefix.
func normalizeSubcommand(sub string) string {
	words := shWords(sub)
	for {
		before := len(words)
		words = stripLeadingEnv(words)
		words = stripWrappers(words)
		if len(words) == before {
			break
		}
	}
	words = stripRedirections(words)
	return strings.Join(words, " ")
}

// stripLeadingEnv drops leading NAME=value assignments. Without this,
// FOO=bar rm -rf / sails past a rm deny because the first token is an
// assignment (doc 04 section 7.7).
func stripLeadingEnv(words []string) []string {
	for len(words) > 0 && isEnvAssignment(words[0]) {
		words = words[1:]
	}
	return words
}

func isEnvAssignment(word string) bool {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	name := word[:eq]
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// stripWrappers drops leading wrappers that do not change what runs:
// nohup, time, nice (with its -n level), timeout (with its duration and
// flags), and a bare env (its assignments fall to stripLeadingEnv).
func stripWrappers(words []string) []string {
	if len(words) == 0 {
		return words
	}
	switch words[0] {
	case "nohup", "time", "env":
		return words[1:]
	case "nice":
		rest := words[1:]
		if len(rest) > 0 && rest[0] == "-n" && len(rest) > 1 {
			return rest[2:]
		}
		if len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			return rest[1:]
		}
		return rest
	case "timeout":
		rest := words[1:]
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			if rest[0] == "-k" || rest[0] == "--kill-after" || rest[0] == "-s" || rest[0] == "--signal" {
				rest = rest[min(2, len(rest)):]
				continue
			}
			rest = rest[1:]
		}
		if len(rest) > 0 {
			return rest[1:] // the duration
		}
		return rest
	}
	return words
}

// stripRedirections drops >, >>, <, 2>&1 and friends, with or without a
// separate target word, so echo hi > /etc/passwd matches as echo hi and
// the write shows up in the permission consequence, not past it.
func stripRedirections(words []string) []string {
	var out []string
	skipNext := false
	for _, w := range words {
		if skipNext {
			skipNext = false
			continue
		}
		trimmed := strings.TrimLeft(w, "0123456789")
		switch {
		case trimmed == ">" || trimmed == ">>" || trimmed == "<" || trimmed == "<<" || trimmed == "<<<":
			skipNext = true
		case strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "<"):
			// >file, >>file, 2>&1: the target is attached, drop the word.
		default:
			out = append(out, w)
		}
	}
	return out
}

// shPrefixMatcher tests a permission pattern against a command by
// normalizing each subcommand, then prefix-matching at word boundaries.
// A prefix or wildcard rule never matches a compound command as a unit,
// only its parts, which is what stops chaining from widening a rule.
type shPrefixMatcher struct {
	subcommands []string
}

func newShMatcher(command string) shPrefixMatcher {
	var subs []string
	for _, part := range splitCompound(command) {
		subs = append(subs, normalizeSubcommand(part))
	}
	return shPrefixMatcher{subcommands: subs}
}

// Matches reports whether every subcommand is covered by the pattern.
// An empty command matches nothing, the conservative direction.
func (m shPrefixMatcher) Matches(p Pattern) bool {
	if len(m.subcommands) == 0 {
		return false
	}
	for _, sub := range m.subcommands {
		if !p.CoversSubcommand(sub) {
			return false
		}
	}
	return true
}

// shReadOnlyCommands are commands that observe without mutating. A
// command not in this set is not read-only, the conservative default.
var shReadOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "rg": true, "find": true, "file": true, "stat": true,
	"pwd": true, "echo": true, "printf": true, "which": true, "type": true,
	"du": true, "df": true, "ps": true, "date": true, "whoami": true,
	"uname": true, "hostname": true, "id": true, "env": true, "printenv": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"sort": true, "uniq": true, "cut": true, "tr": true, "diff": true,
	"cmp": true, "md5": true, "md5sum": true, "shasum": true, "sha256sum": true,
	"true": true, "false": true, "test": true, "sleep": true,
}

// shReadOnlySubcommands are two-word commands that observe without
// mutating; the bare first word alone is not enough to decide.
var shReadOnlySubcommands = map[string]bool{
	"git status": true, "git log": true, "git diff": true, "git show": true,
	"git branch": true, "git remote": true, "git blame": true, "git describe": true,
	"git rev-parse": true, "git ls-files": true, "git shortlog": true, "git tag": true,
	"go version": true, "go env": true, "go list": true, "go vet": true, "go doc": true,
}

// shDestructiveCommands are known irreversible operations; the
// permission renderer escalates what it shows for these (doc 05).
var shDestructiveCommands = map[string]bool{
	"rm": true, "rmdir": true, "dd": true, "mkfs": true, "shred": true,
	"truncate": true, "killall": true, "shutdown": true, "reboot": true,
}

// shIsReadOnly reports whether every subcommand of the command line is
// known read-only. An empty or unparseable command is not read-only.
func shIsReadOnly(command string) bool {
	subs := splitCompound(command)
	if len(subs) == 0 {
		return false
	}
	for _, sub := range subs {
		words := shWords(normalizeSubcommand(sub))
		if len(words) == 0 {
			return false
		}
		if shReadOnlyCommands[words[0]] {
			continue
		}
		if len(words) > 1 && shReadOnlySubcommands[words[0]+" "+words[1]] {
			continue
		}
		return false
	}
	return true
}

// shIsDestructive reports whether any subcommand is a known
// irreversible operation.
func shIsDestructive(command string) bool {
	for _, sub := range splitCompound(command) {
		words := shWords(normalizeSubcommand(sub))
		if len(words) == 0 {
			continue
		}
		if shDestructiveCommands[words[0]] {
			return true
		}
		if words[0] == "git" && len(words) > 1 {
			rest := strings.Join(words[1:], " ")
			if strings.HasPrefix(rest, "push") && (strings.Contains(rest, "--force") || strings.Contains(rest, "-f")) {
				return true
			}
			if strings.HasPrefix(rest, "reset --hard") || strings.HasPrefix(rest, "clean") {
				return true
			}
		}
	}
	return false
}
