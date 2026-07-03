// Package ui is the root of the terminal client: one tea.Model that
// coordinates controllers, routes input by scope, and composites every
// widget onto one cell buffer (doc 02 sections 11 and 18). Behavior
// lives in the controllers; the root owns size, state, focus, the
// overlay, and the theme, and nothing else.
package ui

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/sidebar"
)

// Layout is the full set of screen regions for a frame. It is
// comparable, so the draw path recomputes it every frame and resizes
// children only when the new layout differs from the last.
type Layout struct {
	Area    uv.Rectangle
	Header  uv.Rectangle // compact mode's one-line stand-in for the sidebar
	Sidebar uv.Rectangle
	Main    uv.Rectangle // the chat list
	Pills   uv.Rectangle // todos/queue/subagent tabs; zero height until M3
	Editor  uv.Rectangle
	Status  uv.Rectangle // help, status, and notifications share this line
	Compact bool
}

// Compact breakpoint and editor growth bounds (doc 02 sections 11.2
// and 11.3).
const (
	compactWidth  = 120
	compactHeight = 30
	editorMin     = 3
	editorMax     = 15
)

// ComputeLayout is pure: same inputs, same Layout. editorLines is the
// editor's content height; it grows the editor region between the
// bounds, and past the cap the textarea scrolls internally. forceCompact
// lets the user pin compact mode regardless of size.
func ComputeLayout(w, h int, editorLines int, forceCompact bool) Layout {
	l := Layout{
		Area:    uv.Rect(0, 0, w, h),
		Compact: forceCompact || w < compactWidth || h < compactHeight,
	}

	editorH := min(max(editorLines, editorMin), editorMax)
	// Never let the editor and chrome squeeze the chat below one line.
	if over := editorH + 3 - (h - 1); over > 0 {
		editorH = max(editorH-over, 1)
	}

	top := 0
	if l.Compact {
		l.Header = uv.Rect(0, 0, w, 1)
		top = 1
	}

	statusY := h - 1
	editorY := statusY - editorH
	l.Status = uv.Rect(0, statusY, w, 1)
	l.Editor = uv.Rect(0, max(editorY, top), w, min(editorH, statusY-top))
	l.Pills = uv.Rect(0, l.Editor.Min.Y, w, 0) // grows when M3 has tabs to show

	mainH := max(l.Editor.Min.Y-top, 0)
	if l.Compact {
		l.Main = uv.Rect(0, top, w, mainH)
	} else {
		sw := min(sidebar.Width, w/3)
		l.Main = uv.Rect(0, top, w-sw, mainH)
		l.Sidebar = uv.Rect(w-sw, top, sw, mainH)
	}
	return l
}
