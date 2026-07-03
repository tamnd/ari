package tool

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// htmlToMarkdown reduces an HTML page to markdown the model can read:
// headings, links, code, lists, and text survive; scripts, styles, and
// navigation chrome do not (doc 04 section 9.2). It is a reducer, not a
// renderer; fidelity loses to readability on purpose.
func htmlToMarkdown(page string) string {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		// A page that does not parse is still text to a reader.
		return page
	}
	var b strings.Builder
	walkHTML(&b, doc)
	return tidyMarkdown(b.String())
}

// htmlSkip is the chrome and machinery stripped wholesale.
var htmlSkip = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"nav": true, "header": true, "footer": true, "aside": true,
	"svg": true, "iframe": true, "form": true, "button": true,
	"select": true, "input": true, "head": false, // head handled for title
}

func walkHTML(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(collapseSpace(n.Data))
		return
	case html.ElementNode:
		if htmlSkip[n.Data] {
			return
		}
		switch n.Data {
		case "title":
			b.WriteString("\n\n# ")
			walkChildren(b, n)
			b.WriteString("\n\n")
			return
		case "h1", "h2", "h3", "h4", "h5", "h6":
			b.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			walkChildren(b, n)
			b.WriteString("\n\n")
			return
		case "p", "div", "section", "article", "main", "table", "tr", "blockquote", "figcaption":
			b.WriteString("\n\n")
			walkChildren(b, n)
			b.WriteString("\n\n")
			return
		case "br":
			b.WriteString("\n")
			return
		case "li":
			b.WriteString("\n- ")
			walkChildren(b, n)
			return
		case "pre":
			b.WriteString("\n\n```\n")
			b.WriteString(strings.TrimSpace(rawText(n)))
			b.WriteString("\n```\n\n")
			return
		case "code":
			b.WriteString("`")
			walkChildren(b, n)
			b.WriteString("`")
			return
		case "a":
			var href string
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					href = attr.Val
				}
			}
			text := strings.TrimSpace(inlineText(n))
			switch {
			case text == "":
				return
			case href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:"):
				b.WriteString(text)
			default:
				b.WriteString("[" + text + "](" + href + ")")
			}
			return
		case "img":
			for _, attr := range n.Attr {
				if attr.Key == "alt" && strings.TrimSpace(attr.Val) != "" {
					b.WriteString("(" + strings.TrimSpace(attr.Val) + ")")
				}
			}
			return
		}
	}
	walkChildren(b, n)
}

func walkChildren(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkHTML(b, c)
	}
}

// rawText gathers text verbatim, for pre blocks where whitespace is
// content.
func rawText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// inlineText gathers collapsed text, for link labels.
func inlineText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(collapseSpace(n.Data))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

var spaceRun = regexp.MustCompile(`[ \t\r\n]+`)

func collapseSpace(s string) string {
	return spaceRun.ReplaceAllString(s, " ")
}

var blankRun = regexp.MustCompile(`\n{3,}`)
var trailingSpace = regexp.MustCompile(`[ \t]+\n`)

func tidyMarkdown(s string) string {
	s = trailingSpace.ReplaceAllString(s, "\n")
	s = blankRun.ReplaceAllString(s, "\n\n")
	// Lines that are only a stray space from collapsed inline nodes.
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		out = append(out, strings.TrimRight(line, " \t"))
	}
	s = strings.Join(out, "\n")
	s = blankRun.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s) + "\n"
}
