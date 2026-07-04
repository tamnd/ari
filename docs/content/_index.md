---
title: "ari"
description: "ari (アリ, ant) is a coding agent for the terminal, shaped like an ant colony. One binary carries a headless core and every surface as a client of it, a TUI you live in and ari -p for scripts and CI. Secure by default, honest about cost, and built on plain diffable files."
heroTitle: "A coding agent shaped like a colony"
heroLead: "ari asks before it edits or runs anything, keeps your secrets out of the model's sight, and never phones home. One binary is both the TUI you live in and the headless core that ari -p drives in CI. Sessions are append-only JSONL you can diff, and every model call lands in a ledger with tokens and dollars."
heroPrimaryURL: "/getting-started/quick-start/"
heroPrimaryText: "Get started"
---

Most coding agents ask you to trust them with a shell, a diff you cannot see, and an API key sitting in a config file. ari (アリ, "ant") is built the other way around. Every tool call passes through an ordered permission pipeline that shows you the real diff or the exact command before anything runs, a safety floor stops edits to your binary, your VCS internals, and your shell startup files in every mode, and an API key is always a reference to an environment variable, never a literal ari can read back to a model.

Today ari is one excellent ant: six tools, a strict read-before-write edit gate, and sessions that resume byte-identical from plain JSONL. The colony (a queen that routes work to specialist ants, shared memory, eval-gated evolution) arrives milestone by milestone on seams that are already in the code.

```bash
ari                         # open the TUI in a repo
ari -p "summarize main.go"  # one headless turn, then exit
ari doctor --fix            # audit the nest and repair the safe findings
```

## What it does

- **Asks before it acts.** In the default mode ari shows you a syntax-highlighted diff for every edit and the exact command for every shell call, and waits. Deny beats ask beats allow, so no convenience grant can undo a stop.
- **Keeps secrets out of sight.** Keys come from the environment or the OS keychain, are redacted from events and logs, and never reach the model. A literal key in a config file is a doctor finding, not a working setup.
- **Meters honestly.** Every model call lands in a ledger with input and output tokens and a dollar cost, shown live in the sidebar. Nothing is estimated after the fact.
- **Runs headless.** The TUI is a client of the same core that `ari -p` drives. Anything you can do in the chat, a CI step can do with `ari -p --json`, and the exit code is the turn's terminal reason.
- **Keeps plain files.** Sessions are append-only JSONL under `~/.ari`, diffable and crash-safe, and a resume replays them byte-identical.
- **Audits itself.** `ari doctor` checks the mistakes that turn a coding agent into someone else's remote shell: loose credential permissions, a committed secret, an ungitignored local config, a broken permission default.

## Where to go next

- New here? Start with the [introduction](/getting-started/introduction/), then the [quick start](/getting-started/quick-start/).
- Want to install it? See [installation](/getting-started/installation/).
- Care about safety? The [permission model](/guides/permission-model/) explains the pipeline, the four modes, and the safety floor.
- Automating it? The [headless guide](/guides/headless/) covers `ari -p`, `--json`, and the exit-code contract.
- Need every flag? The [CLI reference](/reference/cli/) is the full surface.
