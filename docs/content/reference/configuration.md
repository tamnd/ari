---
title: "Configuration"
description: "The config file, its layered precedence from defaults to flags, providers and tiers, and the rule that a key is always an env reference."
weight: 20
---

ari's config is TOML. A fresh install with only an API key in the environment runs with no config file at all; you add a file only to change a default.

## Precedence

Settings resolve in a fixed order, later layers overriding earlier ones:

1. Built-in defaults.
2. `~/.ari/config.toml`, the user config.
3. `<repo>/.ari/config.toml`, the project config, committed and shared.
4. `<repo>/.ari/local.toml`, the per-checkout override, gitignored.
5. Flags for the run.

The permission mode is the exception: it is a flag only (`--mode`) and is never read from or written to a file, so a checkout cannot ship a standing `full-auto`. `ari doctor` warns if it finds one set anyway.

## A key is always a reference

An API key is written as a reference to an environment variable, never as a literal:

```toml
[provider.anthropic]
kind     = "anthropic"
base_url = "https://api.anthropic.com"
api_key  = "${ANTHROPIC_API_KEY}"
```

ari resolves `${ANTHROPIC_API_KEY}` from the environment at startup, keeps it out of every event and log, and never puts it in the model's context. A missing variable is an error that names the variable, never a value. A literal key written into a file is a critical `ari doctor` finding.

## Providers and tiers

A provider is an endpoint and a dialect. A tier is a named failover chain of provider-and-model links, and an ant names a tier rather than a model, so swapping models is a one-line edit.

```toml
[provider.anthropic]
kind     = "anthropic"
base_url = "https://api.anthropic.com"
api_key  = "${ANTHROPIC_API_KEY}"

[provider.openai]
kind     = "openai"
base_url = "https://api.openai.com/v1"
api_key  = "${OPENAI_API_KEY}"

[tier.frontier]
chain = [
  { provider = "anthropic", model = "claude-opus-4-8" },
  { provider = "openai",    model = "gpt-5" },
]
```

If the first link errors, ari falls to the next. A provider or setting a later milestone does not recognize is a warning, not a crash, so a newer config still runs on an older binary.

## Inspecting what resolved

The TUI sidebar shows the model that actually resolved for the run. `ari doctor` reports config health: a load error is critical, and any warning from an unknown key or a shadowed setting is surfaced as a warning. See the [doctor reference](/reference/doctor/).
