package ui

import (
	"time"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/colonyview"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// ColonyController projects the colony's worker lifecycle events into the
// colony panel's state, the same shape SidebarController folds bus messages
// into the sidebar. It reads the worker events the dispatch emits (woke,
// progress, blocked, finished) and never recounts anything client-side: the
// token figure is the spend the ledger metered and the colony reported (D5,
// D18). A pure projection, so the same event stream always yields the same
// frame, which is what makes the golden colony-view tests sound (doc 09, doc 02
// section 10.5).
type ColonyController struct {
	panel *colonyview.View
	ants  map[string]colonyview.Ant
	order []string // insertion order, so a rebuilt slice is stable before the view sorts
	focus string   // ant id whose transcript is open, "" for the list
	open  bool     // the panel is pushed on the overlay
}

// NewColony builds an empty colony panel. It shows nothing until the first
// worker wakes, because a colony with no live ants has no list to draw.
func NewColony(th theme.Theme) *ColonyController {
	return &ColonyController{
		panel: colonyview.New(th),
		ants:  map[string]colonyview.Ant{},
	}
}

// Apply folds one bus message into the panel state. Every worker event keys on
// the forager lane, so two siblings on the same card stay two rows. An event
// for an ant not seen before inserts it; a later event updates it in place, so
// a woke then progress then finished walks one row through its lifecycle
// without ever losing it.
func (c *ColonyController) Apply(msg btea.Msg) {
	switch m := msg.(type) {
	case bus.WorkerWokeMsg:
		a := c.upsert(m.Ant)
		a.Status = colonyview.Awake
		a.Question = ""
		c.ants[m.Ant] = a
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

// HandleMsg consumes input while the panel is on top. Escape-to-close is the
// overlay's job, and the panel has no key actions of its own yet, so it reports
// nothing and lets the live worker stream keep updating it underneath. The
// drill-in navigation that will read keys here is a later slice.
func (c *ColonyController) HandleMsg(msg btea.Msg) dialog.Action { return nil }

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

// Closed clears the open flag when the overlay pops the panel on escape, so a
// later keypress opens a fresh push rather than being swallowed by a stale flag.
func (c *ColonyController) Closed() { c.open = false }

// Focus opens an ant's drill-in, or clears it when id is "". The transcript
// itself is fed by a later slice; this records which ant the panel is drilled
// into so the view knows to draw the drill pane instead of the list.
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

// Draw projects the current state onto the panel and paints it.
func (c *ColonyController) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	ants := make([]colonyview.Ant, 0, len(c.order))
	for _, id := range c.order {
		ants = append(ants, c.ants[id])
	}
	c.panel.SetState(colonyview.State{Ants: ants, Focused: c.focus})
	return c.panel.Draw(scr, area)
}
