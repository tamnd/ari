// Package input holds the pieces that sit between the terminal and the
// update loop (doc 02 section 14): a filter that coalesces high-rate
// mouse traffic so a wheel flick cannot starve the keyboard, and the
// external editor escape hatch for prompts that outgrow a textarea.
package input

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	btea "charm.land/bubbletea/v2"
)

// CoalescedWheel is the message a burst of wheel events becomes. Delta
// is the summed step count, positive scrolling down, matching the sign
// ScrollBy takes. X and Y are from the latest event, for hit-testing
// which pane the wheel is over.
type CoalescedWheel struct {
	Delta int
	X, Y  int
}

// sampleWindow is roughly one frame; mouse traffic is sampled to one
// message per window.
const sampleWindow = 16 * time.Millisecond

// Filter coalesces mouse motion and wheel events before they reach
// Update. Wheel deltas accumulate so a fast scroll is one message with
// a summed delta, not a storm; everything else passes through untouched
// so coalescing can never cost a keystroke. Plug it in with
// tea.WithFilter(f.Filter).
type Filter struct {
	lastWheel  time.Time
	lastMotion time.Time
	wheelAcc   int

	// now is the clock, swappable in tests.
	now func() time.Time
}

// NewFilter returns a filter on the real clock.
func NewFilter() *Filter {
	return &Filter{now: time.Now}
}

// Filter implements the tea.WithFilter contract.
func (f *Filter) Filter(_ btea.Model, msg btea.Msg) btea.Msg {
	switch m := msg.(type) {
	case btea.MouseWheelMsg:
		f.wheelAcc += wheelStep(m.Button)
		if f.now().Sub(f.lastWheel) < sampleWindow {
			return nil // swallowed; the sum rides the next event in the burst
		}
		f.lastWheel = f.now()
		out := CoalescedWheel{Delta: f.wheelAcc, X: m.X, Y: m.Y}
		f.wheelAcc = 0
		if out.Delta == 0 {
			return nil // up and down cancelled out
		}
		return out
	case btea.MouseMotionMsg:
		if f.now().Sub(f.lastMotion) < sampleWindow {
			return nil
		}
		f.lastMotion = f.now()
		return msg
	default:
		return msg // keys, paste, resize, focus: never delayed
	}
}

func wheelStep(b btea.MouseButton) int {
	switch b {
	case btea.MouseWheelDown:
		return 1
	case btea.MouseWheelUp:
		return -1
	default:
		return 0 // horizontal wheel: not a scroll step for us
	}
}

// EditorClosed reports the external editor closing: the edited text,
// or the error that kept it from round-tripping.
type EditorClosed struct {
	Content string
	Err     error
}

// OpenEditor suspends the TUI, opens the user's editor on the prompt
// content with the cursor at line and col (both 1-based), and delivers
// EditorClosed with the edited text. The temp file is .md so the
// editor lights up markdown; it is removed after reading back.
func OpenEditor(content string, line, col int) btea.Cmd {
	path, err := writePrompt(content)
	if err != nil {
		return func() btea.Msg { return EditorClosed{Err: err} }
	}
	return btea.ExecProcess(editorCommand(path, line, col), reload(path))
}

// writePrompt stages the prompt in a temp .md file.
func writePrompt(content string) (string, error) {
	tmp, err := os.CreateTemp("", "ari_prompt_*.md")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	_, werr := tmp.WriteString(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return "", werr
	}
	return path, nil
}

// reload builds the callback that reads the edited prompt back and
// removes the temp file, whatever happened.
func reload(path string) btea.ExecCallback {
	return func(execErr error) btea.Msg {
		data, readErr := os.ReadFile(path)
		_ = os.Remove(path)
		if execErr != nil {
			return EditorClosed{Err: execErr}
		}
		if readErr != nil {
			return EditorClosed{Err: readErr}
		}
		return EditorClosed{Content: strings.TrimRight(string(data), "\n")}
	}
}

// editorCommand builds the invocation, honoring $VISUAL then $EDITOR,
// and positions the cursor for the editors whose flags we know. The
// env value may carry flags of its own ("code -w").
func editorCommand(path string, line, col int) *exec.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	fields := strings.Fields(editor)
	name, flags := fields[0], fields[1:]

	var args []string
	switch filepath.Base(name) {
	case "vi", "vim", "nvim":
		args = append(flags, fmt.Sprintf("+call cursor(%d,%d)", line, col), path)
	case "nano", "micro":
		args = append(flags, fmt.Sprintf("+%d,%d", line, col), path)
	case "hx":
		args = append(flags, fmt.Sprintf("%s:%d:%d", path, line, col))
	case "code", "codium":
		args = append(flags, "--wait", "--goto", fmt.Sprintf("%s:%d:%d", path, line, col))
	default:
		args = append(flags, path)
	}
	return exec.Command(name, args...)
}
