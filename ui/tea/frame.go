package tea

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Frame wraps styled lines in a rounded border with one cell of
// padding, padding every row to the full inner width so the box reads
// as a solid panel over whatever is behind it. Dialogs share this so
// every modal has the same chrome.
func Frame(body []string, width int, border lipgloss.Style) []string {
	inner := width - 2
	out := []string{border.Render("╭" + strings.Repeat("─", inner) + "╮")}
	for _, l := range body {
		pad := max(inner-2-ansi.StringWidth(l), 0)
		out = append(out, border.Render("│")+" "+l+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return out
}
