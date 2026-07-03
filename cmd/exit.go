package cmd

import "github.com/tamnd/ari/core"

// exitCode maps the error taxonomy to the stable exit codes shell scripts
// branch on. Adding a code is additive; changing one is a break like the
// event schema (doc 01 section 10.2). This is the single place the mapping
// lives.
func exitCode(err error) int {
	switch core.KindOf(err) {
	case "":
		return 0
	case core.ErrConfig:
		return 2
	case core.ErrNest:
		return 3
	case core.ErrProvider:
		return 4
	case core.ErrPermission:
		return 5
	case core.ErrBudget:
		return 6
	case core.ErrCanceled:
		return 7
	default: // ErrInternal and anything unclassified
		return 1
	}
}
