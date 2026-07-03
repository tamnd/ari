package markdown

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
)

// StreamCache renders a growing markdown string cheaply by caching the
// render of a provably-stable prefix and re-rendering only the volatile
// tail on each call (doc 02 section 6). Exactly one lives at a time, for
// the one message currently streaming; when the part freezes its final
// text goes through Render and the cache is discarded.
type StreamCache struct {
	mu       sync.Mutex // glamour's renderer is stateful (section 6.4)
	width    int
	cfg      ansi.StyleConfig
	renderer *glamour.TermRenderer

	prefix   string // source[:boundary], for non-append detection
	boundary int
	rendered string // normalized render of the stable prefix
}

// NewStreamCache builds a cache rendering at the given width and style.
func NewStreamCache(width int, cfg ansi.StyleConfig) *StreamCache {
	c := &StreamCache{cfg: cfg}
	c.setWidth(width)
	return c
}

// SetWidth changes the render width. Every wrap boundary moves, so the
// whole cache resets: the full-render fallback (section 6.3).
func (c *StreamCache) SetWidth(width int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if width != c.width {
		c.setWidth(width)
	}
}

func (c *StreamCache) setWidth(width int) {
	c.width = width
	c.renderer = nil
	if r, err := glamour.NewTermRenderer(
		glamour.WithStyles(c.cfg), glamour.WithWordWrap(width)); err == nil {
		c.renderer = r
	}
	c.reset()
}

func (c *StreamCache) reset() {
	c.prefix, c.boundary, c.rendered = "", 0, ""
}

// Render returns the render of source, extending the stable prefix when
// the boundary detector can prove more of it settled. The result is
// byte-identical to Render(source, width, cfg): that equality is the
// oracle the tests assert at every prefix length (doc 02 section 17.2).
func (c *StreamCache) Render(source string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renderer == nil {
		return strings.TrimRight(source, "\n")
	}
	// A non-append change invalidates everything: streaming never does
	// this, resume might (section 6.3).
	if !strings.HasPrefix(source, c.prefix) {
		c.reset()
	}
	if b := findSafeBoundary(source, c.boundary); b > c.boundary {
		frag := renderWith(c.renderer, source[c.boundary:b])
		c.rendered = glue(c.rendered, frag)
		c.boundary = b
		c.prefix = source[:b]
	}
	tail := source[c.boundary:]
	if tailDefiesGlue(tail) {
		// Raw HTML at the head of the tail renders to nothing, and
		// glamour then fuses the neighbors without the usual blank
		// separator, so the glue would be wrong. Full render instead:
		// the fallback that keeps conservative correct (section 6.3).
		return renderWith(c.renderer, source)
	}
	return glue(c.rendered, renderWith(c.renderer, tail))
}

// tailDefiesGlue reports whether the volatile tail opens with a
// construct whose fragment render does not compose by blank-line glue:
// raw HTML, which glamour strips, or a definition-list ": def" line,
// which retroactively turns the paragraph above it into a term even
// across a blank line.
func tailDefiesGlue(tail string) bool {
	for line := range strings.SplitSeq(tail, "\n") {
		t := strings.Trim(line, " \t")
		if t == "" {
			continue
		}
		return t[0] == '<' || t[0] == ':'
	}
	return false
}

// glue joins two normalized fragment renders with the one blank line
// glamour puts between top-level blocks.
func glue(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n\n" + b
	}
}
