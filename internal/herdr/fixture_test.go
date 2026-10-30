package herdr_test

import (
	"context"

	_ "obor/internal/agents/claude" // the kinds the panes here run as (their init registers them)
	_ "obor/internal/agents/codex"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

var ctx = context.Background()

// errUnavailable matches an Unavailable *herdr.Error (errors.Is).
var errUnavailable = &herdr.Error{Kind: herdr.Unavailable}

// fixture is a project's herdr session: an agent named pj-o-01 in w1:p1, a shell labelled pj-o-02 in w1:p2, an unnamed
// working agent in w1:p3.
func fixture() *herdrtest.Fake {
	f := herdrtest.New()
	a := f.Agent("w1:p1", "pj-o-01", "idle", "/proj-wt/O-01/")
	a.Label, a.Session = "pj-o-01", "5e1d0c2b-aaaa"
	f.Shell("w1:p2", "pj-o-02", "/proj-wt/O-02")
	f.Agent("w1:p3", "", "working", "/elsewhere")
	return f
}
