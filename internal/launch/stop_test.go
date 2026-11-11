package launch_test

import (
	"bytes"
	"context"
	"testing"

	"obor/internal/app/port"
)

// A stop of an agent with no labelled pane is a plain FAIL, not port.NotFound: a
// label is not the identity, so a missing one never says the agent is gone (serve's identity decides that, stopAgent);
// herdr's pane list failing is a plain FAIL too.
func TestStopNoPanePlainFail(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		prep func(x *fx)
		want string
		gone bool
	}{{
		name: "no pane labelled the agent",
		prep: func(x *fx) {},
		want: "no pane labelled pj-l-01 (pass --pane)",
	}, {
		name: "the terminal pane list failed",
		prep: func(x *fx) { x.srv.Fail("pane.list", "internal", "boom") },
		want: "terminal pane list failed (is the terminal running?)",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.prep(x)
			labelled := false // precondition: pj-l-01 is the config's lane agent, and no pane is labelled it
			x.f.Locked(func() {
				for _, p := range x.f.Panes {
					labelled = labelled || p.Label == "pj-l-01"
				}
			})
			if labelled {
				t.Fatal("precondition: a pane is labelled pj-l-01")
			}
			var out bytes.Buffer
			_, err := x.launcher(&out).Stop(context.Background(), "pj-l-01", "", false)
			if err == nil || err.Error() != c.want {
				t.Fatalf("stop of pj-l-01: %v, want %q", err, c.want)
			}
			if got := port.NotFound(err); got != c.gone {
				t.Errorf("port.NotFound(%v) = %v, want %v", err, got, c.gone)
			}
		})
	}
}
