package launch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
)

// spawned is one Spawn call: the args, the output file and the cwd.
type spawned struct{ args, out, dir string }

// self runs req (stop: --stop) as the agent in pane own would (OBOR_PANE), scheduled: as the detached run.
func (x *fx) self(own string, scheduled bool, req launch.Request, stop bool, tune func(*launch.Launcher)) (string, bool, []spawned) {
	x.t.Helper()
	var out bytes.Buffer
	l := x.launcher(&out)
	kv := []string{"CLAUDE_CONFIG_DIR=" + x.claude, "OBOR_PANE=" + own}
	if scheduled {
		op := "restart"
		if stop {
			op = "stop"
		}
		kv = append(kv, launch.ScheduledEnv+"="+op)
	}
	l.Env = x.p.Env(kv...)
	var spawns []spawned
	l.Spawn = func(dir string, args []string, out string) error {
		spawns = append(spawns, spawned{strings.Join(args, " "), out, dir})
		return nil
	}
	if tune != nil {
		tune(l)
	}
	var line string
	var err error
	if stop {
		line, err = l.Stop(context.Background(), req.Agent, req.Pane, req.Force)
	} else {
		line, err = l.Restart(context.Background(), req)
	}
	s, ok := result(x.t, &out, line, err)
	return s, ok, spawns
}

// idleAfter makes pane p's agent go idle after n agent looks; *got counts them. The status changes under the fake's
// lock: the fake's server writes the pane while the launcher polls it.
type idleAfter struct {
	port.PaneControl
	f   *herdrtest.Fake
	p   *herdrtest.Pane
	n   int
	got *int
}

func (w idleAfter) Pane(ctx context.Context, target string) (port.Pane, error) {
	if target == w.p.ID {
		if *w.got++; *w.got > w.n {
			w.f.Set(w.p.ID, func(p *herdrtest.Pane) { p.Status = "idle" })
		}
	}
	return w.PaneControl.Pane(ctx, target)
}

// obor restart of the caller's own pane schedules a detached run that restarts the agent once it is idle.
func TestRestartOwnPane(t *testing.T) {
	t.Parallel()
	t.Run("own pane: SCHEDULED, a detached obor restart with the same args, nothing stopped", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			name string
			req  launch.Request
			stop bool
			args string
			line string
		}{
			{"resume", launch.Request{Agent: "pj-l-01"}, false, "restart pj-l-01 --pane w1:p3",
				"SCHEDULED restart of pj-l-01 (pane w1:p3): runs once it is idle"},
			{"--fresh --force", launch.Request{Agent: "pj-l-01", Fresh: true, Force: true}, false,
				"restart pj-l-01 --pane w1:p3 --fresh --force", "SCHEDULED restart of pj-l-01 (pane w1:p3): runs once it is idle"},
			{"--stop", launch.Request{Agent: "pj-l-01"}, true, "restart pj-l-01 --pane w1:p3 --stop",
				"SCHEDULED stop of pj-l-01 (pane w1:p3): runs once it is idle"},
		} {
			t.Run(c.name, func(t *testing.T) {
				x := setup(t)
				x.sid("pj-l-01", "sid-1")
				x.agent("w1:p3", "pj-l-01", "claude", "working")
				out, ok, spawns := x.self("w1:p3", false, c.req, c.stop, nil)
				eq(t, last(out), c.line, "result")
				if !ok || len(spawns) != 1 {
					t.Fatalf("one detached run: %v\n%s", spawns, out)
				}
				eq(t, spawns[0].args, c.args, "detached args")
				eq(t, spawns[0].out, filepath.Join(x.p.Root, ".obor", "state", "restart-pj-l-01.log"), "its output")
				eq(t, spawns[0].dir, x.p.Root, "its cwd: this project's main checkout (T-301)")
				if len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
					t.Errorf("nothing typed, stopped or started: %q", x.srv.Calls())
				}
			})
		}
	})
	t.Run("own pane, no session to resume: FAIL now, nothing scheduled", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-l-01", "claude", "working")
		out, ok, spawns := x.self("w1:p3", false, launch.Request{Agent: "pj-l-01"}, false, nil)
		eq(t, failLine(out), "FAIL no session to resume for pj-l-01 (pass --fresh to start a new one)", "result")
		if ok || len(spawns) != 0 {
			t.Errorf("nothing scheduled: %v", spawns)
		}
	})
	t.Run("the detached run: waits while working, restarts once idle", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		got := 0
		x.wrap = func(h port.PaneControl) port.PaneControl { return idleAfter{h, x.f, a, 3, &got} }
		out, ok, spawns := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, false, func(l *launch.Launcher) {
			l.Sched = time.Minute
		})
		if got <= 3 {
			t.Errorf("precondition: polled while working (%d agent gets)", got)
		}
		if !ok || last(out) != "RESTARTED pj-l-01 (pane w1:p3, session sid-1)" {
			t.Fatalf("restarted once idle:\n%s", out)
		}
		contains(t, out, "waiting for pj-l-01 to go idle", "said")
		lacks(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_keys keys=[esc] pane_id=w1:p3", "no Escape: it went idle by itself")
		eqInt(t, len(spawns), 0, "not scheduled again")
	})
	t.Run("the detached run: background tasks (the exit dialog) fail it", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "idle")
		x.f.Set("w1:p3", func(p *herdrtest.Pane) { p.OnQuit = "dialog" })
		out, ok, _ := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, false, nil)
		eq(t, failLine(out), "FAIL pj-l-01 has background tasks (finish them, or --force to stop them)", "result")
		if ok || x.startCall() != "" {
			t.Error("nothing started")
		}
	})
	t.Run("the detached run: still working at the timeout: gives up, logged with evidence, nothing typed", func(t *testing.T) {
		t.Parallel()
		for _, stop := range []bool{false, true} {
			x := setup(t)
			x.sid("pj-l-01", "sid-1")
			x.agent("w1:p3", "pj-l-01", "claude", "working")
			out, ok, _ := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, stop, func(l *launch.Launcher) {
				l.Sched = 30 * time.Millisecond
			})
			word := "restart"
			if stop {
				word = "stop"
			}
			want := "FAIL pj-l-01 still working after 30ms: scheduled " + word + " not run (pane w1:p3)"
			eq(t, failLine(out), want, "result")
			if ok || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
				t.Errorf("nothing typed or started: %q", x.srv.Calls())
			}
			log, err := os.ReadFile(filepath.Join(x.p.Root, ".obor", "state", "launch.log"))
			if err != nil {
				t.Fatal(err)
			}
			contains(t, string(log), strings.TrimPrefix(want, "FAIL "), "launch.log")
			contains(t, string(log), "agent status: working", "launch.log evidence")
		}
	})
	t.Run("another pane: unchanged (FAIL while working, restarts when idle, nothing scheduled)", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "working")
		out, ok, spawns := x.self("w1:p9", false, launch.Request{Agent: "pj-l-01"}, false, nil)
		eq(t, failLine(out), "FAIL pj-l-01 is working (--force to restart anyway)", "working")
		if ok || len(spawns) != 0 {
			t.Errorf("nothing scheduled: %v", spawns)
		}
		x.f.Set("w1:p3", func(p *herdrtest.Pane) { p.Status = "idle" })
		out, ok, spawns = x.self("w1:p9", false, launch.Request{Agent: "pj-l-01"}, false, nil)
		if !ok || last(out) != "RESTARTED pj-l-01 (pane w1:p3, session sid-1)" || len(spawns) != 0 {
			t.Errorf("restarted in place, nothing scheduled: %v\n%s", spawns, out)
		}
	})
}

// polled runs hook before the n-th agent look of pane p (its error, if any, is the call's: herdr unavailable), and
// keeps p's agent status at each /exit typed into p.
type polled struct {
	port.PaneControl
	p      *herdrtest.Pane
	got    *int
	hook   func(n int) error
	atExit *[]string
}

func (w polled) Pane(ctx context.Context, target string) (port.Pane, error) {
	if target == w.p.ID {
		*w.got++
		if err := w.hook(*w.got); err != nil {
			return port.Pane{}, err
		}
	}
	return w.PaneControl.Pane(ctx, target)
}

func (w polled) Write(ctx context.Context, pane, text string, submit bool) error {
	if pane == w.p.ID && strings.Contains(text, "/exit") {
		*w.atExit = append(*w.atExit, w.p.Status)
	}
	return w.PaneControl.Write(ctx, pane, text, submit)
}

// the handoff is `obor restart <you> --fresh` from the agent's own pane, with no --force and no handoff
// machinery: SCHEDULED while it works (nothing stopped or typed), and the detached run, its args round-tripped, waits
// until the agent is idle and restarts it with a new session.
func TestRestartSelfFreshHandoff(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"pj-lead", "pj-l-01"} {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.sid(agent, "sid-1")
			a := x.agent("w1:p3", agent, "claude", "working")
			out, ok, spawns := x.self("w1:p3", false, launch.Request{Agent: agent, Fresh: true}, false, nil)
			eq(t, last(out), "SCHEDULED restart of "+agent+" (pane w1:p3): runs once it is idle", "result")
			if !ok || len(spawns) != 1 {
				t.Fatalf("one detached run: %v\n%s", spawns, out)
			}
			eq(t, spawns[0].args, "restart "+agent+" --pane w1:p3 --fresh", "detached args: no --force")
			if len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
				t.Fatalf("nothing typed, stopped or started while it works: %q", x.srv.Calls())
			}
			req, err := launch.ParseRequest(strings.Fields(spawns[0].args)[1:])
			if err != nil || !req.Fresh || req.Force {
				t.Fatalf("the detached argv: %+v, %v", req, err)
			}
			got := 0
			x.wrap = func(h port.PaneControl) port.PaneControl { return idleAfter{h, x.f, a, 2, &got} }
			out, ok, spawns = x.self("w1:p3", true, req, false, func(l *launch.Launcher) { l.Sched = time.Minute })
			if !ok || len(spawns) != 0 {
				t.Fatalf("the detached run: %v, spawns %v\n%s", ok, spawns, out)
			}
			if got <= 2 {
				t.Errorf("the detached run polled %d times: it did not wait for idle", got)
			}
			eq(t, last(out), "RESTARTED "+agent+" (pane w1:p3, session new)", "result: a fresh session")
		})
	}
}

// the detached run waits first, then restarts or stops on fresh state; an unknown status is no idle.
func TestScheduledWaitsThenDecidesFresh(t *testing.T) {
	t.Parallel()
	own := launch.Request{Agent: "pj-l-01", Pane: "w1:p3"}
	// run is the detached run of req (stop: --stop) with hook on the agent's polls; got counts them, atExit is the
	// status at each /exit.
	run := func(x *fx, a *herdrtest.Pane, req launch.Request, stop bool, sched time.Duration, hook func(n int) error) (out string, ok bool, got int, atExit []string) {
		x.t.Helper()
		x.wrap = func(h port.PaneControl) port.PaneControl { return polled{h, a, &got, hook, &atExit} }
		out, ok, spawns := x.self("w1:p3", true, req, stop, func(l *launch.Launcher) { l.Sched = sched })
		if len(spawns) != 0 {
			x.t.Errorf("not scheduled again: %v", spawns)
		}
		return out, ok, got, atExit
	}
	untouched := func(t *testing.T, x *fx) {
		t.Helper()
		if len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing typed, stopped or started: %q", x.srv.Calls())
		}
	}
	record := func(x *fx) string { return x.recorded("pj-l-01") + "\n" }
	down := errors.New("herdr went away")

	t.Run("#1 #2 a transient agent get error, then working/blocked: no /exit until idle", func(t *testing.T) {
		t.Parallel()
		for _, status := range []string{"working", "blocked"} {
			for _, stop := range []bool{false, true} {
				x := setup(t)
				x.sid("pj-l-01", "sid-1")
				a := x.agent("w1:p3", "pj-l-01", "claude", status)
				failed := false
				out, ok, got, atExit := run(x, a, own, stop, time.Minute, func(n int) error {
					switch {
					case n == 1:
						failed = true
						return down
					case n > 4:
						x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Status = "idle" })
					}
					return nil
				})
				if !failed || got <= 4 {
					t.Fatalf("%s stop=%v precondition: one error, then polled while %s (%d polls)\n%s", status, stop, status, got, out)
				}
				want := "RESTARTED pj-l-01 (pane w1:p3, session sid-1)"
				if stop {
					want = "STOPPED pj-l-01 (pane w1:p3)"
				}
				eq(t, last(out), want, status+" result")
				if !ok || len(atExit) != 1 || atExit[0] != "idle" {
					t.Errorf("%s stop=%v: one /exit, once idle: %v\n%s", status, stop, atExit, out)
				}
				lacks(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_keys keys=[esc] pane_id=w1:p3", "no Escape")
			}
		}
	})
	t.Run("#3 errors for the whole budget: gives up naming the error, no /exit", func(t *testing.T) {
		t.Parallel()
		for _, stop := range []bool{false, true} {
			x := setup(t)
			x.sid("pj-l-01", "sid-1")
			a := x.agent("w1:p3", "pj-l-01", "claude", "working")
			out, ok, got, _ := run(x, a, own, stop, 30*time.Millisecond, func(int) error { return down })
			if got < 2 {
				t.Errorf("precondition: kept polling (%d)", got)
			}
			if ok {
				t.Fatalf("stop=%v: not run\n%s", stop, out)
			}
			contains(t, failLine(out), "FAIL pj-l-01 status unknown after 30ms (last error: ", "result")
			contains(t, failLine(out), "herdr went away", "names the last error")
			word := "restart"
			if stop {
				word = "stop"
			}
			contains(t, failLine(out), "scheduled "+word+" not run (pane w1:p3)", "result")
			untouched(t, x)
		}
	})
	t.Run("#4 agent not found (it exited while waiting): proceeds", func(t *testing.T) {
		t.Parallel()
		for _, stop := range []bool{false, true} {
			x := setup(t)
			x.sid("pj-l-01", "sid-1")
			a := x.agent("w1:p3", "pj-l-01", "claude", "working")
			out, ok, got, atExit := run(x, a, own, stop, time.Minute, func(n int) error {
				if n == 2 {
					x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
				}
				return nil
			})
			want := "RESTARTED pj-l-01 (pane w1:p3, session sid-1)"
			if stop {
				want = "STOPPED pj-l-01 (pane w1:p3, already at a shell)"
			}
			eq(t, last(out), want, "result")
			if !ok || len(atExit) != 0 || got < 2 {
				t.Errorf("stop=%v: no /exit, waited until gone (%d polls): %v\n%s", stop, got, atExit, out)
			}
		}
	})
	t.Run("#5 session changes while waiting, herdr only: resumes the recorded one, warns, record kept (T-577)", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-before")
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Session = "sid-before" })
		out, ok, _, _ := run(x, a, own, false, time.Minute, func(n int) error {
			if n > 2 {
				x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Status, p.Session = "idle", "sid-after" })
			}
			return nil
		})
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-before)", "result")
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "warning: the terminal reports session sid-after for pane w1:p3, the recorded session is sid-before: resuming sid-before", "warned")
		contains(t, x.startCall(), "--resume sid-before", "recorded session resumed")
		eq(t, record(x), "sid-before\n", "record kept")
	})
	t.Run("#6 session changes while waiting, herdr and record: resumes the new one, no warning", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-before")
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Session = "sid-before" })
		out, ok, _, _ := run(x, a, own, false, time.Minute, func(n int) error {
			if n == 3 {
				x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Status, p.Session = "idle", "sid-after" })
				x.sid("pj-l-01", "sid-after")
			}
			return nil
		})
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-after)", "result")
		if !ok || strings.Contains(out, "warning:") {
			t.Errorf("no warning: %q", out)
		}
		contains(t, x.startCall(), "--resume sid-after", "new session resumed")
	})
	t.Run("#7 --fresh while the session changes: a new session, herdr's not resumed", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-before")
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Session = "sid-before" })
		out, ok, _, _ := run(x, a, launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true}, false, time.Minute,
			func(n int) error {
				if n > 2 {
					x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Status, p.Session = "idle", "sid-after" })
				}
				return nil
			})
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
		if !ok || strings.Contains(out, "warning: recorded session") {
			t.Errorf("no session warning: %q", out)
		}
		lacks(t, x.startCall(), "sid-after", "not herdr's new session")
		lacks(t, x.startCall(), "--resume", "a new session")
		eq(t, record(x), "sid-before\n", "record kept")
	})
	t.Run("#8 pane relabelled to another agent while waiting: FAIL, no /exit (also --force)", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			name   string
			change func(a *herdrtest.Pane)
			fail   string
		}{
			{"relabelled for another configured agent", func(a *herdrtest.Pane) { a.Label, a.Name = "pj-rev", "pj-rev" },
				"FAIL pane w1:p3 is labelled pj-rev, not pj-l-01"},
		} {
			for _, stop := range []bool{false, true} {
				for _, force := range []bool{false, true} {
					x := setup(t)
					x.sid("pj-l-01", "sid-1")
					a := x.agent("w1:p3", "pj-l-01", "claude", "working")
					req := own
					req.Force = force
					out, ok, got, atExit := run(x, a, req, stop, time.Minute, func(n int) error {
						if n == 2 {
							x.f.Set(a.ID, func(p *herdrtest.Pane) { c.change(p); p.Status = "idle" })
						}
						return nil
					})
					if got < 2 {
						t.Errorf("precondition: changed while waiting (%d polls)", got)
					}
					eq(t, failLine(out), c.fail, fmt.Sprintf("%s stop=%v force=%v", c.name, stop, force))
					if ok || len(atExit) != 0 {
						t.Errorf("%s stop=%v force=%v: no /exit: %v", c.name, stop, force, atExit)
					}
					untouched(t, x)
				}
			}
		}
	})
}

// After the scheduled wait, sameAgent fails closed. One test per row of the spec's table.
func TestSameAgentAfterScheduledWait(t *testing.T) {
	t.Parallel()
	type kind struct{ agent, pane, kind string }
	claude, codex := kind{"pj-l-01", "w1:p3", "claude"}, kind{"pj-rev", "w1:p7", "codex"}
	down := errors.New("herdr went away")

	// run is the detached run: the agent is working on poll 1; on poll 2 change(a) is applied and it goes idle; from
	// poll 3 on (sameAgent's lookup) later(n) decides the answer. got counts the polls.
	run := func(x *fx, k kind, req launch.Request, stop bool, change func(a *herdrtest.Pane), later func(n int) error) (out string, ok bool, got int, atExit []string) {
		x.t.Helper()
		a := x.agent(k.pane, k.agent, k.kind, "working")
		x.f.Set(a.ID, func(p *herdrtest.Pane) { p.Session = "sid-1" })
		x.wrap = func(h port.PaneControl) port.PaneControl {
			return polled{h, a, &got, func(n int) error {
				switch {
				case n == 2:
					x.f.Set(a.ID, func(p *herdrtest.Pane) {
						change(p)
						p.Status = "idle"
					})
				case n > 2 && later != nil:
					return later(n)
				}
				return nil
			}, &atExit}
		}
		out, ok, spawns := x.self(k.pane, true, req, stop, func(l *launch.Launcher) { l.Sched = time.Minute })
		if len(spawns) != 0 {
			x.t.Errorf("not scheduled again: %v", spawns)
		}
		if got < 3 {
			x.t.Errorf("precondition: waited, then looked up again (%d polls)\n%s", got, out)
		}
		return out, ok, got, atExit
	}
	setupFor := func(t *testing.T, k kind) *fx {
		x := setup(t)
		if k == claude {
			x.sid(k.agent, "sid-1")
		}
		return x
	}
	untouched := func(t *testing.T, x *fx, atExit []string) {
		t.Helper()
		if len(atExit) != 0 || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
			t.Errorf("no /exit, /quit, Escape or start: %q", x.srv.Calls())
		}
	}
	result := func(k kind, stop bool) string {
		if stop {
			return fmt.Sprintf("STOPPED %s (pane %s)", k.agent, k.pane)
		}
		return fmt.Sprintf("RESTARTED %s (pane %s, session sid-1)", k.agent, k.pane)
	}

	t.Run("the pane reads as before: proceeds", func(t *testing.T) {
		t.Parallel()
		for _, stop := range []bool{false, true} {
			x := setupFor(t, claude)
			out, ok, _, _ := run(x, claude, launch.Request{Agent: claude.agent, Pane: claude.pane}, stop, func(*herdrtest.Pane) {}, nil)
			if !ok {
				t.Fatalf("stop=%v: %s", stop, out)
			}
			eq(t, last(out), result(claude, stop), "result")
		}
	})
	t.Run("ErrNotFound (no agent in the pane): proceeds", func(t *testing.T) {
		t.Parallel()
		for _, stop := range []bool{false, true} {
			x := setupFor(t, claude)
			out, ok, _, atExit := run(x, claude, launch.Request{Agent: claude.agent, Pane: claude.pane}, stop,
				func(a *herdrtest.Pane) { a.Kind, a.Name, a.Status = "", "", "" }, nil)
			if !ok || len(atExit) != 0 {
				t.Fatalf("stop=%v: no agent to exit: %v\n%s", stop, atExit, out)
			}
			if stop {
				eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3, already at a shell)", "result")
			} else {
				eq(t, last(out), result(claude, false), "result")
			}
		}
	})
	t.Run("any other error: FAIL with evidence, renamed during the wait, restart/stop x force x claude/codex", func(t *testing.T) {
		t.Parallel()
		for _, k := range []kind{claude, codex} {
			for _, stop := range []bool{false, true} {
				for _, force := range []bool{false, true} {
					name := fmt.Sprintf("%s stop=%v force=%v", k.kind, stop, force)
					x := setupFor(t, k)
					out, ok, _, atExit := run(x, k, launch.Request{Agent: k.agent, Pane: k.pane, Force: force}, stop,
						func(a *herdrtest.Pane) { a.Name = "other-agent" }, func(int) error { return down })
					contains(t, failLine(out), fmt.Sprintf("FAIL cannot confirm pane %s runs %s: ", k.pane, k.agent), name+" result")
					contains(t, failLine(out), "herdr went away", name+" names the error")
					contains(t, failLine(out), ": not stopped", name+" result")
					contains(t, out, "agent status:", name+" evidence")
					contains(t, out, "pane processes:", name+" evidence")
					if ok {
						t.Errorf("%s: not run\n%s", name, out)
					}
					untouched(t, x, atExit)
				}
			}
		}
	})
	t.Run("any other error, a resume and --fresh: same FAIL", func(t *testing.T) {
		t.Parallel()
		for _, req := range []launch.Request{
			{Agent: claude.agent, Pane: claude.pane},
			{Agent: claude.agent, Pane: claude.pane, Fresh: true},
		} {
			x := setupFor(t, claude)
			out, ok, _, atExit := run(x, claude, req, false, func(*herdrtest.Pane) {}, func(int) error { return down })
			contains(t, failLine(out), "FAIL cannot confirm pane w1:p3 runs pj-l-01: ", "result")
			contains(t, failLine(out), ": not stopped", "result")
			if ok {
				t.Errorf("%+v: not run\n%s", req, out)
			}
			untouched(t, x, atExit)
		}
	})
}

// callerFg gives pane p's foreground to the caller (`obor restart`, pid 77) for its next n process-info
// looks, then back to the shell; left counts the looks still to come.
type callerFg struct {
	port.PaneControl
	f    *herdrtest.Fake
	p    *herdrtest.Pane
	left *int
}

func (w callerFg) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	if *w.left > 0 {
		*w.left--
		w.f.Set(w.p.ID, func(p *herdrtest.Pane) {
			p.Foreground, p.Procs = 77, []herdr.Process{{PID: 77, Argv0: "obor"}}
		})
	} else {
		w.f.Set(w.p.ID, func(p *herdrtest.Pane) { p.Foreground, p.Procs = 0, nil })
	}
	return w.PaneControl.ProcessInfo(ctx, pane)
}

// waitScheduled does all the waiting of the scheduled run, one row of the spec's table each; the
// normal path then runs with no further wait.
func TestWaitScheduledTable(t *testing.T) {
	t.Parallel()
	const shellWait = "waiting for the shell of pane w1:p3 to have the foreground"
	untouched := func(t *testing.T, x *fx) {
		t.Helper()
		if len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing typed, stopped or started: %q", x.srv.Calls())
		}
	}
	t.Run("an agent, working: whenIdle, then the normal path", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		got := 0
		x.wrap = func(h port.PaneControl) port.PaneControl { return idleAfter{h, x.f, a, 2, &got} }
		out, ok, _ := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, false, func(l *launch.Launcher) { l.Sched = time.Minute })
		if !ok || got <= 2 {
			t.Fatalf("waited until idle (%d looks), then restarted:\n%s", got, out)
		}
		contains(t, out, "waiting for pj-l-01 to go idle", "idle wait")
		lacks(t, out, shellWait, "no shell wait for an agent")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
	})
	t.Run("an agent, idle: nothing more, the normal path", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "idle")
		out, ok, _ := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, false, nil)
		if !ok {
			t.Fatalf("restarted:\n%s", out)
		}
		lacks(t, out, shellWait, "no shell wait for an agent")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session sid-1)", "result")
	})
	t.Run("no agent, the caller in the foreground: wait for the shell, then the normal path", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := x.shell("w1:p3", "pj-l-01", x.lane)
		left := 4
		x.wrap = func(h port.PaneControl) port.PaneControl { return callerFg{h, x.f, p, &left} }
		out, ok, _ := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01", Fresh: true}, false, nil)
		if left != 0 {
			t.Fatalf("precondition: the caller held the foreground for every planned look (%d left)\n%s", left, out)
		}
		if !ok {
			t.Fatalf("started once the shell had the foreground:\n%s", out)
		}
		contains(t, out, shellWait, "shell wait")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
	})
	t.Run("no agent, the shell already in the foreground: no wait, the normal path", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", x.lane)
		out, ok, _ := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01", Fresh: true}, false, nil)
		if !ok {
			t.Fatalf("started:\n%s", out)
		}
		lacks(t, out, shellWait, "no shell wait")
		eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "result")
	})
	t.Run("no agent, the foreground never returns: give up naming the operation, no input", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			name string
			req  launch.Request
			stop bool
			word string
		}{
			{"restart", launch.Request{Agent: "pj-l-01", Fresh: true}, false, "restart"},
			{"stop", launch.Request{Agent: "pj-l-01"}, true, "stop"},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				x := setup(t)
				x.wait = shortWait
				p := x.shell("w1:p3", "pj-l-01", x.lane)
				left := 1 << 30
				x.wrap = func(h port.PaneControl) port.PaneControl { return callerFg{h, x.f, p, &left} }
				out, ok, _ := x.self("w1:p3", true, c.req, c.stop, nil)
				if ok {
					t.Errorf("not run:\n%s", out)
				}
				contains(t, out, shellWait, "shell wait")
				eq(t, failLine(out), fmt.Sprintf("FAIL obor still has the foreground of pane w1:p3 after 60ms: scheduled %s of pj-l-01 not run", c.word), "result")
				contains(t, out, "pane processes:", "evidence")
				untouched(t, x)
			})
		}
	})
	t.Run("scheduled stop, no agent: unchanged (already at a shell)", func(t *testing.T) {
		t.Parallel()
		for _, held := range []int{0, 3} {
			x := setup(t)
			p := x.shell("w1:p3", "pj-l-01", x.lane)
			left := held
			x.wrap = func(h port.PaneControl) port.PaneControl { return callerFg{h, x.f, p, &left} }
			out, ok, spawns := x.self("w1:p3", true, launch.Request{Agent: "pj-l-01"}, true, nil)
			if !ok || len(spawns) != 0 || left != 0 {
				t.Fatalf("held %d: stopped, not scheduled again (%v, %d looks left):\n%s", held, spawns, left, out)
			}
			eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3, already at a shell)", "result")
			untouched(t, x)
		}
	})
}
