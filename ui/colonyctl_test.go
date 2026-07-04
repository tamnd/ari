package ui

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/theme"
)

// colonyFrame renders a controller's current projection so a test can assert
// what a live colony panel would show.
func colonyFrame(c *ColonyController, w, h int) string {
	buf := uv.NewScreenBuffer(w, h)
	c.Draw(buf, uv.Rect(0, 0, w, h))
	return buf.String()
}

func woke(ant, task, tier string) bus.WorkerWokeMsg {
	var m bus.WorkerWokeMsg
	m.Ant, m.Task, m.Tier = ant, task, tier
	return m
}

func progress(ant, task string, tokens int64) bus.ColonyProgressMsg {
	var m bus.ColonyProgressMsg
	m.Ant, m.Task, m.Tokens = ant, task, tokens
	return m
}

func blocked(ant, task, question string) bus.WorkerBlockedMsg {
	var m bus.WorkerBlockedMsg
	m.Ant, m.Task, m.Question = ant, task, question
	return m
}

func finished(ant, task string, ok bool) bus.WorkerFinishedMsg {
	var m bus.WorkerFinishedMsg
	m.Ant, m.Task, m.OK = ant, task, ok
	return m
}

// TestColonyProjectsWorkerLifecycle walks two lanes through a fan-out: both
// wake, one spends and finishes, the other spends then blocks on a Question.
// The panel must show both lanes, the exact spends, the blocked lane's Question
// inline, and the finished lane as done.
func TestColonyProjectsWorkerLifecycle(t *testing.T) {
	c := NewColony(theme.Dark())
	c.Apply(woke("forager-0", "sub-a", "cheap"))
	c.Apply(woke("forager-1", "sub-b", "cheap"))
	c.Apply(progress("forager-0", "sub-a", 4210))
	c.Apply(progress("forager-1", "sub-b", 1875))
	c.Apply(finished("forager-0", "sub-a", true))
	c.Apply(blocked("forager-1", "sub-b", "run go generate?"))

	out := colonyFrame(c, 48, 10)
	for _, want := range []string{"forager-0", "forager-1", "4210 tok", "1875 tok", "done", "blocked", "run go generate?"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q:\n%s", want, out)
		}
	}
}

// TestColonyLiveTracksAwakeAndBlocked proves Live reports the colony busy while
// any lane is awake or blocked and idle once every lane has finished, so the
// root can hide a panel with nothing left to watch.
func TestColonyLiveTracksAwakeAndBlocked(t *testing.T) {
	c := NewColony(theme.Dark())
	if c.Live() {
		t.Error("an empty colony is not live")
	}
	c.Apply(woke("forager-0", "sub-a", "cheap"))
	if !c.Live() {
		t.Error("a colony with an awake lane is live")
	}
	c.Apply(blocked("forager-0", "sub-a", "which module?"))
	if !c.Live() {
		t.Error("a colony with a blocked lane is still live")
	}
	c.Apply(finished("forager-0", "sub-a", true))
	if c.Live() {
		t.Error("a colony whose only lane finished is idle")
	}
}

// TestColonyProgressUpdatesInPlace proves a lane keeps its identity across
// events: a woke then two progress ticks then finished is one row walking its
// lifecycle, not four rows, and the latest tick wins.
func TestColonyProgressUpdatesInPlace(t *testing.T) {
	c := NewColony(theme.Dark())
	c.Apply(woke("forager-0", "sub-a", "cheap"))
	c.Apply(progress("forager-0", "sub-a", 500))
	c.Apply(progress("forager-0", "sub-a", 900))
	c.Apply(finished("forager-0", "sub-a", true))

	out := colonyFrame(c, 48, 10)
	if !strings.Contains(out, "900 tok") {
		t.Errorf("panel did not carry the latest spend:\n%s", out)
	}
	if strings.Contains(out, "500 tok") {
		t.Errorf("panel kept a stale spend:\n%s", out)
	}
	if n := strings.Count(out, "forager-0"); n != 1 {
		t.Errorf("lane rendered %d times, want 1 row:\n%s", n, out)
	}
}
