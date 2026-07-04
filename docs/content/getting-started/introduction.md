---
title: "Introduction"
description: "How ari thinks about a turn: ask before acting, show the real diff or command, keep secrets out of the model, and meter every call."
weight: 10
---

ari is a coding agent that runs in your terminal. You describe a change, and ari reads the code, proposes edits and commands, and carries them out. The difference from most agents is where the trust sits: ari does nothing to your repo until you have seen exactly what it wants to do.

## A turn, step by step

A turn is one exchange: your prompt, the model's work, and the result. Inside a turn ari runs a loop. The model thinks, calls a tool, sees the result, and calls the next one, until it has nothing left to do. You watch it happen live.

Six tools make up the surface:

- `read` opens a file and shows a bounded preview. A successful read is what arms the edit gate for that file.
- `find` searches the tree for text or a glob.
- `edit` replaces an exact, unique string in a file that was read first.
- `write` creates or overwrites a file.
- `sh` runs a shell command.
- `fetch` retrieves a URL.

Two rules shape every turn. First, ari will not edit a file it has not read in the session, so it never writes blind. Second, an `edit` must match exactly one place in the file; a non-unique match is rejected with the occurrence count, and the model corrects itself by adding surrounding context. You see both of these as ordinary tool results in the chat, not as errors.

## Asking first

In the default `ask` mode, every edit and every command pauses for you. An edit shows a real syntax-highlighted diff of the change. A command shows the exact string that will run. You approve once, or approve for the rest of the session, or deny. Nothing runs on your behalf that you did not see.

Other modes widen this deliberately. `auto-edit` lets in-tree edits run without a prompt while commands still ask. `full-auto` runs without prompts for a task you mean to leave unattended. `plan` reads and plans but changes nothing. Whatever the mode, a safety floor holds: ari will not touch its own binary, its nest, your VCS internals, or your shell startup files. The [permission model](/guides/permission-model/) covers this in full.

## Secrets and cost

An API key is a reference to an environment variable, never a literal in a file. ari reads it from your environment at startup, keeps it out of every event and log, and never puts it in the model's context. If a key ends up written into a config file, `ari doctor` flags it.

Every model call is metered. The sidebar shows the running token count and dollar cost for the session as it happens, drawn from a ledger, not an estimate. You always know what a turn cost.

## Plain files

A session is an append-only JSONL file under `~/.ari`. You can read it, diff it, and commit it. When you resume, ari replays the file into a byte-identical chat and appends your next turn to the same file. Nothing about your history is hidden in a binary format.

Next: [installation](/getting-started/installation/).
