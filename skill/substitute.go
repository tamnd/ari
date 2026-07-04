package skill

import (
	"strings"
	"unicode"
)

// Substitute expands the argument placeholders in a command or skill body.
// It supports $ARGUMENTS (the whole argument string), $1 $2 ... (positional,
// whitespace-split), and $name (a declared named argument). A placeholder
// with no matching argument expands to empty. When the body carries no
// placeholder at all and arguments were passed, they are appended as a
// trailing "ARGUMENTS:" line, so a template that forgot to reference them
// still receives them rather than dropping them silently (doc 13 section 4).
func Substitute(body, args string, named map[string]string) string {
	positional := strings.Fields(args)
	out, used := expand(body, args, positional, named)
	if !used && strings.TrimSpace(args) != "" {
		trimmed := strings.TrimRight(out, "\n")
		return trimmed + "\n\nARGUMENTS: " + args + "\n"
	}
	return out
}

// expand walks the body once, replacing every $-placeholder it recognizes and
// reporting whether any placeholder consumed an argument. A literal "$$" is an
// escaped dollar and passes through as a single "$".
func expand(body, all string, positional []string, named map[string]string) (string, bool) {
	var b strings.Builder
	used := false
	for i := 0; i < len(body); {
		c := body[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(body) && body[i+1] == '$' {
			b.WriteByte('$') // "$$" escapes to a single dollar
			i += 2
			continue
		}
		name, next := readPlaceholder(body, i+1)
		if name == "" {
			b.WriteByte('$') // a lone dollar is literal
			i++
			continue
		}
		val, hit := resolve(name, all, positional, named)
		b.WriteString(val)
		if hit && strings.TrimSpace(val) != "" {
			used = true
		}
		if name == "ARGUMENTS" {
			used = true // referencing $ARGUMENTS counts even when empty
		}
		i = next
	}
	return b.String(), used
}

// readPlaceholder reads the identifier after a '$': ARGUMENTS, a run of
// digits, or a named-argument identifier. It returns the name and the index
// past it, or an empty name when the dollar is not a placeholder.
func readPlaceholder(body string, start int) (name string, next int) {
	if start >= len(body) {
		return "", start
	}
	c := body[start]
	switch {
	case c >= '0' && c <= '9':
		j := start
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		return body[start:j], j
	case isIdentStart(rune(c)):
		j := start
		for j < len(body) && isIdent(rune(body[j])) {
			j++
		}
		return body[start:j], j
	default:
		return "", start
	}
}

// resolve maps a placeholder name to its value: ARGUMENTS to the whole
// string, digits to the positional slot, anything else to the named map.
func resolve(name, all string, positional []string, named map[string]string) (string, bool) {
	if name == "ARGUMENTS" {
		return all, true
	}
	if name[0] >= '0' && name[0] <= '9' {
		idx := atoi(name)
		if idx >= 1 && idx <= len(positional) {
			return positional[idx-1], true
		}
		return "", false
	}
	if v, ok := named[name]; ok {
		return v, true
	}
	return "", false
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdent(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// atoi parses a non-negative decimal run, returning 0 on overflow rather than
// erroring: the run is already known to be digits, and an absurd index just
// misses the positional slice.
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
		if n > 1<<20 {
			return 1 << 20
		}
	}
	return n
}
