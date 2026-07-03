package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/tamnd/ari/core"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"internal", core.Errf(core.ErrInternal, "broken invariant"), 1},
		{"config", core.Errf(core.ErrConfig, "no such tier"), 2},
		{"nest", core.Errf(core.ErrNest, "not a project"), 3},
		{"provider", core.Errf(core.ErrProvider, "endpoint down"), 4},
		{"permission", core.Errf(core.ErrPermission, "denied"), 5},
		{"budget", core.Errf(core.ErrBudget, "ceiling hit"), 6},
		{"canceled", core.Errf(core.ErrCanceled, "interrupted"), 7},
		{"bare context cancel", context.Canceled, 7},
		{"unclassified", errors.New("who knows"), 1},
		{"wrapped keeps kind", core.Wrap(core.ErrProvider, errors.New("401"), "auth failed"), 4},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}
