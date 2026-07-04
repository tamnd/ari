---
title: "Headless with ari -p"
description: "Run one turn and exit, stream the event schema with --json, take a prompt from stdin, and branch on the exit code in CI."
weight: 20
---

`ari -p` runs a single turn and exits, using the same core, loop, tools, permission pipeline, and ledger the TUI drives. It is the way to put ari in a script or a CI job.

## One turn, then exit

```bash
ari -p "read main.go and summarize it"
```

ari runs the turn, prints the assistant's final text, and exits. No TUI, no session you have to quit.

## A prompt from stdin

Pass `-` as the prompt to read it from standard input, so ari composes in a pipeline:

```bash
git log -1 --format=%B | ari -p -
echo "summarize the build failure" | ari -p -
```

## Stream the events with --json

`--json` streams the raw event schema instead of prose: a `hello` frame first, then the turn's events. A downstream step can reconstruct the transcript, or resume from a sequence cursor.

```bash
ari -p --json "list the exported functions in this package"
```

Each line is one event with a monotonic sequence number, so a consumer can detect a gap and knows it saw the whole turn.

## Modes in headless

When there is no human to ask, a turn that would need approval is denied, not queued and not silently allowed, so a CI run never stalls on a dialog. To let a headless run make changes, choose the mode on purpose:

```bash
# edits without prompts, commands still denied at the ask stage
ari -p --mode auto-edit "fix the failing test in ./parser"

# fully unattended; the safety floor still holds
ari -p --json --mode full-auto \
  "rename Greeting to Greet across the package, then run go test"
```

## The exit code is the contract

The process exit code is the turn's terminal reason, so a CI step can branch without parsing output:

| Code | Meaning |
| ---- | ------- |
| 0 | ok |
| 1 | internal error |
| 2 | config error |
| 3 | nest error |
| 4 | provider error |
| 5 | permission denied |
| 6 | budget exceeded |
| 7 | canceled |

A run that was blocked by the permission pipeline exits 5, so a job that expected an unattended change can tell "the model declined" from "ari was not allowed to". `ari doctor` uses a separate contract for its own audit result; see the [doctor reference](/reference/doctor/).

## A CI job

```yaml
- name: Let ari take a pass
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
  run: |
    ari -p --json --mode full-auto \
      "apply the TODO in ./parser and run go test ./..." \
      | tee turn.jsonl
```

The key is an environment reference the job provides, ari keeps it out of the streamed events, and the exit code decides whether the step passed.
