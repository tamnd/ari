# House rules for the greeter module

- Always run `go test ./...` after you change any Go file, and do not call the
  change done until it passes.
- Keep every greeting leading with `Hello, <name>!` so the callers that check
  the prefix keep working.
