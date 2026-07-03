package markdown

import "strings"

// findSafeBoundary returns the largest byte offset b >= from such that
// source[:b] is provably stable: no markdown construct spanning into
// source[b:] can change how the prefix renders, and the prefix renders
// identically as a standalone fragment. It advances only past blank
// lines whose preceding block is provably closed, and the discipline is
// one-directional by design: a false "not safe" only costs performance,
// a false "safe" corrupts the render, so every ambiguous case resolves
// to not safe (doc 02 section 6.2).
//
// The caller guarantees the scanner state is clean at from (from is
// either 0 or a previous return value).
func findSafeBoundary(source string, from int) int {
	best := from
	s := scanState{}
	lineStart := from
	// blockClosed reports whether the block ending at the previous
	// non-blank line is provably closed and context-free.
	blockOK := true // an empty region is trivially closed
	sawBlank := false

	for lineStart < len(source) {
		nl := strings.IndexByte(source[lineStart:], '\n')
		if nl < 0 {
			break // an unterminated line is always volatile tail
		}
		line := source[lineStart : lineStart+nl]
		next := lineStart + nl + 1

		// Markdown's blank line is spaces and tabs only; \v or \f on a
		// line makes it paragraph content, not a seam.
		if strings.Trim(line, " \t") == "" {
			sawBlank = true
			// A blank line is a candidate seam: safe when nothing
			// multi-line is open and the block above it closed clean.
			if blockOK && !s.open() {
				best = next
			}
			lineStart = next
			continue
		}
		if sawBlank {
			// First line of a new block: it decides whether this
			// block can ever seal.
			blockOK = true
			sawBlank = false
		}
		blockOK = s.feed(line) && blockOK
		lineStart = next
	}
	return best
}

// scanState tracks the constructs that survive blank lines: fenced code
// blocks and the raw HTML blocks that run to an explicit closer.
type scanState struct {
	fence     string // the opening fence marker, "" when closed
	htmlUntil string // the closer a raw HTML block is waiting for
	hazard    bool   // a reference-link hazard poisons the whole rest
}

func (s *scanState) open() bool {
	return s.fence != "" || s.htmlUntil != "" || s.hazard
}

// feed consumes one non-blank line and reports whether it keeps the
// current block sealable: plain paragraph text, an ATX heading, or
// fence content are fine; anything that could be continued, retroacted
// on, or reinterpreted by later input is not.
func (s *scanState) feed(line string) bool {
	if s.htmlUntil != "" {
		if strings.Contains(strings.ToLower(line), s.htmlUntil) {
			s.htmlUntil = ""
		}
		return false // the whole HTML block region stays volatile
	}
	// A carriage return is a line ending to goldmark but not to this
	// scanner, so a line containing one is really several lines this
	// classifier never saw. Refuse to reason about it.
	if strings.IndexByte(line, '\r') >= 0 {
		return false
	}
	trimmed := strings.TrimLeft(line, " ")
	indent := len(line) - len(trimmed)

	// Fence open and close. Inside a fence every line is literal.
	if s.fence != "" {
		closer := strings.TrimRight(trimmed, " ")
		if indent <= 3 && strings.HasPrefix(closer, s.fence) &&
			strings.TrimRight(closer, string(s.fence[0])) == "" {
			s.fence = ""
		}
		return true // fence content is sealed once the fence closes
	}
	if indent <= 3 {
		if n := runLen(trimmed, '`'); n >= 3 {
			s.fence = strings.Repeat("`", n)
			return true
		}
		if n := runLen(trimmed, '~'); n >= 3 {
			s.fence = strings.Repeat("~", n)
			return true
		}
	}

	// Reference links and footnotes are the retroactive hazard: a
	// definition arriving in the tail can restyle a use already in the
	// prefix, and a definition in the prefix is invisible to a
	// fragment-rendered tail. One sighting poisons the rest, and it
	// must be checked on every non-fence line, including indented list
	// content, before any other reason to reject the line.
	if bracketHazard(trimmed) {
		s.hazard = true
		return false
	}

	// Indented lines: could be code continuation or list-item content.
	if indent > 0 || strings.HasPrefix(line, "\t") {
		return false
	}

	// Raw HTML blocks that ignore blank lines.
	lower := strings.ToLower(trimmed)
	for _, open := range [...][2]string{
		{"<script", "</script>"}, {"<pre", "</pre>"},
		{"<style", "</style>"}, {"<textarea", "</textarea>"},
		{"<!--", "-->"},
	} {
		if strings.HasPrefix(lower, open[0]) {
			if !strings.Contains(lower, open[1]) {
				s.htmlUntil = open[1]
			}
			return false
		}
	}
	if strings.HasPrefix(trimmed, "<") {
		return false // any other HTML block: not worth reasoning about
	}

	switch trimmed[0] {
	case '#':
		return true // ATX heading: single line, closed
	case '>', '|':
		return false // blockquote run, table row
	case ':':
		return false // definition-list body (goldmark extension)
	case '-', '*', '+':
		// A bare marker is a valid empty list item, so it opens a
		// list too.
		if len(trimmed) == 1 || trimmed[1] == ' ' || trimmed[1] == '\t' {
			return false // list item: loose lists continue past blanks
		}
	}
	if n := digitRun(trimmed); n > 0 {
		rest := trimmed[n:]
		if len(rest) > 0 && (rest[0] == '.' || rest[0] == ')') &&
			(len(rest) == 1 || rest[1] == ' ' || rest[1] == '\t') {
			return false // ordered list item, possibly empty
		}
	}
	return true // plain paragraph text
}

// bracketHazard reports whether a line contains bracket syntax that a
// later line could retroactively bind: anything but a complete inline
// link or image. Backtick code spans on the same line are skipped.
func bracketHazard(line string) bool {
	inCode := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '`':
			inCode = !inCode
		case '\\':
			i++ // an escaped bracket binds nothing
		case '[':
			if inCode {
				continue
			}
			close := strings.IndexByte(line[i:], ']')
			if close < 0 {
				return true // label may span lines: hazard
			}
			after := i + close + 1
			if after < len(line) && line[after] == '(' {
				paren := strings.IndexByte(line[after:], ')')
				if paren < 0 {
					return true
				}
				i = after + paren
				continue // complete inline link: safe
			}
			return true // shortcut, full reference, or definition
		}
	}
	return false
}

func runLen(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
