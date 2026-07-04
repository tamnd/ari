---
title: "The permission model"
description: "How the ordered pipeline decides what runs, the four modes, the session grants, and the safety floor no mode can cross."
weight: 10
---

Every tool call ari makes passes through an ordered pipeline before it runs. The order is the whole design: a stop always wins, and no convenience can undo it.

## Deny beats ask beats allow

The pipeline runs stages in a fixed order and takes the first one that decides:

1. **Deny.** A hard stop. If a call matches a deny, it is refused and nothing later can override it.
2. **Safety floor.** A built-in set of protections that hold in every mode (below).
3. **Mode.** What the current mode says to do with this kind of call: run it, or ask.
4. **Ask.** If it reaches here and a human is present, ari shows you the change and waits.
5. **Allow.** A grant you gave earlier in the session lets a matching call through without asking again.

Because deny is first and allow is last, adding an allow can never reopen something a deny closed. This is why a broad "allow for session" is safe: it can only skip a prompt, never cross a stop.

## The four modes

The mode sets how much ari asks. Set it for a run with `--mode`, or as a config default.

- **`ask`** (default). ari asks before any edit or command, showing the real syntax-highlighted diff or the exact command first. This is the mode you want when you are watching.
- **`auto-edit`.** In-tree edits run without a prompt; commands still ask. Good for a focused refactor where you trust the edits but want to see every shell call.
- **`full-auto`.** ari runs without prompts, for a task you mean to leave unattended. The safety floor still holds, so "full" does not mean "anything".
- **`plan`.** ari reads and plans but makes no changes. A dry run for a change you are not ready to land.

## Session grants

When ari asks, you can approve just this call, approve for the rest of the session, or deny. An "allow for session" grant lives only in memory for that session. ari never writes a permission rule for itself into your config, so a grant cannot silently persist across runs. Start a new session and you start from the mode's defaults again.

## The safety floor

Underneath every mode sits a floor that no mode, and no grant, can cross. ari will not:

- edit its own binary,
- edit its nest (`~/.ari` and the project's `.ari/`),
- edit your VCS internals (`.git` and friends),
- edit your shell startup files.

full-auto obeys this floor exactly as ask does. The floor is what makes an unattended run something you can actually walk away from.

## Headless is deny by default

When there is no human to ask, ari does not hang on a dialog and it does not silently allow. A headless turn that reaches the ask stage is denied. If you want a headless run to make changes, widen it on purpose with `--mode full-auto` (or `auto-edit` for edits only). The [headless guide](/guides/headless/) covers this.

## Audit it

`ari doctor` checks the permission default among other things, and warns if a config sets `full-auto` as the standing mode. See the [doctor reference](/reference/doctor/).
