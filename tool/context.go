package tool

import (
	"context"
	"os/exec"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/lsp"
)

// AntID identifies the calling ant, for scoping side effects and
// attributing tokens in the ledger.
type AntID string

// Journal is the slice of the append-only log a tool may write to. The
// colony's journal satisfies it.
type Journal interface {
	Append(e event.Event) event.Event
}

// Sandbox wraps a command so it runs under OS-level isolation. The
// concrete seatbelt, namespace, or container implementation lives in
// doc 14; sh only knows this seam.
type Sandbox interface {
	// Wrap returns a command configured to run under the sandbox, or the
	// command unchanged when the sandbox is disabled for this invocation.
	Wrap(ctx context.Context, cmd *exec.Cmd, spec SandboxSpec) (*exec.Cmd, error)
}

// SandboxSpec is the policy one invocation runs under.
type SandboxSpec struct {
	Cwd          string   // the one directory writes are allowed under, by default
	AllowNet     bool     // whether the command may reach the network
	AllowWrite   []string // extra writable paths beyond cwd
	DisableForce bool     // an explicit escape hatch, gated by permission (doc 05)
}

// ToolContext is the session-scoped surface a tool is allowed to touch.
// The loop builds it per turn; a tool never reaches past it into
// globals. Inject a temp dir Cwd, a fresh FileState, and a fake clock,
// and a tool is fully exercisable without a real session (doc 04
// section 2.3).
type ToolContext struct {
	// Cwd is the working directory sh inherits and paths resolve
	// against. It persists across sh calls within a session; shell state
	// does not.
	Cwd string

	// Files is the read-before-write file-state map (doc 04 section 10).
	// read updates it, edit and write consult and refresh it.
	Files *FileState

	// Ant identifies the calling ant so the tool can scope side effects
	// and the ledger can attribute tokens.
	Ant AntID

	// Sandbox is the seam sh runs commands through (doc 14). Nil in a
	// trusted local run means "no sandbox"; non-nil wraps the command.
	Sandbox Sandbox

	// Spill is where oversized results are written (doc 04 section 3).
	Spill SpillStore

	// Journal records tool events for the append-only log (doc 01).
	Journal Journal

	// LSP is the language-server seam edit, write, and read use to fold
	// compiler diagnostics into their results and to warm the server (doc
	// 04 sections 2, 3, 6). Nil means no language server: every tool
	// degrades to no diagnostics, never a failed edit.
	LSP lsp.LSPClient

	// Now is injected so tests and replay sets are deterministic.
	Now func() time.Time
}

// Diagnostic re-exports the language-server finding type so the typed tool
// displays carry diagnostics without the UI having to import the lsp
// package: the import-graph guard lets the UI reach the core only through
// event and tool (doc 02 section 1). It is an alias, so a value flows
// between tool and lsp with no conversion.
type Diagnostic = lsp.Diagnostic

// projectDiagnoser is the optional half of the LSP seam write reaches for
// when it looks outward: the whole set of other-file diagnostics after a
// whole-file overwrite. The lsp Service satisfies it; a client that does
// not simply skips the project-wide report.
type projectDiagnoser interface {
	ProjectDiagnostics(exclude string) map[string][]lsp.Diagnostic
}
