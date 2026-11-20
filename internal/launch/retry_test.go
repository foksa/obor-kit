package launch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/launch"
	"obor/internal/store"
)

// A failed start's retry hint carries the request's intent: a fresh request (obor setup's starts, restart --fresh) is
// retried with --fresh whatever session is recorded (a plain restart would resume the older one); a resume request
// with a recorded session is retried as a plain restart; a drive's start names obor drive. Every hint that builds a
// restart command says the same (generic failure, Codex self-update).
func TestFailedStartRetryHintFollowsTheRequest(t *testing.T) {
	t.Parallel()
	const (
		generic = "FAIL start failed in w1:p3 (no agent runs there: the pane is at its shell, maybe mid-command: run %s again or start it by hand)"
		update  = "FAIL start failed in w1:p3 (codex updated itself and exited in pane w1:p3: start it again with %s)"
		screen  = "Updating Codex via npm install -g @openai/codex\nUpdated to 0.161.0. Restart Codex.\n$ \n"
	)
	for _, c := range []struct {
		name   string
		agent  string // pj-l-01: claude; pj-rev: codex
		screen string // the pane's screen after the failed start
		req    launch.Request
		want   string
	}{
		{"fresh request, an older session recorded", "pj-l-01", "", launch.Request{Fresh: true},
			"obor restart pj-l-01 --fresh --pane w1:p3"},
		{"resume request, a session recorded", "pj-l-01", "", launch.Request{},
			"obor restart pj-l-01 --pane w1:p3"},
		{"a drive's start", "pj-l-01", "", launch.Request{Fresh: true, Again: "obor drive T-07"}, "obor drive T-07"},
		{"codex self-update, fresh request, an older session recorded", "pj-rev", screen, launch.Request{Fresh: true},
			"obor restart pj-rev --fresh --pane w1:p3"},
		{"codex self-update, resume request", "pj-rev", screen, launch.Request{}, "obor restart pj-rev --pane w1:p3"},
		{"codex self-update, a drive's start", "pj-rev", screen, launch.Request{Fresh: true, Again: "obor drive T-07"},
			"obor drive T-07"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.transcript("sid-old") // a Claude session that resumes
			x.record(c.agent, "sid-old")
			eq(t, x.recorded(c.agent), "sid-old", "precondition: an older session is recorded")
			x.shell("w1:p3", c.agent, "")
			x.f.Set("w1:p3", func(p *mpane) { p.Screen = c.screen })
			x.f.Once("Start", termError("start", "", "boom"))
			c.req.Agent = c.agent
			out, ok := x.restart(c.req)
			eqInt(t, len(x.calls("Start")), 1, "precondition: the start ran and failed")
			if ok {
				t.Fatalf("a failed start succeeded:\n%s", out)
			}
			want := generic
			if c.screen != "" {
				want = update
			}
			eq(t, failLine(out), fmt.Sprintf(want, c.want), "the FAIL line's retry command")
		})
	}
}

// The outcome of a failed start names the retry from the start's own intent for its busy pane and no-shell hints too:
// a fresh start (Start.Fresh) is --fresh with an older session recorded; a resume is not; a drive's Again wins.
func TestOutcomeRetryHintFollowsTheStart(t *testing.T) {
	t.Parallel()
	busy := &port.StartError{Err: termError("start", "busy", "pane w1:p3 is not an available shell")}
	noShell := &port.StartError{NoShell: true, Err: errors.New("no shell")}
	for _, c := range []struct {
		name  string
		err   error
		start launch.Start
		want  string // the retry command the line names
	}{
		{"busy, fresh", busy, launch.Start{Fresh: true}, "obor restart pj-l-01 --fresh --pane w1:p3"},
		{"busy, resume", busy, launch.Start{}, "obor restart pj-l-01 --pane w1:p3"},
		{"busy, drive", busy, launch.Start{Fresh: true, Again: "obor drive T-07"}, "obor drive T-07"},
		{"no shell, fresh", noShell, launch.Start{Fresh: true}, "obor restart pj-l-01 --fresh --pane w1:p3"},
		{"no shell, resume", noShell, launch.Start{}, "obor restart pj-l-01 --pane w1:p3"},
		{"no shell, drive", noShell, launch.Start{Fresh: true, Again: "obor drive T-07"}, "obor drive T-07"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.sessions = map[string]store.Session{"pj-l-01": {ID: "sid-old"}} // an older session is recorded
			x.shellBound = 7 * time.Second
			x.shell("w1:p3", "pj-l-01", x.lane)
			tg := launch.Target{Agent: "pj-l-01", Pane: "w1:p3", Kind: "claude", Role: "worker", Lane: "L-01"}
			c.start.Kind = "claude"
			_, err := x.launcher(&bytes.Buffer{}).Outcome(context.Background(), tg, c.start, c.err)
			if err == nil {
				t.Fatal("no error")
			}
			if got := regexp.MustCompile(`obor (restart|drive) \S+( --fresh)?( --pane \S+)?`).FindString(err.Error()); got != c.want {
				t.Errorf("the line names %q, want %q: %v", got, c.want, err)
			}
		})
	}
}
