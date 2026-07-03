package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// The two palettes that ship at M0 (doc 02 section 10.3). The UI picks a
// default by reading the terminal background; config can override by
// name. The provider you talk to has nothing to do with how your
// terminal looks (doc 02 section 10.6).

func hex(s string) color.Color { return lipgloss.Color(s) }

// Dark is the default palette for dark terminals.
func Dark() Theme {
	return New("dark", Palette{
		Primary:   hex("#c084fc"), // soft violet
		Secondary: hex("#7dd3fc"),
		Accent:    hex("#f0abfc"),
		Keyword:   hex("#fbbf24"),

		FgBase:   hex("#e5e7eb"),
		FgMuted:  hex("#b3b9c4"),
		FgSubtle: hex("#8b93a1"),
		FgFaint:  hex("#5b6270"),

		BgBase:      hex("#101216"),
		BgRaised:    hex("#191c22"),
		BgOverlay:   hex("#22262e"),
		BgSelection: hex("#33384a"),

		Success: hex("#4ade80"),
		Warning: hex("#facc15"),
		Error:   hex("#f87171"),
		Info:    hex("#60a5fa"),

		DiffAdd:     hex("#12261a"),
		DiffDel:     hex("#2d1215"),
		DiffAddEmph: hex("#1f4d2e"),
		DiffDelEmph: hex("#5c1f26"),

		ANSI: [16]color.Color{
			hex("#101216"), hex("#f87171"), hex("#4ade80"), hex("#facc15"),
			hex("#60a5fa"), hex("#c084fc"), hex("#22d3ee"), hex("#b3b9c4"),
			hex("#5b6270"), hex("#fca5a5"), hex("#86efac"), hex("#fde047"),
			hex("#93c5fd"), hex("#d8b4fe"), hex("#67e8f9"), hex("#e5e7eb"),
		},
		Dark: true,
	})
}

// Light is the default palette for light terminals.
func Light() Theme {
	return New("light", Palette{
		Primary:   hex("#7c3aed"),
		Secondary: hex("#0369a1"),
		Accent:    hex("#a21caf"),
		Keyword:   hex("#b45309"),

		FgBase:   hex("#1f2430"),
		FgMuted:  hex("#434b5c"),
		FgSubtle: hex("#697182"),
		FgFaint:  hex("#9aa1b0"),

		BgBase:      hex("#fbfbfa"),
		BgRaised:    hex("#f1f1ef"),
		BgOverlay:   hex("#e8e8e6"),
		BgSelection: hex("#d5ddf6"),

		Success: hex("#15803d"),
		Warning: hex("#a16207"),
		Error:   hex("#b91c1c"),
		Info:    hex("#1d4ed8"),

		DiffAdd:     hex("#e6f4ea"),
		DiffDel:     hex("#fbe9e9"),
		DiffAddEmph: hex("#abd8b8"),
		DiffDelEmph: hex("#f3b8b8"),

		ANSI: [16]color.Color{
			hex("#1f2430"), hex("#b91c1c"), hex("#15803d"), hex("#a16207"),
			hex("#1d4ed8"), hex("#7c3aed"), hex("#0e7490"), hex("#697182"),
			hex("#9aa1b0"), hex("#dc2626"), hex("#16a34a"), hex("#ca8a04"),
			hex("#2563eb"), hex("#9333ea"), hex("#0891b2"), hex("#0b0e14"),
		},
		Dark: false,
	})
}

// Themes lists the built-in themes by name, for config lookup.
func Themes() map[string]Theme {
	return map[string]Theme{"dark": Dark(), "light": Light()}
}
