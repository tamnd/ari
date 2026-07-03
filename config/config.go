// Package config loads ari's TOML configuration with a fixed precedence:
// built-in defaults, then the user file (~/.ari/config.toml), then the
// project file (.ari/config.toml), then the gitignored local file
// (.ari/local.toml), then flags for the run. The merge is per key, each
// resolved value remembers which source set it so ari doctor can say, and
// a bad config is refused at load, never silently defaulted.
package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/tamnd/ari/nest"
)

// Provider is one configured endpoint.
type Provider struct {
	Kind    string `toml:"kind"`     // anthropic or openai
	BaseURL string `toml:"base_url"` //
	APIKey  string `toml:"api_key"`  // ${NAME} interpolates from env at load
}

// Target is one link in a tier's failover chain.
type Target struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	TTL      string `toml:"ttl,omitempty"` // prompt-cache ttl, Anthropic only
}

// Tier is an ordered failover chain. An ant card names a tier, never a
// model, so a model swap is a one-line config edit (D17).
type Tier struct {
	Chain []Target `toml:"chain"`
}

// Embeddings names the vector endpoint doc 07's memory uses.
type Embeddings struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	Dim      int    `toml:"dim"`
}

// Colony holds the pool sizes and budgets. Zero values defer to defaults.
type Colony struct {
	MaxAwake int `toml:"max_awake"`
}

// UI holds the theme and keymap choices.
type UI struct {
	Theme  string `toml:"theme"`
	Keymap string `toml:"keymap"`
}

// Journal holds event-log retention.
type Journal struct {
	RetainDays int `toml:"retain_days"`
}

// Automation is off by default in the shape itself: the zero value is
// disabled, so a fresh install has no heartbeat, no cron, no watchers
// until a user writes them on (D16, D19).
type Automation struct {
	Enabled bool `toml:"enabled"`
}

// Config is the fully merged, resolved configuration for one run.
type Config struct {
	Providers  map[string]Provider `toml:"provider"`
	Tiers      map[string]Tier     `toml:"tier"`
	Embeddings Embeddings          `toml:"embeddings"`
	Colony     Colony              `toml:"colony"`
	UI         UI                  `toml:"ui"`
	Journal    Journal             `toml:"journal"`
	Automation Automation          `toml:"automation"`

	// Mode is the permission mode for the run; flags-only, not persisted.
	Mode string `toml:"-"`

	origins  map[string]string
	warnings []string
}

// FlagOverrides carries the per-run flag values that sit above the files.
type FlagOverrides struct {
	Model string // overrides the frontier tier's first target for the run
	Mode  string // permission mode
	Theme string
}

// Origin reports which source set a top-level key: default, user,
// project, local, or flag. Doctor uses this for provenance.
func (c *Config) Origin(key string) string {
	if o, ok := c.origins[key]; ok {
		return o
	}
	return "default"
}

// Warnings lists non-fatal load notes, such as unknown keys a later
// milestone's config may carry; an unknown key warns, never crashes.
func (c *Config) Warnings() []string { return c.warnings }

// Load resolves the file precedence for the nest, applies flag overrides,
// interpolates ${ENV} references, and validates. Validation reports every
// problem at once so a user fixes config in one pass.
func Load(n nest.Nest, flags FlagOverrides) (*Config, error) {
	c := defaults()
	c.origins = map[string]string{}

	for _, src := range []struct{ name, path string }{
		{"user", n.GlobalConfig()},
		{"project", n.ProjectConfig()},
		{"local", n.LocalConfig()},
	} {
		if err := c.mergeFile(src.name, src.path); err != nil {
			return nil, err
		}
	}

	if flags.Mode != "" {
		c.Mode = flags.Mode
		c.origins["mode"] = "flag"
	}
	if flags.Theme != "" {
		c.UI.Theme = flags.Theme
		c.origins["ui.theme"] = "flag"
	}
	if flags.Model != "" {
		c.overrideModel(flags.Model)
		c.origins["tier.frontier"] = "flag"
	}

	if err := c.interpolate(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// defaults is the config a fresh install runs with: an Anthropic frontier
// chain keyed from the environment and a local Ollama fallback, so a
// machine with only ANTHROPIC_API_KEY set works with no file at all.
func defaults() *Config {
	return &Config{
		Providers: map[string]Provider{
			"anthropic": {Kind: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "${ANTHROPIC_API_KEY}"},
			"ollama":    {Kind: "openai", BaseURL: "http://localhost:11434/v1"},
		},
		Tiers: map[string]Tier{
			"frontier": {Chain: []Target{
				{Provider: "anthropic", Model: "claude-opus-4-8", TTL: "5m"},
				{Provider: "anthropic", Model: "claude-sonnet-5", TTL: "5m"},
			}},
			"mid": {Chain: []Target{
				{Provider: "anthropic", Model: "claude-sonnet-5", TTL: "5m"},
				{Provider: "anthropic", Model: "claude-haiku-4-5"},
			}},
			"cheap": {Chain: []Target{
				{Provider: "anthropic", Model: "claude-haiku-4-5"},
			}},
			"local": {Chain: []Target{
				{Provider: "ollama", Model: "qwen3-coder:30b"},
			}},
		},
		Embeddings: Embeddings{Provider: "ollama", Model: "nomic-embed-text", Dim: 768},
		Colony:     Colony{MaxAwake: 0}, // 0 means size to the machine
		UI:         UI{Theme: "dark"},
		Journal:    Journal{RetainDays: 30},
		Mode:       "ask",
	}
}

// mergeFile overlays one TOML file, per key. A missing file is fine; an
// unknown key is a warning, not an error, so a config written for a later
// milestone opens in this one.
func (c *Config) mergeFile(source, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	var layer Config
	meta, err := toml.Decode(string(data), &layer)
	if err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	var unknown []string
	for _, k := range meta.Undecoded() {
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		c.warnings = append(c.warnings, fmt.Sprintf("%s: unknown keys ignored: %s", path, strings.Join(unknown, ", ")))
	}

	for id, p := range layer.Providers {
		c.Providers[id] = p
		c.origins["provider."+id] = source
	}
	for name, t := range layer.Tiers {
		c.Tiers[name] = t
		c.origins["tier."+name] = source
	}
	if layer.Embeddings != (Embeddings{}) {
		c.Embeddings = layer.Embeddings
		c.origins["embeddings"] = source
	}
	if layer.Colony.MaxAwake != 0 {
		c.Colony.MaxAwake = layer.Colony.MaxAwake
		c.origins["colony.max_awake"] = source
	}
	if layer.UI.Theme != "" {
		c.UI.Theme = layer.UI.Theme
		c.origins["ui.theme"] = source
	}
	if layer.UI.Keymap != "" {
		c.UI.Keymap = layer.UI.Keymap
		c.origins["ui.keymap"] = source
	}
	if layer.Journal.RetainDays != 0 {
		c.Journal.RetainDays = layer.Journal.RetainDays
		c.origins["journal.retain_days"] = source
	}
	if layer.Automation.Enabled {
		c.Automation.Enabled = true
		c.origins["automation.enabled"] = source
	}
	return nil
}

// overrideModel points the frontier tier's head at one model for the run.
func (c *Config) overrideModel(model string) {
	t := c.Tiers["frontier"]
	if len(t.Chain) == 0 {
		t.Chain = []Target{{Provider: "anthropic"}}
	}
	head := t.Chain[0]
	head.Model = model
	t.Chain = append([]Target{head}, t.Chain[1:]...)
	c.Tiers["frontier"] = t
}

var envRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// interpolate replaces ${NAME} values with the environment variable NAME,
// at load time and only here. A missing variable for a configured
// provider fails with the variable's name, never its value (D16).
func (c *Config) interpolate() error {
	var missing []string
	for id, p := range c.Providers {
		m := envRef.FindStringSubmatch(p.APIKey)
		if m == nil {
			continue
		}
		v, ok := os.LookupEnv(m[1])
		if !ok {
			// Only fatal if some tier actually routes to this provider.
			if c.referenced(id) {
				missing = append(missing, fmt.Sprintf("provider %q needs env %s", id, m[1]))
			}
			p.APIKey = ""
		} else {
			p.APIKey = v
		}
		c.Providers[id] = p
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("config: %s", strings.Join(missing, "; "))
	}
	return nil
}

func (c *Config) referenced(provider string) bool {
	for _, t := range c.Tiers {
		for _, tg := range t.Chain {
			if tg.Provider == provider {
				return true
			}
		}
	}
	return c.Embeddings.Provider == provider
}

// validate checks the whole config and reports every problem at once.
func (c *Config) validate() error {
	var problems []string
	for name, t := range c.Tiers {
		if len(t.Chain) == 0 {
			problems = append(problems, fmt.Sprintf("tier %q has an empty chain", name))
		}
		for i, tg := range t.Chain {
			p, ok := c.Providers[tg.Provider]
			if !ok {
				problems = append(problems, fmt.Sprintf("tier %q link %d references undefined provider %q", name, i, tg.Provider))
				continue
			}
			if tg.Model == "" {
				problems = append(problems, fmt.Sprintf("tier %q link %d (%s) has an empty model", name, i, tg.Provider))
			}
			if p.Kind != "anthropic" && p.Kind != "openai" {
				problems = append(problems, fmt.Sprintf("provider %q has unknown kind %q", tg.Provider, p.Kind))
			}
		}
	}
	if _, ok := c.Providers[c.Embeddings.Provider]; c.Embeddings.Provider != "" && !ok {
		problems = append(problems, fmt.Sprintf("embeddings references undefined provider %q", c.Embeddings.Provider))
	}
	switch c.Mode {
	case "ask", "auto-edit", "full-auto", "plan":
	default:
		problems = append(problems, fmt.Sprintf("unknown permission mode %q", c.Mode))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}
