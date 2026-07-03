// Package theme is the semantic palette layer (doc 02 section 10, D18).
// A theme author fills in around thirty semantic colors; a pure expansion
// turns them into every component style the UI draws with. No widget
// constructs a color inline, which is what makes light and dark a
// property of the palette rather than a fork of the rendering code.
package theme

import (
	"fmt"
	"image/color"

	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
)

// Palette is the semantic color set a theme author fills in. Everything
// the UI draws derives from these colors; no component hardcodes one.
type Palette struct {
	// Brand and accent.
	Primary, Secondary, Accent, Keyword color.Color

	// Foreground ramp: from brightest text to faintest.
	FgBase, FgMuted, FgSubtle, FgFaint color.Color

	// Background ramp: base, raised panels, overlays, selection.
	BgBase, BgRaised, BgOverlay, BgSelection color.Color

	// Status.
	Success, Warning, Error, Info color.Color

	// Diff.
	DiffAdd, DiffDel, DiffAddEmph, DiffDelEmph color.Color

	// The ANSI-16 remap for embedded shell output.
	ANSI [16]color.Color

	// Dark reports whether this palette targets a dark terminal.
	Dark bool
}

// DiffStyle is the style set the diff renderer pulls from the theme
// (doc 02 section 9.2).
type DiffStyle struct {
	Add, Del         lipgloss.Style // whole-line backgrounds
	AddEmph, DelEmph lipgloss.Style // intra-line emphasis
	GutterAdd        lipgloss.Style
	GutterDel        lipgloss.Style
	LineNo           lipgloss.Style
	Context          lipgloss.Style
	Header           lipgloss.Style
}

// Styles is the expanded form of a palette: one field per component
// surface. Widgets read these; nothing else colors anything.
type Styles struct {
	// Prose.
	Base, Muted, Subtle, Faint lipgloss.Style

	// Chat surfaces.
	UserPrompt lipgloss.Style // the user's own text
	Reasoning  lipgloss.Style // model thinking, dim
	ToolName   lipgloss.Style
	ToolInput  lipgloss.Style
	ToolOutput lipgloss.Style
	ToolErr    lipgloss.Style

	// Status.
	Success, Warning, Error, Info lipgloss.Style

	// Chrome.
	Title    lipgloss.Style
	Border   lipgloss.Style
	Selected lipgloss.Style

	// Sub-system configs derived from the same palette (doc 02
	// section 10.4).
	Diff     DiffStyle
	Markdown ansi.StyleConfig // glamour
	Chroma   *chroma.Style    // syntax highlighting
}

// Theme bundles a palette, its expanded styles, and the per-ant accent
// assignment (inert until the colony exists, doc 02 section 10.5).
type Theme struct {
	Name    string
	P       Palette
	S       Styles
	accents *accentTable
}

// New builds a theme from a palette.
func New(name string, p Palette) Theme {
	return Theme{Name: name, P: p, S: Expand(p), accents: newAccentTable(p)}
}

// Expand turns a Palette into the full Styles used by every widget.
// Adding a theme is filling in a Palette; the expansion is shared, so
// themes cannot drift in how they color, say, a warning versus an error.
func Expand(p Palette) Styles {
	s := Styles{
		Base:   lipgloss.NewStyle().Foreground(p.FgBase),
		Muted:  lipgloss.NewStyle().Foreground(p.FgMuted),
		Subtle: lipgloss.NewStyle().Foreground(p.FgSubtle),
		Faint:  lipgloss.NewStyle().Foreground(p.FgFaint),

		UserPrompt: lipgloss.NewStyle().Foreground(p.FgBase).Bold(true),
		Reasoning:  lipgloss.NewStyle().Foreground(p.FgFaint).Italic(true),
		ToolName:   lipgloss.NewStyle().Foreground(p.Accent).Bold(true),
		ToolInput:  lipgloss.NewStyle().Foreground(p.FgMuted),
		ToolOutput: lipgloss.NewStyle().Foreground(p.FgSubtle),
		ToolErr:    lipgloss.NewStyle().Foreground(p.Error),

		Success: lipgloss.NewStyle().Foreground(p.Success),
		Warning: lipgloss.NewStyle().Foreground(p.Warning),
		Error:   lipgloss.NewStyle().Foreground(p.Error),
		Info:    lipgloss.NewStyle().Foreground(p.Info),

		Title:    lipgloss.NewStyle().Foreground(p.Primary).Bold(true),
		Border:   lipgloss.NewStyle().Foreground(p.FgFaint),
		Selected: lipgloss.NewStyle().Background(p.BgSelection).Foreground(p.FgBase),

		Diff: DiffStyle{
			Add:       lipgloss.NewStyle().Background(p.DiffAdd).Foreground(p.FgBase),
			Del:       lipgloss.NewStyle().Background(p.DiffDel).Foreground(p.FgBase),
			AddEmph:   lipgloss.NewStyle().Background(p.DiffAddEmph).Foreground(p.FgBase),
			DelEmph:   lipgloss.NewStyle().Background(p.DiffDelEmph).Foreground(p.FgBase),
			GutterAdd: lipgloss.NewStyle().Foreground(p.Success),
			GutterDel: lipgloss.NewStyle().Foreground(p.Error),
			LineNo:    lipgloss.NewStyle().Foreground(p.FgFaint),
			Context:   lipgloss.NewStyle().Foreground(p.FgSubtle),
			Header:    lipgloss.NewStyle().Foreground(p.FgMuted).Bold(true),
		},

		Markdown: markdownConfig(p),
		Chroma:   chromaStyle(p),
	}
	return s
}

// markdownConfig derives glamour's style from the palette so a heading
// and a link are colored by the same accents as the rest of the UI
// (doc 02 section 10.4). It starts from the stock config for the
// palette's polarity and repaints the accents; document margins are
// zeroed because the streaming cache glues fragment renders and a
// per-fragment margin would corrupt the seam (doc 02 section 6).
func markdownConfig(p Palette) ansi.StyleConfig {
	c := styles.DarkStyleConfig
	if !p.Dark {
		c = styles.LightStyleConfig
	}
	zero := uint(0)
	c.Document.Margin = &zero
	c.Document.BlockPrefix = ""
	c.Document.BlockSuffix = ""
	c.Document.Color = hexp(p.FgBase)

	c.H1.BackgroundColor = hexp(p.Primary)
	c.H1.Color = hexp(p.BgBase)
	c.H2.Color = hexp(p.Primary)
	c.H3.Color = hexp(p.Primary)
	c.H4.Color = hexp(p.Secondary)
	c.H5.Color = hexp(p.Secondary)
	c.H6.Color = hexp(p.Secondary)
	c.Link.Color = hexp(p.Accent)
	c.LinkText.Color = hexp(p.Info)
	c.Code.Color = hexp(p.Keyword)
	c.BlockQuote.Color = hexp(p.FgSubtle)
	c.HorizontalRule.Color = hexp(p.FgFaint)
	return c
}

// hexp renders a color as the "#rrggbb" pointer glamour wants.
func hexp(c color.Color) *string {
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	return &s
}
