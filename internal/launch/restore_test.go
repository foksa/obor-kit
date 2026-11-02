package launch_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// rfx is a test project (testkit: prefix pj, lanes of type opus) with a fake herdr and a restart that logs "<root> restart <agent> --pane <pane>[ --fresh]" (and fails for the agents in fail).
type rfx struct {
	t     *testing.T
	p     *testkit.Project
	h     *herdrtest.Fake
	srv   *herdrtest.Server
	pl    *launch.Restorer
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func newRfx(t *testing.T) *rfx {
	t.Helper()
	f := &rfx{t: t, p: testkit.New(t), fail: map[string]bool{}}
	f.h = f.p.Herdr()
	f.srv = herdrtest.Serve(t, f.h)
	f.pl = &launch.Restorer{Env: f.p.Env(), H: f.srv.Client(), Now: func() time.Time { return time.Unix(0, 0) },
		LaneState: func(cfg *config.Config, l config.Lane) string { return checks.NewLaneTable(cfg).LaneState(l).State }}
	f.pl.Restart = func(_ context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
		c := cfg.Root + " restart " + req.Agent + " --pane " + req.Pane
		if req.Fresh {
			c += " --fresh"
		}
		f.record(c)
		fmt.Fprintln(w, "stub restart ran: "+req.Agent)
		if f.fail[req.Agent] {
			return "", project.Fail("no session")
		}
		return "RESTARTED " + req.Agent, nil
	}
	return f
}

func (f *rfx) record(c string) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func (f *rfx) shell(pane, cwd, label string) { f.h.Shell(pane, label, cwd) }

func (f *rfx) read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func (f *rfx) restoreLog() string { return f.read(launch.RestoreLog(f.pl.Env)) }

func (f *rfx) wantCalls(want ...string) {
	f.t.Helper()
	if got := strings.Join(f.calls, "\n"); got != strings.Join(want, "\n") {
		f.t.Errorf("calls:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func has(t *testing.T, s, sub, what string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: %q not in:\n%s", what, sub, s)
	}
}

// bash restore.test.sh case_labelled_obor_panes_restarted_others_skipped
func TestRestoreLabelledOborPanes(t *testing.T) {
	f := newRfx(t)
	lane := f.p.Lane("O-01", "task/T-01")
	f.p.Commit(lane)
	f.p.Config()
	other := filepath.Join(f.p.Base, "other")
	f.p.Git(f.p.Base, "init", "-q", other)
	f.shell("w1:p1", lane, "pj-o-01")
	f.shell("w1:p2", f.p.Root, "pj-lead")
	f.shell("w1:p3", f.p.Root, "")
	f.shell("w1:p4", other, "pj-o-01")
	f.shell("w1:p5", f.p.Root, "my notes")
	f.h.Agent("w1:p6", "pj-int", "idle", f.p.Root).Label = "pj-int"
	recordSessions(t, f.p.Root, store.Session{Agent: "pj-o-01", Kind: "claude", ID: "sid-o-01", Started: time.Now().Unix()})
	f.pl.Restore(context.Background())
	f.wantCalls(f.p.Root+" restart pj-o-01 --pane w1:p1", f.p.Root+" restart pj-lead --pane w1:p2 --fresh")
	log := f.restoreLog()
	has(t, log, " restore pj-o-01 (pane w1:p1) in "+f.p.Root+"\nstub restart ran: pj-o-01\nRESTARTED pj-o-01\n",
		"the restore line, the restart's output and result")
	has(t, log, " restore pj-lead (pane w1:p2) in "+f.p.Root+" (lead, no recorded session: --fresh)\n",
		"a role with no recorded session starts fresh (T-627)")
	has(t, log, " skip pj-int (pane w1:p6): runs claude", "busy pane: skipped, logged")
	if n := strings.Count(log, " restore ") + strings.Count(log, " skip "); n != 3 {
		t.Errorf("%d restore/skip lines, want 3 (unlabelled, non-obor and foreign-label panes: none):\n%s", n, log)
	}
	if _, err := os.Stat(filepath.Join(other, ".obor")); err == nil {
		t.Error("something written in the other repo")
	}
}

// bash restore.test.sh case_outside_obor_silent
func TestRestoreOutsideOborSilent(t *testing.T) {
	f := newRfx(t)
	other := filepath.Join(f.p.Base, "other")
	f.p.Git(f.p.Base, "init", "-q", other)
	f.shell("w1:p1", other, "pj-o-01")
	f.shell("w1:p2", f.p.Base, "pj-lead")
	f.shell("w1:p3", filepath.Join(f.p.Base, "gone"), "pj-lead")
	f.pl.Restore(context.Background())
	f.wantCalls()
	if _, err := os.Stat(launch.RestoreLog(f.pl.Env)); err == nil {
		t.Error("a log outside obor")
	}
	f.p.Config()
	f.shell("w1:p4", f.p.Root, "pj-lead")
	f.srv.Fail("pane.list", "internal", "herdr is down")
	f.pl.Restore(context.Background())
	f.wantCalls()
	if _, err := os.Stat(launch.RestoreLog(f.pl.Env)); err == nil {
		t.Error("pane list failing: a log")
	}
	f.srv.Clear("pane.list")
	f.pl.Restore(context.Background())
	f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p4 --fresh") // precondition: the obor pane restarts once herdr answers
}

// T-658: the monitor pane is a command, not an agent: the restore types `obor monitor` at its shell again, runs no
// restart and records or resumes no session. A command that runs there already, and an agent in the pane, are left
// alone with a log line.
func TestRestoreMonitorPane(t *testing.T) {
	f := newRfx(t)
	f.p.Config()
	f.shell("w1:p1", f.p.Root, "pj-monitor") // back at its shell: the monitor ended
	f.shell("w1:p2", f.p.Root, "pj-monitor")
	f.h.Set("w1:p2", func(p *herdrtest.Pane) { p.Foreground = 2 }) // a command runs there
	f.h.Agent("w1:p3", "x", "idle", f.p.Root).Label = "pj-monitor"
	f.pl.Restore(context.Background())
	f.wantCalls() // no restart for the monitor: it is no agent
	log := f.restoreLog()
	has(t, log, " restore pj-monitor (pane w1:p1) in "+f.p.Root+": obor monitor", "the monitor's command is logged")
	has(t, log, " skip pj-monitor (pane w1:p2): a command runs there", "a busy monitor pane is skipped")
	has(t, log, " skip pj-monitor (pane w1:p3): runs claude", "a pane that runs an agent is skipped")
	var typed []string
	for _, r := range f.srv.Requests() {
		if s := r.String(); strings.HasPrefix(s, "pane.send_input") {
			typed = append(typed, s)
		}
	}
	if len(typed) != 1 || !strings.Contains(typed[0], "pane_id=w1:p1") || !strings.Contains(typed[0], "text=obor monitor") {
		t.Errorf("pane.send_input calls %q, want obor monitor in w1:p1 only", typed)
	}
}

// bash restore.test.sh case_backgrounded_and_failure_logged (the return at once is cmd/obor's detach)
func TestRestoreFailureLoggedNextPaneRestarts(t *testing.T) {
	f := newRfx(t)
	lane := f.p.Lane("O-01", "task/T-01")
	f.p.Commit(lane)
	f.p.Config()
	f.shell("w1:p1", f.p.Root, "pj-lead")
	f.shell("w1:p2", lane, "pj-o-01")
	f.fail["pj-lead"] = true
	f.pl.Restore(context.Background())
	has(t, f.restoreLog(), "obor restart pj-lead: FAIL no session", "a failed restart is logged")
	f.wantCalls(f.p.Root+" restart pj-lead --pane w1:p1 --fresh", f.p.Root+" restart pj-o-01 --pane w1:p2 --fresh")
}

// bash restore.test.sh case_worker_without_session_fresh
func TestRestoreWorkerWithoutSessionFresh(t *testing.T) {
	f := newRfx(t)
	o1, o2 := f.p.Lane("O-01", "task/T-01"), f.p.Lane("O-02", "task/T-02")
	f.p.Commit(o1)
	f.p.Commit(o2)
	f.p.Config()
	f.shell("w1:p1", o1, "pj-o-01")
	f.shell("w1:p2", o2, "o-02")
	f.shell("w1:p3", f.p.Root, "pj-lead")
	f.p.State("O-02", `{"lane":"O-02","agent":"o-02"}`) // a bare lane name is the lane's only when its state names it
	recordSessions(t, f.p.Root, store.Session{Agent: "o-02", Kind: "claude", ID: "sid-o-02", Started: time.Now().Unix()})
	f.pl.Restore(context.Background())
	f.wantCalls(f.p.Root+" restart pj-o-01 --pane w1:p1 --fresh", f.p.Root+" restart o-02 --pane w1:p2",
		f.p.Root+" restart pj-lead --pane w1:p3 --fresh")
	log := f.restoreLog()
	has(t, log, " restore pj-o-01 (pane w1:p1) in "+f.p.Root+" (worker, no recorded session: --fresh)",
		"the fresh start is logged")
	has(t, log, " restore pj-lead (pane w1:p3) in "+f.p.Root+" (lead, no recorded session: --fresh)",
		"a role with no recorded session starts fresh too (T-627)")
	if n := strings.Count(log, "no recorded session"); n != 2 {
		t.Errorf("%d 'no recorded session' lines, want 2 (the worker and the lead)", n)
	}
}

// T-583, T-599: the restore's --fresh decision reads serve's store only, as obor restart does. A worker with a session
// there (Claude or Codex) resumes; none at all: TestRestoreWorkerWithoutSessionFresh. A kind that resumes nothing
// (OpenCode, T-581) starts fresh whatever is recorded.
func TestRestoreWorkerRecordedSession(t *testing.T) {
	t.Run("store-only Codex worker: resumed, no --fresh", func(t *testing.T) {
		f := newRfx(t)
		lane := f.p.Lane("O-01", "task/T-01")
		f.p.Commit(lane)
		f.p.Config(testkit.Set("lane_types.opus", `{"kind":"codex","args":[]}`))
		f.shell("w1:p1", lane, "pj-o-01")
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-o-01", Kind: "codex", ID: "cx-1", Started: time.Now().Unix()})
		f.pl.Restore(context.Background())
		f.wantCalls(f.p.Root + " restart pj-o-01 --pane w1:p1")
		lacks(t, f.restoreLog(), "no recorded session", "no --fresh note")
	})
	t.Run("Claude worker with a session in the store: resumed, no --fresh", func(t *testing.T) {
		f := newRfx(t)
		lane := f.p.Lane("O-01", "task/T-01")
		f.p.Commit(lane)
		f.p.Config()
		f.shell("w1:p1", lane, "pj-o-01")
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-o-01", Kind: "claude", ID: "sid-o-01", Started: time.Now().Unix()})
		f.pl.Restore(context.Background())
		f.wantCalls(f.p.Root + " restart pj-o-01 --pane w1:p1")
		lacks(t, f.restoreLog(), "no recorded session", "no --fresh note")
	})
	t.Run("OpenCode worker: --fresh even with a recorded session", func(t *testing.T) {
		f := newRfx(t)
		lane := f.p.Lane("O-01", "task/T-01")
		f.p.Commit(lane)
		f.p.Config(testkit.Set("lane_types.opus", `{"kind":"opencode","args":[]}`))
		f.shell("w1:p1", lane, "pj-o-01")
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-o-01", Kind: "opencode", ID: "oc-1", Started: time.Now().Unix()})
		f.pl.Restore(context.Background())
		f.wantCalls(f.p.Root + " restart pj-o-01 --pane w1:p1 --fresh")
		has(t, f.restoreLog(), "restore pj-o-01 (pane w1:p1) in "+f.p.Root+" (worker, kind opencode resumes nothing: --fresh)",
			"the fresh start is logged")
	})
}

// T-739: on a pool (untyped) lane the restore's resume decision reads the kind its open dispatch runs, not the
// config's: an OpenCode dispatch starts fresh whatever session is recorded, a Claude dispatch there resumes.
func TestRestorePoolLaneKindIsTheDispatchs(t *testing.T) {
	for _, c := range []struct{ typ, kind, want string }{
		{"oc", "opencode", " --fresh"},
		{"opus", "claude", ""},
	} {
		t.Run(c.kind, func(t *testing.T) {
			f := newRfx(t)
			lane := f.p.Lane("O-01", "task/T-01")
			f.p.Commit(lane)
			f.p.Config(testkit.Set("lane_types.oc", `{"kind":"opencode","args":[]}`), testkit.Set("lanes.O-01", ""))
			cfg, err := config.Load(f.p.Root)
			if err != nil || len(cfg.Lanes()) != 1 || cfg.Lanes()[0].Type != "" {
				t.Fatalf("precondition: an untyped lane O-01: %v, %v", cfg.Lanes(), err)
			}
			f.p.State("O-01", `{"task":"T-01","branch":"task/T-01","base":"main","agent":"pj-o-01","type":"`+c.typ+`"}`)
			f.shell("w1:p1", lane, "pj-o-01")
			recordSessions(t, f.p.Root, store.Session{Agent: "pj-o-01", Kind: c.kind, ID: "s-1", Started: time.Now().Unix()})
			f.pl.Restore(context.Background())
			f.wantCalls(f.p.Root + " restart pj-o-01 --pane w1:p1" + c.want)
			if c.want != "" {
				has(t, f.restoreLog(), "(worker, kind opencode resumes nothing: --fresh)", "the dispatch's kind is logged")
			} else {
				lacks(t, f.restoreLog(), "--fresh", "a Claude dispatch resumes")
			}
		})
	}
}

// bash restore.test.sh case_free_lane_worker_not_started
func TestRestoreFreeLaneWorkerNotStarted(t *testing.T) {
	f := newRfx(t)
	o1, o2 := f.p.Lane("O-01", "task/T-01"), f.p.Lane("O-02", "task/T-02")
	f.p.Commit(o2)
	f.p.Config()
	f.shell("w1:p1", o1, "pj-o-01")
	f.shell("w1:p2", o2, "pj-o-02")
	f.shell("w1:p3", f.p.Root, "pj-lead")
	f.pl.Restore(context.Background())
	f.wantCalls(f.p.Root+" restart pj-o-02 --pane w1:p2 --fresh", f.p.Root+" restart pj-lead --pane w1:p3 --fresh")
	has(t, f.restoreLog(), " skip pj-o-01 (pane w1:p1): lane O-01 is free (dispatch starts its worker)",
		"the skip is logged")

	f.p.Config(testkit.Set("stop_idle_workers", false))
	f.calls = nil
	f.pl.Restore(context.Background())
	f.wantCalls(f.p.Root+" restart pj-o-01 --pane w1:p1 --fresh", f.p.Root+" restart pj-o-02 --pane w1:p2 --fresh",
		f.p.Root+" restart pj-lead --pane w1:p3 --fresh")
}

// bash restore.test.sh case_role_panes_restored_not_skipped
func TestRestoreRolePanesNotSkipped(t *testing.T) {
	f := newRfx(t)
	o1 := f.p.Lane("O-01", "task/T-01")
	f.p.Config(testkit.Set("roles", `{"reviewer": {"type": "opus"}, "deep_dive": {"type": "opus"}}`))
	rev, dd := f.p.Lane("review", "review"), f.p.Lane("deep-dive", "deep-dive")
	f.shell("w1:p1", o1, "pj-o-01")
	f.shell("w1:p7", rev, "pj-rev")
	f.shell("w1:p8", dd, "pj-deep-dive")
	f.pl.Restore(context.Background())
	f.wantCalls(f.p.Root+" restart pj-rev --pane w1:p7 --fresh", f.p.Root+" restart pj-deep-dive --pane w1:p8 --fresh")
	log := f.restoreLog()
	has(t, log, " skip pj-o-01 (pane w1:p1): lane O-01 is free", "the free lane skipped")
	has(t, log, " restore pj-rev (pane w1:p7) in "+f.p.Root+" (reviewer, no recorded session: --fresh)",
		"a role pane with no recorded session starts fresh (T-627)")
	has(t, log, " restore pj-deep-dive (pane w1:p8) in "+f.p.Root+" (deep_dive, no recorded session: --fresh)",
		"and so does the other role (§T-627)")
	if strings.Contains(log, "skip pj-rev") || strings.Contains(log, "skip pj-deep-dive") {
		t.Errorf("a role pane skipped:\n%s", log)
	}
}
