---
title: "Edits, diffs, and diagnostics"
description: "How ari shows a change as a unified diff and folds language-server diagnostics into the edit result so it can catch and fix its own mistakes."
weight: 30
---

Every change ari makes to a file is shown as a real unified diff, and when the language server is on, the diagnostics for the file it just touched come back in the same result. Together they let the ant catch a mistake the moment it makes it and correct it without a build step you have to run.

## Changes render as diffs

When ari edits or writes a file, the chat shows a unified diff of exactly what changed, with the changed span highlighted, not a prose summary you have to trust. The same bytes drive the TUI view and the `--json` stream, so a headless consumer sees the identical diff a person would.

## Diagnostics fold into the result

Turn the language server on in config:

```toml
[lsp]
enabled = true
```

With it enabled, an edit to a Go file touches the file through `gopls` and the error-severity diagnostics come back appended to the edit result. If the ant references a symbol it did not import, the undefined-symbol error is right there in the result it reads next, so it adds the import and edits again, and the loop closes without you running `go build`.

A missing or slow language server is never a failed edit. If `gopls` is not installed, or it does not answer in time, the change still lands and the result simply carries no diagnostics. Run `ari doctor` to see whether LSP is enabled and whether `gopls` is on your PATH.

## Only errors ride back to the model

To keep the result readable and the token cost bounded, only error-severity diagnostics are folded into the model-facing result. The sidebar still counts warnings so you can see them, but the ant's context stays focused on what would actually break the build.
