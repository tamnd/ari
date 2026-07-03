// Package core is the headless colony: Open, Start, Close, and the
// SessionAPI every surface drives. The TUI, ari -p, --json, and serve are
// all clients of this package and none of them reach around it (D2).
//
// The real Colony lands with the core slice; this file exists first so
// the import-graph test that enforces D2 has a home from the first commit.
package core
