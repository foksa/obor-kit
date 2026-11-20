package launch_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/launch"
)

// trustHint is the guidance at pj-l-01's (a Claude worker's) trust dialog in pane w1:p3.
const trustHint = "pj-l-01 waits at a 'Trust this folder?' dialog in pane w1:p3: select its trust option (\"Yes, I trust this folder\") " +
	"there, not the default \"No, exit\", which quits Claude (once per repo)"

// A fresh start the terminal accepted and gave up on (NotReady) whose pane shows a "Trust this folder?" dialog is a started agent:
// Launch returns its Started (its pane the dialog's) with a port.TrustDialog carrying the hint, the start reported (the
// success path), and obor restart's line says RESTARTED with the hint. A resumed start at the dialog (its prompt not in
// the argv) is a failed start, its wake not typed, nothing reported. The same start whose screen shows no dialog fails
// as before.
func TestLaunchHeldByTrustDialog(t *testing.T) {
	t.Parallel()
	const dialog = "> You are in /x\n  Do you trust the files in this folder?\n  Trust this folder? 1. Yes  2. No\n"
	t.Run("a screen with the dialog: started, the trust result with its pane", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.Screen = dialog })
		x.f.Once("Start", notReady())
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
		eq(t, td.Hint, trustHint, "hint")
		eq(t, s.Pane, "w1:p3", "Started's pane")
		eq(t, s.Line, "RESTARTED pj-l-01 (pane w1:p3, session new)", "Started's line")
		if len(reported) != 1 || reported[0] != "pj-l-01" {
			t.Errorf("reported starts %q, want pj-l-01's", reported)
		}
		eqInt(t, len(x.calls("Start")), 1, "one start")
	})
	t.Run("obor restart: RESTARTED, the dialog named", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.Screen = dialog })
		x.f.Once("Start", notReady())
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatalf("a restart held by the dialog failed:\n%s", out)
		}
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new): "+trustHint, "result")
	})
	t.Run("a resumed start (its wake typed once up): the failed start, no trust result, the wake not typed", func(t *testing.T) {
		t.Parallel()
		const wake = "[obor job T-1/1/fix] call the obor job_next tool"
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.Screen = dialog })
		x.f.Once("Start", notReady())
		var out bytes.Buffer
		l := x.launcher(&out)
		var reported []string
		l.Started = func(_ context.Context, agent string) error {
			reported = append(reported, agent)
			return nil
		}
		_, err := l.Launch(context.Background(), launch.Request{Agent: "pj-l-01", Prompt: wake})
		if !strings.Contains(x.startCall(), "--resume sid-1") {
			t.Fatalf("precondition: not a resume: %q", x.startCall())
		}
		if err == nil || port.TrustHeld(err) != nil {
			t.Fatalf("err %v, want a failed start that is no trust result\n%s", err, out.String())
		}
		eq(t, err.Error(), "start failed in w1:p3 ("+trustHint+")", "error")
		eqInt(t, len(x.calls("Prompt")), 0, "wakes typed")
		eqInt(t, len(reported), 0, "starts reported")
		x.f.Once("Start", notReady()) // obor restart's resume, held the same way
		got, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok {
			t.Errorf("obor restart of a resume held by the dialog succeeded, want its failed start:\n%s", got)
		}
		contains(t, got, "start failed in w1:p3 ("+trustHint+")", "obor restart's failure")
	})
	t.Run("a screen without it: the existing NotReady failure, no trust result", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Once("Start", notReady())
		var out bytes.Buffer
		_, err := x.launcher(&out).Launch(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		if err == nil || port.TrustHeld(err) != nil {
			t.Fatalf("err %v, want a failed start that is no trust result", err)
		}
		eq(t, err.Error(), "start failed in w1:p3 (pj-l-01 waits at a startup dialog in pane w1:p3: answer it there)", "error")
	})
}
