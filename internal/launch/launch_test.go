package launch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

const settings = `--settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`

// fx is a project with lane O-01 (type opus: claude --model opus), lane type luna (codex -m gpt-6-luna), roles
// integrator and lead (opus) and reviewer (luna), a fake herdr with no panes, every pid alive and a ps that lists no
// process.
type fx struct {
	t      *testing.T
	p      *testkit.Project
	f      *herdrtest.Fake
	srv    *herdrtest.Server // the fake herdr's socket: what the launch called
	lane   string
	claude string // CLAUDE_CONFIG_DIR
	wrap   func(port.PaneControl) port.PaneControl
	gone   map[string]bool                      // pids that are not alive (every other pid is)
	onExit func()                               // what /exit does besides the fake herdr's OnQuit (nil: nothing)
	kills  []string                             // kill hook calls: "<pgid> <signal>"
	onKill func(pgid int, sig syscall.Signal)   // what a kill does (nil: nothing)
	ps     func(args ...string) ([]byte, error) // the launcher's PS (nil: the real ps)
	wait   time.Duration                        // the launcher's exit wait and idle wait (0: bound, the load-proof default)
	up     time.Duration                        // the launcher's wait for a started agent to come up (0: wait)
	poll   time.Duration                        // the launcher's pane poll interval (0: 1 ms)
	// sessions: the recorded sessions the launcher reads (nil: serve's store); reported: the sessions it reported.
	sessions map[string]store.Session
	reported []store.Session
	// rollout: the launcher's rollout bound (0: 20 ms); background: its rollout search runs in a goroutine, each
	// report also sent on reports (nil: not sent).
	rollout    time.Duration
	background bool
	reports    chan store.Session
	reportErr  error           // what the launcher's Report returns (nil: recorded)
	logged     strings.Builder // what a background search said (the launcher's Log)
	// open: the files each pid holds open (the launcher's Open; hold sets it). The fake herdr's agent is pid 2 unless
	// a test gives its pane another.
	open map[int][]string
	// files replaces the launcher's Open when set: a test's own view of the files (it may act while the search reads
	// them, T-646).
	files func(pids []int) []string
	mu    sync.Mutex // guards reported, logged, open
}

// hold makes pid hold path open (a Codex writing its rollout).
func (x *fx) hold(pid int, path string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.open == nil {
		x.open = map[int][]string{}
	}
	x.open[pid] = append(x.open[pid], path)
}

// Write is the launcher's Log: a background search's lines into x.logged.
func (x *fx) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.logged.Write(p)
}

// log is what background searches said so far.
func (x *fx) log() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.logged.String()
}

// bound is the launcher's wait where the test is about the outcome, not the timing: far above anything a loaded
// machine delays a poll by, since a wait that is met returns at once. shortWait is for a test of the give-up itself
// (the agent never exits, the foreground never returns): it sets x.wait, so that waiting the bound out is quick, and the
// output the test asserts names it.
const (
	bound     = 10 * time.Second
	shortWait = 60 * time.Millisecond
)

func setup(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	p := testkit.New(t)
	f := p.Herdr()
	x := &fx{t: t, p: p, f: f, srv: herdrtest.Serve(t, f), lane: p.Lane("O-01", ""), claude: filepath.Join(p.Base, "claude"),
		gone: map[string]bool{}, ps: func(...string) ([]byte, error) { return nil, nil }}
	p.Config(append([]func(map[string]any){
		testkit.Set("lane_types", `{"opus":{"kind":"claude","args":["--model","opus"]},"luna":{"kind":"codex","args":["-m","gpt-6-luna"]}}`),
		testkit.Set("roles", `{"integrator":{"type":"opus"},"lead":{"type":"opus"},"reviewer":{"type":"luna"}}`),
	}, edits...)...)
	return x
}

func (x *fx) launcher(out io.Writer) *launch.Launcher {
	x.t.Helper()
	cfg, err := config.Load(x.p.Root)
	if err != nil {
		x.t.Fatal(err)
	}
	var h port.PaneControl = x.srv.Client()
	if x.onExit != nil {
		h = exitHook{h, x.onExit}
	}
	if x.wrap != nil {
		h = x.wrap(h)
	}
	wait := x.wait
	if wait == 0 {
		wait = bound
	}
	up := x.up
	if up == 0 {
		up = wait
	}
	poll := x.poll
	if poll == 0 {
		poll = time.Millisecond
	}
	rollout := x.rollout
	if rollout == 0 {
		rollout = 20 * time.Millisecond
	}
	var background func(func())
	if x.background {
		background = func(search func()) { go search() }
	}
	return &launch.Launcher{Cfg: cfg, Env: x.env(), Kit: "/kit", H: h, Out: out,
		Wait: wait, Idle: wait, Up: up, Poll: poll, Rollout: rollout, Background: background, Log: x,
		Kill: func(pgid int, sig syscall.Signal) error {
			x.kills = append(x.kills, fmt.Sprintf("%d %s", pgid, sig))
			if x.onKill != nil {
				x.onKill(pgid, sig)
			}
			return nil
		},
		Alive: func(pid int) bool { return !x.gone[strconv.Itoa(pid)] }, PS: x.ps,
		Open: func(pids []int) []string {
			if x.files != nil {
				return x.files(pids)
			}
			x.mu.Lock()
			defer x.mu.Unlock()
			var files []string
			for _, pid := range pids {
				files = append(files, x.open[pid]...)
			}
			return files
		},
		Sessions: func(agent string) (store.Session, bool) {
			if x.sessions == nil {
				return store.CurrentAt(x.p.Root, agent)
			}
			s, ok := x.sessions[agent]
			return s, ok
		},
		Report: func(_ context.Context, s store.Session) error {
			x.mu.Lock()
			x.reported = append(x.reported, s)
			x.mu.Unlock()
			if x.reports != nil {
				x.reports <- s
			}
			return x.reportErr
		}}
}

func (x *fx) env() project.Env { return x.p.Env("CLAUDE_CONFIG_DIR=" + x.claude) }

// restart runs obor restart: its output with the result line (RESTARTED … or FAIL …) last, and whether it succeeded.
func (x *fx) restart(req launch.Request) (string, bool) {
	x.t.Helper()
	var out bytes.Buffer
	line, err := x.launcher(&out).Restart(context.Background(), req)
	return result(x.t, &out, line, err)
}

func (x *fx) stop(agent, pane string, force bool) (string, bool) {
	x.t.Helper()
	var out bytes.Buffer
	line, err := x.launcher(&out).Stop(context.Background(), agent, pane, force)
	return result(x.t, &out, line, err)
}

func result(t *testing.T, out *bytes.Buffer, line string, err error) (string, bool) {
	t.Helper()
	var fail project.Fail
	if err != nil && !errors.As(err, &fail) {
		t.Fatalf("not a Fail: %v", err)
	}
	if err != nil {
		ev := ""
		if e := launch.Evidence(err); e != "" {
			ev = e + "\n" // like obor restart: the evidence, then the one-line FAIL last
		}
		return out.String() + ev + "FAIL " + err.Error(), false
	}
	return out.String() + line, true
}

// failLine is the FAIL line of out.
func failLine(out string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "FAIL ") {
			return ln
		}
	}
	return ""
}

func last(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

// sid records agent's session id in serve's store (as its session request does), with its Claude transcript under the
// lane worktree and the main checkout (T-137).
func (x *fx) sid(agent, sid string) {
	x.t.Helper()
	recordSessions(x.t, x.p.Root, store.Session{Agent: agent, Kind: "claude", ID: sid, Started: time.Now().Unix()})
	x.transcript(x.lane, sid)
	x.transcript(x.p.Root, sid)
}

// record puts agent's session id in serve's store alone (no transcript): the one record a restart reads.
func (x *fx) record(agent, sid string) {
	x.t.Helper()
	recordSessions(x.t, x.p.Root, store.Session{Agent: agent, Kind: "claude", ID: sid, Started: time.Now().Unix()})
}

// recorded is agent's current session id in serve's store ("": none).
func (x *fx) recorded(agent string) string {
	s, _ := store.CurrentAt(x.p.Root, agent)
	return s.ID
}

// transcript is Claude's transcript of sid for a session started in cwd.
func (x *fx) transcript(cwd, sid string) {
	x.t.Helper()
	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(cwd, "-")
	x.p.Write(filepath.Join(x.claude, "projects", slug, sid+".jsonl"), "")
}

// shell is a pane at its shell (no agent), labelled label.
func (x *fx) shell(pane, label, cwd string) *herdrtest.Pane { return x.f.Shell(pane, label, cwd) }

// agent is a pane labelled and named name with an agent of kind in status, which leads the pane's foreground group 2.
func (x *fx) agent(pane, name, kind, status string) *herdrtest.Pane {
	a := x.f.Agent(pane, name, status, "")
	a.Label, a.Kind = name, kind
	a.Procs = []herdr.Process{{PID: 2, Argv0: kind}}
	return a
}

// calls is the requests whose call line has every part of query, e.g. "pane.send_input keys=[Enter] pane_id=w1:p3".
func (x *fx) calls(query string) []string {
	var out []string
	for _, c := range x.srv.Calls() {
		if hasParts(c, query) {
			out = append(out, c)
		}
	}
	return out
}

// hasParts: line has every space-separated part of query in it.
func hasParts(line, query string) bool {
	for _, part := range strings.Fields(query) {
		if !strings.Contains(line, part) {
			return false
		}
	}
	return true
}

func (x *fx) startCall() string { return strings.Join(x.calls("agent.start"), "\n") }

// index is the position of the first request matching query (-1: none).
func (x *fx) index(query string) int {
	for i, c := range x.srv.Calls() {
		if hasParts(c, query) {
			return i
		}
	}
	return -1
}

func (x *fx) reset() { x.f.Panes = nil }

func contains(t *testing.T, s, sub, what string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: %q does not contain %q", what, s, sub)
	}
}

func lacks(t *testing.T, s, sub, what string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("%s: %q contains %q", what, s, sub)
	}
}

func eq(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// envMarksRE is the env line's two words as typed (T-601): its success word, then its failure word.
var envMarksRE = regexp.MustCompile(` && echo obor-env-"([0-9a-z]+)" \|\| echo obor-env-"([0-9a-z]+)"$`)

// Who: the env line typed at the shell and the start line, per kind of agent.
func TestRestartWho(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string // bash restart.test.sh case
		edit  []func(map[string]any)
		prep  func(x *fx)
		req   launch.Request
		env   func(x *fx) string // the pane run line (env)
		start string
		line  string
		out   string // in the output too
	}{{
		// bash restart.test.sh case_lane_agent_dead_pane_starts_with_lane_env_type_args_and_resume
		name: "lane agent, dead pane: lane env, type args + settings, --resume <sid>",
		prep: func(x *fx) { x.sid("pj-o-01", "sid-1"); x.shell("w1:p3", "pj-o-01", "").Session = "sid-1" },
		req:  launch.Request{Agent: "pj-o-01"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p3 text=cd " + q(x.lane) + " && export OBOR_AGENT=pj-o-01 OBOR_ROLE=worker OBOR_LANE=O-01 && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + " --resume sid-1] kind=claude name=pj-o-01 pane_id=w1:p3",
		line:  "RESTARTED pj-o-01 (pane w1:p3, session sid-1)",
	}, {
		// bash restart.test.sh case_lane_agent_named_lane_lower_with_null_state_agent; T-302: the bare lane name is
		// the lane's only when its state file names it
		name: "lane agent named <lane lower> by the lane state file: the lane",
		prep: func(x *fx) {
			x.p.State("O-01", `{"agent":"o-01"}`)
			x.sid("o-01", "sid-1")
			x.shell("w1:p3", "o-01", "")
		},
		req: launch.Request{Agent: "o-01"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p3 text=cd " + q(x.lane) + " && export OBOR_AGENT=o-01 OBOR_ROLE=worker OBOR_LANE=O-01 && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + " --resume sid-1] kind=claude name=o-01 pane_id=w1:p3",
		line:  "RESTARTED o-01 (pane w1:p3, session sid-1)",
	}, {
		// T-302: another project's agent may be named like a lane; no lane env, no lane type args
		name: "agent named <lane lower>, lane state agent null: not the lane",
		prep: func(x *fx) {
			x.p.State("O-01", `{"agent":null}`)
			x.sid("o-01", "sid-1")
			x.shell("w1:p3", "o-01", "")
		},
		req: launch.Request{Agent: "o-01"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p3 text=export OBOR_AGENT=o-01 && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--resume sid-1] kind=claude name=o-01 pane_id=w1:p3",
		line:  "RESTARTED o-01 (pane w1:p3, session sid-1)",
	}, {
		// bash restart.test.sh case_lane_agent_matched_by_pane_cwd
		name: "an agent whose pane cwd is a lane worktree: that lane",
		prep: func(x *fx) { x.sid("odd-name", "sid-1"); x.shell("w1:p3", "odd-name", x.lane) },
		req:  launch.Request{Agent: "odd-name"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p3 text=cd " + q(x.lane) + " && export OBOR_AGENT=odd-name OBOR_ROLE=worker OBOR_LANE=O-01 && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + " --resume sid-1] kind=claude name=odd-name pane_id=w1:p3",
		line:  "RESTARTED odd-name (pane w1:p3, session sid-1)",
		out:   "lane O-01 (pane w1:p3 cwd)",
	}, {
		// bash restart.test.sh case_role_agent_gets_role_env_and_type
		name: "role agent: role env, its type, cwd the main checkout, OBOR_LANE unset",
		prep: func(x *fx) { x.sid("pj-int", "sid-i"); x.shell("w1:p5", "pj-int", "").Session = "sid-i" },
		req:  launch.Request{Agent: "pj-int"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p5 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=pj-int OBOR_ROLE=integrator && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + " --resume sid-i] kind=claude name=pj-int pane_id=w1:p5",
		line:  "RESTARTED pj-int (pane w1:p5, session sid-i)",
	}, {
		// bash restart.test.sh case_lead_agent_is_a_role_agent, case_lead_unsets_obor_lane
		name: "a lead is a role agent; OBOR_LANE unset",
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-lead", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=pj-lead OBOR_ROLE=lead && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus] kind=claude name=pj-lead pane_id=w1:p6",
		line:  "RESTARTED pj-lead (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_unknown_agent_with_sid_gets_obor_agent_only
		name: "unknown agent with a sid: OBOR_AGENT only, no cd, no type args",
		prep: func(x *fx) {
			x.record("triage", "sid-t")
			x.shell("w1:p8", "triage", "").Session = "sid-t"
		},
		req: launch.Request{Agent: "triage"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p8 text=export OBOR_AGENT=triage && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--resume sid-t] kind=claude name=triage pane_id=w1:p8",
		line:  "RESTARTED triage (pane w1:p8, session sid-t)",
		out:   "warning: pane w1:p8 has no known cwd: recorded session sid-t not checked",
	}, {
		// bash restart.test.sh case_triage_gets_obor_agent_only
		name: "triage role: its type, cwd root, OBOR_AGENT only",
		edit: []func(map[string]any){testkit.Set("roles", `{"lead":{"type":"opus"},"triage":{"type":"opus"}}`)},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-triage", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=pj-triage && unset OBOR_LANE OBOR_ROLE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + "] kind=claude name=pj-triage pane_id=w1:p6",
		line:  "RESTARTED pj-triage (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_deep_dive_gets_role_env_and_its_worktree; T-718: the main checkout, no worktree
		name: "deep_dive role (T-179, T-718): its type, cwd the root, OBOR_ROLE=deep_dive",
		edit: []func(map[string]any){
			testkit.Set("roles", `{"lead":{"type":"opus"},"deep_dive":{"type":"opus"}}`),
			testkit.Set("lanes", `{"O-01":"opus"}`),
		},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-deep-dive", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) +
				" && export OBOR_AGENT=pj-deep-dive OBOR_ROLE=deep_dive && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + "] kind=claude name=pj-deep-dive pane_id=w1:p6",
		line:  "RESTARTED pj-deep-dive (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_agents_entry_gets_type_args_cwd_env (adv)
		name: "agents entry: its type args + settings, its cwd, OBOR_AGENT only; args line",
		edit: []func(map[string]any){
			testkit.Set("lane_types", `{"opus":{"kind":"claude","args":["--model","opus"]},"fable":{"kind":"claude","args":["--model","fable","--permission-mode","plan"]}}`),
			testkit.Set("agents", `{"adv":{"type":"fable","cwd":"sub"},"des":{"type":"opus","role":"designer"},"pj-int":{"type":"fable"}}`),
		},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "adv", Pane: "w1:p6", Sid: "s-adv"},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root+"/sub") + " && export OBOR_AGENT=adv && unset OBOR_LANE OBOR_ROLE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model fable --permission-mode plan " + settings + " --resume s-adv] kind=claude name=adv pane_id=w1:p6",
		line:  "RESTARTED adv (pane w1:p6, session s-adv)",
		out:   "args (claude): --model fable --permission-mode plan " + settings + " --resume s-adv\n",
	}, {
		// bash restart.test.sh case_agents_entry_gets_type_args_cwd_env (des)
		name: "agents entry with a role: cwd the main checkout, OBOR_ROLE from the entry",
		edit: []func(map[string]any){testkit.Set("agents", `{"des":{"type":"opus","role":"designer"}}`)},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "des", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=des OBOR_ROLE=designer && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + "] kind=claude name=des pane_id=w1:p6",
		line:  "RESTARTED des (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_agents_entry_gets_type_args_cwd_env (pj-int)
		name: "a role wins over an agents entry of the same name",
		edit: []func(map[string]any){
			testkit.Set("lane_types", `{"opus":{"kind":"claude","args":["--model","opus"]},"fable":{"kind":"claude","args":["--model","fable"]}}`),
			testkit.Set("agents", `{"pj-int":{"type":"fable"}}`),
		},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-int", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=pj-int OBOR_ROLE=integrator && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus " + settings + "] kind=claude name=pj-int pane_id=w1:p6",
		line:  "RESTARTED pj-int (pane w1:p6, session new)",
	}, {
		// T-280: a lead keeps Remote Control unless its type sets settings
		name: "a lead whose type sets settings: those settings",
		edit: []func(map[string]any){
			testkit.Set("lane_types", `{"opus":{"kind":"claude","args":["--model","opus"],"settings":{"x":1}}}`),
		},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-lead", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=pj-lead OBOR_ROLE=lead && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start args=[--model opus --settings {\"x\":1}] kind=claude name=pj-lead pane_id=w1:p6",
		line:  "RESTARTED pj-lead (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_lead_type_from_leads_entry_or_none (pj-lead)
		name: "a lead without a type: no args",
		edit: []func(map[string]any){testkit.Set("roles", `{"lead":{"agent":"pj-lead"}}`)},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-lead", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "pane.send_input keys=[Enter] pane_id=w1:p6 text=cd " + q(x.p.Root) + " && export OBOR_AGENT=pj-lead OBOR_ROLE=lead && unset OBOR_LANE && unset OBOR_OPENCODE_MODEL"
		},
		start: "agent.start kind=claude name=pj-lead pane_id=w1:p6",
		line:  "RESTARTED pj-lead (pane w1:p6, session new)",
		out:   "args (claude): none",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, c.edit...)
			c.prep(x)
			if s := x.startCall(); s != "" {
				t.Fatalf("precondition: nothing started, got %q", s)
			}
			out, ok := x.restart(c.req)
			if !ok {
				t.Fatalf("restart failed:\n%s", out)
			}
			eq(t, last(out), c.line, "result line")
			runs := x.calls("pane.send_input")
			if len(runs) != 1 {
				t.Fatalf("one env line typed, got %q", runs)
			}
			m := envMarksRE.FindStringSubmatch(runs[0])
			if m == nil {
				t.Fatalf("the env line ends without its success and failure marks: %q", runs[0])
			}
			eq(t, runs[0][:len(runs[0])-len(m[0])], c.env(x), "env line (cwd, OBOR_AGENT, OBOR_ROLE, OBOR_LANE)")
			eq(t, x.startCall(), c.start, "agent start (kind, pane, type args, resume)")
			if c.out != "" {
				contains(t, out, c.out, "output")
			}
			if i, j := strings.Index(out, "args ("), strings.Index(out, "RESTARTED"); i < 0 || i > j {
				t.Errorf("args line before the result: %q", out)
			}
		})
	}
}

// T-303: Request.Args and ParseRequest are one pair; every field goes through both and comes back the same, so a
// field carried on one side only (a handoff flag once was, and the scheduled run lost it) fails here.
func TestRequestArgsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		req  launch.Request
		args string
	}{
		{"agent and pane", launch.Request{Agent: "pj-o-01", Pane: "w1:p3"}, "pj-o-01 --pane w1:p3"},
		{"sid", launch.Request{Agent: "pj-o-01", Pane: "w1:p3", Sid: "sid-9"}, "pj-o-01 --pane w1:p3 --sid sid-9"},
		{"fresh", launch.Request{Agent: "pj-o-01", Pane: "w1:p3", Fresh: true}, "pj-o-01 --pane w1:p3 --fresh"},
		{"force", launch.Request{Agent: "pj-o-01", Pane: "w1:p3", Force: true}, "pj-o-01 --pane w1:p3 --force"},
		{"every combinable field", launch.Request{Agent: "pj-o-01", Pane: "w1:p3", Fresh: true, Force: true, Prompt: "go"},
			"pj-o-01 --pane w1:p3 --fresh --force --prompt go"},
		{"no pane", launch.Request{Agent: "pj-lead"}, "pj-lead"},
		{"fresh with a first prompt", launch.Request{Agent: "pj-w", Fresh: true, Prompt: "[obor job T-1/1/work] call the obor job_next tool"},
			"pj-w --fresh --prompt [obor job T-1/1/work] call the obor job_next tool"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			args := launch.Args(c.req)
			eq(t, strings.Join(args, " "), c.args, "Args")
			got, err := launch.ParseRequest(args)
			if err != nil {
				t.Fatalf("ParseRequest(%q): %v", args, err)
			}
			if got != c.req {
				t.Errorf("round trip:\n got %+v\nwant %+v", got, c.req)
			}
		})
	}
}

// T-303: ParseRequest refuses what Args never writes: a stray flag (--keep-inbox went with the inbox, T-466), a second
// positional, a flag without its value.
func TestParseRequestRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range [][]string{
		{"pj-o-01", "--keep-inbox"},
		{"pj-o-01", "--bogus"},
		{"pj-o-01", "pj-int"},
		{"pj-o-01", "--pane"},
	} {
		if req, err := launch.ParseRequest(c); err == nil {
			t.Errorf("ParseRequest(%q) = %+v, want an error", c, req)
		}
	}
}

// bash restart.test.sh case_lane_agent_found_by_label_without_sid_fails_fresh_starts_new_session,
// case_fresh_starts_without_resume
func TestRestartNoSessionFailsFreshStartsNew(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p9", "", x.lane)
	x.shell("w1:p4", "pj-o-01", "")
	if x.recorded("pj-o-01") != "" {
		t.Fatal("precondition: no recorded sid")
	}
	out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
	contains(t, out, "pane w1:p4 (from label)", "the labelled pane, not the lane cwd one")
	eq(t, last(out), "FAIL no session to resume for pj-o-01 (pass --fresh to start a new one)", "no session")
	if ok || len(x.calls("pane.send_input")) > 0 || x.startCall() != "" {
		t.Errorf("nothing typed or started: %q", x.srv.Calls())
	}
	out, ok = x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p4, session new)", "--fresh")
	lacks(t, x.startCall(), "--resume", "--fresh: no --resume")
	contains(t, x.startCall(), "pane_id=w1:p4", "started in the labelled pane")
}

// bash restart.test.sh case_unknown_agent_without_sid_fails, case_no_pane_fails, case_where_by_label,
// case_refuse_foreign_label, case_no_agent_foreground_not_shell_fails
func TestRestartWhereRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		prep func(x *fx)
		req  launch.Request
		line string
	}{{
		name: "unknown agent without a sid",
		prep: func(x *fx) {},
		req:  launch.Request{Agent: "nobody"},
		line: "FAIL unknown agent nobody (no lane, role, agents entry or recorded session)",
	}, {
		name: "no pane labelled the agent",
		prep: func(x *fx) { x.sid("pj-o-01", "sid-1"); x.shell("w1:p9", "", "/elsewhere") },
		req:  launch.Request{Agent: "pj-o-01"},
		line: "FAIL no pane labelled pj-o-01 (pass --pane)",
	}, {
		name: "the herdr name alone (an unlabelled pane) is not used",
		prep: func(x *fx) {
			x.sid("pj-o-01", "sid-1")
			a := x.agent("w1:p2", "pj-o-01", "claude", "idle")
			a.Label, a.Foreground = "", 1
			x.shell("w1:p3", "", x.lane)
		},
		req:  launch.Request{Agent: "pj-o-01"},
		line: "FAIL no pane labelled pj-o-01 (pass --pane)",
	}, {
		name: "--pane labelled for another agent",
		prep: func(x *fx) { x.sid("pj-o-01", "sid-1"); x.shell("w1:p4", "pj-rev", "") },
		req:  launch.Request{Agent: "pj-o-01", Pane: "w1:p4"},
		line: "FAIL pane w1:p4 is labelled pj-rev, not pj-o-01",
	}, {
		name: "another pane already labelled the agent",
		prep: func(x *fx) {
			x.sid("pj-o-01", "sid-1")
			x.shell("w1:p3", "pj-o-01", "")
			x.shell("w1:p4", "", "")
		},
		req:  launch.Request{Agent: "pj-o-01", Pane: "w1:p4"},
		line: "FAIL pane w1:p3 is already labelled pj-o-01 (obor restart pj-o-01 there)",
	}, {
		name: "no agent, the foreground is another program",
		prep: func(x *fx) {
			x.sid("pj-o-01", "sid-1")
			s := x.shell("w1:p4", "pj-o-01", "")
			s.Foreground, s.Procs = 200, []herdr.Process{{PID: 200, Argv0: "/usr/bin/vim", Name: "vim"}}
		},
		req:  launch.Request{Agent: "pj-o-01"},
		line: "FAIL w1:p4 runs another program (vim)",
	}, {
		name: "--sid and --fresh",
		prep: func(x *fx) {},
		req:  launch.Request{Agent: "pj-o-01", Sid: "s", Fresh: true},
		line: "FAIL pass --sid or --fresh, not both",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.prep(x)
			out, ok := x.restart(c.req)
			eq(t, last(out), c.line, "result")
			if ok || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.rename")) > 0 || x.startCall() != "" {
				t.Errorf("nothing typed, labelled or started: %q", x.srv.Calls())
			}
		})
	}
}

// T-301: two projects with the same names. The kit has lane O-02 (agent alias o-02); axis has its integrator o-02 in
// its own worktree O-02. A restart that loaded the kit's config (axis's scheduled self-restart, spawned in the kit's
// checkout) took axis's integrator pane for the kit's lane and started it in the kit's lane worktree.
func TestRestartOtherProjectsPane(t *testing.T) {
	t.Parallel()
	kitEdits := []func(map[string]any){
		testkit.Set("lane_types", `{"opus":{"kind":"claude","args":["--model","opus"]}}`),
		testkit.Set("roles", `{"integrator":{"type":"opus"}}`),
	}
	// fixtures: the kit (x, lanes O-01 and O-02), axis (its integrator o-02 in <axis>-wt/O-02, no lanes) and the
	// integrator's pane w2:pF in axis's worktree.
	fixtures := func(t *testing.T) (x *fx, axis *config.Config, axisWt string) {
		x = setup(t)
		x.p.Lane("O-02", "")
		x.p.Config(kitEdits...)
		a := testkit.New(t)
		axisWt = a.Lane("O-02", "")
		a.Config(testkit.Set("lanes", `{}`),
			testkit.Set("lane_types", `{"opus":{"kind":"claude","args":["--model","opus","--strict-mcp-config"]}}`),
			testkit.Set("roles", `{"integrator":{"type":"opus","agent":"o-02","worktree":"O-02"}}`))
		axis, err := config.Load(a.Root)
		if err != nil {
			t.Fatal(err)
		}
		pane := x.agent("w2:pF", "o-02", "claude", "idle")
		pane.Cwd = axisWt
		return x, axis, axisWt
	}
	restart := func(x *fx, cfg *config.Config) (string, bool) {
		var out bytes.Buffer
		l := x.launcher(&out)
		if cfg != nil {
			l.Cfg = cfg
		}
		line, err := l.Restart(context.Background(), launch.Request{Agent: "o-02", Pane: "w2:pF", Fresh: true, Force: true})
		return result(x.t, &out, line, err)
	}

	t.Run("the kit's config: FAIL with evidence, nothing stopped, typed or started", func(t *testing.T) {
		t.Parallel()
		x, _, axisWt := fixtures(t)
		kit, err := config.Load(x.p.Root)
		if err != nil {
			t.Fatal(err)
		}
		// The kit's lane O-02 runs an agent named o-02 too (a real name since T-302: its state file says so).
		x.p.State("O-02", `{"lane":"O-02","agent":"o-02"}`)
		if info, ok := project.AgentLookup(kit, "o-02", ""); !ok || info.Lane != "O-02" {
			t.Fatalf("precondition: the kit's config takes o-02 for its lane O-02: %+v %v", info, ok)
		}
		out, ok := restart(x, nil)
		eq(t, failLine(out), "FAIL pane w2:pF is not this project's: its cwd "+axisWt+" is outside "+x.p.Root+
			" (another project's agent? run obor restart o-02 in its own project)", "result")
		contains(t, out, "pane screen", "evidence")
		contains(t, x.p.Read(".obor/state/launch.log"), "pane w2:pF is not this project's", "evidence logged")
		if ok || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing stopped, typed or started: %q", x.srv.Calls())
		}
	})
	t.Run("control: axis's config restarts its integrator in its own worktree", func(t *testing.T) {
		t.Parallel()
		x, axis, axisWt := fixtures(t)
		out, ok := restart(x, axis)
		if !ok || last(out) != "RESTARTED o-02 (pane w2:pF, session new)" {
			t.Fatalf("restarted:\n%s", out)
		}
		contains(t, strings.Join(x.calls("pane.send_input"), "\n"), "cd "+axisWt+" && export OBOR_AGENT=o-02 OBOR_ROLE=integrator",
			"started in axis's worktree")
		contains(t, x.startCall(), "--strict-mcp-config", "axis's lane type")
	})
	t.Run("control: the kit's own lane pane restarts in the kit's lane worktree", func(t *testing.T) {
		t.Parallel()
		x, _, _ := fixtures(t)
		x.f.Pane("w2:pF").Cwd = x.p.LaneDir("O-02")
		out, ok := restart(x, nil)
		if !ok || last(out) != "RESTARTED o-02 (pane w2:pF, session new)" {
			t.Fatalf("restarted:\n%s", out)
		}
		contains(t, strings.Join(x.calls("pane.send_input"), "\n"), "cd "+x.p.LaneDir("O-02")+" && export OBOR_AGENT=o-02",
			"started in the kit's lane")
	})
	t.Run("control: a pane in another worktree of the kit's repository is the kit's", func(t *testing.T) {
		t.Parallel()
		x, _, _ := fixtures(t)
		stable := filepath.Join(x.p.Base, "stable")
		x.p.Git(x.p.Root, "worktree", "add", "-q", "--detach", stable, "main")
		x.f.Pane("w2:pF").Cwd = stable
		out, ok := restart(x, nil)
		if !ok || last(out) != "RESTARTED o-02 (pane w2:pF, session new)" {
			t.Fatalf("restarted:\n%s", out)
		}
	})
}

// bash restart.test.sh case_where_by_label, case_label_set_on_start
func TestRestartLabel(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-o-01", "sid-1")
	a := x.agent("w1:p2", "pj-o-01", "claude", "idle")
	a.Label, a.Foreground = "", 1 // herdr still names w1:p2 pj-o-01, its agent exited
	x.shell("w1:p3", "pj-o-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
	contains(t, out, "pane w1:p3 (from label)", "the label wins over the herdr name")
	if ok {
		t.Errorf("another pane holds the herdr name: no move\n%s", out)
	}
	eq(t, last(out), "FAIL pane w1:p2 holds the name pj-o-01: restart it there (obor restart pj-o-01)",
		"the name is elsewhere: FAIL with a hint")

	x.reset()
	x.shell("w1:p4", "", "")
	out, ok = x.restart(launch.Request{Agent: "pj-o-01", Pane: "w1:p4"})
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p4, session sid-1)", "an unlabelled --pane is fine")
	contains(t, out, "labelled pane w1:p4 pj-o-01", "said")
	eq(t, x.f.Panes[0].Label, "pj-o-01", "label set in herdr")
	env, ren, start := x.index("pane.send_input keys=[Enter] pane_id=w1:p4 text=cd "), x.index("pane.rename label=pj-o-01 pane_id=w1:p4"), x.index("agent.start")
	if !(env >= 0 && env < ren && ren < start) {
		t.Errorf("env, then label, then start: %q", x.srv.Calls())
	}

	x.f.Panes[0].Kind, x.f.Panes[0].Foreground = "", 0 // the agent exited again
	n := len(x.srv.Calls())
	out, _ = x.restart(launch.Request{Agent: "pj-o-01"})
	contains(t, out, "pane w1:p4 (from label)", "found by the label just set")
	lacks(t, strings.Join(x.srv.Calls()[n:], "\n"), "pane.rename", "already labelled: no rename")
	lacks(t, out, "labelled pane", "nothing said")

	x.reset()
	x.shell("w1:p5", "", "")
	x.srv.Fail("pane.rename", "pane_not_found", "gone")
	n = len(x.srv.Calls())
	out, ok = x.restart(launch.Request{Agent: "pj-o-01", Pane: "w1:p5"})
	eq(t, last(out), "FAIL herdr pane rename w1:p5 pj-o-01 failed", "rename failing")
	if ok || strings.Contains(strings.Join(x.srv.Calls()[n:], "\n"), "agent.start") {
		t.Error("no start after a failed rename")
	}
}

// codexMCP is the server Codex gets on every start's command line (T-375); codexUpdate is its startup update check off
// (T-570, from the type's launch args, so the start adds none).
const codexMCP = `-c mcp_servers.obor.command="/kit/bin/obor" -c mcp_servers.obor.args=["mcp"] ` +
	`-c mcp_servers.obor.env_vars=["OBOR_ROLE","OBOR_AGENT","OBOR_LANE","HERDR_PANE_ID","OBOR_STATE_HOME"] ` +
	`-c mcp_servers.obor.env={OBOR_NO_SELF_HOST="1"} ` +
	`-c mcp_servers.obor.default_tools_approval_mode="approve"`

const codexUpdate = "-c check_for_update_on_startup=false"

// T-375: the first prompt per kind (the wake of a fresh start), Codex's MCP registration on every start, and no
// prompt on a resume or for a kind without MCP.
func TestStartFirstPrompt(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	const types = `{"opus":{"kind":"claude","args":["--model","opus"]},"luna":{"kind":"codex","args":["-m","gpt-6-luna"]},` +
		`"oc":{"kind":"opencode","args":["--auto"]}}`
	settings := " --settings " + config.Compact(map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false})
	for _, c := range []struct {
		name, role, typ, kind string
		req                   launch.Request
		resume                bool
		want                  string
	}{
		// bash-free cases: the table is the spec
		{"claude, wake", "reviewer", "opus", "claude", launch.Request{Fresh: true, Prompt: wake}, false,
			"-- --model opus" + settings + " " + wake},
		{"claude, no wake", "reviewer", "opus", "claude", launch.Request{Fresh: true}, false,
			"-- --model opus" + settings},
		{"claude, resume carries no wake", "reviewer", "opus", "claude", launch.Request{Prompt: wake}, true,
			"-- --model opus" + settings + " --resume sid-1"},
		{"codex, wake", "reviewer", "luna", "codex", launch.Request{Fresh: true, Prompt: wake}, false,
			"-- -m gpt-6-luna " + codexUpdate + " " + codexMCP + " " + wake},
		{"codex, no wake: still registered", "reviewer", "luna", "codex", launch.Request{Fresh: true}, false,
			"-- -m gpt-6-luna " + codexUpdate + " " + codexMCP},
		{"codex, resume carries no wake", "reviewer", "luna", "codex", launch.Request{Prompt: wake}, true,
			"-- resume sid-1 -m gpt-6-luna " + codexUpdate + " " + codexMCP},
		{"opencode, wake after --prompt", "reviewer", "oc", "opencode", launch.Request{Fresh: true, Prompt: wake}, false,
			"-- --auto --prompt " + wake},
		{"opencode, no wake", "reviewer", "oc", "opencode", launch.Request{Fresh: true}, false, "-- --auto"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("lane_types", types), testkit.Set("roles", `{"reviewer":{"type":"`+c.typ+`"}}`))
			a := x.agent("w1:p7", "pj-rev", c.kind, "idle")
			a.Session = "sid-1"
			c.req.Agent = "pj-rev"
			out, ok := x.restart(c.req)
			if !ok {
				t.Fatal(out)
			}
			want := "agent.start kind=" + c.kind + " name=pj-rev pane_id=w1:p7"
			if args := strings.TrimPrefix(c.want, "-- "); args != "" {
				want = "agent.start args=[" + args + "] kind=" + c.kind + " name=pj-rev pane_id=w1:p7"
			}
			eq(t, x.startCall(), want, "agent.start")
		})
	}
}

// T-387: herdr's start waits for an agent ready for input; one working on its first prompt is not, herdr gives up
// after 30 s and keeps no name for it. A start with a prompt whose agent runs and works in the pane is a start, named.
func TestStartBusyOnFirstPrompt(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	for _, c := range []struct {
		name, prompt, status string
		ok                   bool
	}{
		{"wake, working at herdr's timeout: RESTARTED, named", wake, "working", true},
		{"wake, blocked at herdr's timeout: FAIL", wake, "blocked", false},
		{"no wake, working at herdr's timeout: FAIL as before", "", "working", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.poll = bound // let herdr's immediate timeout win before started() polls the new agent
			x.shell("w1:p3", "pj-o-01", "")
			x.f.Set("w1:p3", func(p *herdrtest.Pane) { p.OnStart = c.status })
			out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true, Prompt: c.prompt})
			eqInt(t, len(x.calls("agent.start")), 1, "precondition: one start")
			contains(t, out, "(timeout)", "precondition: herdr gave up")
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v: %s", ok, c.ok, out)
			}
			if !c.ok {
				contains(t, failLine(out), "FAIL start failed in w1:p3", "result")
				eqInt(t, len(x.calls("agent.rename")), 0, "not named")
				return
			}
			eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "result")
			contains(t, out, "pj-o-01 runs in w1:p3 (working on its first prompt): started", "said")
			eq(t, strings.Join(x.calls("agent.rename"), "\n"), "agent.rename name=pj-o-01 target=w1:p3", "named")
			eq(t, x.f.Pane("w1:p3").Name, "pj-o-01", "herdr knows the name")
		})
	}
}

// T-548: the launcher finds the agent's pane in the one pane list it reads to resolve the restart, not in a second list
// that can differ (or fail, which it reported as "no pane labelled"): the label gone from every later list changes nothing.
func TestRestartResolvesFromOnePaneList(t *testing.T) {
	t.Parallel()
	x := setup(t)
	pane := x.shell("w1:p3", "pj-o-01", "")
	lists := 0
	x.srv.Before(func(r herdrtest.Request) {
		if r.Method != "pane.list" {
			return
		}
		if lists++; lists > 1 {
			x.f.Locked(func() { pane.Label = "" })
		}
	})
	out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	eqInt(t, len(x.calls("agent.start")), 1, "precondition: the restart got as far as the start")
	lacks(t, out, "no pane labelled", "found in the first list")
	if !ok {
		t.Fatalf("restart failed:\n%s", out)
	}
}

// T-544: herdr answers agent.start at once (launch_pending), before the agent runs; a start whose agent never comes
// up (live: the command cut off at the shell, left at an open quote) is a FAIL that says so, not RESTARTED.
func TestStartAgentNeverUp(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.wait = 200 * time.Millisecond
	x.shell("w1:p3", "pj-o-01", "").OnStart = "never"
	out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true, Prompt: strings.Repeat("a long brief ", 130)})
	eqInt(t, len(x.calls("agent.start")), 1, "precondition: one start, sent once")
	lacks(t, out, "(timeout)", "precondition: herdr answered the start with success")
	if ok || strings.Contains(out, "RESTARTED") {
		t.Fatalf("a start with no agent up said RESTARTED:\n%s", out)
	}
	contains(t, out, "no claude agent came up in w1:p3 within 200ms", "why")
	eq(t, failLine(out), "FAIL start failed in w1:p3 (no agent runs there: the pane is at its shell, maybe mid-command: "+
		"run obor restart pj-o-01 --pane w1:p3 again or start it by hand)", "result")
	eqInt(t, len(x.calls("agent.rename")), 0, "nothing named")
}

// T-544: the start's command is typed only once the shell has run the env line and is back at its prompt (zsh runs a
// line with the tty in cooked mode, where a long command typed at once is cut off): the mark it echoes, then a prompt.
func TestRestartWaitsForTheEnvLine(t *testing.T) {
	t.Parallel()
	t.Run("the shell back: the start after the mark", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		env, read, start := x.index("pane.send_input pane_id=w1:p3"), x.index("pane.read pane_id=w1:p3"), x.index("agent.start")
		if env < 0 || read < env || start < read {
			t.Errorf("env line %d, screen read %d, start %d: want the start after the read after the env line:\n%s",
				env, read, start, strings.Join(x.srv.Calls(), "\n"))
		}
		contains(t, x.f.Pane("w1:p3").Screen, "obor-env-", "precondition: the shell echoed the mark")
	})
	t.Run("the shell never back: FAIL, nothing started", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = 200 * time.Millisecond
		x.shell("w1:p3", "pj-o-01", "").OnRun = "hang"
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		eqInt(t, len(x.calls("pane.send_input pane_id=w1:p3")), 1, "precondition: the env line typed")
		if ok {
			t.Fatalf("restarted while the shell still ran the env line:\n%s", out)
		}
		eq(t, failLine(out), "FAIL the shell in w1:p3 did not finish the env line in 200ms (nothing started; the env line may have failed: check the pane)", "result")
		eq(t, x.startCall(), "", "no start")
	})
}

// T-601 (RV-64 #3): the env line's success mark proves the env was set. A cd that fails echoes the failure mark instead
// (no success mark), and the launch gives up before it starts the agent in the shell's old cwd with the old OBOR_* env.
func TestRestartFailsWhenTheEnvLineFailed(t *testing.T) {
	t.Parallel()
	t.Run("cd fails: the failure mark, no start, FAIL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		pane := x.shell("w1:p3", "pj-o-01", "")
		pane.OnRun = "fail"
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		if ok {
			t.Fatalf("restarted after a failed env line:\n%s", out)
		}
		runs := x.calls("pane.send_input pane_id=w1:p3")
		if len(runs) != 1 {
			t.Fatalf("one env line typed, got %q", runs)
		}
		okWord, badWord := envWords(t, runs[0])
		lacks(t, pane.Screen, okWord, "the screen: the success mark")
		contains(t, pane.Screen, badWord, "the screen: the failure mark")
		eq(t, failLine(out), "FAIL the env line in w1:p3 failed (nothing started; check the pane)", "result")
		eq(t, x.startCall(), "", "no start")
	})
	t.Run("the line runs: the success mark, the agent starts", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		pane := x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		runs := x.calls("pane.send_input pane_id=w1:p3")
		if len(runs) != 1 {
			t.Fatalf("one env line typed, got %q", runs)
		}
		okWord, badWord := envWords(t, runs[0])
		contains(t, pane.Screen, okWord, "the screen: the success mark")
		lacks(t, pane.Screen, badWord, "the screen: the failure mark")
		if s := x.startCall(); !strings.Contains(s, "name=pj-o-01 pane_id=w1:p3") {
			t.Errorf("the agent started after the env line succeeded, got %q", s)
		}
	})
}

// envWords is the success and failure words the typed env line echoes (T-601), quotes removed.
func envWords(t *testing.T, line string) (ok, bad string) {
	t.Helper()
	m := envMarksRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("the env line has no success and failure marks: %q", line)
	}
	return "obor-env-" + m[1], "obor-env-" + m[2]
}

// T-404: herdr's agent start answers only once the agent is ready for input (30 s), which an agent busy on its first
// prompt never is: a start ends with its agent up and named, while herdr's answer is still pending. An agent herdr
// left unnamed is named by the launch; one it never names is still started, with a warning (DD-v0.13 A7).
func TestStartEndsWithTheAgentUp(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		onStart  string // what agent start leaves behind (the fake's OnStart)
		noRename bool
		renames  int
		line     string // the result (a RESTARTED line, or the FAIL's start)
	}{
		{"named at once: RESTARTED without herdr's answer", "", false, 0,
			"RESTARTED pj-o-01 (pane w1:p3, session new)"},
		{"unnamed: named by the launch, RESTARTED without herdr's answer", "unnamed", false, 1,
			"RESTARTED pj-o-01 (pane w1:p3, session new)"},
		{"never named: the rename refused, RESTARTED with a warning", "unnamed", true, 1,
			"RESTARTED pj-o-01 (pane w1:p3, session new)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			release, ended := make(chan struct{}), make(chan struct{})
			defer close(release) // herdr's answer comes after the test's checks
			x.pendingStart(release, ended)
			x.shell("w1:p3", "pj-o-01", "").OnStart = c.onStart
			if c.noRename {
				x.srv.Fail("agent.rename", "agent_not_found", "no agent named")
			}
			var out bytes.Buffer
			done := make(chan struct {
				line string
				err  error
			}, 1)
			go func() {
				line, err := x.launcher(&out).Restart(context.Background(), launch.Request{Agent: "pj-o-01", Fresh: true})
				done <- struct {
					line string
					err  error
				}{line, err}
			}()
			select {
			case got := <-done:
				if got.err != nil {
					if !strings.HasPrefix(got.err.Error(), c.line) {
						t.Fatalf("the start's FAIL: %q, want %q", got.err.Error(), c.line)
					}
					eqInt(t, len(x.calls("agent.rename target=w1:p3")), c.renames, "renames")
					return
				}
				eq(t, got.line, c.line, "result")
			case <-time.After(10 * time.Second):
				t.Fatal("the start waited for herdr's answer")
			}
			select { // T-405: the start owns herdr's call: ended (cancelled) before the start returns
			case <-ended:
			default:
				t.Error("herdr's agent start call still runs after the start returned")
			}
			lacks(t, out.String(), "(timeout)", "herdr's answer is not waited for")
			eqInt(t, len(x.calls("agent.rename target=w1:p3")), c.renames, "renames")
			if c.noRename {
				contains(t, out.String(), "warning: pj-o-01 started in w1:p3, but herdr has no agent of that name there", "warned")
				eq(t, x.f.Pane("w1:p3").Name, "", "herdr kept no name")
				return
			}
			eq(t, x.f.Pane("w1:p3").Name, "pj-o-01", "herdr knows the name")
		})
	}
}

// T-404: a start is its own agent. An agent of the kind already in the pane (herdr lost its name) is not taken for the
// start: obor setup's Start on such a pane waits for herdr's answer, which refuses it (the pane hosts an agent).
func TestStartNotTakenForAnOldAgent(t *testing.T) {
	t.Parallel()
	x := setup(t)
	release := make(chan struct{})
	x.pendingStart(release, nil)
	x.f.Agent("w1:p3", "", "idle", "") // an old claude herdr lost the name of
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- x.launcher(&out).Start(context.Background(), launch.Start{Agent: "pj-o-01", Kind: "claude", Pane: "w1:p3"})
	}()
	select {
	case err := <-done:
		t.Fatalf("the start took the old agent for its own: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pane w1:p3 already hosts an agent") {
			t.Fatalf("the start's answer: %v, want herdr's refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the start did not end with herdr's answer")
	}
	eq(t, x.f.Pane("w1:p3").Name, "", "the old agent is not named pj-o-01")
}

// pendingStart holds herdr's agent start answer until release: herdr acts (its state shows the start), the call has
// no answer yet (T-404). ended (nil: not needed) closes once the start call returns (T-405).
func (x *fx) pendingStart(release chan struct{}, ended chan struct{}) {
	rel, _ := x.srv.Hold("agent.start")
	go func() {
		<-release
		rel()
	}()
	if ended != nil {
		x.wrap = func(h port.PaneControl) port.PaneControl { return startEnds{h, ended} }
	}
}

// startEnds closes ended when its agent start returns.
type startEnds struct {
	port.PaneControl
	ended chan struct{}
}

func (s startEnds) Start(ctx context.Context, name, kind, pane string, args ...string) (port.AgentInfo, error) {
	defer close(s.ended)
	return s.PaneControl.Start(ctx, name, kind, pane, args...)
}

// holdsAgent makes agent.get fail with not_found from the agent.start on, while pending is true: herdr has not
// detected the agent yet (the test clears pending when herdr's answer comes).
func (x *fx) holdsAgent(pending *atomic.Bool) {
	x.srv.Before(func(r herdrtest.Request) {
		switch r.Method {
		case "agent.start":
			pending.Store(true)
		case "agent.get":
			if pending.Load() {
				x.srv.Fail("agent.get", "agent_not_found", "detection pending")
				return
			}
			x.srv.Clear("agent.get")
		}
	})
}

// T-405 (RV-44 #1): herdr renames by pane, so a start names only the agent it started. herdr's answer to an unnamed
// start comes while the pane's agent is not detected yet; by then another agent holds the pane (another kind, or a
// new session): the start renames nothing and fails with why; the same agent is named.
func TestStartRenamesOnlyItsAgent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name          string
		kind, session string // the pane's agent when herdr's answer comes ("": the started one, unchanged)
		fail          string // "": named
	}{
		{"another kind: renamed nothing, FAIL", "codex", "replacement", "but the pane holds a codex agent (session replacement, name other)"},
		{"another session: renamed nothing, FAIL", "claude", "replacement", "but the pane holds a claude agent (session replacement, name other)"},
		{"the started agent: named", "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			release := make(chan struct{})
			rel, entered := x.srv.Hold("agent.start")
			var pending atomic.Bool
			x.holdsAgent(&pending)
			pane := x.shell("w1:p3", "pj-o-01", "")
			pane.OnStart, pane.Session = "unnamed", "started"
			done := make(chan error, 1)
			go func() {
				done <- x.launcher(&bytes.Buffer{}).Start(context.Background(),
					launch.Start{Agent: "pj-o-01", Kind: "claude", Pane: "w1:p3", Label: "pj-o-01", Prompt: "wake"})
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the start never reached herdr")
			}
			eq(t, pane.Kind, "claude", "precondition: the start's agent in the pane")
			if c.kind != "" {
				pane.Kind, pane.Name, pane.Session = c.kind, "other", c.session
			}
			pending.Store(false)
			close(release)
			rel()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the start did not end")
			}
			if c.fail == "" {
				if err != nil {
					t.Fatalf("the start: %v", err)
				}
				eq(t, pane.Name, "pj-o-01", "the started agent named")
				eqInt(t, len(x.calls("agent.rename target=w1:p3")), 1, "renames")
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.fail) || !strings.Contains(err.Error(), "renamed nothing") {
				t.Fatalf("the start's FAIL: %v, want %q", err, c.fail)
			}
			eq(t, pane.Name, "other", "the other agent keeps its name")
			eqInt(t, len(x.calls("agent.rename")), 0, "renames")
		})
	}
}

// T-387: a start ends with herdr knowing the agent by its name: a start herdr answered but left unnamed is named;
// one that cannot be named is still a start, with a warning that says so (DD-v0.13 A7: nothing finds it by name).
func TestStartNamesTheAgent(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		noRename bool
		want     string
	}{
		{"unnamed after the start: renamed, RESTARTED", false, "RESTARTED pj-o-01 (pane w1:p3, session new)"},
		{"unnamed and the rename refused: RESTARTED, warned with the reason", true,
			"warning: pj-o-01 started in w1:p3, but herdr has no agent of that name there (herdr agent rename w1:p3 pj-o-01: " +
				"herdr agent.rename: not found: no agent named (agent_not_found))"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.shell("w1:p3", "pj-o-01", "").OnStart = "unnamed"
			if c.noRename {
				x.srv.Fail("agent.rename", "agent_not_found", "no agent named")
			}
			out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
			eqInt(t, len(x.calls("agent.start")), 1, "precondition: one start")
			lacks(t, out, "(timeout)", "precondition: herdr answered the start")
			if !ok {
				t.Fatalf("not started: %s", out)
			}
			if c.noRename {
				eqInt(t, len(x.calls("agent.rename name=pj-o-01 target=w1:p3")), 1, "precondition: the rename was tried")
				contains(t, out, c.want, "warned")
				eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "result: the start stands")
				return
			}
			eq(t, last(out), c.want, "result")
			contains(t, out, "named the agent in w1:p3 pj-o-01 (herdr had no name for it)", "said")
			eq(t, x.f.Pane("w1:p3").Name, "pj-o-01", "herdr knows the name")
		})
	}
}

// Stop before the start: bash restart.test.sh case_running_idle_exits_then_starts,
// case_running_agent_that_never_exits_fails, case_working_fails_and_force_restarts,
// case_codex_role_resumes_with_herdr_session_id, case_background_task_fails_up_front_force_proceeds,
// case_exit_dialog_escape_or_force_stops_tasks
func TestRestartStopsTheRunningAgent(t *testing.T) {
	t.Parallel()
	t.Run("idle: /exit, wait for the shell, then start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		x.agent("w1:p3", "pj-o-01", "claude", "idle")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "result")
		if e, s := x.index("pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit"), x.index("agent.start"); e < 0 || e > s {
			t.Errorf("/exit before the start: %q", x.srv.Calls())
		}
		contains(t, out, "stopped claude in pane w1:p3", "said")
	})
	t.Run("never exits: FAIL after the wait", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		x.sid("pj-o-01", "sid-1")
		x.agent("w1:p3", "pj-o-01", "claude", "done").OnQuit = "stay"
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, failLine(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		if ok || x.startCall() != "" {
			t.Error("nothing started")
		}
	})
	t.Run("working: FAIL; --force: Escape, then /exit, start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		x.agent("w1:p3", "pj-o-01", "claude", "working")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, failLine(out), "FAIL pj-o-01 is working (--force to restart anyway)", "result")
		if ok || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing typed or started: %q", x.srv.Calls())
		}
		out, ok = x.restart(launch.Request{Agent: "pj-o-01", Force: true})
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "--force restarts")
		esc, exit := x.index("pane.send_keys keys=[esc] pane_id=w1:p3"), x.index("pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit")
		if !(ok && esc >= 0 && esc < exit) {
			t.Errorf("--force: Escape, then /exit: %q", x.srv.Calls())
		}
	})
	t.Run("codex: /quit, resume <herdr session id>", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		a := x.agent("w1:p7", "pj-rev", "codex", "idle")
		a.Session = "cx-9"
		out, ok := x.restart(launch.Request{Agent: "pj-rev"})
		eq(t, last(out), "RESTARTED pj-rev (pane w1:p7, session cx-9)", "result")
		contains(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p7 text=/quit", "Codex quits with /quit")
		eq(t, x.startCall(), "agent.start args=[resume cx-9 -m gpt-6-luna "+codexUpdate+" "+codexMCP+"] kind=codex name=pj-rev pane_id=w1:p7", "codex resume <id>")
		contains(t, out, "resuming cx-9 (from herdr)", "source")
		_ = ok
	})
	// T-312: no background-task pre-check: Claude's Bash tool shells lead their own sessions, which process info does
	// not list (live 2026-10-17); Claude's exit dialog decides (the next test).
	t.Run("a shell in the agent's group, no exit dialog: no pre-check, restarts", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		a := x.agent("w1:p3", "pj-o-01", "claude", "idle")
		a.Procs = append(a.Procs, herdr.Process{PID: 300, Argv0: "/bin/zsh"}, herdr.Process{PID: 301, Argv0: "node"})
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if !ok || !strings.HasPrefix(last(out), "RESTARTED pj-o-01 ") || len(x.calls("pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit")) != 1 {
			t.Errorf("/exit, then restarted:\n%s\n%q", out, x.srv.Calls())
		}
	})
	t.Run("exit dialog: Escape + FAIL; --force: Exit and stop tasks", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		a := x.agent("w1:p3", "pj-o-01", "claude", "idle")
		a.OnQuit = "dialog"
		if len(a.Procs) != 1 {
			t.Fatal("precondition: no background task seen")
		}
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "precondition: /exit sent")
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_keys keys=[esc] pane_id=w1:p3", "Escape closes the dialog")
		eqInt(t, len(x.calls("pane.send_keys keys=[enter] pane_id=w1:p3")), 0, "no Enter into the exit dialog")
		eqInt(t, len(x.calls("pane.send_keys keys=[1] pane_id=w1:p3")), 0, "no option chosen")
		eq(t, failLine(out), "FAIL pj-o-01 has background tasks (finish them, or --force to stop them)", "result")
		if ok || x.startCall() != "" || x.f.Panes[0].Screen != "" {
			t.Error("nothing started; dialog closed")
		}
		n := len(x.srv.Calls())
		out, ok = x.restart(launch.Request{Agent: "pj-o-01", Force: true})
		calls := strings.Join(x.srv.Calls()[n:], "\n")
		eqInt(t, strings.Count(calls, "pane.send_keys keys=[1] pane_id=w1:p3"), 1, "chose 1. Exit and stop tasks, once")
		lacks(t, calls, "pane.send_keys keys=[esc] pane_id=w1:p3", "no Escape")
		contains(t, out, "exit dialog: chose 1. Exit and stop tasks (--force)", "said")
		if !ok || !strings.HasPrefix(last(out), "RESTARTED pj-o-01 ") {
			t.Errorf("--force restarts:\n%s", out)
		}
	})
}

func eqInt(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", what, got, want)
	}
}

// Name release and the start: bash restart.test.sh case_same_pane_name_cleared_then_start_failure_fails,
// case_codex_same_pane_clear_fails_once_retried, case_start_name_taken_once_cleared_and_retried
func TestRestartReleasesTheName(t *testing.T) {
	t.Parallel()
	t.Run("the pane still holds the name: cleared before the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		x.sid("pj-o-01", "sid-1")
		x.agent("w1:p3", "pj-o-01", "claude", "idle").Foreground = 1 // exited; herdr not done with it yet
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		clr, start := x.index("agent.rename name=null target=w1:p3"), x.index("agent.start")
		if !ok || clr < 0 || clr > start {
			t.Errorf("cleared before agent start:\n%s\n%q", out, x.srv.Calls())
		}
		lacks(t, strings.Join(x.calls("pane.send_input"), "\n"), "/exit", "at its shell: nothing to quit")
	})
	t.Run("start fails twice: retried once, herdr's error, FAIL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		x.shell("w1:p3", "pj-o-01", "")
		taken := herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-o-01 taken"}
		x.srv.Once("agent.start", taken, taken)
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Pane: "w1:p3"})
		lacks(t, strings.Join(x.calls("agent.rename"), "\n"), "w1:p3 --clear", "unnamed pane: no clear by pane")
		eqInt(t, len(x.calls("agent.start")), 2, "agent_name_taken: retried once")
		contains(t, out, "agent_name_taken", "herdr's error printed")
		eq(t, failLine(out), "FAIL start failed in w1:p3 (no agent runs there: the pane is at its shell, maybe mid-command: run obor restart pj-o-01 --pane w1:p3 again or start it by hand)", "result")
		if ok {
			t.Error("failed")
		}
	})
	t.Run("start agent_name_taken once: name cleared, retried, RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		x.shell("w1:p3", "pj-o-01", "")
		x.srv.Once("agent.start", herdrtest.Failure{Code: "agent_name_taken", Message: "name pj-o-01 taken"})
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		contains(t, out, "agent start: name pj-o-01 taken: clearing it and retrying", "said")
		eqInt(t, len(x.calls("agent.start")), 2, "retried once")
		if clr, start := x.index("agent.rename name=null target=pj-o-01"), len(x.srv.Calls())-1; clr < 0 || clr > start {
			t.Errorf("cleared before the retry: %q", x.srv.Calls())
		}
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "result")
		_ = ok
	})
	t.Run("start blocked at a startup dialog (agent_not_ready): FAIL that says so, not retried", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-o-01", "")
		x.srv.Once("agent.start", herdrtest.Failure{Code: "agent_not_ready", Message: "blocked during startup"})
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		contains(t, out, "agent_not_ready", "herdr's error printed")
		eqInt(t, len(x.calls("agent.start")), 1, "not retried")
		eq(t, failLine(out), "FAIL start failed in w1:p3 (pj-o-01 waits at a startup dialog in pane w1:p3: answer it there)", "result")
		if ok {
			t.Error("a failure")
		}
	})
	t.Run("codex: first clear by pane fails, retried by name", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		a := x.agent("w1:p7", "pj-rev", "codex", "working")
		a.Session, a.OnQuit = "cx-9", "linger"
		x.srv.Once("agent.rename", herdrtest.Failure{Code: "agent_not_found", Message: "no agent in w1:p7"})
		out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Force: true})
		contains(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p7 text=/quit", "precondition: Codex stopped")
		eq(t, strings.Join(x.calls("agent.rename"), "\n"), "agent.rename name=null target=w1:p7\nagent.rename name=null target=pj-rev",
			"first clear failed → retried by name")
		lacks(t, out, "warning: herdr agent rename", "the retry took")
		eqInt(t, len(x.calls("agent.start")), 1, "started once")
		eq(t, last(out), "RESTARTED pj-rev (pane w1:p7, session new)", "result")
		_ = ok
	})
}

// Cut (T-208): no fg resume, no moves; both FAIL with a hint before anything is typed. Replaces bash restart.test.sh
// case_stopped_job_gets_fg_and_resumed, case_suspended_agent_found_by_label_fg_renames_after_detection,
// case_fg_without_detection_does_not_rename, case_fg_resumes_something_else_warns,
// case_move_to_other_pane_clears_old_name_resumes_recorded_or_old_session, case_move_live_old_agent_fails_before_stopping,
// case_move_codex_resumes_old_pane_session_else_fails.
func TestRestartCuts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		prep func(x *fx)
		req  launch.Request
		line string
	}{{
		name: "--pane elsewhere while the old pane's exited agent holds the name",
		prep: func(x *fx) {
			x.sid("pj-o-01", "sid-1")
			a := x.agent("w1:p3", "pj-o-01", "claude", "idle")
			a.Label, a.Foreground, a.Session = "", 1, "sid-old"
			x.shell("w1:p8", "", "").Session = "sid-target"
		},
		req:  launch.Request{Agent: "pj-o-01", Pane: "w1:p8"},
		line: "FAIL pane w1:p3 holds the name pj-o-01: restart it there (obor restart pj-o-01)",
	}, {
		name: "--pane elsewhere while the old pane runs the live agent (--force)",
		prep: func(x *fx) {
			x.sid("pj-o-01", "sid-1")
			x.agent("w1:p3", "pj-o-01", "claude", "idle").Label = ""
			x.agent("w1:p8", "", "claude", "idle").Label = ""
		},
		req:  launch.Request{Agent: "pj-o-01", Pane: "w1:p8", Force: true},
		line: "FAIL pane w1:p3 holds the name pj-o-01: restart it there (obor restart pj-o-01)",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.prep(x)
			out, ok := x.restart(c.req)
			eq(t, last(out), c.line, "result")
			if ok || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 ||
				len(x.calls("agent.rename")) > 0 || x.startCall() != "" {
				t.Errorf("nothing stopped, typed, renamed or started: %q", x.srv.Calls())
			}
		})
	}
}

// RV-35 #1: Claude suspends on Ctrl+Z and herdr then sees an idle shell; a stopped child of the pane's shell refuses
// restart and stop before anything is typed (a second Claude next to the frozen one). The shell is a real sh with a
// real sleep child, in a throwaway process group; the real ps reads it.
func TestRestartSuspended(t *testing.T) {
	t.Parallel()
	sh := exec.Command("sh", "-c", "sleep 300 & echo $!; wait")
	sh.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := sh.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-sh.Process.Pid, syscall.SIGKILL); _ = sh.Wait() })
	var sleep int
	if _, err := fmt.Fscan(stdout, &sleep); err != nil || sleep <= 0 {
		t.Fatalf("sleep pid: %d %v", sleep, err)
	}
	// stat sends sig to the sleep child and waits until ps shows it in a state starting with one of want's letters (T
	// stopped, S/R/I running). ps may still show the old state: a SIGSTOP sent while the child is between fork and exec
	// is discarded by the exec, and under load that window is long enough to lose it (T-329: 15 of 50 runs under load
	// never saw T with one signal). So resend each poll, with a generous upper bound.
	stat := func(want string, sig syscall.Signal) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		var last []byte
		for {
			_ = syscall.Kill(sleep, sig)
			out, _ := exec.Command("ps", "-o", "ppid=,stat=", "-p", strconv.Itoa(sleep)).Output()
			last = out
			f := strings.Fields(string(out))
			if len(f) == 2 && f[0] == strconv.Itoa(sh.Process.Pid) && strings.ContainsRune(want, rune(f[1][0])) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("precondition: sleep %d (child of sh %d) not in state %s within 30s (last %q)",
					sleep, sh.Process.Pid, want, last)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// The pane's shell is the test's sh, and it has the foreground.
	atSh := func(x *fx) {
		x.ps = nil
		shp := sh.Process.Pid
		x.procInfo(func(p *herdrtest.Pane) { p.Shell, p.Foreground = shp, shp })
	}
	nothing := func(x *fx) {
		t.Helper()
		if len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing typed or started: %q", x.srv.Calls())
		}
	}

	stat("T", syscall.SIGSTOP)
	x := setup(t)
	atSh(x)
	x.shell("w1:p3", "pj-o-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	if ok {
		t.Fatal("restarted next to a suspended agent:\n" + out)
	}
	eq(t, last(out), "FAIL pj-o-01 is suspended in pane w1:p3 (Ctrl+Z): fg it and /exit, or close the pane", "restart --fresh")
	contains(t, out, fmt.Sprintf("(pid %d) is stopped", sleep), "the stopped child named")
	nothing(x)
	x.sid("pj-o-01", "sid-1")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01"})
	eq(t, last(out), "FAIL pj-o-01 is suspended in pane w1:p3 (Ctrl+Z): fg it and /exit, or close the pane", "restart, same session")
	out, _ = x.stop("pj-o-01", "", false)
	eq(t, last(out), "FAIL pj-o-01 is suspended in pane w1:p3 (Ctrl+Z): fg it and /exit, or close the pane", "stop")
	nothing(x)

	// The same child running (not stopped) at the shell: starts, as before.
	stat("SRI", syscall.SIGCONT)
	x = setup(t)
	atSh(x)
	x.shell("w1:p3", "pj-o-01", "")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "a running child: starts")

	// No child the shell stopped (the fake herdr's shell, pid 1, with the real ps): starts.
	x = setup(t)
	x.ps = nil
	x.shell("w1:p3", "pj-o-01", "")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "no stopped child: starts")

	// ps cannot say (denied, as in a sandbox): a warning, and it starts.
	x = setup(t)
	x.ps = func(...string) ([]byte, error) { return nil, errors.New("operation not permitted") }
	x.shell("w1:p3", "pj-o-01", "")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	contains(t, out, "warning: cannot check pane w1:p3 for a suspended agent (ps: operation not permitted)", "warned")
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "ps denied: starts")

	// An agent in the foreground: ps is not asked.
	x = setup(t)
	asked := 0
	x.ps = func(...string) ([]byte, error) { asked++; return []byte("1 T 9 claude\n"), nil }
	x.agent("w1:p3", "pj-o-01", "claude", "idle")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "agent running: restarted")
	eqInt(t, asked, 0, "ps asked only at a shell")
}

// bash restart.test.sh case_stale_recorded_sid_fails_sid_option_resumes, case_recorded_sid_checked_against_pane_cwd_codex_skipped
func TestRestartRecordedSession(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.record("pj-o-01", "sid-x")
	x.transcript("/Users/someone/other-project", "sid-x")
	x.shell("w1:p3", "pj-o-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
	eq(t, last(out), "FAIL recorded session sid-x is not from "+x.lane+" (pass --fresh, or --sid <id>)", "another project's sid")
	if ok || len(x.calls("pane.send_input")) > 0 || x.startCall() != "" {
		t.Error("nothing typed or started")
	}
	x.transcript(x.lane, "sid-x")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01"})
	contains(t, out, "resuming sid-x (from record)", "source printed")
	eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-x)", "transcript under the lane → resumes")

	x.reset()
	x.shell("w1:p3", "pj-o-01", "")
	x.record("pj-o-01", "sid-stale")
	out, _ = x.restart(launch.Request{Agent: "pj-o-01", Sid: "sid-y"})
	contains(t, out, "resuming sid-y (from --sid)", "source printed")
	contains(t, x.calls("agent.start")[1], "--resume sid-y", "--sid wins over the stale record")

	// No lane or role: the pane's cwd is checked; herdr's session is not checked; Codex records are not checked.
	x.reset()
	x.record("triage", "sid-t")
	x.transcript("/Users/someone/elsewhere", "sid-t")
	x.shell("w1:p8", "triage", "/work/maps/").Session = "sid-t"
	out, _ = x.restart(launch.Request{Agent: "triage"})
	eq(t, last(out), "FAIL recorded session sid-t is not from /work/maps (pass --fresh, or --sid <id>)", "the pane cwd")
	x.transcript("/work/maps", "sid-t")
	out, _ = x.restart(launch.Request{Agent: "triage"})
	eq(t, last(out), "RESTARTED triage (pane w1:p8, session sid-t)", "transcript under the pane cwd")

	x.reset()
	x.shell("w1:p8", "", "/work/maps").Session = "sid-h"
	out, _ = x.restart(launch.Request{Agent: "scout", Pane: "w1:p8"})
	contains(t, out, "resuming sid-h (from herdr)", "herdr's session, unchecked")

	x.reset()
	x.record("pj-rev", "cx-1")
	x.shell("w1:p7", "pj-rev", "")
	out, _ = x.restart(launch.Request{Agent: "pj-rev"})
	contains(t, out, "recorded session cx-1 not checked (codex keeps no transcript per cwd)", "said")
	contains(t, x.startCall(), "args=[resume cx-1", "codex resumes its record")
}

// T-627: the session rule, restart and restore, as the state model's row 51 says it. A recorded session is resumed
// when its transcript is under the pane's cwd, refused when it is under another, and started fresh (said) when it
// exists nowhere: Claude writes no transcript before the first prompt, so a session never prompted has none. A pane
// with no recorded session starts fresh in restore, role or worker (a role started fresh with no prompt has no
// transcript either).
func TestSessionResumeRule(t *testing.T) {
	t.Parallel()
	t.Run("restart: transcript in the pane's cwd: resumed", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "resumed")
		contains(t, x.startCall(), "--resume sid-1", "the transcript's session")
		lacks(t, out, "starting fresh", "no fresh start")
	})
	t.Run("restart: transcript under another cwd: refused", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-o-01", "sid-x")
		x.transcript("/Users/someone/other-project", "sid-x")
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if ok {
			t.Fatal(out)
		}
		eq(t, last(out), "FAIL recorded session sid-x is not from "+x.lane+" (pass --fresh, or --sid <id>)", "refused")
		lacks(t, out, "starting fresh", "never fresh")
		if x.startCall() != "" || len(x.calls("pane.send_input")) > 0 {
			t.Errorf("nothing typed or started")
		}
	})
	t.Run("restart: config dir with glob metacharacters, transcript under another cwd: refused", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.claude = filepath.Join(x.p.Base, "cl[aude")
		x.record("pj-o-01", "sid-x")
		x.transcript("/Users/someone/other-project", "sid-x")
		if _, err := os.Stat(filepath.Join(x.claude, "projects")); err != nil {
			t.Fatalf("precondition: the transcript exists under the bracketed dir: %v", err)
		}
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if ok {
			t.Fatal(out)
		}
		eq(t, last(out), "FAIL recorded session sid-x is not from "+x.lane+" (pass --fresh, or --sid <id>)", "refused")
		lacks(t, out, "starting fresh", "never fresh")
		if x.startCall() != "" {
			t.Errorf("nothing started")
		}
	})
	t.Run("restart: unreadable projects dir: refused, never fresh", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-o-01", "sid-x")
		x.transcript("/Users/someone/other-project", "sid-x")
		projects := filepath.Join(x.claude, "projects")
		if err := os.Chmod(projects, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(projects, 0o755) })
		if _, err := os.ReadDir(projects); err == nil {
			t.Skip("the projects dir is readable despite mode 0 (root)")
		}
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if ok {
			t.Fatal(out)
		}
		contains(t, last(out), "FAIL recorded session sid-x: cannot", "refused with the error")
		lacks(t, out, "starting fresh", "never fresh")
		if x.startCall() != "" {
			t.Errorf("nothing started")
		}
	})
	t.Run("restart: transcript nowhere (never prompted): fresh, said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-o-01", "sid-x")
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "recorded session sid-x has no transcript (never prompted): starting fresh", "said")
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session new)", "a new session")
		lacks(t, x.startCall(), "--resume", "no resume")
	})
	t.Run("restore: a role with no recorded session: fresh, said", func(t *testing.T) {
		t.Parallel()
		f := newRfx(t)
		f.p.Config(testkit.Set("roles", `{"reviewer": {"type": "opus"}}`))
		rev := f.p.Lane("review", "review")
		f.shell("w1:p7", rev, "pj-rev")
		f.pl.Restore(context.Background())
		f.wantCalls(f.p.Root + " restart pj-rev --pane w1:p7 --fresh")
		has(t, f.restoreLog(), " restore pj-rev (pane w1:p7) in "+f.p.Root+" (reviewer, no recorded session: --fresh)",
			"the fresh start is logged")
	})
	t.Run("restore: a worker of a busy lane with no recorded session: fresh, as before", func(t *testing.T) {
		t.Parallel()
		f := newRfx(t)
		lane := f.p.Lane("O-01", "task/T-01")
		f.p.Commit(lane)
		f.p.Config()
		f.shell("w1:p1", lane, "pj-o-01")
		f.pl.Restore(context.Background())
		f.wantCalls(f.p.Root + " restart pj-o-01 --pane w1:p1 --fresh")
		log := f.restoreLog()
		has(t, log, " restore pj-o-01 (pane w1:p1) in "+f.p.Root+" (worker, no recorded session: --fresh)",
			"the fresh start is logged")
		lacks(t, log, "skip pj-o-01", "the busy lane's worker is not skipped")
	})
}

// Stop (pain 4): idle → /exit, no start, the pane stays labelled at a shell; at a shell already; working → FAIL unless
// force; force → Escape, wait for idle, the same exit. Ported from the --stop paths of bash restart.test.sh (merge and
// drop called it) plus the new --stop --force.
func TestStop(t *testing.T) {
	t.Parallel()
	t.Run("idle: /exit, STOPPED, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-o-01", "claude", "idle")
		out, ok := x.stop("pj-o-01", "", false)
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "only /exit typed")
		if !ok || x.startCall() != "" || x.f.Panes[0].Label != "pj-o-01" || x.f.Panes[0].Kind != "" {
			t.Errorf("stopped, labelled, not started: %+v %q", x.f.Panes[0], x.srv.Calls())
		}
	})
	t.Run("at a shell already", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.stop("pj-o-01", "", false)
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3, already at a shell)", "result")
		if !ok || len(x.calls("pane.send_input")) > 0 {
			t.Error("nothing typed")
		}
	})
	t.Run("working, no force: FAIL, nothing typed", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-o-01", "claude", "working")
		out, ok := x.stop("pj-o-01", "", false)
		eq(t, failLine(out), "FAIL pj-o-01 is working (left running; --force stops it)", "result")
		if ok || len(x.calls("pane.send_input")) > 0 || len(x.calls("pane.send_keys")) > 0 {
			t.Error("nothing typed")
		}
	})
	t.Run("working, --force: Escape, idle, input cleared, /exit (one step)", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		a := x.agent("w1:p3", "pj-o-01", "claude", "working")
		a.Procs = append(a.Procs, herdr.Process{PID: 300, Argv0: "/bin/zsh"}) // a background task: --force stops it too
		out, ok := x.stop("pj-o-01", "", true)
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		esc, clr, exit := x.index("pane.send_keys keys=[esc] pane_id=w1:p3"), x.index("pane.send_keys keys=[ctrl+c] pane_id=w1:p3"), x.index("pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit")
		if !ok || esc < 0 || esc > clr || clr > exit {
			t.Errorf("Escape, then ctrl+c (the restored prompt cleared), then /exit: %q", x.srv.Calls())
		}
		lacks(t, out, "warning", "it went idle")
	})
	t.Run("blocked, --force, Escape ignored: exits anyway after the idle wait", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		a := x.agent("w1:p3", "pj-o-01", "claude", "blocked")
		a.OnEsc = "ignore"
		out, ok := x.stop("pj-o-01", "", true)
		contains(t, out, "warning: pj-o-01 still blocked 60ms after Escape: exiting anyway", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		_ = ok
	})
	t.Run("--force, never exits, no agent process seen: FAIL, no signal", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		a := x.agent("w1:p3", "pj-o-01", "claude", "working")
		a.OnQuit, a.Procs = "stay", []herdr.Process{} // herdr lists nothing in the group: no exit, no leader
		out, ok := x.stop("pj-o-01", "", true)
		eq(t, failLine(out), `FAIL pj-o-01 did not exit in 60ms (pane w1:p3; not terminated: the group's leader (pid 2) is "", not claude)`, "result")
		eqInt(t, len(x.kills), 0, "no signal")
		if ok {
			t.Error("failed")
		}
	})
}

// T-249: an agent that ignores /exit. Without --force: FAIL with the hint. With --force, while it provably still leads
// the pane's foreground group (same shell, same leader pid, claude/codex): SIGTERM, then SIGKILL, then the leftover
// check and the start as for an agent that exited. Not proven: no signal, FAIL with evidence.
func TestStopForceTerminates(t *testing.T) {
	t.Parallel()
	// ignoring: an idle Claude leading group 2 with uvx, that ignores /exit; a signal in dies (pid 2 gone, unlisted)
	// takes the leader, and with leftovers the uvx stays until the next signal.
	ignoring := func(x *fx, dies syscall.Signal, leftovers bool) *herdrtest.Pane {
		p := x.agent("w1:p3", "pj-o-01", "claude", "idle")
		p.OnQuit = "stay"
		p.Procs = []herdr.Process{{PID: 2, Argv0: "/Users/u/.local/bin/claude"}, {PID: 300, Argv0: "uvx"}}
		x.onKill = func(_ int, sig syscall.Signal) {
			switch {
			case x.gone["2"]:
				p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil // the leftovers go too
			case sig == dies && leftovers:
				p.Procs, x.gone["2"] = p.Procs[1:], true
			case sig == dies:
				p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil
				x.gone["2"] = true
			}
		}
		return p
	}
	t.Run("no --force: FAIL with the hint, no signal", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		ignoring(x, syscall.SIGTERM, false)
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		contains(t, out, "in the group: 2 claude", "evidence")
		eqInt(t, len(x.kills), 0, "no signal")
	})
	t.Run("--force: SIGTERM ends it, STOPPED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		ignoring(x, syscall.SIGTERM, false)
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2 only")
		contains(t, out, "terminated pj-o-01 (claude pid 2, SIGTERM): it did not exit in 60ms\n", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("--force, SIGTERM ignored: SIGKILL, STOPPED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		ignoring(x, syscall.SIGKILL, false)
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		contains(t, out, "terminated pj-o-01 (claude pid 2, SIGKILL): it did not exit in 60ms\n", "said")
	})
	t.Run("--force, restart: SIGTERM, leftovers stopped, then the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		x.sid("pj-o-01", "sid-1")
		ignoring(x, syscall.SIGTERM, true)
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Force: true})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 terminated", "TERM to the agent, then TERM to its leftovers")
		contains(t, out, "terminated pj-o-01 (claude pid 2, SIGTERM)", "agent terminated")
		contains(t, out, "stopped 1 leftover process(es) of pj-o-01 (uvx)", "leftover line")
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "result")
	})
	t.Run("--force, TERM and KILL ignored: FAIL with evidence, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		x.sid("pj-o-01", "sid-1")
		ignoring(x, 0, false)
		out, ok := x.restart(launch.Request{Agent: "pj-o-01", Force: true})
		if ok {
			t.Fatal("must fail")
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms, nor on SIGTERM and SIGKILL (pane w1:p3)", "result")
		contains(t, out, "agent status: idle\n", "evidence")
		eq(t, x.startCall(), "", "no start")
	})
	// What is not proven the agent's is never signalled: checked after the wait (no signal) and after TERM (no KILL).
	for _, c := range []struct {
		name  string
		when  string // "wait": changed before the first check; "term": after SIGTERM
		shell int    // the pane's shell from then on (0: unchanged)
		procs []herdr.Process
		kills string
		fail  string
	}{
		{name: "leader changed: no signal", when: "wait", procs: []herdr.Process{{PID: 2, Argv0: "vim"}},
			fail: `not terminated: the group's leader (pid 2) is "vim", not claude`},
		// No longer listed is gone (T-312): the agent counts as exited, what is left is the leftovers' ladder's.
		{name: "leader gone from the list: exited, its leftovers signalled", when: "wait", procs: []herdr.Process{{PID: 300, Argv0: "uvx"}},
			kills: "2 terminated,2 killed", fail: "pj-o-01 exited, but its leftover process(es) (uvx) kept the foreground after SIGTERM and SIGKILL (pane w1:p3)"},
		{name: "shell changed: no signal", when: "wait", shell: 7,
			fail: "not terminated: the pane's shell is pid 7, not 1"},
		{name: "leader changed after TERM: no KILL", when: "term", procs: []herdr.Process{{PID: 2, Argv0: "vim"}},
			kills: "2 terminated", fail: `nor on SIGTERM (pane w1:p3; no SIGKILL: the group's leader (pid 2) is "vim", not claude)`},
		{name: "shell changed after TERM: no KILL", when: "term", shell: 7,
			kills: "2 terminated", fail: "nor on SIGTERM (pane w1:p3; no SIGKILL: the pane's shell is pid 7, not 1)"},
	} {
		t.Run("--force, "+c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.wait = shortWait
			p := ignoring(x, 0, false)
			changed := false
			change := func() {
				changed = true
				if c.procs != nil {
					p.Procs = c.procs
				}
			}
			x.procInfo(func(p *herdrtest.Pane) {
				if changed && c.shell != 0 {
					p.Shell = c.shell
				}
			})
			if c.when == "wait" {
				x.onExit = change
			} else {
				x.onKill = func(int, syscall.Signal) { change() }
			}
			out, ok := x.stop("pj-o-01", "", true)
			if ok || !changed {
				t.Fatalf("must fail after the change:\n%s", out)
			}
			eq(t, strings.Join(x.kills, ","), c.kills, "signals")
			contains(t, last(out), c.fail, "result")
			contains(t, out, "pane processes:", "evidence")
		})
	}
}

// lockSpy notes, when /exit is typed and when the agent is started, which of the restart locks it watches are held.
type lockSpy struct {
	port.PaneControl
	locks []*project.Lock
	seen  *[]string
}

func (s lockSpy) note(what string) {
	for _, l := range s.locks {
		if l.Held() {
			*s.seen = append(*s.seen, what+":"+filepath.Base(l.Path))
		}
	}
}

func (s lockSpy) Type(ctx context.Context, pane, text string) error {
	s.note("run")
	return s.PaneControl.Type(ctx, pane, text)
}

func (s lockSpy) Start(ctx context.Context, name, kind, pane string, args ...string) (port.AgentInfo, error) {
	s.note("start")
	return s.PaneControl.Start(ctx, name, kind, pane, args...)
}

// T-500: one restart lock (the lane's, else the agent's) held for the whole operation, freed at its end.
func TestOneRestartLock(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-o-01", "sid-1")
	x.agent("w1:p3", "pj-o-01", "claude", "idle")
	var seen []string
	lane, agent := launch.RestartLock(x.p.Root, "O-01", ""), launch.RestartLock(x.p.Root, "", "pj-int")
	x.wrap = func(h port.PaneControl) port.PaneControl { return lockSpy{h, []*project.Lock{lane, agent}, &seen} }
	out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
	if !ok {
		t.Fatal(out)
	}
	eq(t, strings.Join(seen, " "), "run:restart-O-01.lock run:restart-O-01.lock start:restart-O-01.lock",
		"the lane's restart lock held at the /exit, the env line and the start")
	if lane.Held() {
		t.Error("held after the restart")
	}
	if _, err := os.Stat(lane.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock file left: %v", err)
	}

	seen = nil
	x.reset()
	x.agent("w1:p6", "pj-int", "claude", "idle")
	if _, ok := x.stop("pj-int", "", false); !ok {
		t.Fatal("stop")
	}
	eq(t, strings.Join(seen, " "), "run:restart-pj-int.lock", "no lane: the agent's lock")
}

// lockedBuf is a bytes.Buffer a restart's goroutine writes while the test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// T-500: two restarts of one lane: the second waits (saying so) until the first frees the lane's restart lock, and
// touches the pane only then.
func TestConcurrentRestartWaits(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-o-01", "sid-1")
	x.agent("w1:p3", "pj-o-01", "claude", "idle")
	first := launch.RestartLock(x.p.Root, "O-01", "") // the restart already running (another obor process)
	if ok, err := first.Try(); !ok || err != nil {
		t.Fatalf("precondition: the first restart holds the lock: %v, %v", ok, err)
	}
	type res struct {
		line string
		err  error
	}
	var out lockedBuf
	l := x.launcher(&out)
	done := make(chan res, 1)
	go func() {
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-o-01"})
		done <- res{line, err}
	}()
	// Wait for the second restart's waiting line: it is what proves it reached the lock (under load, not within any
	// fixed sample of the test).
	said := "waiting for the restart of lane O-01 (pid " + strconv.Itoa(os.Getpid()) + ")\n"
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(out.String(), said); {
		select {
		case r := <-done:
			t.Fatalf("the second restart did not wait: %q, %v", r.line, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second restart never said %q: %q", said, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	eqInt(t, len(x.calls("pane.send_input")), 0, "nothing typed while it waits")
	eqInt(t, len(x.calls("agent.start")), 0, "nothing started while it waits")
	first.Unlock()
	var r res
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the second restart still waits after the first ended")
	}
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, out.String())
	}
	eq(t, r.line, "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "result")
	eqInt(t, len(x.calls("agent.start")), 1, "started once the first ended")
}

// q is s as one shell word, as the env line quotes it.
func q(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// failCalls fails the pane calls of ops: "screen" (pane read) or "process-info".
type failCalls struct {
	port.PaneControl
	ops map[string]bool
	why error
}

func (f failCalls) Screen(ctx context.Context, pane string, lines int) (string, error) {
	if f.ops["screen"] {
		return "", f.why
	}
	return f.PaneControl.Screen(ctx, pane, lines)
}

func (f failCalls) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	if f.ops["process-info"] {
		return port.ProcessInfo{}, f.why
	}
	return f.PaneControl.ProcessInfo(ctx, pane)
}

// T-213: a give-up carries its evidence: the agent's status, the pane's last non-empty lines, the processes.
func TestGiveUpCarriesEvidence(t *testing.T) {
	t.Parallel()
	stuck := func(x *fx) {
		x.sid("pj-o-01", "sid-1")
		p := x.agent("w1:p3", "pj-o-01", "claude", "done")
		p.OnQuit = "stay"
		var lines []string
		for i := 1; i <= 20; i++ {
			lines = append(lines, "screen line "+strings.Repeat("x", i%3), "", "   ")
		}
		p.Screen = strings.Join(lines, "\n") + "\ndraft in the box"
		p.Foreground, p.Procs = 200, []herdr.Process{{PID: 200, Argv0: "/usr/bin/claude", Name: "claude", Cmdline: "claude --resume sid-1"},
			{PID: 301, Argv0: "/usr/bin/sleep", Cmdline: "/usr/bin/sleep 60"}}
	}
	t.Run("never exits: FAIL line, then status, screen tail, processes; the same block in launch.log", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		stuck(x)
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if ok {
			t.Fatal("must fail")
		}
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "the one-line result is last")
		tail := out
		contains(t, tail, "\nagent status: done\n", "status labelled")
		contains(t, tail, "\npane screen (last 15 non-empty lines):\n", "screen labelled")
		contains(t, tail, "  draft in the box", "last screen line")
		eqInt(t, strings.Count(tail, "\n  screen line"), 14, "15 lines in all: the draft plus 14, blanks dropped")
		contains(t, tail, "\npane processes:\n", "processes labelled")
		contains(t, tail, "foreground: claude (process group 200", "foreground command")
		contains(t, tail, "in the group: 200 claude --resume sid-1", "group member")
		contains(t, tail, "in the group: 301 /usr/bin/sleep 60", "another member")

		log, err := os.ReadFile(filepath.Join(x.p.Root, ".obor", "state", "launch.log"))
		if err != nil {
			t.Fatal(err)
		}
		contains(t, string(log), " pj-o-01 pane w1:p3: pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)\nagent status: done\n", "log head")
		contains(t, string(log), "in the group: 301 /usr/bin/sleep 60", "log carries the processes")
		eqInt(t, strings.Count(string(log), "did not exit"), 1, "one block per failure")
	})
	t.Run("stop: the same evidence", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		stuck(x)
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		contains(t, out, "agent status: done\n", "stop gives up the same way")
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "the one-line result is last")
		contains(t, out, "pane processes:", "processes")
	})
	t.Run("herdr cannot read the screen or the processes: FAIL still printed, the gaps said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		stuck(x)
		x.wrap = func(h port.PaneControl) port.PaneControl {
			return failCalls{h, map[string]bool{"screen": true}, errors.New("herdr went away")}
		}
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if ok {
			t.Fatal("must fail")
		}
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "FAIL line last")
		contains(t, out, "(screen unavailable: ", "screen gap")
		contains(t, out, "herdr went away", "why")
		contains(t, out, "foreground: claude", "processes still given")

		x.wrap = func(h port.PaneControl) port.PaneControl {
			return failCalls{h, map[string]bool{"process-info": true}, errors.New("herdr went away")}
		}
		out, _ = x.restart(launch.Request{Agent: "pj-o-01"})
		contains(t, out, "(process info unavailable: ", "process gap")
		contains(t, out, "  draft in the box", "screen still given")
	})
	t.Run("start failure at a startup dialog carries it too", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-o-01", "")
		x.srv.Once("agent.start", herdrtest.Failure{Code: "agent_not_ready", Message: "blocked during startup"})
		out, _ := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		contains(t, out, "agent status:", "evidence")
		eq(t, last(out), "FAIL start failed in w1:p3 (pj-o-01 waits at a startup dialog in pane w1:p3: answer it there)", "FAIL last")
		contains(t, out, "pane screen (last 15 non-empty lines):", "screen")
	})
}

// exitHook runs fn after each /exit typed into a pane.
type exitHook struct {
	port.PaneControl
	fn func()
}

func (e exitHook) Type(ctx context.Context, pane, text string) error {
	err := e.PaneControl.Type(ctx, pane, text)
	if text == "/exit" {
		e.fn()
	}
	return err
}

// procFix sets a pane's process info before each look (the fake's shell pid and foreground group).
type procFix struct {
	port.PaneControl
	fix func(pane string)
}

func (p procFix) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	p.fix(pane)
	return p.PaneControl.ProcessInfo(ctx, pane)
}

// procInfo sets pane's process info before each look (its shell pid and foreground group).
func (x *fx) procInfo(fix func(p *herdrtest.Pane)) {
	x.wrap = func(h port.PaneControl) port.PaneControl {
		return procFix{h, func(pane string) { x.f.Set(pane, fix) }}
	}
}

// quitTyped sets quit when /exit is typed into a pane (before herdr acts on it).
func (x *fx) quitTyped(quit *atomic.Bool) {
	x.srv.Before(func(r herdrtest.Request) {
		if r.Method == "pane.send_input" && strings.Contains(string(r.Params), `"/exit"`) {
			quit.Store(true)
		}
	})
}

// T-217: after /exit the agent (the foreground group's leader) is gone while its MCP servers keep the group in the
// foreground: the exit is the leader's pid gone; the leftovers get SIGTERM, then SIGKILL once; never the shell's group.
func TestStopLeftoverMCP(t *testing.T) {
	t.Parallel()
	mcp := leftoverMCP
	t.Run("leader exits, shell gets the foreground: no kill", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-o-01", "claude", "idle")
		out, ok := x.stop("pj-o-01", "", false)
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
		contains(t, out, "stopped claude in pane w1:p3", "said")
		lacks(t, out, "leftover", "no leftovers")
		eqInt(t, len(x.kills), 0, "no kill")
		_ = ok
	})
	t.Run("leader exits, uvx and python stay: TERM to the group, then the shell", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := mcp(x)
		x.onKill = func(pgid int, sig syscall.Signal) {
			if sig == syscall.SIGTERM {
				p.Foreground = 1
			}
		}
		out, ok := x.stop("pj-o-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to pgid 2 only")
		contains(t, out, "stopped 2 leftover process(es) of pj-o-01 (uvx, python)\n", "leftover line")
		contains(t, out, "stopped claude in pane w1:p3", "stopped line")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("TERM ignored: KILL once, then the shell", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := mcp(x)
		x.onKill = func(pgid int, sig syscall.Signal) {
			if sig == syscall.SIGKILL {
				p.Foreground = 1
			}
		}
		out, ok := x.stop("pj-o-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		contains(t, out, "stopped 2 leftover process(es) of pj-o-01 (uvx, python)", "leftover line")
	})
	t.Run("TERM and KILL ignored: FAIL with evidence", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		mcp(x)
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		eq(t, last(out), "FAIL pj-o-01 exited, but its leftover process(es) (uvx, python) kept the foreground after SIGTERM and SIGKILL (pane w1:p3)", "result")
		contains(t, out, "agent status: idle\n", "evidence")
		contains(t, out, "in the group: 300 uvx", "evidence: processes")
		lacks(t, out, "stopped 2 leftover", "no success line")
	})
	// T-245: the leftovers exit by themselves while the foreground is still their (now empty) group, before the
	// first check, after TERM or after KILL: a clean pane, not a FAIL; nothing (more) signalled.
	for _, c := range []struct {
		name  string
		after syscall.Signal // 0: empty already at the first check
		kills string
	}{
		{"group empty before the first check: no signal, STOPPED", 0, ""},
		{"group empty after TERM, shell not back yet: no KILL, STOPPED", syscall.SIGTERM, "2 terminated"},
		{"group empty after KILL, shell not back yet: STOPPED", syscall.SIGKILL, "2 terminated,2 killed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			p := mcp(x)
			if c.after == 0 {
				x.onExit = func() { p.Procs, x.gone["2"] = []herdr.Process{}, true }
			}
			x.onKill = func(_ int, sig syscall.Signal) {
				if sig == c.after {
					p.Procs = []herdr.Process{}
				}
			}
			out, ok := x.stop("pj-o-01", "", false)
			if !ok {
				t.Fatal(out)
			}
			eqInt(t, p.Foreground, 0, "precondition: the foreground stays group 2")
			contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
			eq(t, strings.Join(x.kills, ","), c.kills, "signals")
			if c.after == 0 {
				lacks(t, out, "leftover", "nothing stopped")
			} else {
				contains(t, out, "stopped 2 leftover process(es) of pj-o-01 (uvx, python)\n", "leftover line")
			}
			eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		})
	}
	t.Run("restart: leftovers stopped, then the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait // the exit wait is given up on (the leftovers keep the foreground)
		x.up = bound       // the start's outcome, not a give-up: 60ms failed under the gate's load (T-721)
		x.sid("pj-o-01", "sid-1")
		p := mcp(x)
		x.onKill = func(int, syscall.Signal) { p.Foreground = 1 }
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to the group")
		contains(t, out, "stopped 2 leftover process(es) of pj-o-01 (uvx, python)", "leftover line")
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "result")
	})
	t.Run("leader still alive after the wait: FAIL as today, no kill", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		mcp(x)
		x.onExit = nil // the leader never exits: listed and alive throughout
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		eqInt(t, len(x.kills), 0, "no kill")
	})
	t.Run("pgid == ShellPID or ≤ 1: never killed", func(t *testing.T) {
		t.Parallel()
		// After /exit the pane's shell is pid 2, the recorded group (the foreground moved to 9).
		x := setup(t)
		x.wait = shortWait
		mcp(x)
		quit := &atomic.Bool{}
		x.quitTyped(quit)
		x.procInfo(func(p *herdrtest.Pane) {
			if quit.Load() {
				p.Shell, p.Foreground = 2, 9
			}
		})
		out, ok := x.stop("pj-o-01", "", false)
		if ok || !quit.Load() {
			t.Fatalf("must fail after /exit:\n%s", out)
		}
		eq(t, last(out), "FAIL pj-o-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the pane's shell is pid 2, not 1)", "result")
		eqInt(t, len(x.kills), 0, "shell's group not killed")

		// The foreground group is 1 (shell 5): not recorded, the wait is today's.
		y := setup(t)
		y.wait = shortWait
		mcp(y)
		y.gone["1"] = true
		q := &atomic.Bool{}
		y.quitTyped(q)
		y.procInfo(func(p *herdrtest.Pane) { p.Shell, p.Foreground = 5, 1 })
		out, ok = y.stop("pj-o-01", "", false)
		if ok || !q.Load() {
			t.Fatalf("must fail after /exit:\n%s", out)
		}
		eq(t, last(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		eqInt(t, len(y.kills), 0, "group 1 not killed")
	})
}

// leftoverMCP: an idle Claude in w1:p3 leading group 2 (listed, alive) that exits on /exit, its pid gone and no
// longer listed, but leaves uvx and python in its group (T-217; one consistent process model, RV-24).
func leftoverMCP(x *fx) *herdrtest.Pane {
	p := x.agent("w1:p3", "pj-o-01", "claude", "idle")
	p.OnQuit = "stay"
	p.Procs = []herdr.Process{{PID: 2, Argv0: "claude"}, {PID: 300, Argv0: "/usr/local/bin/uvx"}, {PID: 301, Name: "python"}}
	x.onExit = func() {
		p.Procs = p.Procs[1:]
		x.gone["2"] = true
	}
	return p
}

// RV-24 #1 (T-227): no signal to a group not proven to be the exited agent's leftovers. Process info that cannot be
// read proves nothing; the leader still listed, a changed foreground or shell refuse the signal, before TERM and
// before KILL.
func TestStopUnprovenLeftovers(t *testing.T) {
	t.Parallel()
	// unreadable makes herdr's process info fail from then on.
	unreadable := func(x *fx) {
		x.srv.Fail("pane.process_info", "internal", "denied")
	}
	// RV-24's probe: /exit does nothing (claude and uvx stay in group 2), and process info is unreadable after it.
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("process info unreadable during the exit wait, force %v: no kill, FAIL with evidence", force), func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.wait = shortWait
			p := x.agent("w1:p3", "pj-o-01", "claude", "idle")
			p.OnQuit = "stay"
			p.Procs = []herdr.Process{{PID: 2, Argv0: "claude"}, {PID: 300, Argv0: "uvx"}}
			x.onExit = func() { unreadable(x) }
			out, ok := x.stop("pj-o-01", "", force)
			if ok {
				t.Fatal("must fail:\n" + out)
			}
			contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
			want := "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)"
			if force {
				want = "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; not terminated: process info unavailable: "
			}
			contains(t, last(out), want, "result")
			eqInt(t, len(x.kills), 0, "no kill")
			contains(t, out, "pane processes:\n  (process info unavailable: ", "evidence: process info error")
		})
	}
	t.Run("restart, process info unreadable after the exit: no kill, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		leftoverMCP(x)
		exit := x.onExit
		x.onExit = func() { exit(); unreadable(x) }
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "/exit sent")
		contains(t, last(out), "FAIL pj-o-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: process info unavailable: ", "result")
		eqInt(t, len(x.kills), 0, "no kill")
		eq(t, x.startCall(), "", "no start")
	})
	t.Run("leader dead but still listed in process info: no kill", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		x.onExit = func() { x.gone["2"] = true } // the pid is gone, process info still lists it
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, last(out), "FAIL pj-o-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the agent (pid 2) is still in the group)", "result")
		eqInt(t, len(x.kills), 0, "no kill")
		contains(t, out, "agent status: idle", "evidence")
	})
	t.Run("foreground moves to another group after TERM: no KILL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := leftoverMCP(x)
		x.onKill = func(_ int, sig syscall.Signal) {
			if sig == syscall.SIGTERM {
				p.Foreground, p.Procs = 9, []herdr.Process{{PID: 9, Argv0: "other-program"}}
			}
		}
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM only")
		eq(t, last(out), "FAIL pj-o-01 exited, but its leftover process(es) (uvx, python) kept the foreground after SIGTERM (pane w1:p3; no SIGKILL: the foreground is group 9, not 2)", "result")
		contains(t, out, "in the group: 9 other-program", "evidence")
	})
	t.Run("process info unreadable after TERM: no KILL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		x.onKill = func(int, syscall.Signal) { unreadable(x) }
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM only")
		contains(t, last(out), "FAIL pj-o-01 exited, but its leftover process(es) (uvx, python) kept the foreground after SIGTERM (pane w1:p3; no SIGKILL: process info unavailable: ", "result")
	})
	// The shell is pid 1 until /exit, then shell (fg stays group 2), and from TERM on afterTerm.
	shellChange := func(x *fx, shell, afterTerm int) {
		var quit, termed atomic.Bool
		x.onKill = func(int, syscall.Signal) { termed.Store(true) }
		x.quitTyped(&quit)
		x.procInfo(func(p *herdrtest.Pane) {
			switch {
			case termed.Load():
				p.Shell = afterTerm
			case quit.Load():
				p.Shell = shell
			}
		})
	}
	t.Run("shell identity changes before TERM: no signal", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		shellChange(x, 7, 7)
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), "FAIL pj-o-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the pane's shell is pid 7, not 1)", "result")
	})
	t.Run("shell identity changes after TERM: no KILL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		shellChange(x, 1, 7)
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM only")
		contains(t, last(out), "(pane w1:p3; no SIGKILL: the pane's shell is pid 7, not 1)", "result")
	})
}

// T-577 (was T-265): the recorded session wins over herdr's live one for the pane, which is only said; herdr's is
// resumed when none is recorded. The record is never rewritten from herdr.
func TestRestartRecordOverLiveSession(t *testing.T) {
	t.Parallel()
	record := func(x *fx) string { return x.recorded("pj-o-01") + "\n" }
	t.Run("record == herdr: resumes it, no warning", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-1")
		x.shell("w1:p3", "pj-o-01", "").Session = "sid-1"
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-1)", "result")
		if !ok || strings.Contains(out, "warning:") {
			t.Errorf("no warning: %q", out)
		}
		contains(t, x.startCall(), "--resume sid-1", "resumed")
	})
	t.Run("record != herdr: resumes the record, warns, record kept", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-rec")
		x.shell("w1:p3", "pj-o-01", "").Session = "sid-live"
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-rec)", "result")
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "warning: herdr reports session sid-live for pane w1:p3, the recorded session is sid-rec: resuming sid-rec", "warned")
		contains(t, out, "resuming sid-rec (from record)", "source")
		contains(t, x.startCall(), "--resume sid-rec", "the record resumed")
		eq(t, record(x), "sid-rec\n", "record kept")
	})
	t.Run("stale record of another project, herdr set: the record is checked, not replaced", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-o-01", "sid-old")
		x.transcript("/Users/someone/other-project", "sid-old")
		x.shell("w1:p3", "pj-o-01", "").Session = "sid-live"
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, last(out), "FAIL recorded session sid-old is not from "+x.lane+" (pass --fresh, or --sid <id>)", "checked")
		if ok || x.startCall() != "" {
			t.Error("nothing started")
		}
		eq(t, record(x), "sid-old\n", "record kept")
	})
	t.Run("no herdr session: record, transcript checked", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-o-01", "sid-x")
		x.transcript("/Users/someone/other-project", "sid-x")
		x.shell("w1:p3", "pj-o-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, last(out), "FAIL recorded session sid-x is not from "+x.lane+" (pass --fresh, or --sid <id>)", "checked")
		if ok || x.startCall() != "" {
			t.Error("nothing started")
		}
		x.transcript(x.lane, "sid-x")
		out, _ = x.restart(launch.Request{Agent: "pj-o-01"})
		contains(t, out, "resuming sid-x (from record)", "source")
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-x)", "record resumed")
		eq(t, record(x), "sid-x\n", "record kept")
	})
	t.Run("no record, herdr set: herdr", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-o-01", "").Session = "sid-h"
		if x.recorded("pj-o-01") != "" {
			t.Fatal("precondition: no record")
		}
		out, ok := x.restart(launch.Request{Agent: "pj-o-01"})
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-h)", "result")
		if !ok || strings.Contains(out, "warning:") {
			t.Errorf("no warning: %q", out)
		}
		contains(t, out, "resuming sid-h (from herdr)", "source")
	})
	t.Run("--sid and --fresh unchanged: no warning, record kept", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-o-01", "sid-old")
		x.shell("w1:p3", "pj-o-01", "").Session = "sid-live"
		out, _ := x.restart(launch.Request{Agent: "pj-o-01", Sid: "sid-y"})
		contains(t, out, "resuming sid-y (from --sid)", "--sid wins")
		eq(t, last(out), "RESTARTED pj-o-01 (pane w1:p3, session sid-y)", "--sid result")
		x.reset()
		x.shell("w1:p3", "pj-o-01", "").Session = "sid-live"
		out2, _ := x.restart(launch.Request{Agent: "pj-o-01", Fresh: true})
		eq(t, last(out2), "RESTARTED pj-o-01 (pane w1:p3, session new)", "--fresh result")
		if strings.Contains(out+out2, "warning: recorded session") {
			t.Errorf("no warning: %q", out+out2)
		}
		eq(t, record(x), "sid-old\n", "record kept")
	})
}

// popup is opencode's slash-command popup: it takes the Enter typed with /exit (nothing happens, the text stays in
// the input), and a separate Enter then runs the quit.
type popup struct {
	port.PaneControl
	enters *int
}

func (p popup) Type(ctx context.Context, pane, text string) error {
	if text == "/exit" {
		return nil
	}
	return p.PaneControl.Type(ctx, pane, text)
}

func (p popup) SendKeys(ctx context.Context, pane string, keys ...string) error {
	for _, k := range keys {
		if k == "enter" {
			*p.enters++
			return p.PaneControl.Type(ctx, pane, "/exit")
		}
	}
	return p.PaneControl.SendKeys(ctx, pane, keys...)
}

// slowAfterQuit holds the pane's process info past a short wait once the quit is typed: the exit poll's first look
// eats the whole wait, so the second Enter can only come from the give-up.
type slowAfterQuit struct {
	port.PaneControl
	quit *bool
}

func (s slowAfterQuit) Type(ctx context.Context, pane, text string) error {
	if text == "/exit" {
		*s.quit = true
	}
	return s.PaneControl.Type(ctx, pane, text)
}

func (s slowAfterQuit) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	if *s.quit {
		time.Sleep(2 * shortWait)
	}
	return s.PaneControl.ProcessInfo(ctx, pane)
}

// T-292: stop for every agent kind. The leader to prove is the program of the kind herdr sees running (RV-33 #2: not
// the lane type's kind); the quit is the kind's own (opencode: /exit, then a second Enter for the popup); a kind with
// no quit (no bridge: Cursor's rows went with T-495) goes straight to the proven SIGTERM, without the wait.
func TestStopEveryKind(t *testing.T) {
	t.Parallel()
	// kind: lane O-01 of a lane type of that kind; its agent runs as leader of group 2 and ignores the quit.
	kind := func(t *testing.T, kind string, leader herdr.Process) (*fx, *herdrtest.Pane) {
		x := setup(t, testkit.Set("lane_types", `{"opus":{"kind":"claude"},"luna":{"kind":"codex"},"oc":{"kind":"opencode"},"cu":{"kind":"cursor"},"gem":{"kind":"gemini"}}`),
			testkit.Set("lanes", `{"O-01":"`+map[string]string{"opencode": "oc", "cursor": "cu", "gemini": "gem", "claude": "opus"}[kind]+`"}`))
		p := x.agent("w1:p3", "pj-o-01", kind, "idle")
		p.OnQuit = "stay"
		p.Procs = []herdr.Process{leader}
		x.onKill = func(_ int, sig syscall.Signal) {
			if sig == syscall.SIGTERM {
				p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil
				x.gone["2"] = true
			}
		}
		return x, p
	}
	t.Run("opencode, quit ignored, --force: /exit and a second Enter, SIGTERM, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "opencode", herdr.Process{PID: 2, Argv0: "opencode"})
		x.wait = shortWait
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "opencode's quit typed")
		eqInt(t, len(x.calls("pane.send_keys keys=[enter] pane_id=w1:p3")), 1, "one second Enter")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-o-01 (opencode pid 2, SIGTERM): it did not exit in 60ms\n", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("opencode, the popup takes the first Enter, no --force: the second Enter exits it, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "opencode", herdr.Process{PID: 2, Argv0: "opencode"})
		p.OnQuit = ""
		enters := 0
		x.wrap = func(h port.PaneControl) port.PaneControl { return popup{h, &enters} }
		out, ok := x.stop("pj-o-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, enters, 1, "the second Enter sent")
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	// T-712: Claude Code's slash-command menu (many plugin commands) took the Enter in the owner's run: the second Enter submits.
	t.Run("claude, the slash-command menu takes the first Enter, no --force: the second Enter exits it, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", herdr.Process{PID: 2, Argv0: "claude"})
		p.OnQuit = ""
		enters := 0
		x.wrap = func(h port.PaneControl) port.PaneControl { return popup{h, &enters} }
		out, ok := x.stop("pj-o-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, enters, 1, "the second Enter sent")
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("opencode, a wait shorter than two polls, --force: the second Enter is sent before the give-up", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "opencode", herdr.Process{PID: 2, Argv0: "opencode"})
		x.wait = shortWait
		quit := false
		x.wrap = func(h port.PaneControl) port.PaneControl { return slowAfterQuit{h, &quit} }
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "opencode's quit typed")
		eqInt(t, len(x.calls("pane.send_keys keys=[enter] pane_id=w1:p3")), 1, "the second Enter, from the give-up")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("leader opencode, herdr kind opencode in a claude lane, --force: proven by herdr's kind, SIGTERM, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", herdr.Process{PID: 2, Argv0: "opencode"})
		x.wait = shortWait
		p.Kind = "opencode" // herdr sees opencode in the claude lane's pane
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "opencode's quit typed")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-o-01 (opencode pid 2, SIGTERM)", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	// RV-33 #2: the proof reads the kind herdr sees running, as the quit does, not the configured one.
	t.Run("retyped lane: configured opus, herdr kind codex, leader codex, quit ignored, --force: SIGTERM, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", herdr.Process{PID: 2, Argv0: "codex"})
		x.wait = shortWait
		p.Kind = "codex" // the old codex worker still runs in the lane now typed opus
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/quit", "codex's quit typed")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-o-01 (codex pid 2, SIGTERM): it did not exit in 60ms\n", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("retyped lane, leader matching no known kind, --force: refused, no signal", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", herdr.Process{PID: 2, Argv0: "vim"})
		x.wait = shortWait
		p.Kind = "codex"
		out, ok := x.stop("pj-o-01", "", true)
		if ok {
			t.Fatal("must fail")
		}
		eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/quit", "codex's quit typed")
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), `FAIL pj-o-01 did not exit in 60ms (pane w1:p3; not terminated: the group's leader (pid 2) is "vim", not codex)`, "result")
	})
	t.Run("a kind with no quit in a pane configured as claude: proven by herdr's kind, SIGTERM at once, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", herdr.Process{PID: 2, Argv0: "gemini"})
		p.Kind = "gemini"
		x.onExit = func() { t.Error("no quit typed") }
		out, ok := x.stop("pj-o-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, len(x.calls("pane.send_input")), 0, "nothing typed")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-o-01 (gemini pid 2, SIGTERM): it has no clean quit\n", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	// RV-33 #3: a popup kind that gives up leaves no quit in its input for the next typed message.
	for _, c := range []struct{ kind, argv0, clear string }{
		{"opencode", "opencode", "pane.send_keys keys=[esc] pane_id=w1:p3\npane.send_keys keys=[ctrl+u] pane_id=w1:p3"},
	} {
		t.Run(c.kind+", the popup stays, no --force: the input cleared, FAIL says so, no signal", func(t *testing.T) {
			t.Parallel()
			x, _ := kind(t, c.kind, herdr.Process{PID: 2, Argv0: c.argv0})
			x.wait = shortWait
			out, ok := x.stop("pj-o-01", "", false)
			if ok {
				t.Fatal("must fail")
			}
			keys := x.calls("pane.send_keys")
			eqInt(t, len(keys), 5, "the clear before the quit, the second Enter, the clear again: "+strings.Join(keys, ", "))
			eq(t, strings.Join(keys[:2], "\n"), c.clear, "the clear keys before the quit")
			eq(t, keys[2], "pane.send_keys keys=[enter] pane_id=w1:p3", "the second Enter next")
			eq(t, strings.Join(keys[3:], "\n"), c.clear, "the clear keys after the wait")
			eqInt(t, len(x.kills), 0, "no signal")
			eq(t, failLine(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		})
	}
	// T-712: text a person typed into an idle agent's input is cleared before the quit, or the quit joins it.
	for _, c := range []struct{ kind, argv0, quit, clear string }{
		{"claude", "claude", "/exit", "ctrl+u"},
		{"codex", "codex", "/quit", "ctrl+u"},
		{"opencode", "opencode", "/exit", "esc"},
	} {
		t.Run(c.kind+", idle with text in the input: cleared before the quit, which then exits", func(t *testing.T) {
			t.Parallel()
			x, p := kind(t, c.kind, herdr.Process{PID: 2, Argv0: c.argv0})
			p.OnQuit, p.Input = "", "t"
			out, ok := x.stop("pj-o-01", "", false)
			if !ok {
				t.Fatal(out)
			}
			clr, quit := x.index("pane.send_keys keys=["+c.clear+"] pane_id=w1:p3"), x.index("pane.send_input keys=[Enter] pane_id=w1:p3 text="+c.quit)
			if clr < 0 || quit < 0 || clr > quit {
				t.Errorf("the clear key, then the quit: %q", x.srv.Calls())
			}
			if p.Input != "" || p.Kind != "" {
				t.Errorf("input %q, kind %q: the quit must have run on an empty input", p.Input, p.Kind)
			}
			eqInt(t, len(x.calls("pane.send_input")), 1, "the quit typed once")
			eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		})
	}
	t.Run("claude, working, --force: Escape then ctrl+c only, no ctrl+u (unchanged)", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", herdr.Process{PID: 2, Argv0: "claude"})
		p.OnQuit, p.Status = "", "working"
		out, ok := x.stop("pj-o-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("pane.send_keys"), "\n"),
			"pane.send_keys keys=[esc] pane_id=w1:p3\npane.send_keys keys=[ctrl+c] pane_id=w1:p3", "the keys of a working agent")
	})
	t.Run("a kind with no quit: nothing typed, SIGTERM at once, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "gemini", herdr.Process{PID: 2, Argv0: "gemini"})
		x.onExit = func() { t.Error("no quit typed") }
		out, ok := x.stop("pj-o-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, len(x.calls("pane.send_input")), 0, "nothing typed")
		eqInt(t, len(x.calls("pane.send_keys")), 0, "no clear key either: a kind without a clear sequence is unchanged")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-o-01 (gemini pid 2, SIGTERM): it has no clean quit\n", "said")
		eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
	})
	t.Run("a kind with no quit, leader not its program: refused, no signal", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "gemini", herdr.Process{PID: 2, Argv0: "node"})
		out, ok := x.stop("pj-o-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), `FAIL pj-o-01 has no clean quit (pane w1:p3; not terminated: the group's leader (pid 2) is "node", not gemini)`, "result")
	})
}

// T-500: stop per kind, on fake process tables (the agent leads group 2 in a lane of its kind): a clean quit; a quit
// ignored, --force: the terminate ladder on the kind's leader; the wrong group (another program leads it, or another
// group has the foreground after the wait) is never signalled.
func TestStopPerKind(t *testing.T) {
	t.Parallel()
	kinds := []struct{ kind, typ, quit string }{{"claude", "opus", "/exit"}, {"codex", "luna", "/quit"}, {"opencode", "oc", "/exit"}}
	for _, k := range kinds {
		lane := func(t *testing.T, leader string, onQuit string) (*fx, *herdrtest.Pane) {
			x := setup(t, testkit.Set("lane_types", `{"opus":{"kind":"claude"},"luna":{"kind":"codex"},"oc":{"kind":"opencode"}}`),
				testkit.Set("lanes", `{"O-01":"`+k.typ+`"}`))
			p := x.agent("w1:p3", "pj-o-01", k.kind, "idle")
			p.OnQuit = onQuit
			p.Procs = []herdr.Process{{PID: 2, Argv0: leader}}
			x.onKill = func(_ int, sig syscall.Signal) {
				if sig == syscall.SIGTERM {
					p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil
					x.gone["2"] = true
				}
			}
			return x, p
		}
		t.Run(k.kind+": clean quit, no signal, STOPPED", func(t *testing.T) {
			t.Parallel()
			x, _ := lane(t, k.kind, "")
			out, ok := x.stop("pj-o-01", "", false)
			if !ok {
				t.Fatal(out)
			}
			eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text="+k.quit, "the kind's quit typed")
			eqInt(t, len(x.kills), 0, "no signal")
			eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		})
		t.Run(k.kind+": quit ignored, --force: SIGTERM to its leader's group, STOPPED", func(t *testing.T) {
			t.Parallel()
			x, _ := lane(t, k.kind, "stay")
			x.wait = shortWait
			out, ok := x.stop("pj-o-01", "", true)
			if !ok {
				t.Fatal(out)
			}
			eq(t, strings.Join(x.calls("pane.send_input"), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text="+k.quit, "the kind's quit typed")
			eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
			contains(t, out, "terminated pj-o-01 ("+k.kind+" pid 2, SIGTERM): it did not exit in 60ms\n", "said")
			eq(t, last(out), "STOPPED pj-o-01 (pane w1:p3)", "result")
		})
		t.Run(k.kind+": quit ignored, no --force: FAIL, no signal", func(t *testing.T) {
			t.Parallel()
			x, _ := lane(t, k.kind, "stay")
			x.wait = shortWait
			out, ok := x.stop("pj-o-01", "", false)
			if ok {
				t.Fatal("must fail")
			}
			eqInt(t, len(x.kills), 0, "no signal")
			contains(t, failLine(out), "did not exit in 60ms (pane w1:p3", "result")
		})
		t.Run(k.kind+": --force, another program leads the group: FAIL, no signal", func(t *testing.T) {
			t.Parallel()
			x, _ := lane(t, "zsh", "stay")
			x.wait = shortWait
			out, ok := x.stop("pj-o-01", "", true)
			if ok {
				t.Fatal("must fail")
			}
			eqInt(t, len(x.kills), 0, "no signal")
			eq(t, failLine(out), `FAIL pj-o-01 did not exit in 60ms (pane w1:p3; not terminated: the group's leader (pid 2) is "zsh", not `+k.kind+`)`, "result")
		})
		t.Run(k.kind+": --force, another group in the foreground after the wait: FAIL, no signal", func(t *testing.T) {
			t.Parallel()
			x, _ := lane(t, k.kind, "stay")
			x.wait = shortWait
			x.procInfo(func(p *herdrtest.Pane) {
				if len(x.calls("pane.send_input")) > 0 {
					p.Foreground = 5 // the user's next job, after the quit was typed
				}
			})
			out, ok := x.stop("pj-o-01", "", true)
			if ok {
				t.Fatal("must fail")
			}
			eqInt(t, len(x.calls("pane.send_input")), 1, "precondition: the quit was typed")
			eqInt(t, len(x.kills), 0, "no signal")
			eq(t, failLine(out), "FAIL pj-o-01 did not exit in 60ms (pane w1:p3; not terminated: the foreground is group 5, not 2)", "result")
		})
	}
}

// T-312: the agent's exit is its leader gone: not alive (kill 0), or no longer listed in its group by process info.
// Either one alone counts; what is left in the group is the leftovers' ladder's.
func TestStopGone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		dead, drop bool // after /exit: pid 2 not alive; pid 2 no longer listed
		kills      string
		last       string
	}{
		{name: "dead pid, still listed: exited; the listed agent refuses the leftovers' signal", dead: true,
			last: "FAIL pj-o-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the agent (pid 2) is still in the group)"},
		{name: "pid no longer listed, still alive: exited; leftovers stopped", drop: true, kills: "2 terminated",
			last: "STOPPED pj-o-01 (pane w1:p3)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			p := x.agent("w1:p3", "pj-o-01", "claude", "idle")
			p.OnQuit = "stay"
			p.Procs = []herdr.Process{{PID: 2, Argv0: "claude"}, {PID: 300, Argv0: "uvx"}}
			x.onExit = func() {
				x.gone["2"] = c.dead
				if c.drop {
					p.Procs = p.Procs[1:]
				}
			}
			x.onKill = func(int, syscall.Signal) { p.Foreground, p.Procs = 1, nil }
			out, _ := x.stop("pj-o-01", "", false)
			contains(t, strings.Join(x.srv.Calls(), "\n"), "pane.send_input keys=[Enter] pane_id=w1:p3 text=/exit", "precondition: /exit sent")
			lacks(t, out, "did not exit", "the exit counted")
			eq(t, strings.Join(x.kills, ","), c.kills, "signals")
			eq(t, last(out), c.last, "result")
		})
	}
}

// T-418, P5-3: Launch reports the pane, kind and session herdr showed for the agent it started: serve's registry row
// of a start (app/registry.go).
func TestLaunchReportsIdentity(t *testing.T) {
	t.Parallel()
	x := setup(t)
	pane := x.shell("w1:p3", "pj-o-01", "")
	pane.Session = "original-task-session"
	s, err := x.launcher(&bytes.Buffer{}).Launch(context.Background(), launch.Request{Agent: "pj-o-01", Fresh: true})
	if err != nil {
		t.Fatalf("the start: %v", err)
	}
	eq(t, s.Line, "RESTARTED pj-o-01 (pane w1:p3, session new)", "the start's line")
	eq(t, s.Pane+" "+s.Kind+" "+s.Session, "w1:p3 claude original-task-session", "the identity the start reports")
}

// T-474 (RV-50 #5): a role with no configured type still gets its kind's launch defaults: Claude's memory off for
// every role but the lead.
func TestUntypedRoleLaunchDefaults(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ role, agent string }{{"integrator", "pj-int"}, {"reviewer", "pj-rev"}, {"lead", "pj-lead"}} {
		t.Run(c.role, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("roles."+c.role, `{}`))
			x.shell("w1:p1", c.agent, x.p.Root)
			if out, ok := x.restart(launch.Request{Agent: c.agent, Fresh: true}); !ok {
				t.Fatal(out)
			}
			if got := strings.Contains(x.startCall(), `"autoMemoryEnabled":false`); got != (c.role != "lead") {
				t.Errorf("memory off = %v in %s", got, x.startCall())
			}
		})
	}
}

// T-500: the restart lock is <state>/restart-<lane>.lock (no lane: the agent's); Held probes it without taking it.
func TestRestartLockHeld(t *testing.T) {
	root := t.TempDir()
	lock := launch.RestartLock(root, "O-01", "pj-o-01")
	if want := filepath.Join(project.StateDir(root), "restart-O-01.lock"); lock.Path != want {
		t.Errorf("path = %q, want %q", lock.Path, want)
	}
	if got := launch.RestartLock(root, "", "pj-int").Path; got != filepath.Join(project.StateDir(root), "restart-pj-int.lock") {
		t.Errorf("no lane: path = %q, want the agent's", got)
	}
	probe := launch.RestartLock(root, "O-01", "")
	if probe.Held() {
		t.Fatal("held before anyone took it")
	}
	if _, err := os.Stat(lock.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the probe created the file: %v", err)
	}
	if ok, err := lock.Try(); !ok || err != nil {
		t.Fatalf("Try = %v, %v", ok, err)
	}
	if !lock.Held() || !probe.Held() {
		t.Errorf("Held = %v (holder) / %v (probe), want true / true", lock.Held(), probe.Held())
	}
	if launch.RestartLock(root, "O-02", "").Held() {
		t.Error("another lane's lock is held")
	}
	lock.Unlock()
	if probe.Held() {
		t.Error("held after Unlock")
	}
}
