---
title: "CLI"
description: "Every ari command and the flags that shape a run: the TUI, headless -p, resume, and doctor."
weight: 10
---

Run `ari` with no arguments to open the TUI in the current repo. The commands and flags below are the full surface for M0.

## ari

Open the interactive TUI in the current directory's repo.

| Flag | Description |
| ---- | ----------- |
| `-p`, `--print <prompt>` | Run one turn headless and exit instead of opening the TUI. Pass `-` to read the prompt from stdin. |
| `--json` | With `-p`, stream the raw event schema instead of prose. |
| `--resume` | Reopen the most recent session, or with the switcher, an older one. |
| `--mode <mode>` | Permission mode for the run: `ask` (default), `auto-edit`, `full-auto`, or `plan`. Not persisted; it applies only to this run. |

Examples:

```bash
ari                                  # TUI
ari -p "summarize main.go"           # one headless turn
echo "explain this diff" | ari -p -  # prompt from stdin
ari -p --json --mode full-auto "..."  # stream events, unattended
ari --resume                          # reopen the last session
```

See the [headless guide](/guides/headless/) for the event stream and the exit-code contract, and the [permission model](/guides/permission-model/) for what each mode allows.

## ari doctor

Audit the nest, config, and any listening surface, and optionally repair the safe findings.

| Flag | Description |
| ---- | ----------- |
| `--fix` | Apply the safe repairs (tighten credential permissions, add the local-config gitignore line). Judgment calls are left for you. |
| `--audit` | Run the deeper integrity checks a reviewer would. |

```bash
ari doctor          # report
ari doctor --fix    # repair the safe findings
ari doctor --audit  # deeper checks
```

The exit code is a CI contract: 0 clean, 1 warnings only, 2 at least one critical, 3 doctor could not run. See the [doctor reference](/reference/doctor/).

## Data locations

- `~/.ari/config.toml` is the user config, `~/.ari/auth` holds credentials at `0700`.
- A project's `.ari/` holds its config, and `.ari/local.toml` is the gitignored per-checkout override.
- Sessions and the journal live under `~/.ari/projects/<key>/`, as append-only JSONL.

`ARI_HOME` overrides `~/.ari` if you want the nest somewhere else.
