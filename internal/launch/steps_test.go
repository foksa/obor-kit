package launch_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// The session step's rows the launcher-level case table (TestRestartSessionDecision) does not hold: the session a
// start resumes and where it came from, or a fresh start, or the refusal of a start with none to resume. A resume is
// always recorded (record), a fresh start never.
func TestLaunchSessionStep(t *testing.T) {
	t.Parallel()
	if k := testkit.Kinds(); !k.Resumes("opencode") || !k.Resumes("claude") {
		t.Fatal("precondition: opencode and claude resume sessions")
	}
	x := setup(t)
	gone := filepath.Join(x.claude, "projects", "-work-lane", "sid-gone.jsonl")
	const noSession = "no session to resume for pj-l-01 (pass --fresh to start a new one)"
	for _, c := range []struct {
		name     string
		tg       launch.Target
		req      launch.Request
		sid, src string
		fail     string // the refusal ("": none)
		said     string // a line the step says ("": not checked)
	}{
		{name: "none recorded, not fresh: refused", tg: launch.Target{Kind: "claude"}, fail: noSession},
		{name: "none recorded, a first prompt carried: fresh", tg: launch.Target{Kind: "claude"},
			req: launch.Request{Prompt: "go"}},
		{name: "a record of no kind (an older record): this kind's, it resumes", tg: launch.Target{Kind: "opencode",
			Sid: "oc-1"}, sid: "oc-1", src: "record"},
		{name: "a claude session never prompted: fresh", tg: launch.Target{Kind: "claude", Sid: "sid-gone",
			SidKind: "claude", Transcript: gone}, said: "recorded session sid-gone has no transcript"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.tg.Agent, c.tg.Pane = "pj-l-01", "w1:p3"
			var out bytes.Buffer
			sid, src, record, err := x.launcher(&out).SessionOf(context.Background(), c.tg, c.req)
			switch {
			case c.fail != "":
				if err == nil || err.Error() != c.fail {
					t.Fatalf("err %v, want %q", err, c.fail)
				}
				return
			case err != nil:
				t.Fatalf("refused: %v", err)
			}
			if sid != c.sid || src != c.src {
				t.Errorf("session %q from %q, want %q from %q", sid, src, c.sid, c.src)
			}
			if record != (c.sid != "") {
				t.Errorf("record %v for session %q, want a resume recorded, a fresh start not", record, sid)
			}
			if c.said != "" && !strings.Contains(out.String(), c.said) {
				t.Errorf("said %q, want %q in it", out.String(), c.said)
			}
		})
	}
}

// The first prompt step: the prompt in the start's argv, the wake typed into a resumed session, the job whose wake it
// is; a job serve cannot tell refuses before anything is stopped.
func TestLaunchFirstPromptStep(t *testing.T) {
	t.Parallel()
	x := setup(t)
	lead := launch.Target{Agent: "pj-lead", Kind: "claude", Role: "lead"}
	worker := launch.Target{Agent: "pj-l-01", Kind: "claude", Role: "worker", Lane: "L-01"}
	for _, c := range []struct {
		name               string
		tg                 launch.Target
		req                launch.Request
		sid                string
		job                string // what Job answers ("": none)
		jobErr             error
		prompt, wake, with string // with: the job the wake is
		fail               string
	}{
		{name: "a fresh role start: the role's prompt", tg: lead, req: launch.Request{Fresh: true}, prompt: "/obor:lead"},
		{name: "a handoff restart: the handoff form", tg: lead, req: launch.Request{Fresh: true, Handoff: true},
			prompt: "/obor:lead handoff"},
		{name: "the request's prompt wins", tg: lead, req: launch.Request{Fresh: true, Prompt: "brief"}, prompt: "brief"},
		{name: "a resume carries none, its prompt the wake", tg: lead, req: launch.Request{Prompt: "brief"}, sid: "sid-1",
			wake: "brief"},
		{name: "a fresh worker with its job: the job's wake", tg: worker, req: launch.Request{Fresh: true},
			job: "T-1/1/work", prompt: wire.WakeLine("T-1/1/work"), wake: wire.WakeLine("T-1/1/work"), with: "T-1/1/work"},
		{name: "a resumed worker with its job: the wake typed", tg: worker, sid: "sid-1", job: "T-1/1/work",
			wake: wire.WakeLine("T-1/1/work"), with: "T-1/1/work"},
		{name: "a job serve cannot tell: refused", tg: worker, sid: "sid-1", jobErr: errors.New("serve down"),
			fail: "pj-l-01 not restarted (nothing stopped): serve down"},
		{name: "restart's own refusal as it is", tg: worker, sid: "sid-1",
			jobErr: project.Failf("obor serve is not running"), fail: "obor serve is not running"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := x.launcher(&bytes.Buffer{})
			asked := 0
			l.Job = func(context.Context, string, string) (string, error) {
				asked++
				return c.job, c.jobErr
			}
			prompt, wake, job, err := l.FirstPrompt(context.Background(), c.tg, c.req, c.sid)
			switch {
			case c.fail != "":
				if err == nil || err.Error() != c.fail {
					t.Fatalf("err %v, want %q", err, c.fail)
				}
				return
			case err != nil:
				t.Fatalf("refused: %v", err)
			}
			if prompt != c.prompt || wake != c.wake || job != c.with {
				t.Errorf("prompt %q wake %q job %q, want %q %q %q", prompt, wake, job, c.prompt, c.wake, c.with)
			}
			if want := c.req.Prompt == "" && c.tg.Role == "worker"; (asked == 1) != want {
				t.Errorf("Job asked %d times, want asked %v", asked, want)
			}
		})
	}
}

// The start outcome step: a start's error as the launch's answer: none; a fresh start held by a trust dialog is
// started (its confirmed start reported) and returned as the dialog; every other StartError its FAIL line, which
// carries the command that starts the agent again (project.RetryHint: the start's Again, a drive's, else a restart)
// unless its hint names something else to do.
func TestLaunchOutcomeStep(t *testing.T) {
	t.Parallel()
	busy := termError("start", "busy", "pane w1:p3 is not an available shell")
	for _, c := range []struct {
		name    string
		err     error
		trust   string // the dialog's hint ("": none)
		fail    string // the FAIL line ("": none)
		started bool   // the start was reported confirmed
		again   string // the start's own command (Start.Again)
		hinted  bool   // the error carries the command that starts the agent again
		placed  bool   // the pane was placed for the start
	}{
		{name: "no error", started: false},
		{name: "a trust dialog on a fresh start: started", err: &port.StartError{NotReady: true, Trust: true, Hint: "trust it"},
			trust: "trust it", started: true},
		{name: "no shell", err: &port.StartError{NoShell: true, Err: errors.New("no shell")},
			fail: "pane w1:p3 is not an available shell after 7s: start the agent there with obor restart pj-l-01 --fresh " +
				"--pane w1:p3", hinted: true},
		{name: "no shell, a drive's start: its own command", err: &port.StartError{NoShell: true, Err: errors.New("no shell")},
			again: "obor drive T-07", fail: "pane w1:p3 is not an available shell after 7s: start the agent there with " +
				"obor drive T-07", hinted: true},
		{name: "not ready", err: &port.StartError{NotReady: true, Hint: "answer the dialog"},
			fail: "start failed in w1:p3 (answer the dialog)"},
		{name: "not ready, its hint naming the start again", err: &port.StartError{NotReady: true, Retry: true,
			Hint: "codex updated itself: start it again with obor drive T-07"}, again: "obor drive T-07",
			fail: "start failed in w1:p3 (codex updated itself: start it again with obor drive T-07)", hinted: true},
		{name: "busy", err: &port.StartError{Err: busy},
			fail: "start failed in w1:p3 (the pane is not an available shell: the old agent has not left it; run " +
				"obor restart pj-l-01 --fresh --pane w1:p3 again or start it by hand)", hinted: true},
		{name: "busy, a placed pane: no old agent named", err: &port.StartError{Err: busy}, placed: true,
			fail: "start failed in w1:p3 (the pane is not an available shell; run obor restart pj-l-01 --fresh --pane " +
				"w1:p3 again or start it by hand)", hinted: true},
		{name: "any other start error", err: &port.StartError{Err: errors.New("boom")},
			fail: "start failed in w1:p3 (no agent runs there: the pane is at its shell, maybe mid-command: run " +
				"obor restart pj-l-01 --fresh --pane w1:p3 again or start it by hand)", hinted: true},
		{name: "any other start error, a drive's start", err: &port.StartError{Err: errors.New("boom")},
			again: "obor drive T-07", fail: "start failed in w1:p3 (no agent runs there: the pane is at its shell, maybe " +
				"mid-command: run obor drive T-07 again or start it by hand)", hinted: true},
		{name: "not a start error: as it is", err: project.Failf("label failed"), fail: "label failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.sessions = map[string]store.Session{} // none recorded: the retry is a --fresh start
			x.shellBound = 7 * time.Second
			x.shell("w1:p3", "pj-l-01", x.lane)
			tg := launch.Target{Agent: "pj-l-01", Pane: "w1:p3", Kind: "claude", Role: "worker", Lane: "L-01", Placed: c.placed}
			trust, err := x.launcher(&bytes.Buffer{}).Outcome(context.Background(), tg,
				launch.Start{Kind: "claude", Again: c.again}, c.err)
			switch {
			case c.fail != "":
				if err == nil || err.Error() != c.fail {
					t.Fatalf("err %v, want %q", err, c.fail)
				}
			case err != nil:
				t.Fatalf("err %v, want none", err)
			}
			if got := project.RetryHinted(err); got != c.hinted {
				t.Errorf("the error carries the start's command: %v, want %v", got, c.hinted)
			}
			if got := ""; trust != nil {
				got = trust.Hint
				if got != c.trust {
					t.Errorf("trust dialog %q, want %q", got, c.trust)
				}
			} else if c.trust != "" {
				t.Errorf("no trust dialog, want %q", c.trust)
			}
			if got := len(x.startReports()) == 1; got != c.started {
				t.Errorf("start reported %v, want %v", got, c.started)
			}
		})
	}
}
