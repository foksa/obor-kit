package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"obor/internal/project"
	"obor/internal/view"
)

// T-803: the page renders the idle state (idle, not acknowledged since <time>; idle: <why> once acknowledged), and
// /api/view carries it, so obor web shows what obor status does.
func TestPageShowsTheIdleState(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"not acknowledged since", "obor idle", "last: "} {
		if !strings.Contains(bundle(t), want) {
			t.Errorf("the page's bundle has no %q: the idle state is not rendered", want)
		}
	}
	at := time.Date(2026, 11, 4, 9, 5, 0, 0, time.UTC)
	v := view.Project{Serve: view.Serve{Up: true},
		Idle: &view.Idle{Since: at.Unix(), Last: "T-1 merged abc1234", Acked: true, Why: "nothing to dispatch"}}
	s := &Server{Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: t.TempDir()}} },
		Build: func(context.Context, string) (view.Project, error) { return v, nil }}
	r := get(t, s.Handler(), "/api/view?project=pj")
	if r.Code != 200 {
		t.Fatalf("/api/view: %d %s", r.Code, r.Body)
	}
	var got view.Project
	decode(t, r, &got)
	if got.Idle == nil || !got.Idle.Acked || got.Idle.Why != "nothing to dispatch" || got.Idle.Last != "T-1 merged abc1234" {
		t.Errorf("/api/view idle %+v, want the acknowledged state", got.Idle)
	}
}
