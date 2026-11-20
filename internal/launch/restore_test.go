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
	"obor/internal/checks"
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
}

func newRfx(t *testing.T) *rfx {
	t.Helper()
	return newRfxAt(t, false)
}

// newGitRfx is newRfx over a real repository: for the test of the lane state read (checks.LaneTable runs git).
func newGitRfx(t *testing.T) *rfx {
	t.Helper()
	return newRfxAt(t, true)
}

func newRfxAt(t *testing.T, git bool) *rfx {
	t.Helper()
	p := testkit.NewPlain(t)
	if git {
		p = testkit.New(t)
	}
	f := &rfx{t: t, p: p, fail: map[string]bool{}, prompts: map[string]string{}}
	f.h = newTerm()
	f.pl = &launch.Restorer{Env: f.p.Env(), H: f.h, Now: func() time.Time { return time.Unix(0, 0) },
		LaneState: func(cfg *config.Config, l config.Lane) string {
			if !git {
				return port.LaneWorking // a lane with its task: the free-lane skip is newGitRfx's
			}
			return checks.NewLaneTable(context.Background(), cfg).LaneState(l).State
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
	f.wantCalls(f.p.Root+" restart pj-l-01 --pane w1:p1", f.p.Root+" restart pj-lead --pane w1:p2 --fresh")
	log := f.restoreLog()
	has(t, log, " restore pj-l-01 (pane w1:p1) in "+f.p.Root+"\nstub restart ran: pj-l-01\nRESTARTED pj-l-01\n",
		"the restore line, the restart's output and result")
	has(t, log, " restore pj-lead (pane w1:p2) in "+f.p.Root+" (lead, no recorded session: --fresh)\n",
		"a role with no recorded session starts fresh (T-627)")
	has(t, log, " skip pj-int (pane w1:p6): runs claude", "busy pane: skipped, logged")
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
	f := newGitRfx(t) // the free lane is checks.LaneTable's answer: git
	o1 := f.p.Lane("L-01", "task/T-01")
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

// A restored lane worker and the integrator start with the wake of the job serve holds open for them (Restorer.Job)
// as their first prompt; an agent with no open job, and a role that has no job (lead, reviewer), start with none. A
// free lane's worker stays skipped, its job never asked for.
func TestRestoreCarriesTheOpenJobsWake(t *testing.T) {
	f := newGitRfx(t) // the free lane is checks.LaneTable's answer: git
	o1, o2 := f.p.Lane("L-01", "task/T-01"), f.p.Lane("L-03", "task/T-03")
	f.p.Config(testkit.Set("agents.roles", `{"integrator": "", "reviewer": "opus"}`))
	f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
	f.shell("w1:p1", o1, "pj-l-01")
	f.shell("w1:p2", f.p.Root, "pj-int")
	f.shell("w1:p3", f.p.Root, "pj-lead")
	f.shell("w1:p4", f.p.Root, "pj-rev")
	f.shell("w1:p5", o2, "pj-l-03")
	f.pl.OpenWork = func(string, string) (bool, error) { return true, nil }
	jobs := map[string]string{"pj-l-01": "T-01/1/work", "pj-int": "T-09/2/review"}
	var asked []string
	f.pl.Job = func(root, agent, role, lane string) (string, error) {
		if root != f.p.Root {
			t.Errorf("job asked for root %q, want %q", root, f.p.Root)
		}
		asked = append(asked, role+":"+agent+":"+lane)
		return jobs[agent], nil
	}
	f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
	for agent, want := range map[string]string{
		"pj-l-01": wire.WakeLine("T-01/1/work"), // the working lane's worker: job N's wake
		"pj-int":  wire.WakeLine("T-09/2/review"),
		"pj-lead": "", // no job for a role that has none
		"pj-rev":  "",
	} {
		got, ran := f.prompts[agent]
		if !ran {
			t.Errorf("%s was not restarted: calls %q", agent, f.calls)
		} else if got != want {
			t.Errorf("%s restarted with the prompt %q, want %q", agent, got, want)
		}
	}
	if _, ran := f.prompts["pj-l-03"]; ran {
		t.Errorf("the free lane's worker was restarted: %q", f.calls)
	}
	slices.Sort(asked)
	if want := []string{"integrator:pj-int:", "worker:pj-l-01:L-01"}; !slices.Equal(asked, want) {
		t.Errorf("the job was asked for %q, want %q (only a lane's worker and the integrator)", asked, want)
	}
	has(t, f.restoreLog(), " skip pj-l-03 (pane w1:p5): lane L-03 is free", "the free lane skipped")
	has(t, f.restoreLog(), "(wake: "+wire.WakeLine("T-01/1/work")+")", "the wake is logged")
}

// A worker with no open job restarts with no prompt; a job that cannot be read restores the pane with no wake, and the
// log says why.
func TestRestoreWithNoJobOrAnUnreadableOne(t *testing.T) {
	for _, c := range []struct {
		name string
		job  func(root, agent, role, lane string) (string, error)
		log  string
	}{
		{"no open job", func(string, string, string, string) (string, error) { return "", nil }, ""},
		{"job unknown", func(string, string, string, string) (string, error) { return "", errors.New("no served project") },
			"its open job is not known (no served project): restored with no wake"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRfx(t)
			o1 := f.p.PlainLane("L-01", "task/T-01")
			f.p.Config()
			f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
			f.shell("w1:p1", o1, "pj-l-01")
			f.pl.Job = c.job
			f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
			f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1 --fresh")
			if p := f.prompts["pj-l-01"]; p != "" {
				t.Errorf("restarted with the prompt %q, want none", p)
			}
			if c.log != "" {
				has(t, f.restoreLog(), c.log, "the log's reason")
			}
		})
	}
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
