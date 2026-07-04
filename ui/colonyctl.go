package ui

import (
	"context"
	"time"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/colonyview"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/parts"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// colonyTranscript carries a drilled-in ant's sidechain back to the update
// loop. It names the ant it was fetched for so a stale fetch, one the user
// drilled away from before it landed, is dropped instead of overwriting the
// pane the user is now looking at.
type colonyTranscript struct {
	ant   string
	parts []parts.Part
	err   error
}

// colonyDrill is the action HandleMsg returns when the user opens an ant's
// drill-in, so the root issues the sidechain fetch off the update loop. It
// carries nothing because the controller already holds which ant is focused.
type colonyDrill struct{}

// ColonyController projects the colony's worker lifecycle events into the
// colony panel's state, the same shape SidebarController folds bus messages
// into the sidebar. It reads the worker events the dispatch emits (woke,
// progress, blocked, finished) and never recounts anything client-side: the
// token figure is the spend the ledger metered and the colony reported (D5,
// D18). A pure projection, so the same event stream always yields the same
// frame, which is what makes the golden colony-view tests sound (doc 09, doc 02
// section 10.5).
type ColonyController struct {
	client Client
	panel  *colonyview.View
	ants   map[string]colonyview.Ant
	order  []string          // insertion order, so a rebuilt slice is stable before the view sorts
	files  map[string]string // ant id to the sidechain file it writes, the drill-in locator
	sess   string            // the session the workers run under, for the drill-in fetch
	sel    string            // ant id the list cursor sits on
	focus  string            // ant id whose transcript is open, "" for the list
	script []parts.Part      // the focused ant's fetched sidechain
	open   bool              // the panel is pushed on the overlay
}

// NewColony builds an empty colony panel. It shows nothing until the first
// worker wakes, because a colony with no live ants has no list to draw. The
// client is the seam the drill-in reads a sidechain through.
func NewColony(client Client, th theme.Theme) *ColonyController {
	return &ColonyController{
		client: client,
		panel:  colonyview.New(th),
		ants:   map[string]colonyview.Ant{},
		files:  map[string]string{},
	}
}

// Apply folds one bus message into the panel state. Every worker event keys on
// the forager lane, so two siblings on the same card stay two rows. An event
// for an ant not seen before inserts it; a later event updates it in place, so
// a woke then progress then finished walks one row through its lifecycle
// without ever losing it. A woke also records the ant's sidechain locator and
// the session it runs under, which is what the drill-in fetch reads.
func (c *ColonyController) Apply(msg btea.Msg) {
	switch m := msg.(type) {
	case bus.WorkerWokeMsg:
		a := c.upsert(m.Ant)
		a.Status = colonyview.Awake
		a.Question = ""
		c.ants[m.Ant] = a
		if m.File != "" {
			c.files[m.Ant] = m.File
		}
		if m.Session != "" {
			c.sess = m.Session
		}
		if c.sel == "" {
			c.sel = m.Ant
		}
	case bus.ColonyProgressMsg:
		a := c.upsert(m.Ant)
		if m.Tokens > 0 {
			a.Tokens = m.Tokens
		}
		c.ants[m.Ant] = a
	case bus.WorkerBlockedMsg:
		a := c.upsert(m.Ant)
		a.Status = colonyview.Blocked
		a.Question = m.Question
		c.ants[m.Ant] = a
	case bus.WorkerFinishedMsg:
		a := c.upsert(m.Ant)
		a.Status = colonyview.Done
		a.Question = ""
		c.ants[m.Ant] = a
	}
}

// upsert returns the ant row for an id, creating it on first sight. The name
// defaults to the id: the worker events carry only the lane, so the lane is the
// name until a later slice threads the card through.
func (c *ColonyController) upsert(id string) colonyview.Ant {
	a, ok := c.ants[id]
	if !ok {
		a = colonyview.Ant{ID: id, Name: id}
		c.order = append(c.order, id)
	}
	return a
}

// ID identifies the panel for the overlay's same-id reopen detection.
func (c *ColonyController) ID() string { return "colony" }

// HandleMsg drives the panel while it is on top. In the list the arrows walk the
// cursor through the ants in the order they are drawn, and enter drills into the
// selected ant, returning the action that fires its sidechain fetch. In the
// drill-in, backspace or left returns to the list. Escape closes the whole panel
// and is the overlay's job, so the drill-in cannot trap the user (doc 02 section
// 8, the overlay owns escape).
func (c *ColonyController) HandleMsg(msg btea.Msg) dialog.Action {
	key, ok := msg.(btea.KeyPressMsg)
	if !ok {
		return nil
	}
	if c.focus != "" {
		switch key.String() {
		case "backspace", "left", "h":
			c.focus = ""
			c.script = nil
		}
		return nil
	}
	switch key.String() {
	case "up", "k":
		c.move(-1)
	case "down", "j":
		c.move(1)
	case "enter":
		if c.sel != "" {
			c.focus = c.sel
			c.script = nil
			return colonyDrill{}
		}
	}
	return nil
}

// move walks the cursor by delta through the ants in draw order, clamped at the
// ends so a press at the top or bottom stays put rather than wrapping.
func (c *ColonyController) move(delta int) {
	order := colonyview.Order(c.antSlice())
	if len(order) == 0 {
		return
	}
	idx := 0
	for i, a := range order {
		if a.ID == c.sel {
			idx = i
			break
		}
	}
	idx = min(max(idx+delta, 0), len(order)-1)
	c.sel = order[idx].ID
}

// Fetch reads the focused ant's sidechain off the update loop. It reads the
// locator the woke event recorded, so an ant with no file, one that never
// spoke, fetches nothing rather than erroring.
func (c *ColonyController) Fetch() btea.Cmd {
	file := c.files[c.focus]
	if file == "" || c.sess == "" {
		return nil
	}
	client, session, ant := c.client, c.sess, c.focus
	return func() btea.Msg {
		ps, err := client.Transcript(context.Background(), session, file)
		return colonyTranscript{ant: ant, parts: ps, err: err}
	}
}

// OnTranscript lands a fetched sidechain on the panel, unless the user drilled
// away before it arrived, in which case the ant no longer matches the focus and
// the result is dropped.
func (c *ColonyController) OnTranscript(m colonyTranscript) {
	if m.err != nil || m.ant != c.focus {
		return
	}
	c.script = m.parts
}

// Open pushes the panel as a toggled dialog. A second colony keypress never
// reaches here because the open dialog owns input; the flag guards a double
// push if one ever did.
func (c *ColonyController) Open(overlay *dialog.Overlay, now time.Time) btea.Cmd {
	if c.open {
		return nil
	}
	c.open = true
	overlay.Push(c, now)
	return nil
}

// Closed clears the open flag when the overlay pops the panel on escape and
// drops back to the list, so reopening starts at the population rather than a
// stale drill-in.
func (c *ColonyController) Closed() {
	c.open = false
	c.focus = ""
	c.script = nil
}

// Focus opens an ant's drill-in, or clears it when id is "". The transcript is
// fetched separately; this only records which ant the panel is drilled into so
// the view draws the drill pane instead of the list.
func (c *ColonyController) Focus(id string) { c.focus = id }

// Live reports whether any ant is still awake or blocked, so the root can hide
// an idle colony panel rather than show a wall of done rows.
func (c *ColonyController) Live() bool {
	for _, id := range c.order {
		switch c.ants[id].Status {
		case colonyview.Awake, colonyview.Blocked:
			return true
		}
	}
	return false
}

// SetTheme swaps the palette.
func (c *ColonyController) SetTheme(th theme.Theme) { c.panel.SetTheme(th) }

// antSlice rebuilds the ant rows in insertion order; the view sorts them for
// display, and Order gives the controller the same sorted order for its cursor.
func (c *ColonyController) antSlice() []colonyview.Ant {
	ants := make([]colonyview.Ant, 0, len(c.order))
	for _, id := range c.order {
		ants = append(ants, c.ants[id])
	}
	return ants
}

// Draw projects the current state onto the panel and paints it.
func (c *ColonyController) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	c.panel.SetState(colonyview.State{
		Ants:       c.antSlice(),
		Selected:   c.sel,
		Focused:    c.focus,
		Transcript: c.script,
	})
	return c.panel.Draw(scr, area)
}
