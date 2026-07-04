# Changelog

## v0.3

The headline is colony memory: the ant now remembers across sessions. Each
project gets its own database, kept outside the repo, that the ant fills as it
works and reads back the next time you open the same codebase. Where an `ARI.md`
house rule is memory you write, this is memory the ant earns.

Memory is deliberately hard to poison. A proposal is queued, not stored; only
consolidation, the fold that runs between turns, writes a live memory after
weighing it against what is already known. Merging a cluster takes the strongest
single note's importance, never the sum, so saying the same thing ten times buys
no rank. And the load-bearing memories render into a pinned index the ant carries
at the head of every prompt, rebuilt only at a fold boundary, so the prompt prefix
stays stable and the model's cache keeps paying off.

- Three memory tools: `remember` proposes a memory for the next fold, `recall`
  runs a hybrid full-text and vector search ranked by relevance, recency, and
  importance, and `forget` archives a row without ever deleting it.
- `ari memory export` renders a namespace to markdown you can edit, and `ari
  memory import` reads it back: an edited body updates the row and marks it
  read-only, a new block becomes a memory, a deleted block archives its row.
- Press `ctrl+r` in the TUI for the memory panel: the live pinned index, a
  search over archival memory, and a tail of the fold log so you watch
  consolidation happen.
- `ari doctor` gained a colony memory check: the database is outside the repo, at
  the head schema version, and its write-ahead log is healthy. A `colony.db` in a
  committable path is flagged critical, because a memory file in a commit is a
  memory file in every clone.

## v0.2

The headline is the self-correcting edit loop. Turn the language server on and
ari catches its own mistakes as it makes them: an edit that references a symbol
it did not import comes back with the diagnostic attached, so the ant reads it
and fixes the import without you running a build. Everything else in this
release supports that loop or extends the same idea to another surface.

Each new surface is opt-in. Upgrading turns nothing on by itself; you choose
what to enable.

- Edits and writes render as real unified diffs, with the changed span
  highlighted, in the TUI and in the `--json` stream alike.
- Language-server diagnostics fold into the edit and write results when you set
  `lsp.enabled` in config and have `gopls` on your PATH. A missing or slow
  server is zero diagnostics, never a failed edit.
- Skills package a repeatable procedure as a Markdown file you invoke by name
  or run as a slash command. The full body loads only on use, so a repo full of
  skills never taxes a turn.
- Hooks run your own commands at lifecycle points. A cloned repo's hooks do not
  run until you trust the workspace with `ari trust`; there is no auto-trust and
  the gate fails closed.
- Project memory: an `ARI.md` at the repo root gives the ant standing house
  rules every session, capped per file so a rule you wrote is a rule in force.
- The MCP bridge attaches a configured server's tools through an `mcp.toml`.
  Schemas load on demand, every call is gated, and MCP output is treated as
  untrusted content.
- The headless `--json` stream carries all of it as newline-delimited events, so
  a CI job sees the diagnostic, the corrective edit, and the passing test in
  order.
- `ari doctor` gained M1 checks: an oversized `ARI.md`, whether LSP is enabled
  and `gopls` is found, and the MCP servers a session would attach.

## v0.1

First release. One ant, six tools, the permission pipeline with its safety
floor, the JSONL session store and event journal, the TUI, and the headless
`ari -p`. `ari doctor` ships from day one.
