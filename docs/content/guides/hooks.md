---
title: "Hooks and workspace trust"
description: "Run your own commands at lifecycle points, and the trust gate that keeps a cloned repo's hooks from running until you say so."
weight: 50
---

A hook is a command you register to run at a point in the ant's lifecycle: before or after a tool call, when a turn finishes, when the ant would stop. Hooks let you enforce a house rule with your own code, like running `gofmt` after every edit or blocking a commit that fails a check.

## Registering a hook

Declare hooks in config, keyed by the event they fire on:

```toml
[[hook.PostToolUse]]
command = "gofmt -w ."
```

A hook can also gate a tool call: a hook on `PreToolUse` that exits non-zero blocks the call it fired on. A hook can never widen a permission, only narrow it, so a hook cannot turn a denied call into an allowed one.

## The trust gate

Hooks are arbitrary code, so a repo you just cloned must not be able to run its committed hooks on your machine without your say-so. Repo hooks do not run until you explicitly trust the workspace:

```bash
ari trust
```

There is no auto-trust, and a missing or corrupt trust record reads as untrusted, so the gate fails closed. Hooks you wrote in your own global config always run, because you wrote them. Run `ari doctor` to see which repo hooks a workspace carries and whether it is trusted.

## The safety floor still holds

A hook runs inside the same permission model as everything else. It cannot escalate a call past the safety floor, a blocking hook that loops is caught by a spiral guard and surfaced rather than hanging the turn, and anything a hook prints that looks like a secret is redacted from the transcript. Trusting a workspace lets its hooks run; it does not hand them the keys.
