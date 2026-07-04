package skill

import (
	"fmt"
	"strings"
)

// frontmatter is the small YAML subset a SKILL.md or command file carries:
// scalar keys, block sequences (a key followed by "- item" lines), and
// inline flow sequences ("[a, b]"). It is deliberately not a general YAML
// parser. A skill body is instructions for a model, and the frontmatter is
// a handful of declared fields, so a hundred-line YAML engine and its
// dependency would be all cost and no benefit (doc 13 section 2.2). Unknown
// keys are preserved and ignored so a foreign skill still loads.
type frontmatter struct {
	scalars map[string]string
	lists   map[string][]string
}

func (f frontmatter) scalar(key string) string { return f.scalars[key] }

func (f frontmatter) list(key string) []string { return f.lists[key] }

// bool reads a scalar as a boolean, true only for the literal "true".
func (f frontmatter) bool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(f.scalars[key]), "true")
}

// splitFrontmatter separates the leading "---" fenced YAML block from the
// markdown body. A file with no frontmatter is all body, which is a valid
// command template: it just declares nothing. A frontmatter fence opened
// and never closed is a parse error, since silently treating the whole
// file as frontmatter would drop the instructions.
func splitFrontmatter(data string) (front, body string, err error) {
	s := strings.TrimPrefix(data, "\uFEFF") // tolerate a UTF-8 BOM
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", data, nil
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	for _, marker := range []string{"\n---\n", "\n---\r\n"} {
		if i := strings.Index(rest, marker); i >= 0 {
			return rest[:i], rest[i+len(marker):], nil
		}
	}
	if strings.HasSuffix(rest, "\n---") {
		return strings.TrimSuffix(rest, "\n---"), "", nil
	}
	return "", "", fmt.Errorf("frontmatter opened with --- but never closed")
}

// parseFrontmatter parses the YAML subset into scalars and lists. A key with
// an empty value followed by indented "- " lines is a block sequence; a key
// whose value is "[...]" is a flow sequence; anything else is a scalar.
func parseFrontmatter(front string) (frontmatter, error) {
	fm := frontmatter{scalars: map[string]string{}, lists: map[string][]string{}}
	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			return fm, fmt.Errorf("line %q is not a key: value pair", line)
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		switch {
		case val == "":
			// A block sequence follows on the indented "- " lines, if any.
			items := gatherBlockItems(lines, &i)
			if len(items) > 0 {
				fm.lists[key] = items
			} else {
				fm.scalars[key] = ""
			}
		case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
			fm.lists[key] = parseFlowList(val)
		default:
			fm.scalars[key] = unquote(val)
		}
	}
	return fm, nil
}

// gatherBlockItems consumes the "- item" lines that follow a bare key,
// advancing the line index past them.
func gatherBlockItems(lines []string, i *int) []string {
	var items []string
	for *i+1 < len(lines) {
		next := strings.TrimSpace(strings.TrimRight(lines[*i+1], "\r"))
		if !strings.HasPrefix(next, "- ") && next != "-" {
			break
		}
		*i++
		items = append(items, unquote(strings.TrimSpace(strings.TrimPrefix(next, "-"))))
	}
	return items
}

// parseFlowList splits an inline "[a, b, c]" sequence.
func parseFlowList(val string) []string {
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(inner, ",") {
		if p := unquote(strings.TrimSpace(part)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// unquote strips one layer of matching single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
