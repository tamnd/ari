package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/tamnd/ari/event"
)

// ErrorKind classifies a failure so clients and scripts can react without
// parsing message strings. It travels in the error event payload and maps
// to a process exit code for the one-shot surfaces (doc 01 section 10).
type ErrorKind string

const (
	ErrConfig     ErrorKind = "config"     // bad config, missing provider, unresolved tier
	ErrNest       ErrorKind = "nest"       // not a project, unwritable nest, corrupt db
	ErrProvider   ErrorKind = "provider"   // model endpoint down, auth failed, rate limited
	ErrPermission ErrorKind = "permission" // a tool was denied by the pipeline (D15)
	ErrBudget     ErrorKind = "budget"     // a task hit its token budget ceiling (D5)
	ErrTool       ErrorKind = "tool"       // a tool failed in a way the loop surfaces
	ErrCanceled   ErrorKind = "canceled"   // the user or a parent context canceled
	ErrInternal   ErrorKind = "internal"   // a bug, a panic recovered, an invariant broken
)

// Error is the structured form every core error carries across the kernel
// boundary. The message is subject to the human-voice rule like every other
// string (D24).
type Error struct {
	Kind      ErrorKind
	Message   string
	Retryable bool
	Ant       string
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// Errf builds a core error of the given kind.
func Errf(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Wrap classifies an underlying error as it crosses the kernel boundary.
// Wrapping a *Error keeps its kind unless the new kind is more specific
// than internal.
func Wrap(kind ErrorKind, err error, message string) *Error {
	return &Error{Kind: kind, Message: message, Cause: err}
}

// KindOf extracts the taxonomy kind from any error chain; an unclassified
// error is internal, and a context cancellation is canceled.
func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	if ce, ok := errors.AsType[*Error](err); ok {
		return ce.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrCanceled
	}
	return ErrInternal
}

// Info converts an error to the wire payload of the error event. A nil
// error yields a zero Info.
func Info(err error) event.ErrorInfo {
	if err == nil {
		return event.ErrorInfo{}
	}
	if ce, ok := errors.AsType[*Error](err); ok {
		info := event.ErrorInfo{
			Code:      string(ce.Kind),
			Message:   ce.Message,
			Retryable: ce.Retryable,
			Ant:       ce.Ant,
		}
		if ce.Cause != nil {
			info.Cause = ce.Cause.Error()
		}
		return info
	}
	return event.ErrorInfo{Code: string(KindOf(err)), Message: err.Error()}
}
