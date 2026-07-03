package theme

import (
	"hash/fnv"
	"image/color"
	"sync"
)

// accentTable assigns each ant a stable accent color and glyph (doc 02
// section 10.5, D18). The assignment is deterministic from the ant's ID
// and drawn from a palette-derived ramp, so the same ant is the same
// color across sessions and the colors suit the active theme. In
// single-ant mode the worker gets the theme's primary, so the machinery
// is inert until the colony exists.
type accentTable struct {
	ramp   []color.Color
	glyphs []rune

	mu     sync.Mutex
	assign map[string]Accent
}

// Accent is one ant's stable color and glyph.
type Accent struct {
	Color color.Color
	Glyph rune
}

func newAccentTable(p Palette) *accentTable {
	return &accentTable{
		ramp:   []color.Color{p.Primary, p.Secondary, p.Accent, p.Info, p.Success, p.Warning},
		glyphs: []rune{'π', 'σ', 'δ', 'λ', 'φ', 'ω', 'ξ', 'ψ'},
		assign: map[string]Accent{},
	}
}

// Accent returns the stable accent for an ant, assigning one on first
// sight. The worker (and the empty ID) always gets the theme's primary
// with the pi glyph.
func (t *Theme) Accent(id string) Accent {
	a := t.accents
	a.mu.Lock()
	defer a.mu.Unlock()
	if got, ok := a.assign[id]; ok {
		return got
	}
	acc := Accent{Color: a.ramp[0], Glyph: a.glyphs[0]}
	if id != "" && id != "worker" {
		h := fnv.New32a()
		h.Write([]byte(id))
		n := h.Sum32()
		acc = Accent{
			Color: a.ramp[n%uint32(len(a.ramp))],
			Glyph: a.glyphs[n%uint32(len(a.glyphs))],
		}
	}
	a.assign[id] = acc
	return acc
}
