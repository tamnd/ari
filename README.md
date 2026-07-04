# ari

ari (アリ, ant) is a coding agent for the terminal, shaped like an ant colony.

One binary carries a headless core and every surface as a client of it: a TUI you live in, `ari -p` for scripts and CI, and later an HTTP surface.
Today it is one excellent ant: six tools, a strict edit gate, a permission pipeline that shows you the real diff or the real command before anything runs, and sessions that resume byte-identical from plain JSONL.
The colony (a queen that routes work to specialist ants, shared memory, eval-gated evolution) arrives milestone by milestone on seams that are already in the code.

Status: pre-release, building toward v0.1.0.

## Principles

- Secure by default. ari asks before it edits or runs anything, secrets stay out of the model's sight, and nothing phones home.
- Honest metering. Every model call lands in a ledger with tokens and dollars, shown in the sidebar.
- Headless core. The TUI is a client. Anything the TUI can do, `ari -p` can do in CI.
- Plain files. Sessions are append-only JSONL under `~/.ari`, diffable and crash-safe.

## Install

Build from source:

```
go install github.com/tamnd/ari@latest
```

Packaged channels land with the first tagged release: Homebrew on macOS, Scoop on Windows, apt and dnf on Linux, and a container image on GHCR.

## First run

Open ari in the root of a repo:

```
ari
```

On first run a short onboarding explains that ari asks before it edits or runs commands and that its data lives in `.ari/`, then it writes a small config to `~/.ari/config.toml` with only an env reference for your API key, never the key itself. Set the key in your environment first:

```
export ANTHROPIC_API_KEY=...   # or OPENAI_API_KEY, or an OpenRouter key
```

Then the chat opens with the editor focused, the sidebar showing the resolved model, an empty context bar, and a zero cost. Nothing has touched the repo, because ari does no work until asked.

### The walkthrough

Ask for a small refactor:

> Rename Greeting to Greet across the package and update the test, then run go test.

Here is what you see, and it is the same sequence the demo fixture replays in CI:

1. A dim, collapsed reasoning block with its elapsed time, then assistant text streaming in.
2. `find` for `Greeting`, rendered as a grouped match list.
3. `read` on the files it will touch, in one parallel batch, each a file header and a bounded preview. A successful read arms the edit gate for that file.
4. A first `edit` that ari rejects because `old_string` was not unique, shown in the chat as a tool result with the occurrence count and the fix, not an error. The model adds a surrounding line and retries, and it lands.
5. A permission dialog for the edit, because the default mode is ask. It shows a real syntax-highlighted diff of the change. Approve for the session and the next edit runs without a second prompt.
6. `sh` with `go test ./...`. The pipeline asks once, showing the exact command. Allow it for the session, the tests run, and the turn finishes.

The sidebar meters every model call live, and the cache hit rate stays high because the prompt prefix is stable across the turn.

### Resume

Quit with the quit key. Next time, `ari --resume` (or pick the session from the switcher) replays the transcript from JSONL into a byte-identical chat, and you ask a follow-up in the same session, appended to the same file.

## The permission model

Every tool call passes through an ordered pipeline before it runs. Deny beats ask beats allow because deny is the first stage and allow is near the last, so no convenience can undo a stop. In the middle sits a safety floor that no mode can cross: ari will not edit its own binary, the nest, your VCS internals, or your shell startup files, whatever mode you are in.

Four modes set how much ari asks:

- `ask` (default): ari asks before any edit or command, and shows you the real diff or the exact command first.
- `auto-edit`: in-tree edits run without a prompt; commands still ask.
- `full-auto`: ari runs without prompts, for a run you mean to leave unattended. The safety floor still holds.
- `plan`: ari plans and reads but makes no changes.

Set the mode for a run with `--mode`, or as a config default. A grant you give in the chat (allow for session) lasts the session and is never a rule ari wrote for itself.

## Config

Config is TOML with a fixed precedence: built-in defaults, then `~/.ari/config.toml`, then the project's `.ari/config.toml`, then the gitignored `.ari/local.toml`, then flags for the run. A fresh install with only an API key in the environment runs with no config file at all.

An API key is always a reference, never a literal:

```toml
[provider.anthropic]
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key = "${ANTHROPIC_API_KEY}"

[tier.frontier]
chain = [{ provider = "anthropic", model = "claude-opus-4-8" }]
```

A tier is a failover chain, and an ant names a tier rather than a model, so swapping models is a one-line edit. A key a later milestone does not know about is a warning, not a crash.

## Headless: ari -p

`ari -p` runs one turn and exits, using the same core, loop, tools, and ledger the TUI drives:

```
ari -p "read main.go and summarize it"
```

The prompt can also come from stdin, so ari composes in a pipeline:

```
echo "summarize the build failure" | ari -p -
```

`--json` streams the raw event schema, a `hello` first and then the turn, so a downstream step can reconstruct the transcript or resume from a sequence cursor:

```
ari -p --json --mode full-auto "rename Greeting to Greet, then run go test"
```

A headless turn that would need a human auto-denies rather than hanging, so a CI run never stalls on a dialog. Widen it deliberately with `--mode full-auto` for a run you have decided to leave unattended. The exit code is the terminal reason, so a CI step can branch on success without parsing output.

## ari doctor

`ari doctor` audits the nest, the config, and any listening surface for the mistakes that turn a coding agent into someone else's remote shell. It checks credential-directory permissions, a literal secret in config, config health, the permission mode default, whether `.ari/local.toml` is gitignored, the listening surface, and journal continuity.

```
ari doctor          # report
ari doctor --fix    # apply the safe repairs, leave the judgment calls
ari doctor --audit  # the deeper integrity checks a reviewer would run
```

The exit code is a CI contract: 0 clean, 1 warnings only, 2 at least one critical, 3 doctor could not run. Wire it into a pre-merge check to fail on a committed secret or a loose credential file.

## License

MIT
