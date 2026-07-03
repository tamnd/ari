package theme

import (
	"fmt"
	"image/color"

	"github.com/alecthomas/chroma/v2"
)

// chromaStyle derives a chroma syntax style from the palette, so code in
// a diff, in a chat message, and in shell output all tokenize to the
// same colors as the rest of the UI (doc 02 section 10.4). The mapping
// is small on purpose: a dozen semantic buckets beat a hundred
// hand-tuned token rules that drift from the theme.
func chromaStyle(p Palette) *chroma.Style {
	entry := func(c color.Color) string {
		r, g, b, _ := c.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	return chroma.MustNewStyle("ari", chroma.StyleEntries{
		chroma.Text:           entry(p.FgBase),
		chroma.Keyword:        entry(p.Keyword),
		chroma.KeywordType:    entry(p.Secondary),
		chroma.Name:           entry(p.FgBase),
		chroma.NameFunction:   entry(p.Primary),
		chroma.NameBuiltin:    entry(p.Secondary),
		chroma.LiteralString:  entry(p.Success),
		chroma.LiteralNumber:  entry(p.Warning),
		chroma.Comment:        entry(p.FgFaint),
		chroma.CommentPreproc: entry(p.Info),
		chroma.Operator:       entry(p.FgMuted),
		chroma.Punctuation:    entry(p.FgSubtle),
		chroma.GenericEmph:    "italic",
		chroma.GenericStrong:  "bold",
		chroma.Error:          entry(p.Error),
	})
}
