package hook

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Default timeouts, per event class. Tool hooks get a generous window because
// a linter or a test can be slow; interactive-path hooks get a short one so a
// slow hook does not stall a turn (doc 05 section 13.4).
const (
	defaultToolTimeout = 60 * time.Second
	defaultFastTimeout = 5 * time.Second
)

// Spec is one hook as written in a config file. It is the TOML shape; the
// event key and the source layer are supplied by the loader, not the file.
type Spec struct {
	Matcher string `toml:"matcher"`
	If      string `toml:"if"`
	Command string `toml:"command"`
	Shell   string `toml:"shell"`
	Timeout string `toml:"timeout"`
	Once    bool   `toml:"once"`
	Async   bool   `toml:"async"`
}

// Command is one configured hook resolved for a run: the file fields plus the
// event it fires on, the layer it came from (which gates it against workspace
// trust), and the compiled matcher.
type Command struct {
	Event   Event
	Layer   string // "user", "project", or "local"
	If      string
	Command string
	Shell   string
	Timeout time.Duration
	Once    bool
	Async   bool

	matcher string
	match   func(tool string) bool
}

// Build resolves a Spec into a Command for an event and a source layer,
// applying the default shell and timeout and compiling the matcher. A bad
// timeout or an uncompilable regex matcher is an error the loader turns into
// a config warning, so one broken hook never wedges the whole config.
func (s Spec) Build(ev Event, layer string) (Command, error) {
	if strings.TrimSpace(s.Command) == "" {
		return Command{}, fmt.Errorf("hook %s has an empty command", ev)
	}
	timeout := defaultTimeout(ev)
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return Command{}, fmt.Errorf("hook %s has a bad timeout %q: %w", ev, s.Timeout, err)
		}
		if d <= 0 {
			return Command{}, fmt.Errorf("hook %s timeout must be positive", ev)
		}
		timeout = d
	}
	match, err := compileMatcher(s.Matcher)
	if err != nil {
		return Command{}, fmt.Errorf("hook %s matcher %q: %w", ev, s.Matcher, err)
	}
	return Command{
		Event:   ev,
		Layer:   layer,
		If:      strings.TrimSpace(s.If),
		Command: s.Command,
		Shell:   s.Shell,
		Timeout: timeout,
		Once:    s.Once,
		Async:   s.Async,
		matcher: s.Matcher,
		match:   match,
	}, nil
}

// Applies reports whether this command should fire for a tool. A non-tool
// event ignores the matcher and always applies; a tool event consults it.
func (c Command) Applies(tool string) bool {
	if !toolEvent(c.Event) {
		return true
	}
	return c.match(tool)
}

func defaultTimeout(ev Event) time.Duration {
	switch ev {
	case SessionStart, SessionEnd, Stop, UserPromptSubmit:
		return defaultFastTimeout
	default:
		return defaultToolTimeout
	}
}

// pipeList matches a plain exact-or-pipe-list matcher: alphanumerics,
// underscores, and pipes only. Anything else is treated as a regex.
var pipeList = regexp.MustCompile(`^[A-Za-z0-9_|]+$`)

// compileMatcher turns a matcher string into a predicate against a tool name.
// An empty or "*" matcher matches every tool; a pipe list is an exact-or-list
// match; anything else is a regular expression (doc 05 section 13.4).
func compileMatcher(matcher string) (func(string) bool, error) {
	m := strings.TrimSpace(matcher)
	if m == "" || m == "*" {
		return func(string) bool { return true }, nil
	}
	if pipeList.MatchString(m) {
		set := map[string]bool{}
		for name := range strings.SplitSeq(m, "|") {
			if name != "" {
				set[name] = true
			}
		}
		return func(tool string) bool { return set[tool] }, nil
	}
	re, err := regexp.Compile(m)
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}
