---
title: "Project memory with ARI.md"
description: "Give the ant standing house rules for a repo in an ARI.md file it reads every session, with a size cap so a rule you wrote is a rule in force."
weight: 60
---

An `ARI.md` at the root of a repo is its project memory: the standing house rules the ant reads every session, so you do not repeat them in every prompt. Put the things that are always true of this codebase there.

```markdown
# House rules

- Always run `go test ./...` after a change, and do not call it done until it passes.
- Keep every public function documented.
```

## What to put in it

Rules that hold across the whole repo and every task: the test command to run, the formatting to keep, the conventions a change must follow. ari also honors an `AGENTS.md` or `CLAUDE.md` for compatibility, so a repo already set up for another agent works without a rewrite. When more than one is present, `ARI.md` is the native name and wins.

## The size cap

Each memory file is trimmed to a per-file cap before it is injected, so a runaway `ARI.md` cannot quietly eat the model's context window. That cap is also why `ari doctor` warns when `ARI.md` is over it: a rule past the cap is one the ant only partly reads, which means a rule you think is in force is not. Keep it tight, and move long procedures into a skill the ant loads on demand.
