package ui

import (
	"encoding/json"
	"strconv"
	"time"

	"strings"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/chatlist"
	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/parts"
	"github.com/tamnd/ari/ui/splash"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// partItem adapts one part to the chat list's Item contract. The part
// is shared by pointer: the projection mutates it and bumps Version,
// and the list's memo notices without diffing content.
type partItem struct {
	id chatlist.ItemID
	p  *parts.Part
}

func (i partItem) Identity() chatlist.ItemID { return i.id }
func (i partItem) Version() uint64           { return i.p.Version }
func (i partItem) Finished() bool            { return !i.p.Finished.IsZero() }

func (i partItem) Render(width int, th theme.Theme) []uv.Line {
	block := parts.Render(*i.p, width, th)
	out := make([]uv.Line, 0, len(block)+1)
	for _, l := range block {
		out = append(out, tea.ToLines(l)...)
	}
	return append(out, uv.Line{}) // one blank line between parts
}

// ChatController owns the scrollback: the lazy list, follow mode, and
// the projection of bus messages into parts (doc 02 sections 4 and 5).
// It knows nothing about permissions or the editor.
type ChatController struct {
	th    theme.Theme
	list  *chatlist.List
	parts map[string]*parts.Part
	order []string // append order, kept for theme rebuilds
	usage map[string]parts.Usage
	page  int // last drawn height, the page-scroll unit
	now   func() time.Time
}

// NewChat builds an empty chat in follow mode.
func NewChat(th theme.Theme, now func() time.Time) *ChatController {
	return &ChatController{
		th:    th,
		list:  chatlist.New(th),
		parts: map[string]*parts.Part{},
		usage: map[string]parts.Usage{},
		now:   now,
	}
}

// part returns the projected part for key, creating and appending it on
// first sight.
func (c *ChatController) part(key string, mk func() *parts.Part) *parts.Part {
	if p, ok := c.parts[key]; ok {
		return p
	}
	p := mk()
	p.Version = 1
	c.parts[key] = p
	c.order = append(c.order, key)
	c.list.Append(partItem{chatlist.ItemID(key), p})
	return p
}

func pkey(turn string, part int) string {
	return turn + "/" + strconv.Itoa(part)
}

// Apply projects one bus message into the part list. Unknown messages
// are ignored; the projection is the only writer of parts.
func (c *ChatController) Apply(msg btea.Msg) {
	switch m := msg.(type) {
	case bus.TurnStartedMsg:
		now := c.now()
		c.part("user:"+m.ID, func() *parts.Part {
			return &parts.Part{Kind: parts.KindText, Role: parts.RoleUser,
				Text: m.Prompt, Ant: m.Ant, Started: now, Finished: now}
		})
	case bus.TextDeltaMsg:
		p := c.part(pkey(m.Turn, m.Part), func() *parts.Part {
			return &parts.Part{Kind: parts.KindText, Role: parts.RoleAssistant, Started: c.now()}
		})
		p.Text += m.Text
		p.Version++
	case bus.TextEndMsg:
		if p, ok := c.parts[pkey(m.Turn, m.Part)]; ok {
			p.Finished = c.now()
			p.Version++
		}
	case bus.ThinkingDeltaMsg:
		p := c.part(pkey(m.Turn, m.Part), func() *parts.Part {
			return &parts.Part{Kind: parts.KindReasoning, Role: parts.RoleAssistant, Started: c.now()}
		})
		p.Text += m.Text
		p.Version++
	case bus.ThinkingEndMsg:
		if p, ok := c.parts[pkey(m.Turn, m.Part)]; ok {
			p.Finished = c.now()
			p.Started = p.Finished.Add(-time.Duration(m.DurationMS) * time.Millisecond)
			p.Version++
		}
	case bus.ToolStartMsg:
		now := c.now()
		c.part(pkey(m.Turn, m.Part), func() *parts.Part {
			return &parts.Part{Kind: parts.KindToolCall, Role: parts.RoleAssistant,
				Tool: m.Tool, Call: m.Call, Args: json.RawMessage(m.Input),
				Started: now, Finished: now}
		})
	case bus.ToolProgressMsg:
		p := c.part("call:"+m.Call, func() *parts.Part {
			return &parts.Part{Kind: parts.KindToolResult, Role: parts.RoleTool,
				Call: m.Call, Started: c.now()}
		})
		s, _ := p.Result.(string)
		p.Result = s + m.Text
		p.Version++
	case bus.ToolEndMsg:
		p := c.part("call:"+m.Call, func() *parts.Part {
			return &parts.Part{Kind: parts.KindToolResult, Role: parts.RoleTool,
				Call: m.Call, Started: c.now()}
		})
		p.Tool, p.OK, p.Result = m.Tool, m.OK, m.Display
		p.Finished = c.now()
		p.Version++
	case bus.TurnFinishedMsg:
		now := c.now()
		p := c.part("fin:"+m.ID, func() *parts.Part {
			return &parts.Part{Kind: parts.KindFinish, Role: parts.RoleAssistant,
				Started: now, Finished: now}
		})
		p.Stop = m.Reason
		if m.Error != "" {
			p.Stop = m.Reason + ": " + m.Error
		}
		p.Usage = c.usage[m.ID]
		p.Version++
	case bus.LedgerTurnMsg:
		u := parts.Usage{Input: m.Input, Output: m.Output,
			CacheRead: m.CacheRead, CacheWrite: m.CacheWrite}
		c.usage[m.LedgerTurn.Turn] = u
		if p, ok := c.parts["fin:"+m.LedgerTurn.Turn]; ok {
			p.Usage = u
			p.Version++
		}
	case bus.ErrorMsg:
		c.applyError(m.ErrorInfo)
	}
}

// applyError renders a client-facing failure as a finish-style line.
func (c *ChatController) applyError(e event.ErrorInfo) {
	now := c.now()
	p := c.part("err:"+strconv.Itoa(len(c.order)), func() *parts.Part {
		return &parts.Part{Kind: parts.KindFinish, Role: parts.RoleAssistant,
			Started: now, Finished: now}
	})
	p.Stop = "error (" + e.Code + "): " + e.Message
	p.Version++
}

// Handle runs a chat-scope key action.
func (c *ChatController) Handle(a keys.Action, _ btea.KeyPressMsg) btea.Cmd {
	switch a {
	case keys.ScrollUp:
		c.list.ScrollBy(-1)
	case keys.ScrollDown:
		c.list.ScrollBy(1)
	case keys.PageUp:
		c.list.ScrollBy(-max(c.page-1, 1))
	case keys.PageDown:
		c.list.ScrollBy(max(c.page-1, 1))
	case keys.GotoBottom:
		c.list.ScrollToBottom()
	}
	return nil
}

// Scroll moves the window by a coalesced wheel delta.
func (c *ChatController) Scroll(delta int) { c.list.ScrollBy(delta) }

// Empty reports whether nothing has been said yet, which is what keeps
// the landing screen up.
func (c *ChatController) Empty() bool { return c.list.Len() == 0 }

// SetTheme swaps the palette by rebuilding the list; the memo is keyed
// by width and version, not theme, so a rebuild is the honest reset.
func (c *ChatController) SetTheme(th theme.Theme) {
	c.th = th
	c.list = chatlist.New(th)
	for _, key := range c.order {
		c.list.Append(partItem{chatlist.ItemID(key), c.parts[key]})
	}
}

// Draw paints the scrollback, or the landing wordmark while the chat is
// empty.
func (c *ChatController) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	c.page = area.Dy()
	if c.Empty() {
		c.drawLanding(scr, area)
		return nil
	}
	return c.list.Draw(scr, area)
}

func (c *ChatController) drawLanding(scr uv.Screen, area uv.Rectangle) {
	mark := splash.Wordmark(c.th)
	hint := c.th.S.Faint.Render("type a prompt to start")
	top := area.Min.Y + max((area.Dy()-len(mark)-2)/2, 0)
	for i, l := range mark {
		row := uv.Rect(area.Min.X, top+i, area.Dx(), 1)
		tea.DrawStyled(scr, row, center(l, area.Dx()))
	}
	row := uv.Rect(area.Min.X, top+len(mark)+1, area.Dx(), 1)
	tea.DrawStyled(scr, row, center(hint, area.Dx()))
}

// center pads a styled line so it sits centered in width.
func center(l string, width int) string {
	return strings.Repeat(" ", max((width-ansi.StringWidth(l))/2, 0)) + l
}
