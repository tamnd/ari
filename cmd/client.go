package cmd

import (
	"context"

	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/ui"
)

// colonyClient adapts the core facade to the ui.Client seam. The ui
// package declares the interface and never imports core; this file is
// the one place the two sides meet (D2, the import-graph guard).
type colonyClient struct {
	c *core.Colony
}

func (a colonyClient) NewSession(ctx context.Context, title string) (string, error) {
	id, err := a.c.NewSession(ctx, core.NewSessionRequest{Title: title})
	return string(id), err
}

func (a colonyClient) Sessions(ctx context.Context) ([]ui.SessionInfo, error) {
	sums, err := a.c.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]ui.SessionInfo, 0, len(sums))
	for _, s := range sums {
		infos = append(infos, ui.SessionInfo{ID: string(s.ID), Title: s.Title})
	}
	return infos, nil
}

func (a colonyClient) Submit(ctx context.Context, session, text string) (string, error) {
	id, err := a.c.Submit(ctx, core.SubmitRequest{Session: core.SessionID(session), Text: text})
	return string(id), err
}

func (a colonyClient) Cancel(ctx context.Context, session string) error {
	return a.c.Cancel(ctx, core.SessionID(session))
}

func (a colonyClient) Respond(ctx context.Context, session, requestID, decision string) error {
	return a.c.Respond(ctx, core.RespondRequest{
		Session:   core.SessionID(session),
		RequestID: requestID,
		Decision:  core.RespondChoice(decision),
	})
}
