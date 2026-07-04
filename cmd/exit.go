package cmd

import (
	"errors"

	"github.com/tamnd/ari/core"
)

// codeError carries an explicit process exit code, for a command whose exit
// contract is not the error taxonomy. doctor uses it so its 0/1/2/3 CI
// contract (doc 14 section 12.4) reaches the process without being remapped
// through KindOf. A silent codeError is one whose message was already
// printed by the command, so Execute does not print it again.
type codeError struct {
	code   int
	err    error
	silent bool
}

func (e codeError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e codeError) Unwrap() error { return e.err }

// coded wraps err with an explicit exit code. A nil err with a nonzero code
// is a silent signal: the command already printed its own report, so there
// is nothing for Execute to echo.
func coded(code int, err error) error {
	if code == 0 && err == nil {
		return nil
	}
	return codeError{code: code, err: err, silent: err == nil}
}

// exitCode maps an error to the stable exit code shell scripts branch on. A
// codeError names its own code; everything else maps through the taxonomy.
// Adding a code is additive; changing one is a break like the event schema
// (doc 01 section 10.2). This is the single place the mapping lives.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ce codeError
	if errors.As(err, &ce) {
		return ce.code
	}
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

// silent reports whether an error already had its message printed by the
// command that produced it, so Execute should not print it a second time.
func silent(err error) bool {
	var ce codeError
	return errors.As(err, &ce) && ce.silent
}
