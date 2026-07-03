package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/tamnd/ari/event"
)

// Script is one replay set: a recorded sequence of provider responses and
// the events driving them through the core must produce. The format is
// checked-in JSON a human can read and a recorder can regenerate, so a
// real session is captured once and re-run offline forever (D23).
type Script struct {
	// Name identifies the fixture in failures.
	Name string `json:"name"`
	// Prompt is the user turn the script answers.
	Prompt string `json:"prompt"`
	// Responses are the scripted provider replies, in the order the
	// provider will be called within the turn. The shape is owned by the
	// provider layer; the harness treats each as opaque JSON.
	Responses []json.RawMessage `json:"responses"`
	// Want is the exact event sequence the turn must emit.
	Want []event.Event `json:"want"`
}

// LoadScript reads a replay set from disk.
func LoadScript(path string) (Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Script{}, err
	}
	var s Script
	if err := json.Unmarshal(data, &s); err != nil {
		return Script{}, fmt.Errorf("replay %s: %w", path, err)
	}
	return s, nil
}

// SaveScript writes a replay set, indented, for the recorder path.
func SaveScript(path string, s Script) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// DriveFunc runs one scripted turn and returns the events it produced.
// Slice 2 points this at the real core with a scripted provider; the
// harness itself only asserts the sequence.
type DriveFunc func(s Script) ([]event.Event, error)

// Replay drives the script and asserts the produced events match the
// recorded sequence exactly, after normalization: Seq and Time are
// zeroed on both sides because they are assigned at run time and would
// make every replay a diff. Everything else, order included, must be
// byte-identical, which is what "the loop is deterministic" means as a
// test (D6, D23).
func Replay(t TB, s Script, drive DriveFunc) {
	t.Helper()
	got, err := drive(s)
	if err != nil {
		t.Fatalf("replay %s: drive failed: %v", s.Name, err)
	}
	want := normalize(s.Want)
	norm := normalize(got)
	n := min(len(norm), len(want))
	for i := range n {
		g, w := marshal(t, norm[i]), marshal(t, want[i])
		if !bytes.Equal(g, w) {
			t.Errorf("replay %s: event %d diverged\nwant: %s\ngot:  %s", s.Name, i, w, g)
			return
		}
	}
	if len(norm) != len(want) {
		t.Errorf("replay %s: got %d events, want %d (first %d match)", s.Name, len(norm), len(want), n)
	}
}

func normalize(evs []event.Event) []event.Event {
	out := make([]event.Event, len(evs))
	for i, e := range evs {
		e.Seq = 0
		e.Time = time.Time{}
		out[i] = e
	}
	return out
}

func marshal(t TB, e event.Event) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return b
}
