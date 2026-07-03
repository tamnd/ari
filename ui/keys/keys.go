// Package keys is the binding registry (doc 02 section 12). Every
// keystroke the UI acts on is a semantic Action bound in one scope;
// config rebinds by Action name, the help line reads the live binding,
// and a collision inside a scope is a load error rather than a silent
// winner.
package keys

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
)

// Action names a semantic action, not a key.
type Action int

const (
	// Global scope.
	Quit Action = iota
	HelpToggle
	Palette
	ThemePick
	Cancel
	FocusNext

	// Editor scope.
	Submit
	Newline
	ExternalEditor

	// Chat scope.
	ScrollUp
	ScrollDown
	PageUp
	PageDown
	GotoBottom

	// Dialog scope.
	Confirm
	NextChoice
	PrevChoice

	actionCount
)

// names maps config spelling to Action; String is its inverse.
var names = map[string]Action{
	"quit": Quit, "help": HelpToggle, "palette": Palette,
	"theme": ThemePick, "cancel": Cancel, "focus_next": FocusNext,
	"submit": Submit, "newline": Newline, "external_editor": ExternalEditor,
	"scroll_up": ScrollUp, "scroll_down": ScrollDown,
	"page_up": PageUp, "page_down": PageDown, "goto_bottom": GotoBottom,
	"confirm": Confirm, "next_choice": NextChoice, "prev_choice": PrevChoice,
}

func (a Action) String() string {
	for n, act := range names {
		if act == a {
			return n
		}
	}
	return fmt.Sprintf("action(%d)", int(a))
}

// Scope is where a binding applies. The router decides scope before
// matching keys: an open dialog owns input, else the focused pane, else
// global, so one key can mean different things without ambiguity.
type Scope int

const (
	Global Scope = iota
	Editor
	Chat
	Dialog
)

func (s Scope) String() string {
	return [...]string{"global", "editor", "chat", "dialog"}[s]
}

// scopeOf fixes which scope each action lives in.
var scopeOf = map[Action]Scope{
	Quit: Global, HelpToggle: Global, Palette: Global,
	ThemePick: Global, Cancel: Global, FocusNext: Global,
	Submit: Editor, Newline: Editor, ExternalEditor: Editor,
	ScrollUp: Chat, ScrollDown: Chat, PageUp: Chat,
	PageDown: Chat, GotoBottom: Chat,
	Confirm: Dialog, NextChoice: Dialog, PrevChoice: Dialog,
}

// Map is the full live keymap. The zero value is unusable; start from
// Default and overlay config with Apply.
type Map struct {
	bindings map[Action]key.Binding
}

// Default returns the built-in bindings.
func Default() Map {
	b := func(help string, ks ...string) key.Binding {
		return key.NewBinding(key.WithKeys(ks...), key.WithHelp(ks[0], help))
	}
	return Map{bindings: map[Action]key.Binding{
		Quit:       b("quit", "ctrl+c"),
		HelpToggle: b("help", "ctrl+g"),
		Palette:    b("palette", "ctrl+p"),
		ThemePick:  b("theme", "ctrl+t"),
		Cancel:     b("cancel", "esc"),
		FocusNext:  b("switch focus", "ctrl+w"),

		Submit:         b("send", "enter"),
		Newline:        b("newline", "shift+enter", "ctrl+j"),
		ExternalEditor: b("open $EDITOR", "ctrl+e"),

		ScrollUp:   b("scroll up", "up", "k"),
		ScrollDown: b("scroll down", "down", "j"),
		PageUp:     b("page up", "pgup", "ctrl+u"),
		PageDown:   b("page down", "pgdown", "ctrl+d"),
		GotoBottom: b("bottom", "G", "end"),

		Confirm:    b("confirm", "enter"),
		NextChoice: b("next", "tab", "right"),
		PrevChoice: b("previous", "shift+tab", "left"),
	}}
}

// Apply overlays user bindings, keyed by action name, then validates.
// A rebind replaces the whole key set for that action; the first key
// listed becomes the one help shows. Unknown action names and same-
// scope collisions are errors, listed exhaustively so one config pass
// fixes them all.
func (m Map) Apply(overrides map[string][]string) (Map, error) {
	out := Map{bindings: maps.Clone(m.bindings)}
	var errs []string
	for name, ks := range overrides {
		a, ok := names[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("unknown action %q", name))
			continue
		}
		if len(ks) == 0 {
			errs = append(errs, fmt.Sprintf("action %q has no keys", name))
			continue
		}
		help := out.bindings[a].Help().Desc
		out.bindings[a] = key.NewBinding(key.WithKeys(ks...), key.WithHelp(ks[0], help))
	}
	errs = append(errs, out.collisions()...)
	if len(errs) > 0 {
		sort.Strings(errs)
		return Map{}, fmt.Errorf("keymap: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

// collisions reports every key bound to two actions that can be live at
// the same time: two actions in one scope, or a pane action against a
// global one (the router falls through pane to global, so those clash
// too). Dialog does not fall through, and Editor and Chat are never
// focused together, so those pairs may share keys.
func (m Map) collisions() []string {
	var errs []string
	owner := map[string]Action{} // "scope/key" -> action
	claim := func(sc Scope, a Action, k string) {
		id := sc.String() + "/" + k
		if prev, taken := owner[id]; taken {
			lo, hi := prev, a
			if lo > hi {
				lo, hi = hi, lo
			}
			errs = append(errs, fmt.Sprintf("%q bound to both %s and %s in scope %s", k, lo, hi, sc))
			return
		}
		owner[id] = a
	}
	for a := range actionCount {
		sc := scopeOf[a]
		for _, k := range m.bindings[a].Keys() {
			claim(sc, a, k)
			if sc == Editor || sc == Chat {
				claim(Global, a, k) // panes fall through to global
			}
		}
	}
	return errs
}

// Lookup matches a key string against one scope only; the caller has
// already decided the scope. The bool reports a match.
func (m Map) Lookup(scope Scope, keystroke string) (Action, bool) {
	for a := range actionCount {
		if scopeOf[a] != scope {
			continue
		}
		if slices.Contains(m.bindings[a].Keys(), keystroke) {
			return a, true
		}
	}
	return 0, false
}

// Binding returns the live binding for an action, for help rendering.
func (m Map) Binding(a Action) key.Binding { return m.bindings[a] }

// Help projects the short help for a scope plus the always-on globals,
// in a stable order, reading the live bindings so a rebind shows up.
func (m Map) Help(scope Scope) []key.Binding {
	var actions []Action
	switch scope {
	case Dialog:
		actions = []Action{NextChoice, Confirm}
	case Editor:
		actions = []Action{Submit, Newline, ExternalEditor, FocusNext, HelpToggle, Quit}
	case Chat:
		actions = []Action{ScrollUp, ScrollDown, GotoBottom, FocusNext, HelpToggle, Quit}
	default:
		actions = []Action{HelpToggle, Quit}
	}
	out := make([]key.Binding, len(actions))
	for i, a := range actions {
		out[i] = m.bindings[a]
	}
	return out
}
