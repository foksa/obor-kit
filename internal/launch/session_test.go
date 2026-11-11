package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/store"
	"obor/internal/testkit"
)

// a kind that resumes nothing (OpenCode) starts fresh whatever session is recorded — serve's store's or a
// older record — and its first prompt survives; no resume reaches the start.
func TestOpencodeStartsFresh(t *testing.T) {
	t.Parallel()
	x := setupRunning(t, "deepseek")
	x.sessions = map[string]store.Session{"pj-l-01": {Agent: "pj-l-01", ID: "sid-o", Started: time.Now().Unix()}}
	x.shell("w1:p3", "pj-l-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Prompt: "go"})
	if !ok {
		t.Fatal(out)
	}
	eq(t, last(out), "RESTARTED pj-l-01 (pane w1:p3, session new)", "fresh, not resumed")
	contains(t, out, "warning: no resume for kind opencode: new session", "said")
	if strings.Contains(out, "resuming sid-o") {
		t.Errorf("a resume said:\n%s", out)
	}
	start := x.startCall()
	contains(t, start, "--prompt go", "the first prompt survives")
	if strings.Contains(start, "resume") || strings.Contains(start, "sid-o") || strings.Contains(start, "--session") {
		t.Errorf("a resume reached the start: %s", start)
	}
	if len(x.reported) != 0 { // its plugin reports the session on its first event
		t.Errorf("the start reports a session itself: %+v", x.reported)
	}
}

// setupRunning is setup with lane L-01's open dispatch running model (its lane row's task and type): a start
// of pj-l-01 runs that model's kind.
func setupRunning(t *testing.T, model string) *fx {
	t.Helper()
	x := setup(t)
	x.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01","type":"`+model+`"}`)
	return x
}

// recordSessions writes session facts to the project's store as serve does (app's session request).
func recordSessions(t *testing.T, root string, ss ...store.Session) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		data, _ := json.Marshal(s)
		if _, err := log.Append(event.Record{Task: store.SessionTask, Step: store.StepSession, Kind: event.KindFact,
			Class: event.ClassFact, Data: data}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// a fresh Codex start in a lane reports its new session: the rollout its own process holds open (its
// first line's session_meta: the id; the file: the transcript), not a newer one of the same cwd that it does not hold.
func TestCodexStartReportsRollout(t *testing.T) {
	t.Parallel()
	x := setupRunning(t, "luna")
	var fresh, other string
	x.wrap = func(h port.PaneControl) port.PaneControl {
		return onStart{h, func() {
			fresh = writeRollout(x, "cx-new", x.lane, time.Now())
			x.hold(2, fresh)
			other = writeRollout(x, "cx-other", x.lane, time.Now().Add(time.Second)) // a Codex started by hand there
		}}
	}
	x.shell("w1:p3", "pj-l-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	if !ok {
		t.Fatal(out)
	}
	if _, err := os.Stat(other); err != nil || fresh == "" {
		t.Fatalf("precondition: both rollouts in the lane's cwd (%v, %q)", err, fresh)
	}
	if len(x.reported) != 1 {
		t.Fatalf("one session reported, got %+v\n%s", x.reported, out)
	}
	s := x.reported[0]
	eq(t, s.ID, "cx-new", "the rollout the agent's process holds open")
	eq(t, s.Transcript, fresh, "its transcript")
	eq(t, s.Agent+" "+s.Kind+" "+s.Lane+" "+s.Pane, "pj-l-01 codex L-01 w1:p3", "agent, kind, lane, pane")
	if s.Started == 0 {
		t.Error("started recorded")
	}
	contains(t, out, "session cx-new ("+fresh+", open in pid 2)", "said")

	t.Run("a resume reports the session it resumed", func(t *testing.T) {
		x.reset()
		x.reported = nil
		x.shell("w1:p3", "pj-l-01", "").Session = "cx-new"
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, x.startCall(), "resume cx-new", "resumed")
		if len(x.reported) != 1 {
			t.Fatalf("one report of the resumed session (serve ends the agent's skew line at it), got %+v\n%s",
				x.reported, out)
		}
		s := x.reported[0]
		eq(t, s.Agent+" "+s.Kind+" "+s.ID+" "+s.Lane+" "+s.Pane, "pj-l-01 codex cx-new L-01 w1:p3",
			"the session it resumed")
	})
}

// A resumed start reports the session it resumed from obor serve's launcher too: serve's fresh starts search a rollout in
// the background, and a resume's report waits for no search.
func TestServeResumeReportsItsSession(t *testing.T) {
	t.Parallel()
	x := setupRunning(t, "luna")
	x.background = true
	x.shell("w1:p3", "pj-l-01", "").Session = "cx-1"
	out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
	if !ok {
		t.Fatal(out)
	}
	contains(t, x.startCall(), "resume cx-1", "the recorded session")
	if len(x.reported) != 1 {
		t.Fatalf("one report of the resumed session (serve ends the agent's skew line at it), got %+v\n%s",
			x.reported, out)
	}
	s := x.reported[0]
	eq(t, s.Agent+" "+s.Kind+" "+s.ID+" "+s.Lane+" "+s.Pane, "pj-l-01 codex cx-1 L-01 w1:p3",
		"the session it resumed")
}

// writeRollout writes a Codex rollout of session id begun in cwd at at under x's home: its path.
func writeRollout(x *fx, id, cwd string, at time.Time) string {
	path := filepath.Join(x.p.Base, "home", ".codex", "sessions", "2026", "10", "30",
		"rollout-"+at.UTC().Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	x.p.Write(path, `{"timestamp":"`+at.UTC().Format(time.RFC3339Nano)+`","type":"session_meta","payload":{"id":"`+id+
		`","timestamp":"`+at.UTC().Format(time.RFC3339Nano)+`","cwd":"`+cwd+`","originator":"codex_cli_rs"}}`+"\n"+
		`{"timestamp":"`+at.UTC().Format(time.RFC3339Nano)+`","type":"event_msg","payload":{"type":"task_started"}}`+"\n")
	return path
}

// a pid the system reuses while the search reads the files a process holds open is not recorded under: the pin
// is read again after the scan (checking the pin's lstart only before the files are read would miss a reuse
// in between that records the new process's rollout as the agent's).
func TestCodexSearchPidReusedWhileTheFilesAreRead(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("agents.roles", `{"integrator":"luna"}`))
	x.background, x.rollout, x.reports = true, time.Minute, make(chan store.Session, 2)
	var gen atomic.Int32
	x.ps = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "-o lstart= -p 205" {
			return []byte("start-" + strconv.Itoa(int(gen.Load())) + "\n"), nil
		}
		return nil, nil
	}
	x.wrap = func(h port.PaneControl) port.PaneControl { return pids{h, x.f, map[string]int{"w1:p5": 205}} }
	x.shell("w1:p5", "pj-int", "")
	stranger := writeRollout(x, "cx-stranger", x.p.Root, time.Now())
	var once sync.Once
	x.files = func([]int) []string {
		once.Do(func() { gen.Store(1) }) // the pid is taken by another process as the files are read
		return []string{stranger}
	}
	if out, ok := x.restart(launch.Request{Agent: "pj-int", Fresh: true}); !ok {
		t.Fatal(out)
	}
	deadline := time.Now().Add(bound)
	for !strings.Contains(x.log(), "is gone") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	contains(t, x.log(), "warning: pj-int (pid 205) is gone: its codex session is not recorded", "said")
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.reported) != 0 {
		t.Errorf("recorded the rollout of the process that took the pid: %+v", x.reported)
	}
}

// onStart runs fn once herdr's agent.start answered (the agent writes its rollout as it starts).
type onStart struct {
	port.PaneControl
	fn func()
}

func (s onStart) Start(ctx context.Context, name, kind, pane string, say func(format string, args ...any), args ...string) (port.Pane, error) {
	a, err := s.PaneControl.Start(ctx, name, kind, pane, say, args...)
	s.fn()
	return a, err
}

// pids gives the agent in each pane of pid its own pid (herdr's process info: the foreground group's leader).
type pids struct {
	port.PaneControl
	f   *herdrtest.Fake
	pid map[string]int
}

func (p pids) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	p.f.Set(pane, func(q *herdrtest.Pane) {
		if q.Kind != "" && p.pid[pane] != 0 {
			q.Foreground = p.pid[pane]
		}
	})
	return p.PaneControl.ProcessInfo(ctx, pane)
}

// awaitReports is the next n background reports, by agent (the test fails when they do not come within bound).
func awaitReports(t *testing.T, x *fx, n int) map[string]store.Session {
	t.Helper()
	got := map[string]store.Session{}
	for range n {
		select {
		case s := <-x.reports:
			if _, dup := got[s.Agent]; dup {
				t.Fatalf("two reports for %s: %+v", s.Agent, s)
			}
			got[s.Agent] = s
		case <-time.After(bound):
			t.Fatalf("%d of %d reports came: %+v\nlog:\n%s", len(got), n, got, x.log())
		}
	}
	return got
}

// two Codex roles in the main checkout started one after the other, their searches pending at once:
// each records the rollout its own process holds, whichever is written first, the first agent's after the second's
// launch too. A replay of the session facts gives the same current sessions.
func TestCodexSessionsOfTwoStartsInOneCwd(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"first start's rollout first", "second start's rollout first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.roles", `{"integrator":"luna","lead":"luna"}`))
			x.background, x.rollout, x.reports = true, time.Minute, make(chan store.Session, 4)
			pid := map[string]int{"pj-int": 205, "pj-lead": 206}
			x.wrap = func(h port.PaneControl) port.PaneControl {
				return pids{h, x.f, map[string]int{"w1:p5": pid["pj-int"], "w1:p6": pid["pj-lead"]}}
			}
			x.shell("w1:p5", "pj-int", "")
			x.shell("w1:p6", "pj-lead", "")
			for _, agent := range []string{"pj-int", "pj-lead"} {
				if out, ok := x.restart(launch.Request{Agent: agent, Fresh: true}); !ok {
					t.Fatal(out)
				}
			}
			second := time.Now() // after the second launch
			x.mu.Lock()
			n := len(x.reported)
			x.mu.Unlock()
			if n != 0 {
				t.Fatalf("precondition: no rollout yet, nothing reported (%d)", n)
			}
			paths := map[string]string{}
			write := func(agent string, id string, at time.Time) {
				paths[agent] = writeRollout(x, id, x.p.Root, at)
				x.hold(pid[agent], paths[agent])
			}
			if order == "first start's rollout first" {
				write("pj-int", "cx-int", second)
				write("pj-lead", "cx-lead", second.Add(time.Second))
			} else {
				write("pj-lead", "cx-lead", second)
				write("pj-int", "cx-int", second.Add(time.Second))
			}
			got := awaitReports(t, x, 2)
			eq(t, got["pj-int"].ID+" "+got["pj-int"].Transcript, "cx-int "+paths["pj-int"], "the first start's own session")
			eq(t, got["pj-lead"].ID+" "+got["pj-lead"].Transcript, "cx-lead "+paths["pj-lead"], "the second start's own session")

			// Replay: the reported facts recorded, then recorded again (serve's log replayed): the same current ones.
			recordSessions(t, x.p.Root, got["pj-int"], got["pj-lead"])
			for range 2 {
				for agent, want := range map[string]string{"pj-int": "cx-int", "pj-lead": "cx-lead"} {
					if s, ok := store.CurrentAt(x.p.Root, agent); !ok || s.ID != want {
						t.Errorf("%s's current session %q (%v), want %s", agent, s.ID, ok, want)
					}
				}
				recordSessions(t, x.p.Root, got["pj-lead"], got["pj-int"])
			}
		})
	}
}

// the rollout is opened by a child of the agent's process (Codex's launcher runs the binary that writes it):
// the child's file counts; a process outside the agent's tree holding another rollout does not.
func TestCodexRolloutOfAChild(t *testing.T) {
	t.Parallel()
	x := setupRunning(t, "luna")
	x.ps = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "-A -o pid=,ppid=" {
			return nil, nil
		}
		return []byte("    2     1\n  302     2\n  303   302\n  402     1\n"), nil
	}
	var path string
	x.wrap = func(h port.PaneControl) port.PaneControl {
		return onStart{h, func() {
			path = writeRollout(x, "cx-child", x.lane, time.Now())
			x.hold(303, path)
			x.hold(402, writeRollout(x, "cx-stranger", x.lane, time.Now()))
		}}
	}
	x.shell("w1:p3", "pj-l-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	if !ok {
		t.Fatal(out)
	}
	if len(x.reported) != 1 {
		t.Fatalf("one session reported, got %+v\n%s", x.reported, out)
	}
	eq(t, x.reported[0].ID+" "+x.reported[0].Transcript, "cx-child "+path, "the grandchild's rollout")
	contains(t, out, "open in pid 2", "the agent's pid said")
}

// the agent's process holds no rollout within the bound (a rollout of its cwd exists, written by another
// Codex): no session fact, a line saying so, nothing recorded: never a guess from the cwd. The line says that
// this is expected of a fresh start with no prompt (Codex writes its rollout at the first one); a prompted start gets
// the plain one.
func TestCodexNoRolloutOpen(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, prompt, line string
	}{
		{"no prompt: expected", "", "warning: pj-l-01 (pid 2) holds no codex transcript open within 20ms (its session is not recorded: expected after a fresh start with no prompt)"},
		{"prompted: the plain line", "work", "warning: pj-l-01 (pid 2) holds no codex transcript open within 20ms (its session is not recorded)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setupRunning(t, "luna")
			x.wrap = func(h port.PaneControl) port.PaneControl {
				return onStart{h, func() { writeRollout(x, "cx-other", x.lane, time.Now()) }}
			}
			x.shell("w1:p3", "pj-l-01", "")
			out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: c.prompt})
			if !ok {
				t.Fatal(out)
			}
			contains(t, x.startCall(), "codex", "precondition: a codex start")
			contains(t, out, c.line, "said")
			if len(x.reported) != 0 {
				t.Errorf("a session fact: %+v", x.reported)
			}
			eq(t, x.recorded("pj-l-01"), "", "nothing recorded")
		})
	}
}

// serve refusing the session (another agent holds it), or not reachable, records nothing and says so; a
// restart resumes the last recorded session, else herdr's.
func TestCodexUnreportedSessionRecordsNothing(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, reportErr error) (*fx, string) {
		x := setupRunning(t, "luna")
		x.reportErr = reportErr
		x.wrap = func(h port.PaneControl) port.PaneControl {
			return onStart{h, func() { x.hold(2, writeRollout(x, "cx-new", x.lane, time.Now())) }}
		}
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatal(out)
		}
		if len(x.reported) != 1 || x.reported[0].ID != "cx-new" {
			t.Fatalf("precondition: cx-new reported once: %+v\n%s", x.reported, out)
		}
		return x, out
	}
	t.Run("refused: said", func(t *testing.T) {
		t.Parallel()
		x, out := run(t, errors.New("session: session already held by pj-lead (pj-l-01's report refused)"))
		contains(t, out, "session cx-new of pj-l-01 not recorded", "said")
		contains(t, out, "session already held by pj-lead", "why")
		eq(t, x.recorded("pj-l-01"), "", "nothing recorded")
	})
	t.Run("serve unreachable: said", func(t *testing.T) {
		t.Parallel()
		x, out := run(t, errors.New("obor serve is not running"))
		contains(t, out, "session cx-new of pj-l-01 not recorded", "said")
		contains(t, out, "a restart resumes pj-l-01's last recorded session", "what follows")
		contains(t, out, "obor restart pj-l-01 --fresh starts a new one", "how to start fresh")
		eq(t, x.recorded("pj-l-01"), "", "nothing recorded")
	})
}

// the start pins the agent's process; the search never looks at the pane again. The pane is held by another
// agent (pid 999, with its own rollout) after the restart returned but before the start's search runs (captured
// and run then, not in a goroutine that may run first): the first agent records its own rollout when its process lives,
// nothing when it is gone (its pid under another process), never the replacement's.
func TestCodexSearchPinnedToStartedProcess(t *testing.T) {
	t.Parallel()
	for _, gone := range []bool{false, true} {
		name := "the process lives: its own rollout"
		if gone {
			name = "the process is gone, its pid reused: nothing"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.roles", `{"integrator":"luna"}`))
			x.capture, x.rollout, x.reports = true, time.Minute, make(chan store.Session, 2)
			var gen atomic.Int32
			x.ps = func(args ...string) ([]byte, error) {
				if strings.Join(args, " ") == "-o lstart= -p 205" {
					return []byte("start-" + strconv.Itoa(int(gen.Load())) + "\n"), nil
				}
				return nil, nil
			}
			pid := map[string]int{"w1:p5": 205}
			x.wrap = func(h port.PaneControl) port.PaneControl { return pids{h, x.f, pid} }
			x.shell("w1:p5", "pj-int", "")
			if out, ok := x.restart(launch.Request{Agent: "pj-int", Fresh: true}); !ok {
				t.Fatal(out)
			}
			// Another agent now holds the pane, and pid 205 is either still the first agent's or a stranger's.
			pid["w1:p5"] = 999
			x.hold(999, writeRollout(x, "cx-replacement", x.p.Root, time.Now()))
			var own string
			if gone {
				gen.Store(1)
				x.hold(205, writeRollout(x, "cx-stranger", x.p.Root, time.Now()))
			} else {
				own = writeRollout(x, "cx-own", x.p.Root, time.Now())
				x.hold(205, own)
			}
			if n := x.runSearches(); n != 1 {
				t.Fatalf("precondition: one search, run after the pane changed: %d", n)
			}
			if gone {
				contains(t, x.log(), "warning: pj-int (pid 205) is gone: its codex session is not recorded", "said")
				x.mu.Lock()
				defer x.mu.Unlock()
				if len(x.reported) != 0 {
					t.Errorf("recorded for a gone process: %+v", x.reported)
				}
				return
			}
			got := awaitReports(t, x, 1)
			eq(t, got["pj-int"].ID+" "+got["pj-int"].Transcript, "cx-own "+own, "its own rollout, not the replacement's")
		})
	}
}
