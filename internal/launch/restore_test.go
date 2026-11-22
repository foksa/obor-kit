package launch_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// rfx is a test project (testkit: prefix pj, lanes of type opus) with an in-memory terminal and a restart that logs "<root> restart <agent> --pane <pane>[ --fresh]" (and fails for the agents in fail).
type rfx struct {
	t     *testing.T
	p     *testkit.Project
	h     *term
	pl    *launch.Restorer
	mu    sync.Mutex
	calls []string
	// prompts is the first prompt the restore's restart carried, by agent ("": none).
	prompts map[string]string
	fail    map[string]bool
	// free are the lanes the lane table answers free (a lane with its task is working): the table's own reading of
	// git is checks' test.
	free map[string]bool
}

func newRfx(t *testing.T) *rfx {
	t.Helper()
	p := testkit.NewPlain(t)
	f := &rfx{t: t, p: p, fail: map[string]bool{}, prompts: map[string]string{}, free: map[string]bool{}}
	f.h = newTerm()
	f.pl = &launch.Restorer{Env: f.p.Env(), H: f.h, Now: func() time.Time { return time.Unix(0, 0) },
		Poll:     time.Millisecond, // the fake panes settle at once: no test waits a real settlePoll
		Sessions: store.CurrentAt,  // the App's fold stands in as the store the tests record into
		LaneState: func(cfg *config.Config, l config.Lane) string {
			if f.free[l.Name] {
				return port.LaneFree
			}
			return port.LaneWorking
		}}
	f.pl.Restart = func(_ context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
		c := cfg.Root + " restart " + req.Agent + " --pane " + req.Pane
		if req.Fresh {
			c += " --fresh"
		}
		f.record(c)
		f.mu.Lock()
		f.prompts[req.Agent] = req.Prompt
		f.mu.Unlock()
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

// repoAt makes dir a repository without an obor config, as files: project.Find reads it, the restore finds no config
// there and stays silent.
func repoAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// bash restore.test.sh case_labelled_obor_panes_restarted_others_skipped
func TestRestoreLabelledOborPanes(t *testing.T) {
	f := newRfx(t)
	lane := f.p.PlainLane("L-01", "task/T-01")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`) // its row holds T-01, not landed: busy
	other := filepath.Join(f.p.Base, "other")
	repoAt(t, other)
	f.shell("w1:p1", lane, "pj-l-01")
	f.shell("w1:p2", f.p.Root, "pj-lead")
	f.shell("w1:p3", f.p.Root, "")
	f.shell("w1:p4", other, "pj-l-01")
	f.shell("w1:p5", f.p.Root, "my notes")
	f.h.Agent("w1:p6", "pj-int", "idle", f.p.Root)
	f.h.Set("w1:p6", func(p *mpane) { p.Label = "pj-int" })
	f.shell("w1:p7", lane, "pj-l-01-drive") // the owner's drive: obor drive starts it again, the restore does not
	recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-01", Kind: "claude", ID: "sid-l-01", Started: time.Now().Unix()},
		store.Session{Agent: "pj-l-01-drive", Kind: "claude", ID: "sid-drive", Started: time.Now().Unix()})
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root+" restart pj-l-01 --pane w1:p1", f.p.Root+" restart pj-lead --pane w1:p2 --fresh",
		f.p.Root+" restart pj-int --pane w1:p6 --fresh")
	log := f.restoreLog()
	has(t, log, " restore pj-l-01 (pane w1:p1) in "+f.p.Root+"\nstub restart ran: pj-l-01\nRESTARTED pj-l-01\n",
		"the restore line, the restart's output and result")
	has(t, log, " restore pj-lead (pane w1:p2) in "+f.p.Root+" (lead, no recorded session: --fresh)\n",
		"a role with no recorded session starts fresh (T-627)")
	has(t, log, " restore pj-int (pane w1:p6) in "+f.p.Root+" (the terminal resumed claude without obor's start: "+
		"obor restart replaces it) (integrator, no recorded session: --fresh)\n",
		"a lost pane running an agent obor did not start: replaced, logged")
	if n := strings.Count(log, " restore ") + strings.Count(log, " skip "); n != 3 {
		t.Errorf("%d restore/skip lines, want 3 (unlabelled, non-obor, foreign-label and drive panes: none):\n%s", n, log)
	}
	if _, err := os.Stat(filepath.Join(other, ".obor")); err == nil {
		t.Error("something written in the other repo")
	}
}

// Lost names the panes the restore restarts: a lost worker's pane is restarted, the lead at its shell in a pane lost
// does not name is left there, with no line for it.
func TestRestoreOnlyLostPanes(t *testing.T) {
	f := newRfx(t)
	lane := f.p.PlainLane("L-01", "task/T-01")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
	f.shell("w1:p1", lane, "pj-l-01")
	f.shell("w1:p2", f.p.Root, "pj-lead")
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root+" restart pj-l-01 --pane w1:p1 --fresh", f.p.Root+" restart pj-lead --pane w1:p2 --fresh") // precondition: both restart when every pane is lost
	f.calls = nil
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{Panes: []string{"w1:p1"}})
	f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1 --fresh")
	if log := f.restoreLog(); strings.Count(log, " restore pj-lead ") != 1 || strings.Contains(log, "skip pj-lead") {
		t.Errorf("the lead's lines in the log: want only the first run's:\n%s", log)
	}
	f.calls = nil
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{Panes: []string{"w1:p9"}})
	f.wantCalls()
}

// bash restore.test.sh case_outside_obor_silent
func TestRestoreOutsideOborSilent(t *testing.T) {
	f := newRfx(t)
	other := filepath.Join(f.p.Base, "other")
	repoAt(t, other)
	f.shell("w1:p1", other, "pj-l-01")
	f.shell("w1:p2", f.p.Base, "pj-lead")
	f.shell("w1:p3", filepath.Join(f.p.Base, "gone"), "pj-lead")
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls()
	if _, err := os.Stat(launch.RestoreLog(f.pl.Env)); err == nil {
		t.Error("a log outside obor")
	}
	f.p.Config()
	f.shell("w1:p4", f.p.Root, "pj-lead")
	f.h.Fail("Panes", termError("panes", "", "herdr is down"))
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls()
	if _, err := os.Stat(launch.RestoreLog(f.pl.Env)); err == nil {
		t.Error("pane list failing: a log")
	}
	f.h.Clear("Panes")
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p4 --fresh") // precondition: the obor pane restarts once herdr answers
}

// obor has no monitor role; a pane still labelled <prefix>-monitor (a project not yet upgraded) is no agent: the
// restore types nothing in it, restarts nothing there and logs nothing of it (the lead's own pane still restarts).
func TestRestoreIgnoresMonitorPane(t *testing.T) {
	f := newRfx(t)
	f.p.Config()
	f.shell("w1:p1", f.p.Root, "pj-lead")
	f.shell("w1:p2", f.p.Root, "pj-monitor")
	f.h.Agent("w1:p3", "x", "idle", f.p.Root)
	f.h.Set("w1:p3", func(p *mpane) { p.Label = "pj-monitor" })
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p1 --fresh") // the lead restarts; the monitor panes do not
	if log := f.restoreLog(); strings.Contains(log, "monitor") {
		t.Errorf("the restore logged a monitor pane:\n%s", log)
	}
	for _, s := range f.h.Calls() {
		if strings.HasPrefix(s, "Write") {
			t.Errorf("the restore typed into a pane: %s", s)
		}
	}
}

// bash restore.test.sh case_backgrounded_and_failure_logged (the return at once is cmd/obor's detach)
func TestRestoreFailureLoggedNextPaneRestarts(t *testing.T) {
	f := newRfx(t)
	lane := f.p.PlainLane("L-01", "task/T-01")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`) // its row holds T-01, not landed: busy
	f.shell("w1:p1", f.p.Root, "pj-lead")
	f.shell("w1:p2", lane, "pj-l-01")
	f.fail["pj-lead"] = true
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	has(t, f.restoreLog(), "obor restart pj-lead: FAIL no session", "a failed restart is logged")
	f.wantCalls(f.p.Root+" restart pj-lead --pane w1:p1 --fresh", f.p.Root+" restart pj-l-01 --pane w1:p2 --fresh")
}

// bash restore.test.sh case_worker_without_session_fresh
func TestRestoreWorkerWithoutSessionFresh(t *testing.T) {
	f := newRfx(t)
	o1, o2 := f.p.PlainLane("L-01", "task/T-01"), f.p.PlainLane("L-02", "task/T-02")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`) // its row holds T-01, not landed: busy
	f.shell("w1:p1", o1, "pj-l-01")
	f.shell("w1:p2", o2, "pj-l-02")
	f.shell("w1:p3", f.p.Root, "pj-lead")
	f.p.State("L-02", `{"lane":"L-02","task":"T-02","branch":"task/T-02"}`)
	recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-02", Kind: "claude", ID: "sid-l-02", Started: time.Now().Unix()})
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root+" restart pj-l-01 --pane w1:p1 --fresh", f.p.Root+" restart pj-l-02 --pane w1:p2",
		f.p.Root+" restart pj-lead --pane w1:p3 --fresh")
	log := f.restoreLog()
	has(t, log, " restore pj-l-01 (pane w1:p1) in "+f.p.Root+" (worker, no recorded session: --fresh)",
		"the fresh start is logged")
	has(t, log, " restore pj-lead (pane w1:p3) in "+f.p.Root+" (lead, no recorded session: --fresh)",
		"a role with no recorded session starts fresh too (T-627)")
	if n := strings.Count(log, "no recorded session"); n != 2 {
		t.Errorf("%d 'no recorded session' lines, want 2 (the worker and the lead)", n)
	}
}

// the restore's --fresh decision reads serve's store only, as obor restart does. A worker with a session
// there (Claude or Codex) resumes; none at all: TestRestoreWorkerWithoutSessionFresh. A kind that resumes nothing
// (OpenCode) starts fresh whatever is recorded.
func TestRestoreWorkerRecordedSession(t *testing.T) {
	t.Run("store-only Codex worker: resumed, no --fresh", func(t *testing.T) {
		f := newRfx(t)
		lane := f.p.PlainLane("L-01", "task/T-01")
		f.p.Config()
		f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","base":"main","agent":"pj-l-01","type":"luna"}`)
		f.shell("w1:p1", lane, "pj-l-01")
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-01", Kind: "codex", ID: "cx-1", Started: time.Now().Unix()})
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1")
		lacks(t, f.restoreLog(), "no recorded session", "no --fresh note")
	})
	t.Run("Claude worker with a session in the store: resumed, no --fresh", func(t *testing.T) {
		f := newRfx(t)
		lane := f.p.PlainLane("L-01", "task/T-01")
		f.p.Config()
		f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`) // its row holds T-01, not landed: busy
		f.shell("w1:p1", lane, "pj-l-01")
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-01", Kind: "claude", ID: "sid-l-01", Started: time.Now().Unix()})
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1")
		lacks(t, f.restoreLog(), "no recorded session", "no --fresh note")
	})
	t.Run("OpenCode worker: --fresh even with a recorded session", func(t *testing.T) {
		f := newRfx(t)
		lane := f.p.PlainLane("L-01", "task/T-01")
		f.p.Config()
		f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","base":"main","agent":"pj-l-01","type":"deepseek"}`)
		f.shell("w1:p1", lane, "pj-l-01")
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-01", Kind: "opencode", ID: "oc-1", Started: time.Now().Unix()})
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1 --fresh")
		has(t, f.restoreLog(), "restore pj-l-01 (pane w1:p1) in "+f.p.Root+" (worker, kind opencode resumes nothing: --fresh)",
			"the fresh start is logged")
	})
}

// the restore's resume decision reads the kind of the model a lane's open dispatch runs: an OpenCode dispatch
// starts fresh whatever session is recorded, a Claude dispatch there resumes.
func TestRestorePoolLaneKindIsTheDispatchs(t *testing.T) {
	for _, c := range []struct{ typ, kind, want string }{
		{"deepseek", "opencode", " --fresh"},
		{"opus", "claude", ""},
	} {
		t.Run(c.kind, func(t *testing.T) {
			f := newRfx(t)
			lane := f.p.PlainLane("L-01", "task/T-01")
			f.p.Config()
			cfg, err := config.Load(f.p.Root)
			if err != nil || len(cfg.Lanes()) != 1 {
				t.Fatalf("precondition: one lane L-01: %v, %v", cfg.Lanes(), err)
			}
			f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","base":"main","agent":"pj-l-01","type":"`+c.typ+`"}`)
			f.shell("w1:p1", lane, "pj-l-01")
			recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-01", Kind: c.kind, ID: "s-1", Started: time.Now().Unix()})
			f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
			f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1" + c.want)
			if c.want != "" {
				has(t, f.restoreLog(), "(worker, kind opencode resumes nothing: --fresh)", "the dispatch's kind is logged")
			} else {
				lacks(t, f.restoreLog(), "--fresh", "a Claude dispatch resumes")
			}
		})
	}
}

// A role pane comes back only while serve's state has work open for it (OpenWork): the lead always, a worker by its
// lane, the integrator while its job is open, the reviewer while its review is, and any other role while its brief
// is. The read is not asked for the lead.
func TestRestoreRolePanesNeedOpenWork(t *testing.T) {
	for _, c := range []struct {
		name, agent, role string
		open, restored    bool
	}{
		{"lead with no work", "pj-lead", "lead", false, true},
		{"integrator with an open job", "pj-int", "integrator", true, true},
		{"integrator with no open job", "pj-int", "integrator", false, false},
		{"reviewer with an open review", "pj-rev", "reviewer", true, true},
		{"reviewer with no open review", "pj-rev", "reviewer", false, false},
		{"deep dive with an open brief", "pj-deep-dive", "deep_dive", true, true},
		{"deep dive with no open work", "pj-deep-dive", "deep_dive", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRfx(t)
			f.p.Config(testkit.Set("agents.roles", `{"integrator": "", "reviewer": "opus", "deep_dive": "opus"}`))
			f.shell("w1:p1", f.p.Root, c.agent)
			var asked []string
			f.pl.OpenWork = func(agent, role string) (bool, error) {
				asked = append(asked, role+":"+agent)
				return c.open, nil
			}
			f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
			if c.restored {
				f.wantCalls(f.p.Root + " restart " + c.agent + " --pane w1:p1 --fresh")
				lacks(t, f.restoreLog(), "no open work", "a restored pane's skip line")
			} else {
				f.wantCalls()
				has(t, f.restoreLog(), " skip "+c.agent+" (pane w1:p1): no open work (its next brief or job starts it)\n",
					"the pane with no work is skipped and logged")
			}
			if c.role == "lead" {
				if len(asked) != 0 {
					t.Errorf("the lead's pane asked for open work: %q", asked)
				}
			} else if want := []string{c.role + ":" + c.agent}; len(asked) != 1 || asked[0] != want[0] {
				t.Errorf("open work asked for %q, want %q", asked, want)
			}
		})
	}
}

// A failed read of the role's open work restores its pane: a real job is never left stopped.
func TestRestoreRolePanesWhenTheOpenWorkReadFails(t *testing.T) {
	f := newRfx(t)
	f.p.Config(testkit.Set("agents.roles", `{"deep_dive": "opus"}`))
	f.shell("w1:p1", f.p.Root, "pj-deep-dive")
	f.pl.OpenWork = func(string, string) (bool, error) {
		return false, errors.New("serve's state unreadable")
	}
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root + " restart pj-deep-dive --pane w1:p1 --fresh")
	lacks(t, f.restoreLog(), "no open work", "a failed read's skip line")
}

// A free lane's worker is skipped: obor dispatch starts its worker.
func TestRestoreFreeLaneSkipped(t *testing.T) {
	f := newRfx(t)
	o1 := f.p.PlainLane("L-01", "task/T-01")
	f.free["L-01"] = true
	f.p.Config()
	f.shell("w1:p1", o1, "pj-l-01")
	f.pl.OpenWork = func(string, string) (bool, error) {
		t.Error("the free lane's worker asked for open work")
		return true, nil
	}
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls()
	has(t, f.restoreLog(), " skip pj-l-01 (pane w1:p1): lane L-01 is free", "the free lane skipped")
}

// a config naming keys this obor does not know (the promotion's) still loads for the
// restore (paneConfig): its lead pane restarts, never skipping the project without a word.
func TestRestoreLoadsAConfigWithUnknownKeys(t *testing.T) {
	f := newRfx(t)
	f.p.Config(testkit.Set("branches.promote", "release"), testkit.Set("merge.promote_weight", 2),
		testkit.Set("merge.weights", map[string]any{"core": 3}))
	if _, unknown, err := config.LoadForValidate(f.p.Root); err != nil || len(unknown) != 3 {
		t.Fatalf("precondition: the config names three unknown keys: %q, %v", unknown, err)
	}
	f.shell("w1:p1", f.p.Root, "pj-lead")
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p1 --fresh")
}

// Serve's run restores its own project alone: another obor project's labelled shell pane is that project's serve's.
func TestRestoreProjectOnlyItsProject(t *testing.T) {
	f := newRfx(t)
	lane := f.p.PlainLane("L-01", "task/T-01")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`) // its row holds T-01, not landed: busy
	f.shell("w1:p1", lane, "pj-l-01")
	f.shell("w1:p2", f.p.Root, "pj-lead")
	other := testkit.NewPlain(t)
	other.Config()
	f.shell("w2:p1", other.Root, "pj-lead")
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	f.wantCalls(f.p.Root+" restart pj-l-01 --pane w1:p1 --fresh", f.p.Root+" restart pj-lead --pane w1:p2 --fresh")
	for _, c := range f.calls {
		if strings.HasPrefix(c, other.Root+" ") {
			t.Errorf("serve's run restarted the other project's pane: %q", c)
		}
	}
}

// A labelled pane at its shell whose agent serve proves live in another pane is no pane to restore it in: the restart's
// decision refuses it before anything is touched, and the refusal is the one line in restore.log.
func TestRestoreRefusedWhileTheAgentRunsElsewhere(t *testing.T) {
	f := newRfx(t)
	lane := f.p.PlainLane("L-01", "task/T-01")
	f.p.Config()
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
	f.shell("w1:p1", lane, "pj-l-01")
	f.h.Agent("w1:p7", "pj-l-01", "idle", lane)
	f.h.Set("w1:p7", func(p *mpane) { p.Label = "notes" })
	recordSessions(t, f.p.Root, store.Session{Agent: "pj-l-01", Kind: "claude", ID: "sid-l-01", Started: time.Now().Unix()})
	f.pl.Restart = func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
		l := &launch.Launcher{Cfg: cfg, Env: f.p.Env(), H: f.h, Out: w,
			Where: func(context.Context, wire.WhereReq) (wire.WhereReply, error) {
				return wire.WhereReply{State: wire.WhereIdentified, Pane: "w1:p7", Source: "session", Label: "notes"}, nil
			}}
		return l.Restart(ctx, req)
	}
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	has(t, f.restoreLog(), "obor restart pj-l-01: FAIL pj-l-01 already runs in pane w1:p7 (proof: session; label notes): "+
		"obor restart pj-l-01 --pane w1:p7 restarts it there, or quit it there first\n", "the refusal logged")
	for _, c := range f.h.Calls() {
		if strings.HasPrefix(c, "Write") || strings.HasPrefix(c, "RenamePane") || strings.HasPrefix(c, "Start") {
			t.Errorf("the refused restore touched a pane: %q", c)
		}
	}
	if p := f.h.At("w1:p7"); p.Kind != "claude" || p.Name != "pj-l-01" {
		t.Errorf("the live agent = %+v, want it running", p)
	}
}

// stops makes the restore's Stop a stub that records "stop <agent> --pane <pane>" and leaves the pane at its shell.
func (f *rfx) stops() *[]string {
	var got []string
	f.pl.Stop = func(_ context.Context, cfg *config.Config, agent, pane string, w io.Writer) (string, error) {
		f.mu.Lock()
		got = append(got, "stop "+agent+" --pane "+pane)
		f.mu.Unlock()
		f.h.Set(pane, func(p *mpane) { p.Kind, p.Name, p.Status = "", "", "" })
		return "STOPPED " + agent, nil
	}
	return &got
}

// resumedPane is a lost labelled pane running the agent the terminal resumed on its own (herdr's bare resume, idle).
func (f *rfx) resumedPane(pane, cwd, label string) {
	f.h.Agent(pane, label, "idle", cwd, func(p *mpane) { p.Label = label })
}

// After a herdr restart with herdr's own resume on, each lost labelled pane runs the agent herdr resumed, which obor did
// not start: the restore treats it as a shell. The lead with a session is restarted by obor (resumed), a working
// lane's worker, the restart of both asking its open job's wake itself (Launcher.Job); a free lane's worker and a role with no open work have herdr's agent stopped and
// their shell left. With herdr's resume off (shells) the same panes restart as before and nothing is stopped.
func TestRestoreReplacesTheTerminalsResumedAgents(t *testing.T) {
	for _, resumed := range []bool{true, false} {
		t.Run(map[bool]string{true: "resume on: herdr's agents", false: "resume off: shells"}[resumed], func(t *testing.T) {
			f := newRfx(t)
			o1, o3 := f.p.PlainLane("L-01", "task/T-01"), f.p.PlainLane("L-03", "task/T-03")
			f.free["L-03"] = true
			f.p.Config(testkit.Set("agents.roles", `{"reviewer": "opus"}`))
			f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
			pane := f.shell
			if resumed {
				pane = f.resumedPane
			}
			pane("w1:p1", o1, "pj-l-01")
			pane("w1:p3", f.p.Root, "pj-lead")
			pane("w1:p4", f.p.Root, "pj-rev")
			pane("w1:p5", o3, "pj-l-03")
			recordSessions(t, f.p.Root,
				store.Session{Agent: "pj-lead", Kind: "claude", ID: "sid-lead", Started: time.Now().Unix()},
				store.Session{Agent: "pj-l-01", Kind: "claude", ID: "sid-l-01", Started: time.Now().Unix()})
			f.pl.OpenWork = func(string, string) (bool, error) { return false, nil }
			stops := f.stops()
			f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})

			f.wantCalls(f.p.Root+" restart pj-l-01 --pane w1:p1", f.p.Root+" restart pj-lead --pane w1:p3")
			for _, agent := range []string{"pj-l-01", "pj-lead"} {
				if got, ran := f.prompts[agent]; !ran || got != "" {
					t.Errorf("%s restarted with the prompt %q (ran %v), want none: a first prompt is the launcher's", agent, got, ran)
				}
			}
			log := f.restoreLog()
			if !resumed {
				if len(*stops) != 0 {
					t.Errorf("shells: stopped %q, want nothing", *stops)
				}
				has(t, log, " skip pj-l-03 (pane w1:p5): lane L-03 is free (dispatch starts its worker)\n", "the free lane skipped")
				has(t, log, " skip pj-rev (pane w1:p4): no open work (its next brief or job starts it)\n", "the idle role skipped")
				lacks(t, log, "the terminal resumed", "no resumed agent named")
				return
			}
			if want := []string{"stop pj-rev --pane w1:p4", "stop pj-l-03 --pane w1:p5"}; !slices.Equal(*stops, want) {
				t.Errorf("stopped %q, want %q", *stops, want)
			}
			for _, id := range []string{"w1:p4", "w1:p5"} {
				if p := f.h.At(id); p.Kind != "" || p.Label == "" {
					t.Errorf("pane %s = %+v, want its labelled shell left", id, p)
				}
			}
			has(t, log, " restore pj-l-01 (pane w1:p1) in "+f.p.Root+" (the terminal resumed claude without obor's start: "+
				"obor restart replaces it)\n", "the worker's line says it replaces herdr's")
			has(t, log, " restore pj-lead (pane w1:p3) in "+f.p.Root+" (the terminal resumed claude without obor's start: "+
				"obor restart replaces it)\n", "the lead's line says it replaces herdr's")
			has(t, log, " stop pj-l-03 (pane w1:p5): lane L-03 is free (dispatch starts its worker); the terminal resumed "+
				"claude without obor's start: stopped, its shell left\nSTOPPED pj-l-03\n", "the free lane's stop line and result")
			has(t, log, " stop pj-rev (pane w1:p4): no open work (its next brief or job starts it); the terminal resumed "+
				"claude without obor's start: stopped, its shell left\nSTOPPED pj-rev\n", "the idle role's stop line and result")
			lacks(t, log, " skip ", "no pane skipped as running an agent")
		})
	}
}

// A lost pane whose agent obor started after the loss (its start record names the pane and the pid leading the pane's
// foreground group) is left as it is: a reconnect's queued run restarts no pane twice. A record of another pid or
// another pane is no proof: that agent is the terminal's, replaced.
func TestRestoreLeavesAPaneOborStartedSince(t *testing.T) {
	for _, c := range []struct {
		name, pane string
		pid        int
		untouched  bool
	}{
		{"its pid recorded in the pane", "w1:p1", 2, true},
		{"another pid recorded", "w1:p1", 7, false},
		{"recorded in another pane", "w1:p9", 2, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRfx(t)
			f.p.Config()
			f.resumedPane("w1:p1", f.p.Root, "pj-lead")
			recordSessions(t, f.p.Root, store.Session{Agent: "pj-lead", Kind: "claude", ID: "sid-lead", Started: time.Now().Unix()})
			if pi, err := f.h.ProcessInfo(context.Background(), "w1:p1"); err != nil || pi.FgPGID != 2 || pi.AtShell() {
				t.Fatalf("precondition: the agent leads the pane's foreground as pid 2: %+v, %v", pi, err)
			}
			if err := launch.RecordStart(f.p.Root, "pj-lead", c.pane, c.pid); err != nil {
				t.Fatal(err)
			}
			stops := f.stops()
			f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
			if len(*stops) != 0 {
				t.Errorf("stopped %q, want nothing", *stops)
			}
			if c.untouched {
				f.wantCalls()
				has(t, f.restoreLog(), " skip pj-lead (pane w1:p1): runs claude (obor started it since)\n", "the skip line")
				return
			}
			f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p1")
			has(t, f.restoreLog(), "(the terminal resumed claude without obor's start: obor restart replaces it)", "replaced")
		})
	}
}

// herdr names the agent it resumes before its process runs (the pane's shell still has the foreground), then reports
// it unknown while it starts: a pane still starting runs an agent, and the restore waits for it to settle before it
// replaces it. A pane whose agent never comes up within the run's bound is a shell.
func TestRestoreWaitsForAResumedAgentToSettle(t *testing.T) {
	t.Run("still starting: waited for, then replaced", func(t *testing.T) {
		f := newRfx(t)
		f.p.Config()
		f.h.Agent("w1:p1", "pj-lead", "idle", f.p.Root, func(p *mpane) { p.Label, p.Foreground = "pj-lead", 1 })
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-lead", Kind: "claude", ID: "sid-lead", Started: time.Now().Unix()})
		f.pl.Settle, f.pl.Poll = 10*time.Second, time.Millisecond
		looks, atRestart := 0, 0
		f.h.Before(func(call string) {
			if call != "ProcessInfo pane=w1:p1" {
				return
			}
			switch looks++; looks {
			case 2: // its process runs, herdr's old status still shown (the probe: ~0.3 s)
				f.h.Set("w1:p1", func(p *mpane) { p.Foreground = 0 })
			case 3: // starting
				f.h.Set("w1:p1", func(p *mpane) { p.Status = "unknown" })
			case 5: // up
				f.h.Set("w1:p1", func(p *mpane) { p.Status = "idle" })
			}
		})
		restart := f.pl.Restart
		f.pl.Restart = func(ctx context.Context, cfg *config.Config, req launch.Request, w io.Writer) (string, error) {
			atRestart = looks
			return restart(ctx, cfg, req, w)
		}
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p1")
		if atRestart < 6 {
			t.Errorf("restarted after %d looks at the pane, want the wait through its start and two looks at it up (≥ 6)",
				atRestart)
		}
		has(t, f.restoreLog(), "(the terminal resumed claude without obor's start: obor restart replaces it)",
			"the starting pane is taken as running an agent")
		lacks(t, f.restoreLog(), "has not settled", "it settled")
	})
	t.Run("never comes up: a shell past the bound", func(t *testing.T) {
		f := newRfx(t)
		f.p.Config()
		f.h.Agent("w1:p1", "pj-lead", "idle", f.p.Root, func(p *mpane) { p.Label, p.Foreground = "pj-lead", 1 })
		recordSessions(t, f.p.Root, store.Session{Agent: "pj-lead", Kind: "claude", ID: "sid-lead", Started: time.Now().Unix()})
		f.pl.Settle, f.pl.Poll = 30*time.Millisecond, time.Millisecond
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-lead --pane w1:p1")
		log := f.restoreLog()
		has(t, log, " pj-lead (pane w1:p1): the terminal's claude has not settled by the restore's bound: taken as it is\n",
			"the bound is logged")
		lacks(t, log, "the terminal resumed", "restored as a shell")
	})
}

// the restore reads the session from the Sessions function (the App's fold), not from the store on disk: a session
// there and none on disk resumes; one on disk and none there starts fresh.
func TestRestoreReadsSessionFromSessions(t *testing.T) {
	setup := func(t *testing.T) *rfx {
		f := newRfx(t)
		lane := f.p.PlainLane("L-01", "task/T-01")
		f.p.Config()
		f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
		f.shell("w1:p1", lane, "pj-l-01")
		return f
	}
	claude := store.Session{Agent: "pj-l-01", Kind: "claude", ID: "sid-l-01", Started: time.Now().Unix()}
	t.Run("in the fold, none on disk: resumes", func(t *testing.T) {
		f := setup(t)
		if _, ok := store.CurrentAt(f.p.Root, "pj-l-01"); ok {
			t.Fatal("precondition: a session on disk")
		}
		f.pl.Sessions = func(root, agent string) (store.Session, bool) {
			return claude, root == f.p.Root && agent == claude.Agent
		}
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1")
		lacks(t, f.restoreLog(), "no recorded session", "no --fresh note")
	})
	t.Run("on disk, none in the fold: fresh", func(t *testing.T) {
		f := setup(t)
		recordSessions(t, f.p.Root, claude)
		if _, ok := store.CurrentAt(f.p.Root, "pj-l-01"); !ok {
			t.Fatal("precondition: no session on disk")
		}
		f.pl.Sessions = func(string, string) (store.Session, bool) { return store.Session{}, false }
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1 --fresh")
		has(t, f.restoreLog(), "no recorded session", "fresh")
	})
}
