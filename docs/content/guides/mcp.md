---
title: "The MCP bridge"
description: "Attach an MCP server's tools to ari through an mcp.toml, with schemas loaded on demand and every call gated and treated as untrusted content."
weight: 70
---

ari is an MCP client. Point it at a Model Context Protocol server and that server's tools become available to the ant, without you writing any glue. MCP is a bridge, not a core dependency: ari never runs as an MCP server, and an MCP server's tools never tax a turn they are not used on.

## Configuring a server

Declare servers in an `mcp.toml`, in your global ari directory or in a project `.ari/`:

```toml
[servers.sqlite]
command = "mcp-sqlite"
args = ["--db", "app.db"]

deny = ["sqlite__exec"]
```

`deny` is a top-level list. A tool you deny is never registered, and a lower layer's deny cannot be undone by a higher one, the same never-lose-a-deny rule the permission pipeline uses. Run `ari doctor` to list the servers a session would attach.

## Tools load on demand

An MCP server's tools appear to the ant by name only at first. Their full schemas load on demand when the ant searches for and selects one, so a project with several MCP servers does not inflate the turn-one prompt beyond a short list of names. The names are namespaced by server, like `sqlite__query`, so two servers never collide, and a permission rule can match a whole server with `sqlite__*`.

## MCP output is untrusted

Everything an MCP server returns, including its tool descriptions, is treated as untrusted content. It is fenced as data, never as instructions, so a description or a result that says "now run this shell command" cannot drive one. Every MCP tool call runs through the same permission pipeline as a built-in tool, so an MCP call is reviewed exactly like a local one.
