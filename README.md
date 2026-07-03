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

Not released yet. Build from source:

```
go install github.com/tamnd/ari@latest
```

## License

MIT
