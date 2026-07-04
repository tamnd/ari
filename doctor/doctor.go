// Package doctor is ari's hardening checker, shipped in the product from
// day one rather than written after a crisis (doc 14 section 12, D16). It
// runs a fixed list of independent checks over the nest, the config, and
// the listening surface, each producing a status, a plain reason, and
// where it is safe a fix. The check list is small in M0 because the only
// listening surface is the serve stub, but the audit habit and the --fix
// shape land now so M5's real surface arrives into a doctor that already
// exists (doc 00 section 8, the OpenClaw lesson).
package doctor

import (
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/nest"
)

// Status is a finding's severity. The order matters: a Report's exit code
// is the worst status, so Critical must sort above Warn above OK.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusCritical
)

// String renders a status for the report header.
func (s Status) String() string {
	switch s {
	case StatusCritical:
		return "critical"
	case StatusWarn:
		return "warn"
	default:
		return "ok"
	}
}

// Context is what every check reads. It is assembled once by the caller so
// a check never touches the environment directly and a test can point the
// whole thing at a temp nest. LoadErr carries a config that would not load,
// so the config-health check can report it without every other check
// crashing on a nil Config.
type Context struct {
	Nest    nest.Nest
	Config  *config.Config
	LoadErr error
	// Audit turns on the deeper checks that --audit runs; in M0 it only
	// deepens the journal check, which is the seam the section 11 hash
	// chain will fill in later.
	Audit bool
}

// Finding is one check's verdict. Fix, when non-nil, is the safe
// remediation --fix runs; Manual is the guidance printed when the right
// action is a judgment call only the operator can make (section 12.3).
type Finding struct {
	Check  string
	Status Status
	Reason string
	Fix    func(ctx *Context) error
	Manual string
}

// Check is one doctor check. It never mutates; the remediation lives on the
// Finding it returns, so running the checks and applying fixes are two
// separate passes.
type Check struct {
	Name string
	Run  func(ctx *Context) Finding
}

// Report is the aggregate of every check, in fixed order so the output is
// stable and golden-testable (D23).
type Report struct {
	Findings []Finding
}

// Worst returns the highest severity in the report, which is what the exit
// code maps from (section 12.4).
func (r Report) Worst() Status {
	worst := StatusOK
	for _, f := range r.Findings {
		if f.Status > worst {
			worst = f.Status
		}
	}
	return worst
}

// Doctor holds the ordered check list.
type Doctor struct {
	checks []Check
}

// New builds a doctor with the check list in its fixed order. M1 adds the
// three checks that audit the surface this milestone opened: an oversized
// ARI.md the ant only partly reads, the language-server opt-in and whether
// gopls is present, and the MCP servers a session would attach.
func New() *Doctor {
	return &Doctor{checks: []Check{
		{Name: "nest permissions", Run: checkNestPermissions},
		{Name: "secrets in config", Run: checkSecretsInConfig},
		{Name: "config health", Run: checkConfigHealth},
		{Name: "permission mode", Run: checkPermissionMode},
		{Name: "local config gitignore", Run: checkLocalGitignore},
		{Name: "workspace trust", Run: checkWorkspaceTrust},
		{Name: "project memory size", Run: checkProjectMemorySize},
		{Name: "language server", Run: checkLanguageServer},
		{Name: "mcp servers", Run: checkMCPServers},
		{Name: "bind status", Run: checkBindStatus},
		{Name: "journal continuity", Run: checkJournalContinuity},
	}}
}

// Run executes every check and aggregates. Order is the New order so the
// report is deterministic.
func (d *Doctor) Run(ctx *Context) Report {
	var r Report
	for _, c := range d.checks {
		f := c.Run(ctx)
		f.Check = c.Name
		r.Findings = append(r.Findings, f)
	}
	return r
}
