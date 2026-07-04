# Changelog

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
