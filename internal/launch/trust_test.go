package launch_test

import (
	"bytes"
	"context"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
)

// A start herdr gives up on (agent_not_ready) whose pane shows a "Trust this folder?" dialog is a started agent:
// Launch returns its Started with a port.TrustDialog naming the pane, the start reported (the success path), and
// obor restart's line says RESTARTED with the dialog. The same start whose screen shows no dialog fails as before.
func TestLaunchHeldByTrustDialog(t *testing.T) {
	t.Parallel()
	const dialog = "> You are in /x\n  Do you trust the files in this folder?\n  Trust this folder? 1. Yes  2. No\n"
	notReady := herdrtest.Failure{Code: "agent_not_ready", Message: "blocked during startup"}
	t.Run("a screen with the dialog: started, the trust result with its pane", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *herdrtest.Pane) { p.Screen = dialog })
		x.srv.Once("agent.start", notReady)
		var out bytes.Buffer
		l := x.launcher(&out)
		var reported []string
		l.Started = func(_ context.Context, agent string) error {
			reported = append(reported, agent)
			return nil
		}
		s, err := l.Launch(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: "the brief"})
		td := port.TrustHeld(err)
		if td == nil {
			t.Fatalf("err %v, want a TrustDialog\n%s", err, out.String())
		}
		eq(t, td.Pane, "w1:p3", "the dialog's pane")
		eq(t, td.Hint, "a 'Trust this folder?' dialog in pane w1:p3: answer it there (Enter = trust; once per repo)", "hint")
		eq(t, s.Pane, "w1:p3", "Started's pane")
		eq(t, s.Line, "RESTARTED pj-l-01 (pane w1:p3, session new)", "Started's line")
		if len(reported) != 1 || reported[0] != "pj-l-01" {
			t.Errorf("reported starts %q, want pj-l-01's", reported)
		}
		eqInt(t, len(x.calls("agent.start")), 1, "one start")
	})
	t.Run("obor restart: RESTARTED, the dialog named", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *herdrtest.Pane) { p.Screen = dialog })
		x.srv.Once("agent.start", notReady)
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatalf("a restart held by the dialog failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new): it waits at a 'Trust this folder?' dialog in "+
			"pane w1:p3: answer it there (Enter = trust; once per repo)", "result")
	})
	t.Run("a screen without it: the existing NotReady failure, no trust result", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.srv.Once("agent.start", notReady)
		var out bytes.Buffer
		_, err := x.launcher(&out).Launch(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		if err == nil || port.TrustHeld(err) != nil {
			t.Fatalf("err %v, want a failed start that is no trust result", err)
		}
		eq(t, err.Error(), "start failed in w1:p3 (pj-l-01 waits at a startup dialog in pane w1:p3: answer it there)", "error")
	})
}
