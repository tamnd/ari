---
title: "Quick start"
description: "Open ari in a repo, ask for a small refactor, watch it read, edit, and test, and resume the session later, byte-identical."
weight: 30
---

This is the walkthrough ari's own CI replays on every change. If it stops working, the release does not ship.

## Open ari in a repo

```bash
export ANTHROPIC_API_KEY=...
cd path/to/your/repo
ari
```

On first run a short onboarding explains that ari asks before it edits or runs commands and that its data lives in `.ari/`, then it writes a small config to `~/.ari/config.toml` holding only an env reference for your key. The chat opens with the editor focused, the sidebar showing the resolved model, an empty context bar, and a zero cost. Nothing has touched the repo, because ari does no work until asked.

## Ask for a refactor

Type a request and send it:

> Rename Greeting to Greet across the package and update the test, then run go test.

Here is what you see, in order:

1. A dim, collapsed reasoning block with its elapsed time, then assistant text streaming in.
2. `find` for `Greeting`, rendered as a grouped match list.
3. `read` on the files it will touch, in one parallel batch, each a file header and a bounded preview. A successful read arms the edit gate for that file.
4. A first `edit` that ari rejects because `old_string` was not unique, shown in the chat as a tool result with the occurrence count and the fix, not an error. The model adds a surrounding line and retries, and it lands.
5. A permission dialog for the edit, because the default mode is ask. It shows a real syntax-highlighted diff of the change. Approve for the session and the next edit runs without a second prompt.
6. `sh` with `go test ./...`. The pipeline asks once, showing the exact command. Allow it for the session, the tests run, and the turn finishes.

The sidebar meters every model call live, and the cache hit rate stays high because the prompt prefix is stable across the turn.

## Resume where you left off

Quit with the quit key. Next time:

```bash
ari --resume
```

ari replays the transcript from JSONL into a byte-identical chat, and a follow-up you ask lands in the same session, appended to the same file. Pick an older session from the switcher instead of the last one if you want.

## Do it without the TUI

The same turn runs headless, which is how ari drives itself in CI:

```bash
ari -p --json --mode full-auto \
  "Rename Greeting to Greet across the package and update the test, then run go test."
```

`ari -p` runs one turn and exits, `--json` streams the event schema for a downstream step, and the exit code is the turn's terminal reason. See the [headless guide](/guides/headless/) for the full contract.

Next: the [permission model](/guides/permission-model/).
