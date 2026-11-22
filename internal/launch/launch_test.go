package launch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

// settings are the settings flags of a Claude agent that is no lead: the project's sources and the agent's own
// settings file in the state dir, its path written <file> (fx.named).
const settings = `--setting-sources project,local --settings <file>`

// settingsFileRE is a start's --settings <the agent's settings file in the state dir>.
var settingsFileRE = regexp.MustCompile(`--settings \S+/\.obor/state/settings/[^/ ]+\.json`)

// laneMCP is a lane agent's own args before its settings: obor's MCP server, then the strict flag that hides
// every server the command line does not name.
const laneMCP = strictMCP + " --strict-mcp-config "

// fx is a project with lane L-01, the fixture's models (opus: claude --permission-mode auto --model opus; luna: codex
// -s workspace-write -a never -m gpt-6-luna), roles integrator and lead (opus) and reviewer (luna), an in-memory
// terminal with no panes (term), every pid alive and a ps that lists no process.
type fx struct {
	t          *testing.T
	p          *testkit.Project
	f          *term // the terminal: its panes and what the launch called
	lane       string
	claude     string // CLAUDE_CONFIG_DIR
	wrap       func(control) control
	gone       map[string]bool                      // pids that are not alive (every other pid is)
	onExit     func()                               // what /exit does besides the terminal's OnQuit (nil: nothing)
	kills      []string                             // kill hook calls: "<pgid> <signal>"
	onKill     func(pgid int, sig syscall.Signal)   // what a kill does (nil: nothing)
	ps         func(args ...string) ([]byte, error) // the launcher's PS (nil: the real ps)
	wait       time.Duration                        // the launcher's exit wait and idle wait (0: bound, the load-proof default)
	up         time.Duration                        // the launcher's wait for a started agent to come up (0: wait)
	poll       time.Duration                        // the launcher's pane poll interval (0: 1 ms)
	shellBound time.Duration                        // the launcher's bound on a stopped pane's shell (0: the default)
	// A stop test may ask for less: waitEnv is OBOR_RESTART_WAIT for the launcher's env ("" none), idle overrides the
	// exit wait for the idle wait alone, waitZero leaves the launcher's Wait unset (the env's or the default's wait),
	// bound is the launcher's stop bound (0: port.StopBound).
	waitEnv  string
	goflags  string // GOFLAGS in the launcher's env ("" none: a lane's config value wins, no merge)
	idle     time.Duration
	waitZero bool
	bound    time.Duration
	// termGrace and killGrace are the launcher's ladder waits (0: ladderWait: a test asserts the signals, not the
	// production 3 s and 2 s).
	termGrace, killGrace time.Duration
	// sessions: the recorded sessions the launcher reads (nil: serve's store); reported: the sessions it reported.
	sessions map[string]store.Session
	reported []store.Session
	// serve: each report the launcher makes lands in serve's store too, as serve records it (Started now when the
	// report names none; the store's fold keeps one record per session): a later launcher reads it.
	serve bool
	// started: the agents whose confirmed start the launcher reported (Started); startErr: what that report returns.
	started  []string
	startErr error
	// rollout: the launcher's rollout bound (0: 20 ms); background: its rollout search runs in a goroutine, each
	// report also sent on reports (nil: not sent); capture keeps that search in searches for the test to run with
	// runSearches (after the pane changed).
	rollout    time.Duration
	background bool
	capture    bool
	searches   []func()
	reports    chan store.Session
	reportErr  error           // what the launcher's Report returns (nil: recorded)
	logged     strings.Builder // what a background search said (the launcher's Log)
	// open: the files each pid holds open (the launcher's Open; hold sets it). The terminal's agent is pid 2 unless
	// a test gives its pane another.
	open map[int][]string
	// files replaces the launcher's Open when set: a test's own view of the files (it may act while the search reads
	// them).
	files func(pids []int) []string
	// install is the launcher's install check machine (zero: no kind's start is refused).
	install agents.InstallHost
	// where is serve's where answer the launcher's Locate reads (nil: serve answers that no pane proves the agent and
	// that the panes asked about run no agent it knows).
	where func(req wire.WhereReq) (wire.WhereReply, error)
	// workspace is the launcher's workspace preparer (nil: its own, project.AddWorktree and project.LinkInto).
	workspace func(ctx context.Context, dir string) error
	mu        sync.Mutex // guards reported, logged, open, searches
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

// startReports is the agents whose confirmed start the launcher reported so far.
func (x *fx) startReports() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.started...)
}

// runSearches runs the background searches x captured (capture), here in the test's goroutine, and returns how many
// ran (a test that changes the pane between the start and the search).
func (x *fx) runSearches() int {
	x.mu.Lock()
	searches := x.searches
	x.searches = nil
	x.mu.Unlock()
	for _, search := range searches {
		search()
	}
	return len(searches)
}

// bound is the launcher's wait where the test is about the outcome, not the timing: far above anything a loaded
// machine delays a poll by, since a wait that is met returns at once. shortWait is for a test of the give-up itself
// (the agent never exits, the foreground never returns): it sets x.wait, so that waiting the bound out is quick, and the
// output the test asserts names it.
const (
	bound     = 10 * time.Second
	shortWait = 60 * time.Millisecond
	// ladderWait is the ladder's SIGTERM and SIGKILL grace in a test: a test asserts the signals and their order, so
	// the waits only have to let the fake's state change land (the production 3 s and 2 s are in Launcher).
	ladderWait = 5 * time.Millisecond
)

// setup is a plain project laid out as files (testkit.NewPlain): the launch code under test reads the repository
// (project.Find) but runs no git, so a git fixture would only build one. The role worktrees are there; the test of
// the launcher's own worktree add is an integration test (setupGit).
func setup(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	p := testkit.NewPlain(t)
	return newFx(t, p, p.PlainLane("L-01", ""), edits...)
}

// newFx is the launcher's fixture on the project p, whose lane (a directory, "": none) the tests start the worker in.
func newFx(t *testing.T, p *testkit.Project, lane string, edits ...func(map[string]any)) *fx {
	t.Helper()
	x := &fx{t: t, p: p, f: newTerm(), lane: lane, claude: filepath.Join(p.Base, "claude"),
		gone: map[string]bool{}, ps: func(...string) ([]byte, error) { return nil, nil }}
	p.Config(append([]func(map[string]any){
		testkit.Set("agents.roles", `{"integrator":"opus","lead":"opus","reviewer":"luna"}`),
	}, edits...)...)
	return x
}

func (x *fx) launcher(out io.Writer) *launch.Launcher {
	x.t.Helper()
	cfg, err := config.Load(x.p.Root)
	if err != nil {
		x.t.Fatal(err)
	}
	var h control = x.f
	if x.onExit != nil {
		h = exitHook{h, x.onExit}
	}
	if x.wrap != nil {
		h = x.wrap(h)
	}
	wait := x.wait
	if wait == 0 && !x.waitZero {
		wait = bound
	}
	idle := x.idle
	if idle == 0 {
		idle = wait
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
	termGrace, killGrace := x.termGrace, x.killGrace
	if termGrace == 0 {
		termGrace = ladderWait
	}
	if killGrace == 0 {
		killGrace = ladderWait
	}
	var background func(func())
	switch {
	case x.capture:
		background = func(search func()) {
			x.mu.Lock()
			defer x.mu.Unlock()
			x.searches = append(x.searches, search)
		}
	case x.background:
		background = func(search func()) { go search() }
	}
	return &launch.Launcher{Cfg: cfg, Env: x.env(), Kit: "/kit", H: h, Out: out,
		Wait: wait, Idle: idle, Up: up, Poll: poll, Rollout: rollout, Bound: x.bound, Background: background, Log: x,
		TermGrace: termGrace, KillGrace: killGrace, Shell: x.shellBound,
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
			if x.serve && x.reportErr == nil {
				if s.Started == 0 {
					s.Started = time.Now().Unix()
				}
				recordSessions(x.t, x.p.Root, s)
			}
			if x.reports != nil {
				x.reports <- s
			}
			return x.reportErr
		},
		Started: func(_ context.Context, agent string) error {
			x.mu.Lock()
			x.started = append(x.started, agent)
			x.mu.Unlock()
			return x.startErr
		},
		Install:   func() agents.InstallHost { return x.install },
		Workspace: x.workspace,
		Where: func(_ context.Context, req wire.WhereReq) (wire.WhereReply, error) {
			if x.where != nil {
				return x.where(req)
			}
			return wire.WhereReply{State: wire.WhereNone, Why: "no proof"}, nil
		}}
}

func (x *fx) env() project.Env {
	kv := []string{"CLAUDE_CONFIG_DIR=" + x.claude}
	if x.waitEnv != "" {
		kv = append(kv, "OBOR_RESTART_WAIT="+x.waitEnv)
	}
	if x.goflags != "" {
		kv = append(kv, "GOFLAGS="+x.goflags)
	}
	return x.p.Env(kv...)
}

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
	var refused port.Refused // Locate's refusal: the CLI prints it as a FAIL line too
	if err != nil && !errors.As(err, &fail) && !errors.As(err, &refused) {
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

// sid records agent's session in serve's store with the transcript its SessionStart hook reported, as a confirmed
// session has it (the path is the session's own, no cwd slug).
func (x *fx) sid(agent, sid string) {
	x.t.Helper()
	x.recordAt(agent, sid, x.transcript(sid))
}

// record puts agent's session id in serve's store with no transcript: a start obor named, before its SessionStart
// hook confirmed it.
func (x *fx) record(agent, sid string) {
	x.t.Helper()
	recordSessions(x.t, x.p.Root, store.Session{Agent: agent, Kind: "claude", ID: sid, Started: time.Now().Unix()})
}

// recordAt is record with transcript as the path the session reported (the file is the test's to write: one that is
// gone is a session never prompted).
func (x *fx) recordAt(agent, sid, transcript string) {
	x.t.Helper()
	recordSessions(x.t, x.p.Root, store.Session{Agent: agent, Kind: "claude", ID: sid, Transcript: transcript,
		Started: time.Now().Unix()})
}

// recorded is agent's current session id in serve's store ("": none).
func (x *fx) recorded(agent string) string {
	s, _ := store.CurrentAt(x.p.Root, agent)
	return s.ID
}

// transcript writes Claude's transcript of sid and returns its path, in the dir Claude picks for a project (the hook
// reports just that path).
func (x *fx) transcript(sid string) string {
	x.t.Helper()
	path := filepath.Join(x.claude, "projects", "-work-lane", sid+".jsonl")
	x.p.Write(path, "")
	return path
}

// shell is a pane at its shell (no agent), labelled label.
func (x *fx) shell(pane, label, cwd string) *mpane { return x.f.Shell(pane, label, cwd) }

// agent is a pane labelled and named name with an agent of kind in status, which leads the pane's foreground group 2.
func (x *fx) agent(pane, name, kind, status string) *mpane {
	return x.f.Agent(pane, name, status, "", func(a *mpane) {
		a.Label, a.Kind = name, kind
		a.Procs = []port.Process{{PID: 2, Argv0: kind}}
	})
}

// calls is the requests whose call line has every part of query, e.g. "Write pane=w1:p3 submit=true".
func (x *fx) calls(query string) []string {
	var out []string
	for _, c := range x.f.Calls() {
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

func (x *fx) startCall() string { return strings.Join(x.calls("Start"), "\n") }

// namedSession is the session id the launcher recorded before its start for a kind whose sessions obor names (its
// report carries the start's time, chosenSession; a resume's report names none: serve stamps it);
// "" when none was reported.
func (x *fx) namedSession() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	for i := len(x.reported) - 1; i >= 0; i-- {
		if x.reported[i].Kind == "claude" && x.reported[i].Transcript == "" && x.reported[i].Started != 0 {
			return x.reported[i].ID
		}
	}
	return ""
}

// named is s with the session id obor chose hidden as the tables write it, "<new>": the id is random (a uuid; the
// start that carries it is TestStartNamesItsSession's), and an agent's settings file as "<file>" (its path and
// content are TestStartWritesTheSettingsFile's).
func (x *fx) named(s string) string {
	if id := x.namedSession(); id != "" {
		s = strings.ReplaceAll(s, id, "<new>")
	}
	return settingsFileRE.ReplaceAllString(s, "--settings <file>")
}

// settingsFile is the settings file obor wrote for agent's start in the project's state dir, as one line of JSON with
// sorted keys; "": none is there.
func (x *fx) settingsFile(agent string) string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(project.StateDir(x.p.Root), claude.StateSettings(agent)))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	var v any
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	if err != nil {
		x.t.Fatalf("the settings file of %s: %v", agent, err)
	}
	return config.Compact(v)
}

// A Claude agent that is no lead starts on the project's settings and its own settings file,
// <state>/settings/<agent>.json, written at each start (the last start's replaced): the user's settings with the
// user's plugins off, the project's override file (.obor/claude/settings_override.json, read at the project's root)
// laid over them and obor's own keys. A lead starts on the user's settings as they are: no --setting-sources, no
// --settings and no file. An override file that does not parse refuses the start, naming the file.
func TestStartWritesTheSettingsFile(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.p.Write(".obor/claude/settings_override.json", `{"env": {"KIND": "k"}, "mcpServers": {"x-srv": null}}`)
	// the user's settings: a variable, a plugin and two MCP servers (the override deletes x-srv)
	x.p.Write(filepath.Join(x.claude, "settings.json"), `{"env":{"USER":"u"},"enabledPlugins":{"a@m":true},`+
		`"mcpServers":{"keep":{"command":"k"},"x-srv":{"command":"x"}}}`)
	x.p.Write(filepath.Join(project.StateDir(x.p.Root), claude.StateSettings("pj-int")), `{"env":{"LAST":"start"}}`)
	start := func(agent, pane, cwd string) string {
		t.Helper()
		x.shell(pane, agent, cwd)
		if out, ok := x.restart(launch.Request{Agent: agent, Fresh: true}); !ok {
			t.Fatal(out)
		}
		calls := x.calls("Start")
		return calls[len(calls)-1]
	}
	const want = `{"autoMemoryEnabled":false,"enabledPlugins":{"a@m":false},"env":{"KIND":"k","USER":"u"},` +
		`"mcpServers":{"keep":{"command":"k"}},"remoteControlAtStartup":false}`
	file := "--setting-sources project,local --settings " + filepath.Join(project.StateDir(x.p.Root), "settings", "pj-int.json")
	if got := start("pj-int", "w1:p5", x.p.Root); !strings.Contains(got, file+" ") {
		t.Errorf("the integrator's start %q lacks %q", got, file)
	}
	if got := x.settingsFile("pj-int"); got != want {
		t.Errorf("the integrator's settings file\n %s\nwant\n %s", got, want)
	}
	// a lane's worker: the same override (the project root's, not its lane's), its own file
	start("pj-l-01", "w1:p3", "")
	if got := x.settingsFile("pj-l-01"); got != want {
		t.Errorf("the worker's settings file\n %s\nwant\n %s", got, want)
	}
	lead := start("pj-lead", "w1:p1", x.p.Root)
	if strings.Contains(lead, "--setting-sources") || strings.Contains(lead, "--settings") || x.settingsFile("pj-lead") != "" {
		t.Errorf("the lead's start %q (settings file %q): want no --setting-sources, no --settings and no file", lead, x.settingsFile("pj-lead"))
	}

	x.p.Write(".obor/claude/settings_override.json", `{"env": `)
	before := len(x.calls("Start"))
	x.shell("w1:p5", "pj-int", x.p.Root)
	out, ok := x.restart(launch.Request{Agent: "pj-int", Fresh: true})
	if ok || !strings.Contains(out, ".obor/claude/settings_override.json: does not parse") || len(x.calls("Start")) != before {
		t.Errorf("an override file that does not parse: restart = %q, %v with %d more starts; want it refused, naming the file",
			out, ok, len(x.calls("Start"))-before)
	}
}

// A start whose settings file cannot be written is refused, naming the file: no agent starts.
func TestStartRefusedWhenTheSettingsFileCannotBeWritten(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.p.Write(filepath.Join(project.StateDir(x.p.Root), "settings"), "a file where the dir goes\n")
	x.shell("w1:p5", "pj-int", x.p.Root)
	out, ok := x.restart(launch.Request{Agent: "pj-int", Fresh: true})
	if ok || !strings.Contains(out, "settings/pj-int.json") {
		t.Errorf("restart = %q, %v: want it refused, naming settings/pj-int.json", out, ok)
	}
	if calls := x.calls("Start"); len(calls) != 0 {
		t.Errorf("Start %q, want no start", calls)
	}
}

// index is the position of the first request matching query (-1: none).
func (x *fx) index(query string) int {
	for i, c := range x.f.Calls() {
		if hasParts(c, query) {
			return i
		}
	}
	return -1
}

// lastIndex is the position of the last request matching query (-1: none): a call's line is no unique key, since a
// request repeated with the same params (a poll's Pane) reads the same both times.
func (x *fx) lastIndex(query string) int {
	calls, last := x.f.Calls(), -1
	for i := len(calls) - 1; i >= 0; i-- {
		if hasParts(calls[i], query) {
			last = i
			break
		}
	}
	return last
}

func (x *fx) reset() { x.f.All = nil }

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

// envMarksRE is the env line's two words as typed: its success word, then its failure word.
var envMarksRE = regexp.MustCompile(` && echo obor-env-"([0-9a-z]+)" \|\| echo obor-env-"([0-9a-z]+)"$`)

// a lane worker's env line carries the generic fact OBOR_LANE_CPUS (lanes.cpus, default 2) and the
// project's lanes.env with {cpus} replaced; a var the launcher's env had is the config's, never merged; a role
// agent's line carries none of it.
func TestRestartLaneEnv(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, goflags, agent, pane string
		edit                       []func(map[string]any)
		want                       string // the export's tail
		none                       bool   // no lane env on the line
	}{
		{name: "lane, no lane_env: the fact alone", agent: "pj-l-01", pane: "w1:p3",
			want: "export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 OBOR_LANE_CPUS=2"},
		{name: "lane, lanes.env", agent: "pj-l-01", pane: "w1:p3",
			edit: []func(map[string]any){testkit.Set("lanes.count", 1), testkit.Set("lanes.env",
				`{"GOMAXPROCS": "{cpus}", "GOFLAGS": "-p={cpus} -trimpath"}`)},
			want: "export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 GOFLAGS='-p=2 -trimpath' GOMAXPROCS=2 OBOR_LANE_CPUS=2"},
		{name: "lane, lanes.cpus 3", agent: "pj-l-01", pane: "w1:p3",
			edit: []func(map[string]any){testkit.Set("lanes.count", 1), testkit.Set("lanes.cpus", 3), testkit.Set("lanes.env", `{"GOMAXPROCS": "{cpus}"}`)},
			want: "export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 GOMAXPROCS=3 OBOR_LANE_CPUS=3"},
		{name: "lane, a GOFLAGS in the launcher's env: the config's wins, no merge", agent: "pj-l-01", pane: "w1:p3", goflags: "-mod=mod",
			edit: []func(map[string]any){testkit.Set("lanes.count", 1), testkit.Set("lanes.env", `{"GOFLAGS": "-p={cpus}"}`)},
			want: "export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 GOFLAGS=-p=2 OBOR_LANE_CPUS=2"},
		{name: "role agent", agent: "pj-int", pane: "w1:p5", goflags: "-mod=mod", none: true, want: "export OBOR_ROLE=integrator OBOR_PANE=w1:p5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, c.edit...)
			x.goflags = c.goflags
			x.sid(c.agent, "sid-1")
			x.shell(c.pane, c.agent, "")
			x.f.Set(c.pane, func(p *mpane) { p.Session = "sid-1" })
			if s := x.startCall(); s != "" {
				t.Fatalf("precondition: nothing started, got %q", s)
			}
			if out, ok := x.restart(launch.Request{Agent: c.agent}); !ok {
				t.Fatalf("restart failed:\n%s", out)
			}
			runs := x.calls("Write")
			if len(runs) != 1 {
				t.Fatalf("one env line typed, got %q", runs)
			}
			line := envMarksRE.ReplaceAllString(runs[0], "")
			if !strings.HasSuffix(line, c.want) {
				t.Errorf("env line %q, want it to end in %q", line, c.want)
			}
			if has := strings.Contains(line, "OBOR_LANE_CPUS"); has == c.none {
				t.Errorf("env line %q: OBOR_LANE_CPUS present %v, want %v", line, has, !c.none)
			}
		})
	}
}

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
		name: "lane agent, dead pane: lane env, settings (no dispatch: no model), --resume <sid>",
		prep: func(x *fx) {
			x.sid("pj-l-01", "sid-1")
			x.shell("w1:p3", "pj-l-01", "")
			x.f.Set("w1:p3", func(p *mpane) { p.Session = "sid-1" })
		},
		req: launch.Request{Agent: "pj-l-01"},
		env: func(x *fx) string {
			return "Write pane=w1:p3 submit=true text=cd " + q(x.lane) + " && unset OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 OBOR_LANE_CPUS=2"
		},
		start: "Start argv=[" + laneMCP + settings + " --resume sid-1] kind=claude label=pj-l-01 pane=w1:p3",
		line:  "RESTARTED pj-l-01 (pane w1:p3, session sid-1)",
	}, {
		// A trimmed Claude start loads no user hooks, herdr's among them, so herdr reports no session for its pane:
		// the pane is found by its label and the start resumes the session obor recorded.
		name: "lane agent, herdr reports no session for its pane: trimmed, --resume <the recorded sid>",
		prep: func(x *fx) {
			x.sid("pj-l-01", "sid-1")
			if p := x.shell("w1:p3", "pj-l-01", ""); p.Session != "" || p.Transcript != "" {
				x.t.Fatalf("precondition: herdr reports no session for the pane, got %q %q", p.Session, p.Transcript)
			}
		},
		req: launch.Request{Agent: "pj-l-01"},
		env: func(x *fx) string {
			return "Write pane=w1:p3 submit=true text=cd " + q(x.lane) + " && unset OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 OBOR_LANE_CPUS=2"
		},
		start: "Start argv=[" + laneMCP + settings + " --resume sid-1] kind=claude label=pj-l-01 pane=w1:p3",
		line:  "RESTARTED pj-l-01 (pane w1:p3, session sid-1)",
	}, {
		// another project's agent may be named like a lane; no lane env, no lane settings
		name: "agent named <lane lower>, lane state agent null: not the lane",
		prep: func(x *fx) {
			x.p.State("L-01", `{"agent":null}`)
			x.sid("l-01", "sid-1")
			x.shell("w1:p3", "l-01", "")
		},
		req: launch.Request{Agent: "l-01"},
		env: func(x *fx) string {
			return "Write pane=w1:p3 submit=true text=unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_PANE=w1:p3"
		},
		start: "Start argv=[--resume sid-1] kind=claude label=l-01 pane=w1:p3",
		line:  "RESTARTED l-01 (pane w1:p3, session sid-1)",
	}, {
		// bash restart.test.sh case_lane_agent_matched_by_pane_cwd
		name: "an agent whose pane cwd is a lane worktree: that lane",
		prep: func(x *fx) { x.sid("odd-name", "sid-1"); x.shell("w1:p3", "odd-name", x.lane) },
		req:  launch.Request{Agent: "odd-name"},
		env: func(x *fx) string {
			return "Write pane=w1:p3 submit=true text=cd " + q(x.lane) + " && unset OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p3 OBOR_LANE_CPUS=2"
		},
		start: "Start argv=[" + laneMCP + settings + " --resume sid-1] kind=claude label=odd-name pane=w1:p3",
		line:  "RESTARTED odd-name (pane w1:p3, session sid-1)",
		out:   "lane L-01 (pane w1:p3 cwd)",
	}, {
		// bash restart.test.sh case_role_agent_gets_role_env_and_type
		name: "role agent: role env, its type, cwd its role worktree (T-853), OBOR_LANE unset",
		prep: func(x *fx) {
			x.sid("pj-int", "sid-i")
			x.shell("w1:p5", "pj-int", "")
			x.f.Set("w1:p5", func(p *mpane) { p.Session = "sid-i" })
		},
		req: launch.Request{Agent: "pj-int"},
		env: func(x *fx) string {
			return "Write pane=w1:p5 submit=true text=cd " + q(x.p.LaneDir("integrator")) + " && unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=integrator OBOR_PANE=w1:p5"
		},
		start: "Start argv=[--permission-mode auto --model opus " + settings + " --resume sid-i] kind=claude label=pj-int pane=w1:p5",
		line:  "RESTARTED pj-int (pane w1:p5, session sid-i)",
	}, {
		// bash restart.test.sh case_lead_agent_is_a_role_agent, case_lead_unsets_obor_lane
		name: "a lead is a role agent; OBOR_LANE unset",
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-lead", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "Write pane=w1:p6 submit=true text=cd " + q(x.p.Root) + " && unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=lead OBOR_PANE=w1:p6"
		},
		start: "Start argv=[--permission-mode auto --model opus --session-id <new> /obor:lead] kind=claude label=pj-lead pane=w1:p6",
		line:  "RESTARTED pj-lead (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_unknown_agent_with_sid_gets_obor_agent_only
		name: "unknown agent with a sid: OBOR_AGENT and OBOR_LANE unset only, no cd, no type args",
		prep: func(x *fx) {
			x.sid("triage", "sid-t")
			x.shell("w1:p8", "triage", "")
			x.f.Set("w1:p8", func(p *mpane) { p.Session = "sid-t" })
		},
		req: launch.Request{Agent: "triage"},
		env: func(x *fx) string {
			return "Write pane=w1:p8 submit=true text=unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_PANE=w1:p8"
		},
		start: "Start argv=[--resume sid-t] kind=claude label=triage pane=w1:p8",
		line:  "RESTARTED triage (pane w1:p8, session sid-t)",
	}, {
		// OBOR_ROLE=triage like every other role
		name: "triage role: its type, cwd root, OBOR_ROLE=triage (no OBOR_AGENT)",
		edit: []func(map[string]any){testkit.Set("agents.roles", `{"lead":"opus","triage":"opus"}`)},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-triage", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "Write pane=w1:p6 submit=true text=cd " + q(x.p.Root) + " && unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=triage OBOR_PANE=w1:p6"
		},
		start: "Start argv=[--permission-mode auto --model opus " + settings + " --session-id <new>] kind=claude label=pj-triage pane=w1:p6",
		line:  "RESTARTED pj-triage (pane w1:p6, session new)",
	}, {
		// its role worktree
		name: "deep_dive role (T-179, T-853): its type, cwd its role worktree, OBOR_ROLE=deep_dive",
		edit: []func(map[string]any){
			testkit.Set("agents.roles", `{"lead":"opus","deep_dive":"opus"}`),
		},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-deep-dive", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "Write pane=w1:p6 submit=true text=cd " + q(x.p.LaneDir("deep_dive")) +
				" && unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=deep_dive OBOR_PANE=w1:p6"

		},
		start: "Start argv=[--permission-mode auto --model opus " + settings + " --session-id <new>] kind=claude label=pj-deep-dive pane=w1:p6",
		line:  "RESTARTED pj-deep-dive (pane w1:p6, session new)",
	}, {
		// bash restart.test.sh case_lead_type_from_leads_entry_or_none (pj-lead)
		name: "a lead without a model: no args",
		edit: []func(map[string]any){testkit.Set("agents.roles", `{"lead":""}`)},
		prep: func(x *fx) { x.shell("w1:p6", "", "") },
		req:  launch.Request{Agent: "pj-lead", Pane: "w1:p6", Fresh: true},
		env: func(x *fx) string {
			return "Write pane=w1:p6 submit=true text=cd " + q(x.p.Root) + " && unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=lead OBOR_PANE=w1:p6"
		},
		start: "Start argv=[--session-id <new> /obor:lead] kind=claude label=pj-lead pane=w1:p6",
		line:  "RESTARTED pj-lead (pane w1:p6, session new)",
		out:   "args (claude): --session-id <new> /obor:lead",
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
			out = x.named(out)
			eq(t, last(out), c.line, "result line")
			runs := x.calls("Write")
			if len(runs) != 1 {
				t.Fatalf("one env line typed, got %q", runs)
			}
			m := envMarksRE.FindStringSubmatch(runs[0])
			if m == nil {
				t.Fatalf("the env line ends without its success and failure marks: %q", runs[0])
			}
			eq(t, runs[0][:len(runs[0])-len(m[0])], c.env(x), "env line (cwd, OBOR_ROLE, OBOR_LANE, a stale OBOR_AGENT unset)")
			eq(t, x.named(x.startCall()), c.start, "agent start (kind, pane, type args, resume)")
			if c.out != "" {
				contains(t, out, c.out, "output")
			}
			if i, j := strings.Index(out, "args ("), strings.Index(out, "RESTARTED"); i < 0 || i > j {
				t.Errorf("args line before the result: %q", out)
			}
		})
	}
}

// A role agent's start links lanes.links into its role worktree, so a file git does not track (the project's
// .claude/settings.json of a fresh init, which enables obor's plugin) reaches the agent: a worktree the start adds
// gets the links (TestRestartAddsTheMissingRoleWorktreeWithTheLinks), one already there gets the link it lacks, and a
// file at a link's path stays as it is.
func TestRestartLinksLaneLinksIntoTheRoleWorktree(t *testing.T) {
	t.Parallel()
	links := testkit.Set("lanes.links", `["local.env", ".claude/settings.json"]`)
	t.Run("an existing role worktree gets the link it lacks and keeps the file it has", func(t *testing.T) {
		t.Parallel()
		x := setup(t, links)
		wt := x.p.LaneDir("integrator")
		own := filepath.Join(wt, ".claude", "settings.json")
		before, err := os.ReadFile(own)
		if fi, lerr := os.Lstat(own); err != nil || lerr != nil || !fi.Mode().IsRegular() {
			t.Fatalf("precondition: the role worktree has its own %s (a regular file): %v %v", own, err, lerr)
		}
		if _, err := os.Lstat(filepath.Join(wt, "local.env")); err == nil {
			t.Fatal("precondition: the role worktree has no local.env")
		}
		restartIntegratorWithLinks(t, x)
		linkedInto(t, x, wt, "local.env")
		after, _ := os.ReadFile(own)
		if fi, err := os.Lstat(own); err != nil || !fi.Mode().IsRegular() || string(after) != string(before) {
			t.Errorf("the worktree's own %s changed: %q, want the regular file %q", own, after, before)
		}
		if string(before) == x.p.Read(filepath.Join(x.p.Root, ".claude", "settings.json")) {
			t.Fatal("the worktree's file and the main checkout's are the same text: the test cannot tell them apart")
		}
	})
}

// restartIntegratorWithLinks restarts pj-int, whose cwd is its role worktree, in a project whose main checkout has a
// local.env and a .claude/settings.json (the config's lanes.links lists both).
func restartIntegratorWithLinks(t *testing.T, x *fx) {
	t.Helper()
	x.p.Write(filepath.Join(x.p.Root, "local.env"), "A=1\n")
	x.p.Write(filepath.Join(x.p.Root, ".claude", "settings.json"), `{"main":true}`+"\n")
	restartIntegratorAsIs(t, x)
}

// linkedInto fails unless link in the role worktree wt is a link to the main checkout's.
func linkedInto(t *testing.T, x *fx, wt, link string) {
	t.Helper()
	got, err := os.Readlink(filepath.Join(wt, link))
	if want := filepath.Join(x.p.Root, link); err != nil || project.Physical(got) != project.Physical(want) {
		t.Errorf("%s in the role worktree: link to %q (%v), want a link to %s", link, got, err, want)
	}
}

// restartIntegrator restarts pj-int, whose cwd is its role worktree, in a project whose main checkout has an
// untracked .claude/settings.json that lanes.links does not list.
func restartIntegrator(t *testing.T, x *fx) {
	t.Helper()
	cfg, err := config.Load(x.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cfg.LaneLinks(), project.PluginSettings) {
		t.Fatalf("precondition: lanes.links %q does not list the settings file", cfg.LaneLinks())
	}
	if _, err := os.Stat(filepath.Join(x.p.Root, project.PluginSettings)); err != nil {
		t.Fatalf("precondition: the main checkout has a settings file: %v", err)
	}
	restartIntegratorAsIs(t, x)
}

// restartIntegratorAsIs restarts pj-int, whose cwd is its role worktree.
func restartIntegratorAsIs(t *testing.T, x *fx) {
	t.Helper()
	x.sid("pj-int", "sid-i")
	x.shell("w1:p5", "pj-int", "")
	x.f.Set("w1:p5", func(p *mpane) { p.Session = "sid-i" })
	if out, ok := x.restart(launch.Request{Agent: "pj-int"}); !ok {
		t.Fatalf("restart failed:\n%s", out)
	}
}

// settingsLinked fails unless the role worktree wt has .claude/settings.json as a link to the main checkout's.
func settingsLinked(t *testing.T, x *fx, wt string) {
	t.Helper()
	got, err := os.Readlink(filepath.Join(wt, project.PluginSettings))
	if want := filepath.Join(x.p.Root, project.PluginSettings); err != nil || project.Physical(got) != project.Physical(want) {
		t.Errorf("the role worktree's settings file: link to %q (%v), want a link to %s", got, err, want)
	}
}

// A restart of a role agent whose worktree is there links the main checkout's untracked settings file into it, though
// lanes.links does not list the file: the agent starts with obor's plugin.
func TestRestartLinksUnlistedUntrackedSettingsIntoAnExistingRoleWorktree(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.p.Write(filepath.Join(x.p.Root, ".claude", "settings.json"), `{"main":true}`+"\n")
	wt := x.p.LaneDir("integrator")
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		t.Fatalf("precondition: the role worktree %s is there: %v", wt, err)
	}
	if err := os.RemoveAll(filepath.Join(wt, ".claude")); err != nil {
		t.Fatal(err)
	}
	restartIntegrator(t, x)
	settingsLinked(t, x, wt)
}

// Request.Args and ParseRequest are one pair; the CLI's fields (agent, pane, fresh, force) go through both and
// come back the same, so a field carried on one side only fails here. A Prompt, Effort or Type is a start's (serve's), never a scheduled CLI restart's: no flag carries
// it, and ParseRequest refuses the flags restart does not take.
func TestRequestArgsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		req  launch.Request
		args string
	}{
		{"agent and pane", launch.Request{Agent: "pj-l-01", Pane: "w1:p3"}, "pj-l-01 --pane w1:p3"},
		{"fresh", launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true}, "pj-l-01 --pane w1:p3 --fresh"},
		{"force", launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Force: true}, "pj-l-01 --pane w1:p3 --force"},
		{"fresh and force", launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true, Force: true},
			"pj-l-01 --pane w1:p3 --fresh --force"},
		{"fresh handoff", launch.Request{Agent: "pj-lead", Pane: "w1:p1", Fresh: true, Handoff: true},
			"pj-lead --pane w1:p1 --fresh --handoff"},
		{"no pane", launch.Request{Agent: "pj-lead"}, "pj-lead"},
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

// ParseRequest refuses what Args never writes: a stray flag, a second
// positional, a flag without its value; and the flags restart does not take, with their values.
func TestParseRequestRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range [][]string{
		{"pj-l-01", "--keep-inbox"},
		{"pj-l-01", "--bogus"},
		{"pj-l-01", "pj-int"},
		{"pj-l-01", "--pane"},
		{"pj-l-01", "--prompt", "go"},
		{"pj-l-01", "--type", "luna"},
		{"pj-l-01", "--effort", "high"},
		{"pj-l-01", "--sid", "sid-9"},
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
	x.shell("w1:p4", "pj-l-01", "")
	if x.recorded("pj-l-01") != "" {
		t.Fatal("precondition: no recorded sid")
	}
	out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
	contains(t, out, "pane w1:p4 (from label)", "the labelled pane, not the lane cwd one")
	eq(t, last(out), "FAIL no session to resume for pj-l-01 (pass --fresh to start a new one)", "no session")
	if ok || len(x.calls("Write")) > 0 || x.startCall() != "" {
		t.Errorf("nothing typed or started: %q", x.f.Calls())
	}
	out, ok = x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	restarted(t, last(out), "pj-l-01", "w1:p4", "new")
	lacks(t, x.startCall(), "--resume", "--fresh: no --resume")
	contains(t, x.startCall(), "pane=w1:p4", "started in the labelled pane")
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
		line: "FAIL unknown agent nobody (no lane, role or recorded session)",
	}, {
		name: "no pane labelled the agent",
		prep: func(x *fx) { x.sid("pj-l-01", "sid-1"); x.shell("w1:p9", "", "/elsewhere") },
		req:  launch.Request{Agent: "pj-l-01"},
		line: "FAIL no pane labelled pj-l-01 (pass --pane)",
	}, {
		name: "--pane labelled for another agent",
		prep: func(x *fx) { x.sid("pj-l-01", "sid-1"); x.shell("w1:p4", "pj-rev", "") },
		req:  launch.Request{Agent: "pj-l-01", Pane: "w1:p4"},
		line: "FAIL pane w1:p4 is labelled pj-rev, not pj-l-01",
	}, {
		name: "another pane already labelled the agent",
		prep: func(x *fx) {
			x.sid("pj-l-01", "sid-1")
			x.shell("w1:p3", "pj-l-01", "")
			x.shell("w1:p4", "", "")
		},
		req:  launch.Request{Agent: "pj-l-01", Pane: "w1:p4"},
		line: "FAIL pane w1:p3 is already labelled pj-l-01 (obor restart pj-l-01 there)",
	}, {
		name: "no agent, the foreground is another program",
		prep: func(x *fx) {
			x.sid("pj-l-01", "sid-1")
			s := x.shell("w1:p4", "pj-l-01", "")
			x.f.Set(s.ID, func(p *mpane) {
				p.Foreground, p.Procs = 200, []port.Process{{PID: 200, Argv0: "/usr/bin/vim", Name: "vim"}}
			})
		},
		req:  launch.Request{Agent: "pj-l-01"},
		line: "FAIL w1:p4 runs another program (vim)",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.prep(x)
			out, ok := x.restart(c.req)
			eq(t, last(out), c.line, "result")
			if ok || len(x.calls("Write")) > 0 || len(x.calls("RenamePane")) > 0 || x.startCall() != "" {
				t.Errorf("nothing typed, labelled or started: %q", x.f.Calls())
			}
		})
	}
}

// two projects with the same names. The kit has lane L-02 (agent pj-l-02); axis has a lane L-02 too, in its own
// worktree. A restart that loaded the kit's config (axis's scheduled self-restart, spawned in the kit's checkout) took
// axis's lane pane for the kit's lane and started it in the kit's lane worktree. An unconfigured name names nothing; the
// pane's lane cwd is what makes it axis's.
func TestRestartOtherProjectsPane(t *testing.T) {
	t.Parallel()
	kitEdits := []func(map[string]any){testkit.Set("agents.roles", `{"integrator":"opus"}`)}
	// fixtures: the kit (x, lanes L-01 and L-02), axis (its lane L-02 in <axis>-wt/L-02) and its worker's pane w2:pF in
	// axis's worktree.
	fixtures := func(t *testing.T) (x *fx, axis *config.Config, axisWt string) {
		x = setup(t)
		x.p.PlainLane("L-02", "")
		x.p.Config(kitEdits...)
		a := testkit.NewPlain(t)
		axisWt = a.PlainLane("L-02", "")
		a.Config(testkit.Set("lanes.count", 2), testkit.Set("agents.kinds.claude.args", `["--strict-mcp-config"]`),
			testkit.Set("agents.roles", `{"integrator":"opus"}`))
		axis, err := config.Load(a.Root)
		if err != nil {
			t.Fatal(err)
		}
		pane := x.agent("w2:pF", "l-02", "claude", "idle")
		x.f.Set(pane.ID, func(p *mpane) { p.Cwd = axisWt })
		return x, axis, axisWt
	}
	restart := func(x *fx, cfg *config.Config) (string, bool) {
		var out bytes.Buffer
		l := x.launcher(&out)
		if cfg != nil {
			l.Cfg = cfg
		}
		line, err := l.Restart(context.Background(), launch.Request{Agent: "l-02", Pane: "w2:pF", Fresh: true, Force: true})
		return result(x.t, &out, line, err)
	}

	t.Run("the kit's config: FAIL with evidence, nothing stopped, typed or started", func(t *testing.T) {
		t.Parallel()
		x, _, axisWt := fixtures(t)
		kit, err := config.Load(x.p.Root)
		if err != nil {
			t.Fatal(err)
		}
		// axis's L-02 pane runs an agent named as the kit's lane L-02's worker (pj-l-02: derived in both).
		x.f.Set("w2:pF", func(p *mpane) { p.Name, p.Label = "pj-l-02", "pj-l-02" })
		if info, ok := kit.AgentLookup("pj-l-02", ""); !ok || info.Lane != "L-02" {
			t.Fatalf("precondition: the kit's config takes pj-l-02 for its lane L-02: %+v %v", info, ok)
		}
		var buf bytes.Buffer
		line, err := x.launcher(&buf).Restart(context.Background(), launch.Request{Agent: "pj-l-02", Pane: "w2:pF",
			Fresh: true, Force: true})
		out, ok := result(x.t, &buf, line, err)
		eq(t, failLine(out), "FAIL pane w2:pF is not this project's: its cwd "+axisWt+" is outside "+x.p.Root+
			" (another project's agent? run obor restart pj-l-02 in its own project)", "result")
		contains(t, out, "pane screen", "evidence")
		contains(t, x.p.Read(".obor/state/launch.log"), "pane w2:pF is not this project's", "evidence logged")
		if ok || len(x.calls("Write")) > 0 || len(x.calls("SendKeys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing stopped, typed or started: %q", x.f.Calls())
		}
	})
	t.Run("control: axis's config restarts its lane worker in its own worktree", func(t *testing.T) {
		t.Parallel()
		x, axis, axisWt := fixtures(t)
		out, ok := restart(x, axis)
		if !ok || !isRestarted(last(out), "l-02", "w2:pF", "new") {
			t.Fatalf("restarted:\n%s", out)
		}
		contains(t, strings.Join(x.calls("Write"), "\n"), "cd "+axisWt+" && unset OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=worker OBOR_LANE=L-02",
			"started in axis's worktree, its lane's worker")
		contains(t, x.startCall(), "--strict-mcp-config", "axis's kinds.claude.args")
	})
	t.Run("control: the kit's own lane pane restarts in the kit's lane worktree", func(t *testing.T) {
		t.Parallel()
		x, _, _ := fixtures(t)
		x.f.Set("w2:pF", func(p *mpane) { p.Cwd = x.p.LaneDir("L-02") })
		out, ok := restart(x, nil)
		if !ok || !isRestarted(last(out), "l-02", "w2:pF", "new") {
			t.Fatalf("restarted:\n%s", out)
		}
		contains(t, strings.Join(x.calls("Write"), "\n"), "cd "+x.p.LaneDir("L-02")+" && unset OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS && export OBOR_ROLE=worker",
			"started in the kit's lane")
	})
	t.Run("control: a pane in another worktree of the kit's repository is the kit's", func(t *testing.T) {
		t.Parallel()
		x, _, _ := fixtures(t)
		stable := filepath.Join(x.p.Base, "stable")
		x.p.Worktree(stable, "")
		x.f.Set("w2:pF", func(p *mpane) { p.Cwd = stable })
		out, ok := restart(x, nil)
		if !ok || !isRestarted(last(out), "l-02", "w2:pF", "new") {
			t.Fatalf("restarted:\n%s", out)
		}
	})
}

// bash restart.test.sh case_where_by_label, case_label_set_on_start
func TestRestartLabel(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-l-01", "sid-1")
	x.shell("w1:p2", "", "")
	x.shell("w1:p3", "pj-l-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
	contains(t, out, "pane w1:p3 (from label)", "the pane is found by its label")
	restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
	if !ok {
		t.Errorf("restarted:\n%s", out)
	}

	x.reset()
	x.shell("w1:p4", "", "")
	out, ok = x.restart(launch.Request{Agent: "pj-l-01", Pane: "w1:p4"})
	restarted(t, last(out), "pj-l-01", "w1:p4", "sid-1")
	contains(t, out, "labelled pane w1:p4 pj-l-01", "said")
	eq(t, x.f.All[0].Label, "pj-l-01", "label set in herdr")
	env, ren, start := x.index("Write pane=w1:p4 submit=true text=cd "), x.index("RenamePane label=pj-l-01 pane=w1:p4"), x.lastIndex("Start")
	if !(env >= 0 && env < ren && ren < start) {
		t.Errorf("env, then label, then start: %q", x.f.Calls())
	}

	x.f.Set("w1:p4", func(p *mpane) { p.Kind, p.Foreground = "", 0 }) // the agent exited again
	n := len(x.f.Calls())
	out, _ = x.restart(launch.Request{Agent: "pj-l-01"})
	contains(t, out, "pane w1:p4 (from label)", "found by the label just set")
	lacks(t, strings.Join(x.f.Calls()[n:], "\n"), "RenamePane", "already labelled: no rename")
	lacks(t, out, "labelled pane", "nothing said")

	x.reset()
	x.shell("w1:p5", "", "")
	x.f.Fail("RenamePane", termError("rename", "not found", "gone"))
	n = len(x.f.Calls())
	out, ok = x.restart(launch.Request{Agent: "pj-l-01", Pane: "w1:p5"})
	eq(t, last(out), "FAIL terminal pane rename w1:p5 pj-l-01 failed", "rename failing")
	if ok || strings.Contains(strings.Join(x.f.Calls()[n:], "\n"), "Start") {
		t.Error("no start after a failed rename")
	}
}

// codexMCP is the server Codex gets on every start's command line; codexUpdate is its startup update check off
// (from the type's launch args, so the start adds none).
const codexMCP = `-c mcp_servers.obor.command="/kit/bin/obor" -c mcp_servers.obor.args=["mcp"] ` +
	`-c mcp_servers.obor.env_vars=["OBOR_ROLE","OBOR_LANE","OBOR_PANE","OBOR_STATE_HOME"] ` +
	`-c mcp_servers.obor.env={OBOR_NO_SELF_HOST="1"} ` +
	`-c mcp_servers.obor.default_tools_approval_mode="approve"`

const codexUpdate = "-c features.plugins=false -c features.apps=false -c skills.bundled.enabled=false -c check_for_update_on_startup=false"

// obor restart of a lane's worker while its lane's task has a job open (Launcher.Job) restarts it with that job:
// a fresh session's first prompt is the job's wake, a resumed one gets it typed once it is up. No job: unchanged
// (nothing typed, no first prompt); an explicit --prompt wins; an agent on no lane is not asked about.
func TestRestartLaneWorkerWithItsJob(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	restart := func(x *fx, job string, req launch.Request) (string, []string) {
		x.t.Helper()
		var out bytes.Buffer
		var asked []string
		l := x.launcher(&out)
		l.Job = func(_ context.Context, _ string, lane string) (string, error) {
			asked = append(asked, lane)
			return job, nil
		}
		line, err := l.Restart(context.Background(), req)
		got, ok := result(x.t, &out, line, err)
		if !ok {
			x.t.Fatal(got)
		}
		return got, asked
	}
	typed := func(x *fx) []string { return x.calls("Prompt pane=w1:p3 text=" + wake) }
	t.Run("fresh: the wake is the first prompt", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		out, asked := restart(x, "T-1/1/work", launch.Request{Agent: "pj-l-01", Fresh: true})
		if len(asked) != 1 || asked[0] != "L-01" {
			t.Fatalf("precondition: L-01's job asked once: %q", asked)
		}
		contains(t, x.startCall(), " "+wake+"]", "the wake as the start's first prompt")
		contains(t, out, "job T-1/1/work: its wake is the first prompt", "said")
		eqInt(t, len(typed(x)), 0, "wakes typed besides the start")
	})
	t.Run("resumed: the wake typed after the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		out, _ := restart(x, "T-1/1/work", launch.Request{Agent: "pj-l-01"})
		contains(t, x.startCall(), "--resume sid-1", "resumed")
		lacks(t, x.startCall(), wake, "a resume carries no first prompt")
		if start, wakes := x.index("Start"), typed(x); len(wakes) != 1 || x.index(wakes[0]) < start {
			t.Errorf("the wake typed once after the start: %q", x.f.Calls())
		}
		contains(t, out, "job T-1/1/work: its wake typed", "said")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
		eqInt(t, len(x.calls("Write text=[obor job")), 0, "wakes sent through send_input")
	})
	t.Run("resumed, agent blocked: warning, nothing typed (T-800)", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.OnPrompt = "refused" })
		out, _ := restart(x, "T-1/1/work", launch.Request{Agent: "pj-l-01"})
		contains(t, x.startCall(), "--resume sid-1", "precondition: resumed")
		eqInt(t, len(x.calls("Prompt")), 1, "precondition: the wake offered to the terminal's Prompt")
		contains(t, out, "warning: job T-1/1/work's wake not typed into pane w1:p3", "said")
		contains(t, out, "(type it there: "+wake+")", "the line to type by hand")
		lacks(t, out, "its wake typed", "no success line")
		eqInt(t, len(x.calls("Write text=[obor job")), 0, "wakes sent through send_input")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
	})
	t.Run("no job: no wake", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		out, asked := restart(x, "", launch.Request{Agent: "pj-l-01"})
		if len(asked) != 1 {
			t.Fatalf("precondition: the job asked: %q", asked)
		}
		eqInt(t, len(x.calls("text=[obor job")), 0, "wakes typed")
		lacks(t, out, "job ", "said")
	})
	t.Run("--prompt wins: the job not asked", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		_, asked := restart(x, "T-1/1/work", launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: "hello"})
		eqInt(t, len(asked), 0, "jobs asked")
		lacks(t, x.startCall(), wake, "the wake")
	})
	t.Run("an agent on no lane: not asked", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p7", "pj-rev", "")
		_, asked := restart(x, "T-1/1/work", launch.Request{Agent: "pj-rev", Fresh: true})
		eqInt(t, len(asked), 0, "jobs asked")
		lacks(t, x.startCall(), wake, "the wake")
	})
}

// obor restart of the integrator carries its open job as its wake, as a lane worker's restart carries its
// lane's: a fresh session's first prompt is the wake, a resumed one gets it typed once it is up. The job asked for is
// the integrator's (its role, no lane: a task's review or resolve job); no job: no wake.
func TestRestartIntegratorWithItsJob(t *testing.T) {
	t.Parallel()
	restart := func(x *fx, job string, req launch.Request) (string, []string) {
		x.t.Helper()
		var out bytes.Buffer
		var asked []string
		l := x.launcher(&out)
		l.Job = func(_ context.Context, role, lane string) (string, error) {
			asked = append(asked, role+"/"+lane)
			return job, nil
		}
		line, err := l.Restart(context.Background(), req)
		got, ok := result(x.t, &out, line, err)
		if !ok {
			x.t.Fatal(got)
		}
		return got, asked
	}
	typed := func(x *fx, wake string) []string { return x.calls("Prompt pane=w1:p5 text=" + wake) }
	for _, c := range []struct {
		name, job string
		fresh     bool
	}{
		{"fresh: a review job's wake is the first prompt", "T-1/1/review", true},
		{"fresh: a resolve job's wake is the first prompt", "T-2/2/resolve", true},
		{"resumed: a review job's wake typed after the start", "T-1/1/review", false},
		{"resumed: a resolve job's wake typed after the start", "T-2/2/resolve", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			wake := "[obor job " + c.job + "] call the obor job_next tool"
			x := setup(t)
			x.sid("pj-int", "sid-int")
			x.shell("w1:p5", "pj-int", "")
			out, asked := restart(x, c.job, launch.Request{Agent: "pj-int", Fresh: c.fresh})
			if len(asked) != 1 || asked[0] != "integrator/" {
				t.Fatalf("precondition: the integrator's job asked once: %q", asked)
			}
			if c.fresh {
				contains(t, x.startCall(), " "+wake+"]", "the wake as the start's first prompt")
				contains(t, out, "job "+c.job+": its wake is the first prompt", "said")
				eqInt(t, len(typed(x, wake)), 0, "wakes typed besides the start")
				return
			}
			contains(t, x.startCall(), "--resume sid-int", "resumed")
			lacks(t, x.startCall(), wake, "a resume carries no first prompt")
			if start, wakes := x.index("Start"), typed(x, wake); len(wakes) != 1 || x.index(wakes[0]) < start {
				t.Errorf("the wake typed once after the start: %q", x.f.Calls())
			}
			contains(t, out, "job "+c.job+": its wake typed", "said")
			restarted(t, last(out), "pj-int", "w1:p5", "sid-int")
		})
	}
	t.Run("no job: no wake", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-int", "sid-int")
		x.shell("w1:p5", "pj-int", "")
		out, asked := restart(x, "", launch.Request{Agent: "pj-int"})
		eqInt(t, len(asked), 1, "precondition: the job asked")
		eqInt(t, len(x.calls("text=[obor job")), 0, "wakes typed")
		lacks(t, out, "job ", "said")
	})
}

// serve's start of a lane's worker again (no --fresh, its outstanding job's wake as the Prompt): a recorded
// session is resumed and the wake typed once it is up; no session to resume starts fresh with the wake as its first
// prompt instead of the FAIL a CLI restart gets.
func TestLaunchCarriesItsPromptOnAResume(t *testing.T) {
	t.Parallel()
	const wake = "[obor job T-1/1/fix] call the obor job_next tool"
	launchIt := func(x *fx) string {
		x.t.Helper()
		var out bytes.Buffer
		s, err := x.launcher(&out).Launch(context.Background(), launch.Request{Agent: "pj-l-01", Prompt: wake})
		got, ok := result(x.t, &out, s.Line, err)
		if !ok {
			x.t.Fatal(got)
		}
		return got
	}
	typed := func(x *fx) []string { return x.calls("Prompt pane=w1:p3 text=" + wake) }
	t.Run("a session to resume: resumed, the prompt typed after the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		out := launchIt(x)
		contains(t, x.startCall(), "--resume sid-1", "resumed")
		lacks(t, x.startCall(), wake, "a resume carries no first prompt")
		if start, wakes := x.index("Start"), typed(x); len(wakes) != 1 || x.index(wakes[0]) < start {
			t.Errorf("the wake typed once after the start: %q", x.f.Calls())
		}
		contains(t, out, "the first prompt: its wake typed", "said")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
	})
	t.Run("no session to resume: fresh, the prompt its first", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		if x.recorded("pj-l-01") != "" {
			t.Fatal("precondition: no recorded sid")
		}
		out := launchIt(x)
		lacks(t, x.startCall(), "--resume", "a new session")
		contains(t, x.startCall(), " "+wake+"]", "the wake as the start's first prompt")
		eqInt(t, len(typed(x)), 0, "wakes typed besides the start")
		restarted(t, last(out), "pj-l-01", "w1:p3", "new")
	})
}

// a lane's job that cannot be told (Launcher.Job's error: the lane holds a task, serve does not answer) refuses
// the restart before the stop: FAIL naming why, the running agent untouched (no keys, no input, no start).
func TestRestartLaneWorkerJobUnknownRefuses(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.agent("w1:p3", "pj-l-01", "claude", "idle")
	var out bytes.Buffer
	asked := 0
	l := x.launcher(&out)
	l.Job = func(context.Context, string, string) (string, error) {
		asked++
		return "", errors.New("L-01 holds T-1 and obor serve cannot tell its job (connection refused)")
	}
	line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
	got, ok := result(t, &out, line, err)
	eqInt(t, asked, 1, "precondition: the job asked")
	if ok {
		t.Fatalf("restarted: %s", got)
	}
	contains(t, failLine(got), "FAIL pj-l-01 not restarted (nothing stopped): L-01 holds T-1 and obor serve cannot tell "+
		"its job", "result")
	if len(x.calls("SendKeys")) > 0 || len(x.calls("Write")) > 0 || x.startCall() != "" {
		t.Errorf("the agent touched: %q", x.f.Calls())
	}
}

// a Job error that is restart's own line (a project.Fail: the integrator's serve is down and may hold its job)
// is the FAIL line itself, never wrapped as "not restarted (nothing stopped)": obor restart prints exactly what its
// spec names. Nothing is stopped either.
func TestRestartIntegratorServeDownRefuses(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.agent("w1:p5", "pj-int", "claude", "idle")
	var out bytes.Buffer
	l := x.launcher(&out)
	l.Job = func(context.Context, string, string) (string, error) {
		return "", project.Failf("restart pj-int: obor serve is not running; it may hold an open job for pj-int: start " +
			"serve, then restart")
	}
	line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-int", Fresh: true})
	got, ok := result(t, &out, line, err)
	if ok {
		t.Fatalf("restarted: %s", got)
	}
	const want = "FAIL restart pj-int: obor serve is not running; it may hold an open job for pj-int: start serve, then restart"
	eq(t, failLine(got), want, "result")
	if len(x.calls("SendKeys")) > 0 || len(x.calls("Write")) > 0 || x.startCall() != "" {
		t.Errorf("the agent touched: %q", x.f.Calls())
	}
}

// the launcher finds the agent's pane in the one pane list it reads to resolve the restart, not in a second list
// that can differ (or fail, which it reported as "no pane labelled"): the label gone from every later list changes nothing.
func TestRestartResolvesFromOnePaneList(t *testing.T) {
	t.Parallel()
	x := setup(t)
	pane := x.shell("w1:p3", "pj-l-01", "")
	lists := 0
	x.f.Before(func(call string) {
		if call != "Panes" {
			return
		}
		if lists++; lists > 1 {
			x.f.Locked(func() { pane.Label = "" })
		}
	})
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	eqInt(t, len(x.calls("Start")), 1, "precondition: the restart got as far as the start")
	lacks(t, out, "no pane labelled", "found in the first list")
	if !ok {
		t.Fatalf("restart failed:\n%s", out)
	}
}

// a start whose agent never comes up (the terminal's start timed out at Up; live: the command cut off at the shell,
// left at an open quote) is a FAIL that says so, not RESTARTED.
func TestStartAgentNeverUp(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.wait = 200 * time.Millisecond
	x.shell("w1:p3", "pj-l-01", "")
	x.f.Set("w1:p3", func(p *mpane) { p.OnStart = "never" })
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: strings.Repeat("a long brief ", 130)})
	eqInt(t, len(x.calls("Start")), 1, "precondition: one start, sent once")
	if ok || strings.Contains(out, "RESTARTED") {
		t.Fatalf("a start with no agent up said RESTARTED:\n%s", out)
	}
	contains(t, out, "no claude agent came up in w1:p3 within 200ms", "the terminal's error printed")
	eq(t, failLine(out), "FAIL start failed in w1:p3 (no agent runs there: the pane is at its shell, maybe mid-command: "+
		"run obor restart pj-l-01 --fresh --pane w1:p3 again or start it by hand)", "result")
}

// the start's command is typed only once the shell has run the env line and is back at its prompt (zsh runs a
// line with the tty in cooked mode, where a long command typed at once is cut off): the mark it echoes, read on the
// pane's recent output until it shows, then a start.
func TestRestartWaitsForTheEnvLine(t *testing.T) {
	t.Parallel()
	t.Run("the shell back: the start after the mark shows", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.OnRun = "hang" }) // the shell prints the mark at the third look, below
		okWord, looks := "", 0
		x.f.Before(func(call string) {
			switch {
			case strings.HasPrefix(call, "Write pane=w1:p3 submit=true text=") && okWord == "":
				okWord, _ = envWords(t, call)
			case call == "Recent pane=w1:p3" && okWord != "":
				if looks++; looks == 3 {
					x.f.Set("w1:p3", func(p *mpane) { p.Screen += okWord + "\n% \n" })
				}
			}
		})
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		contains(t, x.f.At("w1:p3").Screen, okWord, "precondition: the shell echoed the mark")
		calls := x.f.Calls()
		env, start := x.index("Write pane=w1:p3"), x.index("Start")
		reads := 0
		for _, c := range calls[max(env, 0):max(start, 0)] {
			if c == "Recent pane=w1:p3" {
				reads++
			}
		}
		if env < 0 || start < env || reads < 3 {
			t.Errorf("env line %d, start %d, %d recent reads between: want the start after the reads up to the mark:\n%s",
				env, start, reads, strings.Join(calls, "\n"))
		}
	})
	t.Run("the shell never back: FAIL, nothing started", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = 200 * time.Millisecond
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.OnRun = "hang" })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		eqInt(t, len(x.calls("Write pane=w1:p3")), 1, "precondition: the env line typed")
		if ok {
			t.Fatalf("restarted while the shell still ran the env line:\n%s", out)
		}
		eq(t, failLine(out), "FAIL the shell in w1:p3 did not finish the env line in 200ms (nothing started; the env line may have failed: check the pane)", "result")
		eq(t, x.startCall(), "", "no start")
	})
}

// staleScreen makes pane's visible screen stale, as herdr's after Claude exited (T-970): it still shows the old agent,
// and what the shell prints from now on reaches the pane's recent output only.
func staleScreen(x *fx, pane string) {
	x.f.Set(pane, func(p *mpane) { p.Screen, p.Stale = "╭─ Claude Code ─╮\n❯ a half-typed line", true })
}

// wantMarkInRecentOnly: the shell printed the env line's success mark into pane's recent output, never onto its stale
// visible screen (the precondition of a stale-visible case: a look at the screen would not have seen it).
func wantMarkInRecentOnly(t *testing.T, x *fx, pane string) {
	t.Helper()
	runs := x.calls("Write pane=" + pane)
	if len(runs) == 0 {
		t.Fatalf("precondition: no env line typed in %s", pane)
	}
	okWord, _ := envWords(t, runs[0])
	p := x.f.At(pane)
	lacks(t, p.Screen, okWord, "precondition: the stale visible screen, the mark")
	contains(t, p.Later, okWord, "precondition: the recent output, the mark")
}

// The env-line handshake reads the pane's recent output, not its visible screen (T-970, T-1356): a stale visible
// screen with the mark only in recent output still starts the agent, at every entry point that types an env line
// in launch: obor restart, serve's launch (dispatch), and the restore (each through Launcher.Launch → setEnv).
func TestEnvLineSeenInRecentOutputWhenTheScreenIsStale(t *testing.T) {
	t.Parallel()
	t.Run("obor restart: sees the mark, starts", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		staleScreen(x, "w1:p3")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		wantMarkInRecentOnly(t, x, "w1:p3")
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		contains(t, x.startCall(), "pane=w1:p3", "started in the pane")
	})
	t.Run("serve's launch (dispatch): sees the mark, starts", func(t *testing.T) {
		t.Parallel()
		x := setupRunning(t, "opus")
		x.background = true
		x.shell("w1:p3", "pj-l-01", "")
		staleScreen(x, "w1:p3")
		var out bytes.Buffer
		s, err := x.launcher(&out).Launch(context.Background(),
			launch.Request{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true, Prompt: "the brief"})
		wantMarkInRecentOnly(t, x, "w1:p3")
		if err != nil {
			t.Fatalf("launch failed: %v\n%s", err, out.String())
		}
		contains(t, s.Line, "RESTARTED pj-l-01", "the launch's line")
		contains(t, x.startCall(), "pane=w1:p3", "started in the pane")
	})
	t.Run("restore: sees the mark, starts", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`)
		x.shell("w1:p3", "pj-l-01", x.lane)
		staleScreen(x, "w1:p3")
		r := &launch.Restorer{Env: x.p.Env(), H: x.f, Now: func() time.Time { return time.Unix(0, 0) },
			Sessions:  store.CurrentAt,
			LaneState: func(*config.Config, config.Lane) string { return port.LaneWorking },
			Restart: func(ctx context.Context, _ *config.Config, req launch.Request, w io.Writer) (string, error) {
				return x.launcher(w).Restart(ctx, req)
			}}
		r.RestoreProject(context.Background(), x.p.Root, port.Lost{All: true})
		log, _ := os.ReadFile(launch.RestoreLog(r.Env))
		wantMarkInRecentOnly(t, x, "w1:p3")
		contains(t, string(log), "RESTARTED pj-l-01", "the restore's restart")
		contains(t, x.startCall(), "pane=w1:p3", "started in the pane")
	})
}

// the env line's success mark proves the env was set. A cd that fails echoes the failure mark instead
// (no success mark), and the launch gives up before it starts the agent in the shell's old cwd with the old OBOR_* env.
func TestRestartFailsWhenTheEnvLineFailed(t *testing.T) {
	t.Parallel()
	t.Run("cd fails: the failure mark, no start, FAIL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		pane := x.shell("w1:p3", "pj-l-01", "")
		x.f.Set(pane.ID, func(p *mpane) { p.OnRun = "fail" })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if ok {
			t.Fatalf("restarted after a failed env line:\n%s", out)
		}
		runs := x.calls("Write pane=w1:p3")
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
		pane := x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatalf("restart failed:\n%s", out)
		}
		runs := x.calls("Write pane=w1:p3")
		if len(runs) != 1 {
			t.Fatalf("one env line typed, got %q", runs)
		}
		okWord, badWord := envWords(t, runs[0])
		contains(t, pane.Screen, okWord, "the screen: the success mark")
		lacks(t, pane.Screen, badWord, "the screen: the failure mark")
		if s := x.startCall(); !strings.Contains(s, "label=pj-l-01 pane=w1:p3") {
			t.Errorf("the agent started after the env line succeeded, got %q", s)
		}
	})
}

// envWords is the success and failure words the typed env line echoes, quotes removed.
func envWords(t *testing.T, line string) (ok, bad string) {
	t.Helper()
	m := envMarksRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("the env line has no success and failure marks: %q", line)
	}
	return "obor-env-" + m[1], "obor-env-" + m[2]
}

// waitGone makes the terminal's WaitStatus answer err at once: a stop's idle wait sees the agent leave.
type waitGone struct {
	control
	err error
}

func (w waitGone) WaitStatus(ctx context.Context, target string, until []string, timeout time.Duration) error {
	return w.err
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
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "idle")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
		if e, s := x.index("Write pane=w1:p3 submit=true text=/exit"), x.index("Start"); e < 0 || e > s {
			t.Errorf("/exit before the start: %q", x.f.Calls())
		}
		contains(t, out, "stopped claude in pane w1:p3", "said")
	})
	t.Run("never exits: FAIL after the wait", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "done")
		x.f.Set("w1:p3", func(p *mpane) { p.OnQuit = "stay" })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		eq(t, failLine(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		if ok || x.startCall() != "" {
			t.Error("nothing started")
		}
	})
	t.Run("working: FAIL; --force: Escape, then /exit, start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.agent("w1:p3", "pj-l-01", "claude", "working")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		eq(t, failLine(out), "FAIL pj-l-01 is working (--force to restart anyway)", "result")
		if ok || len(x.calls("Write")) > 0 || len(x.calls("SendKeys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing typed or started: %q", x.f.Calls())
		}
		out, ok = x.restart(launch.Request{Agent: "pj-l-01", Force: true})
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
		esc, exit := x.index("SendKeys keys=[esc] pane=w1:p3"), x.index("Write pane=w1:p3 submit=true text=/exit")
		if !(ok && esc >= 0 && esc < exit) {
			t.Errorf("--force: Escape, then /exit: %q", x.f.Calls())
		}
	})
	t.Run("codex: /quit, resume <herdr session id>", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		a := x.agent("w1:p7", "pj-rev", "codex", "idle")
		x.f.Set(a.ID, func(p *mpane) { p.Session = "cx-9" })
		out, ok := x.restart(launch.Request{Agent: "pj-rev"})
		restarted(t, last(out), "pj-rev", "w1:p7", "cx-9")
		contains(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p7 submit=true text=/quit", "Codex quits with /quit")
		// the reviewer's Codex start writes the main checkout's reviews dir too (made for it).
		reviews := filepath.Join(x.p.Root, "docs", "reviews")
		eq(t, x.startCall(), "Start argv=[resume cx-9 -s workspace-write -a never --no-daemon -m gpt-6-luna --add-dir "+reviews+" "+codexUpdate+" "+codexMCP+"] kind=codex label=pj-rev pane=w1:p7", "codex resume <id>")
		if !x.p.Exists(reviews) {
			t.Errorf("no %s made for the start", reviews)
		}

		contains(t, out, "resuming cx-9 (from herdr)", "source")
		_ = ok
	})
	// no background-task pre-check: Claude's Bash tool shells lead their own sessions, which process info does
	// not list (live 2026-10-17); Claude's exit dialog decides (the next test).
	t.Run("a shell in the agent's group, no exit dialog: no pre-check, restarts", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		a := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		x.f.Set(a.ID, func(p *mpane) {
			p.Procs = append(p.Procs, port.Process{PID: 300, Argv0: "/bin/zsh"}, port.Process{PID: 301, Argv0: "node"})
		})
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok || !strings.HasPrefix(last(out), "RESTARTED pj-l-01 ") || len(x.calls("Write pane=w1:p3 submit=true text=/exit")) != 1 {
			t.Errorf("/exit, then restarted:\n%s\n%q", out, x.f.Calls())
		}
	})
	t.Run("exit dialog: Escape + FAIL; --force: Exit and stop tasks", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		a := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		x.f.Set(a.ID, func(p *mpane) { p.OnQuit = "dialog" })
		if len(a.Procs) != 1 {
			t.Fatal("precondition: no background task seen")
		}
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "precondition: /exit sent")
		contains(t, strings.Join(x.f.Calls(), "\n"), "SendKeys keys=[esc] pane=w1:p3", "Escape closes the dialog")
		eqInt(t, len(x.calls("SendKeys keys=[enter] pane=w1:p3")), 0, "no Enter into the exit dialog")
		eqInt(t, len(x.calls("SendKeys keys=[1] pane=w1:p3")), 0, "no option chosen")
		eq(t, failLine(out), "FAIL pj-l-01 has background tasks (finish them, or --force to stop them)", "result")
		if ok || x.startCall() != "" || x.f.All[0].Screen != "" {
			t.Error("nothing started; dialog closed")
		}
		n := len(x.f.Calls())
		out, ok = x.restart(launch.Request{Agent: "pj-l-01", Force: true})
		calls := strings.Join(x.f.Calls()[n:], "\n")
		eqInt(t, strings.Count(calls, "SendKeys keys=[1] pane=w1:p3"), 1, "chose 1. Exit and stop tasks, once")
		lacks(t, calls, "SendKeys keys=[esc] pane=w1:p3", "no Escape")
		contains(t, out, "exit dialog: chose 1. Exit and stop tasks (--force)", "said")
		if !ok || !strings.HasPrefix(last(out), "RESTARTED pj-l-01 ") {
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

// A start's terminal-adapter lines (port.AgentStart.Say: a name the terminal kept, cleared and retried) are printed
// with the start (herdr's TestStartNameTakenAndNotReady has its cases); a serve start (Out discarded, Diag serve's
// log) puts them in the log and nothing else. A start the terminal answers NotReady is the startup-dialog FAIL. A name
// a live agent holds is Locate's refusal (TestRestartRefusedTouchesNothing).
func TestRestartAgentNameTaken(t *testing.T) {
	t.Parallel()
	const said = "agent start: name pj-l-01 taken: clearing it and retrying"
	t.Run("the adapter's line said with the start: RESTARTED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.Says = said })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		contains(t, out, said, "said")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
		if !ok {
			t.Errorf("restarted:\n%s", out)
		}
	})
	t.Run("a serve start: the adapter's line in serve's log only", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.Says = said })
		var log, out bytes.Buffer
		l := x.launcher(&out) // serve's launcher discards Out: what reaches it is no diagnostic
		l.Diag = &log
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01"})
		if err != nil || !isRestarted(line, "pj-l-01", "w1:p3", "sid-1") {
			t.Fatalf("restart = %q, %v; want the start in its pane", line, err)
		}
		eq(t, log.String(), said+"\n", "serve's log")
		lacks(t, out.String(), said, "Out")
	})
	t.Run("start blocked at a startup dialog (NotReady): FAIL that says so, not retried", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Once("Start", notReady())
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		contains(t, out, "blocked during startup (not ready)", "the terminal's error printed")
		eqInt(t, len(x.calls("Start")), 1, "not retried")
		eq(t, failLine(out), "FAIL start failed in w1:p3 (pj-l-01 waits at a startup dialog in pane w1:p3: answer it there)", "result")
		if ok {
			t.Error("a failure")
		}
	})
}

// Claude suspends on Ctrl+Z and herdr then sees an idle shell; a stopped child of the pane's shell refuses
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
	// is discarded by the exec, and under load that window is long enough to lose it (15 of 50 runs under load
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
		x.procInfo(func(p *mpane) { p.Shell, p.Foreground = shp, shp })
	}
	nothing := func(x *fx) {
		t.Helper()
		if len(x.calls("Write")) > 0 || len(x.calls("SendKeys")) > 0 || x.startCall() != "" {
			t.Errorf("nothing typed or started: %q", x.f.Calls())
		}
	}

	stat("T", syscall.SIGSTOP)
	x := setup(t)
	atSh(x)
	x.shell("w1:p3", "pj-l-01", "")
	out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	if ok {
		t.Fatal("restarted next to a suspended agent:\n" + out)
	}
	eq(t, last(out), "FAIL pj-l-01 is suspended in pane w1:p3 (Ctrl+Z): fg it and /exit, or close the pane", "restart --fresh")
	contains(t, out, fmt.Sprintf("(pid %d) is stopped", sleep), "the stopped child named")
	nothing(x)
	x.sid("pj-l-01", "sid-1")
	out, _ = x.restart(launch.Request{Agent: "pj-l-01"})
	eq(t, last(out), "FAIL pj-l-01 is suspended in pane w1:p3 (Ctrl+Z): fg it and /exit, or close the pane", "restart, same session")
	out, _ = x.stop("pj-l-01", "", false)
	eq(t, last(out), "FAIL pj-l-01 is suspended in pane w1:p3 (Ctrl+Z): fg it and /exit, or close the pane", "stop")
	nothing(x)

	// The same child running (not stopped) at the shell: starts, as before.
	stat("SRI", syscall.SIGCONT)
	x = setup(t)
	atSh(x)
	x.shell("w1:p3", "pj-l-01", "")
	out, _ = x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	restarted(t, last(out), "pj-l-01", "w1:p3", "new")

	// No child the shell stopped (the fake herdr's shell, pid 1, with the real ps): starts.
	x = setup(t)
	x.ps = nil
	x.shell("w1:p3", "pj-l-01", "")
	out, _ = x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	restarted(t, last(out), "pj-l-01", "w1:p3", "new")

	// ps cannot say (denied, as in a sandbox): a warning, and it starts.
	x = setup(t)
	x.ps = func(...string) ([]byte, error) { return nil, errors.New("operation not permitted") }
	x.shell("w1:p3", "pj-l-01", "")
	out, _ = x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	contains(t, out, "warning: cannot check pane w1:p3 for a suspended agent (ps: operation not permitted)", "warned")
	restarted(t, last(out), "pj-l-01", "w1:p3", "new")

	// An agent in the foreground: ps is not asked.
	x = setup(t)
	asked := 0
	x.ps = func(...string) ([]byte, error) { asked++; return []byte("1 T 9 claude\n"), nil }
	x.agent("w1:p3", "pj-l-01", "claude", "idle")
	out, _ = x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
	restarted(t, last(out), "pj-l-01", "w1:p3", "new")
	eqInt(t, asked, 0, "ps asked only at a shell")
}

// a recorded session resumes from the transcript its SessionStart hook reported: obor derives no path from the
// cwd, so a symlinked or long lane path, or a pane whose cwd moved, resumes it. A transcript that is gone was never
// prompted: fresh, said. A record with no path yet (obor named the session, no hook confirmed it) resumes when
// the kind's own file of that id is somewhere. Codex's sessions are not obor's: not checked.
func TestRestartRecordedSession(t *testing.T) {
	t.Parallel()
	t.Run("the hook's path: resumes, whatever the pane's cwd", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		path := x.transcript("sid-x")
		x.recordAt("pj-l-01", "sid-x", path)
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		contains(t, out, "resuming sid-x (from record)", "source printed")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-x")
		lacks(t, out, "is not from", "no cwd refusal")
		if !ok {
			t.Fatal(out)
		}
	})
	t.Run("a symlinked lane cwd: resumes, no not-from-cwd refusal", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		link := filepath.Join(x.p.Base, "lane-link")
		if err := os.Symlink(x.lane, link); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(x.claude, "projects", "a-long-hashed-project-dir", "sid-s.jsonl")
		x.p.Write(path, "")
		x.recordAt("pj-l-01", "sid-s", path)
		x.shell("w1:p3", "pj-l-01", link)
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "resuming sid-s (from record)", "the recorded path, not a slug of the symlink")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-s")
		lacks(t, out, "is not from", "no cwd refusal")
	})
	t.Run("a transcript gone: fresh, said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.recordAt("pj-l-01", "sid-y", filepath.Join(x.claude, "projects", "-gone", "sid-y.jsonl"))
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "recorded session sid-y has no transcript ("+filepath.Join(x.claude, "projects", "-gone", "sid-y.jsonl")+"): starting fresh", "said")
		restarted(t, last(out), "pj-l-01", "w1:p3", "new")
		lacks(t, x.startCall(), "--resume", "no resume")
	})
	t.Run("no recorded path, the kind's file of that id: resumes", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.transcript("sid-z") // the hook has not filled the path in yet; the file is Claude's
		x.record("pj-l-01", "sid-z")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-z")
		lacks(t, out, "starting fresh", "no fresh start")
	})
	t.Run("config dir with glob metacharacters, no recorded path: the search still finds it", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.claude = filepath.Join(x.p.Base, "cl[aude")
		x.transcript("sid-g")
		x.record("pj-l-01", "sid-g")
		if _, err := os.Stat(filepath.Join(x.claude, "projects")); err != nil {
			t.Fatalf("precondition: the transcript exists under the bracketed dir: %v", err)
		}
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-g")
	})
	t.Run("Codex's sessions are not obor's: not checked", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		recordSessions(t, x.p.Root, store.Session{Agent: "pj-rev", Kind: "codex", ID: "cx-1", Started: time.Now().Unix()})
		x.shell("w1:p7", "pj-rev", "")
		out, ok := x.restart(launch.Request{Agent: "pj-rev"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "recorded session cx-1 not checked (codex's sessions are not obor's)", "said")
		contains(t, x.startCall(), "argv=[resume cx-1", "codex resumes its record")
	})
}

// the session rule, restart and restore, as the state model's row 51 says it. A recorded session
// resumes from the transcript the session itself reported; none anywhere means it was never prompted (Claude writes
// no transcript before the first prompt), so the restart starts fresh, said. A pane with no recorded session starts
// fresh in restore, role or worker (a role started fresh with no prompt has no transcript either).
func TestSessionResumeRule(t *testing.T) {
	t.Parallel()
	t.Run("restart: a confirmed transcript: resumed", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
		contains(t, x.startCall(), "--resume sid-1", "the transcript's session")
		lacks(t, out, "starting fresh", "no fresh start")
	})
	t.Run("restart: a transcript under another project's dir: resumed (the hook's path)", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		path := filepath.Join(x.claude, "projects", "-users-someone-other-project", "sid-x.jsonl")
		x.p.Write(path, "")
		x.recordAt("pj-l-01", "sid-x", path)
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-x")
		lacks(t, out, "is not from", "no cwd refusal")
	})
	t.Run("restart: unreadable projects dir, no recorded path: refused, never fresh", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-l-01", "sid-x")
		projects := filepath.Join(x.claude, "projects")
		if err := os.MkdirAll(projects, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(projects, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(projects, 0o755) })
		if _, err := os.ReadDir(projects); err == nil {
			t.Skip("the projects dir is readable despite mode 0 (root)")
		}
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok {
			t.Fatal(out)
		}
		contains(t, last(out), "FAIL recorded session sid-x: cannot", "refused with the error")
		lacks(t, out, "starting fresh", "never fresh")
		if x.startCall() != "" {
			t.Errorf("nothing started")
		}
	})
	t.Run("restart: no transcript anywhere (never prompted): fresh, said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-l-01", "sid-x")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "recorded session sid-x has no transcript (never prompted): starting fresh", "said")
		restarted(t, last(out), "pj-l-01", "w1:p3", "new")
		lacks(t, x.startCall(), "--resume", "no resume")
	})
	t.Run("restore: a role with no recorded session: fresh, said", func(t *testing.T) {
		t.Parallel()
		f := newRfx(t)
		f.p.Config(testkit.Set("agents.roles", `{"reviewer": "opus"}`))
		rev := f.p.PlainLane("review", "review")
		f.shell("w1:p7", rev, "pj-rev")
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-rev --pane w1:p7 --fresh")
		has(t, f.restoreLog(), " restore pj-rev (pane w1:p7) in "+f.p.Root+" (reviewer, no recorded session: --fresh)",
			"the fresh start is logged")
	})
	t.Run("restore: a worker of a busy lane with no recorded session: fresh, as before", func(t *testing.T) {
		t.Parallel()
		f := newRfx(t)
		lane := f.p.PlainLane("L-01", "task/T-01")
		f.p.Config()
		f.p.State("L-01", `{"task":"T-01","branch":"task/T-01","agent":"pj-l-01"}`) // its row holds T-01, not landed: busy
		f.shell("w1:p1", lane, "pj-l-01")
		f.pl.RestoreProject(context.Background(), f.p.Root, port.Lost{All: true})
		f.wantCalls(f.p.Root + " restart pj-l-01 --pane w1:p1 --fresh")
		log := f.restoreLog()
		has(t, log, " restore pj-l-01 (pane w1:p1) in "+f.p.Root+" (worker, no recorded session: --fresh)",
			"the fresh start is logged")
		lacks(t, log, "skip pj-l-01", "the busy lane's worker is not skipped")
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
		x.agent("w1:p3", "pj-l-01", "claude", "idle")
		out, ok := x.stop("pj-l-01", "", false)
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		eq(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p3 submit=true text=/exit", "only /exit typed")
		if !ok || x.startCall() != "" || x.f.All[0].Label != "pj-l-01" || x.f.All[0].Kind != "" {
			t.Errorf("stopped, labelled, not started: %+v %q", x.f.All[0], x.f.Calls())
		}
	})
	t.Run("at a shell already", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.stop("pj-l-01", "", false)
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3, already at a shell)", "result")
		if !ok || len(x.calls("Write")) > 0 {
			t.Error("nothing typed")
		}
	})
	// no "left running": a working agent is interrupted with Escape and quits once idle, force or not.
	// the idle wait is the terminal's WaitStatus (idle, done), one wait instead of a poll of the pane.
	t.Run("working, no force: Escape, wait idle, /exit, STOPPED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-l-01", "claude", "working")
		out, ok := x.stop("pj-l-01", "", false)
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		esc, wait, exit := x.index("SendKeys keys=[esc] pane=w1:p3"), x.index("WaitStatus"), x.index("Write pane=w1:p3 submit=true text=/exit")
		if !ok || esc < 0 || wait < esc || exit < wait || x.f.All[0].Kind != "" {
			t.Errorf("Escape, then WaitStatus, then /exit, the agent gone: %+v %q", x.f.All[0], x.f.Calls())
		}
		waits := x.calls("WaitStatus")
		if len(waits) != 1 {
			t.Fatalf("one idle wait, got %q", waits)
		}
		contains(t, waits[0], "pane=w1:p3 timeout=10s until=[idle done]", "the idle wait's agent and states")
	})
	t.Run("working, no force, the agent leaves in the idle wait: done, /exit, STOPPED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-l-01", "claude", "working")
		x.wrap = func(h control) control {
			return waitGone{h, termError("wait", "", "the agent left w1:p3")}
		}
		out, ok := x.stop("pj-l-01", "", false)
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		if !ok || len(x.calls("Write pane=w1:p3 submit=true text=/exit")) != 1 {
			t.Errorf("the agent gone ends the idle wait, then /exit: %q", x.f.Calls())
		}
	})
	t.Run("working, no force, Escape ignored: FAIL naming --force, no exit typed", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		x.f.Set(a.ID, func(p *mpane) { p.OnEsc = "ignore" })
		out, ok := x.stop("pj-l-01", "", false)
		if len(x.calls("SendKeys keys=[esc] pane=w1:p3")) == 0 {
			t.Fatalf("precondition: Escape sent: %q", x.f.Calls())
		}
		eq(t, failLine(out), "FAIL pj-l-01 still working 60ms after Escape (--force stops it)", "result")
		if ok || len(x.calls("text=/exit")) > 0 || x.f.All[0].Kind != "claude" {
			t.Errorf("not stopped, no /exit typed: %+v %q", x.f.All[0], x.f.Calls())
		}
		if n := len(x.calls("WaitStatus")); n != 1 {
			t.Errorf("%d idle waits, want the one herdr timed out", n)
		}
		// serve's drop refuses on this refusal alone: port.NeedsForce reads it.
		if _, err := x.launcher(io.Discard).Stop(context.Background(), "pj-l-01", "", false); !port.NeedsForce(err) {
			t.Errorf("the refusal %v is no stop only force ends", err)
		}
	})
	t.Run("working, --force: Escape, idle, input cleared, /exit (one step)", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		// a background task: --force stops it too
		x.f.Set(a.ID, func(p *mpane) {
			p.Procs = append(p.Procs, port.Process{PID: 300, Argv0: "/bin/zsh"})
		})
		out, ok := x.stop("pj-l-01", "", true)
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		esc, clr, exit := x.index("SendKeys keys=[esc] pane=w1:p3"), x.index("SendKeys keys=[ctrl+c] pane=w1:p3"), x.index("Write pane=w1:p3 submit=true text=/exit")
		if !ok || esc < 0 || esc > clr || clr > exit {
			t.Errorf("Escape, then ctrl+c (the restored prompt cleared), then /exit: %q", x.f.Calls())
		}
		lacks(t, out, "warning", "it went idle")
	})
	t.Run("blocked, --force, Escape ignored: exits anyway after the idle wait", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		a := x.agent("w1:p3", "pj-l-01", "claude", "blocked")
		x.f.Set(a.ID, func(p *mpane) { p.OnEsc = "ignore" })
		out, ok := x.stop("pj-l-01", "", true)
		contains(t, out, "warning: pj-l-01 still blocked 60ms after Escape: exiting anyway", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		_ = ok
	})
	t.Run("--force, never exits, no agent process seen: FAIL, no signal", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		a := x.agent("w1:p3", "pj-l-01", "claude", "working")
		// herdr lists nothing in the group: no exit, no leader
		x.f.Set(a.ID, func(p *mpane) { p.OnQuit, p.Procs = "stay", []port.Process{} })
		out, ok := x.stop("pj-l-01", "", true)
		eq(t, failLine(out), `FAIL pj-l-01 did not exit in 60ms (pane w1:p3; not terminated: the group's leader (pid 2) is "", not claude)`, "result")
		eqInt(t, len(x.kills), 0, "no signal")
		if ok {
			t.Error("failed")
		}
	})
}

// a stop whose agent never goes idle and never quits is terminated within the stop's one bound (the idle wait,
// the quit wait and the terminate together; the harness's own path to the stop is not part of it): at the default quit
// wait, and with OBOR_RESTART_WAIT set larger (the override is capped, so the bound holds).
func TestStopBound(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, env string
	}{
		{"the default quit wait", ""},
		{"an OBOR_RESTART_WAIT larger than the bound", "3600"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.bound, x.idle, x.waitZero, x.waitEnv = time.Second, shortWait, true, c.env
			p := x.agent("w1:p3", "pj-l-01", "claude", "working")
			x.f.Set(p.ID, func(p *mpane) { p.OnEsc, p.OnQuit = "ignore", "stay" })
			x.onKill = func(_ int, sig syscall.Signal) {
				if sig == syscall.SIGTERM { // the signal ends it at once
					x.f.Set(p.ID, func(p *mpane) {
						p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil
					})
					x.gone["2"] = true
				}
			}
			start := time.Now()
			out, ok := x.stop("pj-l-01", "", true)
			elapsed := time.Since(start)
			if !ok {
				t.Fatalf("the stop of an agent that never quits did not end: %s", out)
			}
			contains(t, out, "terminated pj-l-01 (claude pid 2, SIGTERM)", "the stop terminated it")
			// The bound covers the stop's own phases (stop.go); the path to them (resolve, the project lock, herdr
			// round trips) is outside it, and the gate's load can stall a goroutine past a poll: a loaded machine must
			// slow this test, not fail it (the gate saw 1.012s here). Twice the bound is still far under the
			// 20 s default and the 3600 s env wait, so an uncapped stop still fails by far.
			if elapsed > 2*x.bound {
				t.Errorf("the stop took %s, past twice its bound %s (the bound covers the stop's phases only): %s",
					elapsed, x.bound, out)
			}
		})
	}
}

// an agent that ignores /exit. Without --force: FAIL with the hint. With --force, while it provably still leads
// the pane's foreground group (same shell, same leader pid, claude/codex): SIGTERM, then SIGKILL, then the leftover
// check and the start as for an agent that exited. Not proven: no signal, FAIL with evidence.
func TestStopForceTerminates(t *testing.T) {
	t.Parallel()
	// ignoring: an idle Claude leading group 2 with uvx, that ignores /exit; a signal in dies (pid 2 gone, unlisted)
	// takes the leader, and with leftovers the uvx stays until the next signal.
	ignoring := func(x *fx, dies syscall.Signal, leftovers bool) *mpane {
		p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
		x.f.Set(p.ID, func(p *mpane) {
			p.OnQuit = "stay"
			p.Procs = []port.Process{{PID: 2, Argv0: "/Users/u/.local/bin/claude"}, {PID: 300, Argv0: "uvx"}}
		})
		x.onKill = func(_ int, sig syscall.Signal) {
			switch {
			case x.gone["2"]:
				x.f.Set(p.ID, func(p *mpane) {
					p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil // the leftovers go too
				})
			case sig == dies && leftovers:
				x.f.Set(p.ID, func(p *mpane) { p.Procs = p.Procs[1:] })
				x.gone["2"] = true
			case sig == dies:
				x.f.Set(p.ID, func(p *mpane) {
					p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil
				})
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
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		contains(t, out, "in the group: 2 claude", "evidence")
		eqInt(t, len(x.kills), 0, "no signal")
	})
	t.Run("--force: SIGTERM ends it, STOPPED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		ignoring(x, syscall.SIGTERM, false)
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2 only")
		contains(t, out, "terminated pj-l-01 (claude pid 2, SIGTERM): it did not exit in 60ms\n", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("--force, SIGTERM ignored: SIGKILL, STOPPED", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		ignoring(x, syscall.SIGKILL, false)
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		contains(t, out, "terminated pj-l-01 (claude pid 2, SIGKILL): it did not exit in 60ms\n", "said")
	})
	t.Run("--force, restart: SIGTERM, leftovers stopped, then the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait // the stop's wait is the give-up under test
		x.up = bound       // the start's outcome, not a give-up
		x.sid("pj-l-01", "sid-1")
		ignoring(x, syscall.SIGTERM, true)
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Force: true})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 terminated", "TERM to the agent, then TERM to its leftovers")
		contains(t, out, "terminated pj-l-01 (claude pid 2, SIGTERM)", "agent terminated")
		contains(t, out, "stopped 1 leftover process(es) of pj-l-01 (uvx)", "leftover line")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
	})
	t.Run("--force, TERM and KILL ignored: FAIL with evidence, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		x.sid("pj-l-01", "sid-1")
		ignoring(x, 0, false)
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Force: true})
		if ok {
			t.Fatal("must fail")
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms, nor on SIGTERM and SIGKILL (pane w1:p3)", "result")
		contains(t, out, "agent status: idle\n", "evidence")
		eq(t, x.startCall(), "", "no start")
	})
	// What is not proven the agent's is never signalled: checked after the wait (no signal) and after TERM (no KILL).
	for _, c := range []struct {
		name      string
		when      string // "wait": changed before the first check; "term": after SIGTERM
		shell, fg int    // the pane's shell and foreground group from then on (0: unchanged)
		procs     []port.Process
		kills     string
		fail      string
	}{
		{name: "leader changed: no signal", when: "wait", procs: []port.Process{{PID: 2, Argv0: "vim"}},
			fail: `not terminated: the group's leader (pid 2) is "vim", not claude`},
		// Not listed is gone: the agent counts as exited, what is left is the leftovers' ladder's.
		{name: "leader gone from the list: exited, its leftovers signalled", when: "wait", procs: []port.Process{{PID: 300, Argv0: "uvx"}},
			kills: "2 terminated,2 killed", fail: "pj-l-01 exited, but its leftover process(es) (uvx) kept the foreground after SIGTERM and SIGKILL (pane w1:p3)"},
		{name: "shell changed: no signal", when: "wait", shell: 7,
			fail: "not terminated: the pane's shell is pid 7, not 1"},
		// another group has the foreground after the wait: the user's next job, never the agent's.
		{name: "foreground changed: no signal", when: "wait", fg: 5,
			fail: "not terminated: the foreground is group 5, not 2"},
		{name: "leader changed after TERM: no KILL", when: "term", procs: []port.Process{{PID: 2, Argv0: "vim"}},
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
					x.f.Set(p.ID, func(p *mpane) { p.Procs = c.procs })
				}
			}
			x.procInfo(func(p *mpane) {
				if !changed {
					return
				}
				if c.shell != 0 {
					p.Shell = c.shell
				}
				if c.fg != 0 {
					p.Foreground = c.fg
				}
			})
			if c.when == "wait" {
				x.onExit = change
			} else {
				x.onKill = func(int, syscall.Signal) { change() }
			}
			out, ok := x.stop("pj-l-01", "", true)
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
	control
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

func (s lockSpy) Write(ctx context.Context, pane, text string, submit bool) error {
	s.note("run")
	return s.control.Write(ctx, pane, text, submit)
}

func (s lockSpy) Start(ctx context.Context, a port.AgentStart) (port.Pane, error) {
	s.note("start")
	return s.control.Start(ctx, a)
}

// one restart lock (the lane's, else the agent's) held for the whole operation, freed at its end.
func TestOneRestartLock(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-l-01", "sid-1")
	x.agent("w1:p3", "pj-l-01", "claude", "idle")
	var seen []string
	lane, agent := launch.RestartLock(x.p.Root, "L-01", ""), launch.RestartLock(x.p.Root, "", "pj-int")
	x.wrap = func(h control) control { return lockSpy{h, []*project.Lock{lane, agent}, &seen} }
	out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
	if !ok {
		t.Fatal(out)
	}
	eq(t, strings.Join(seen, " "), "run:restart-L-01.lock run:restart-L-01.lock start:restart-L-01.lock",
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

// A lane's drive agent is its lane's (config.AgentLookup): its restart and its stop hold the lane's restart lock, not
// one of its own name, and its env line is the driver's in the lane, not a worker's nor an unknown agent's.
func TestDriveNameTakesItsLanesRestartLock(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-l-01-drive", "sid-1")
	x.agent("w1:p3", "pj-l-01-drive", "claude", "idle")
	var seen []string
	lane, own := launch.RestartLock(x.p.Root, "L-01", ""), launch.RestartLock(x.p.Root, "", "pj-l-01-drive")
	x.wrap = func(h control) control { return lockSpy{h, []*project.Lock{lane, own}, &seen} }
	out, ok := x.restart(launch.Request{Agent: "pj-l-01-drive"})
	if !ok {
		t.Fatal(out)
	}
	eq(t, strings.Join(seen, " "), "run:restart-L-01.lock run:restart-L-01.lock start:restart-L-01.lock",
		"the lane's restart lock held at the /exit, the env line and the start")
	env := strings.Join(x.calls("Write pane=w1:p3"), "\n")
	has(t, env, "export OBOR_ROLE=driver OBOR_LANE=L-01 OBOR_PANE=w1:p3", "the drive's env line")

	seen = nil
	x.reset()
	x.agent("w1:p3", "pj-l-01-drive", "claude", "idle")
	if out, ok := x.stop("pj-l-01-drive", "", false); !ok {
		t.Fatal(out)
	}
	eq(t, strings.Join(seen, " "), "run:restart-L-01.lock", "the stop: the lane's lock")
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

// two restarts of one lane: the second waits (saying so) until the first frees the lane's restart lock, and
// touches the pane only then.
func TestConcurrentRestartWaits(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-l-01", "sid-1")
	x.agent("w1:p3", "pj-l-01", "claude", "idle")
	first := launch.RestartLock(x.p.Root, "L-01", "") // the restart already running (another obor process)
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
		line, err := l.Restart(context.Background(), launch.Request{Agent: "pj-l-01"})
		done <- res{line, err}
	}()
	// Wait for the second restart's waiting line: it is what proves it reached the lock (under load, not within any
	// fixed sample of the test).
	said := "waiting for the restart of lane L-01 (pid " + strconv.Itoa(os.Getpid()) + ")\n"
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
	eqInt(t, len(x.calls("Write")), 0, "nothing typed while it waits")
	eqInt(t, len(x.calls("Start")), 0, "nothing started while it waits")
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
	restarted(t, r.line, "pj-l-01", "w1:p3", "sid-1")
	eqInt(t, len(x.calls("Start")), 1, "started once the first ended")
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
	control
	ops map[string]bool
	why error
}

func (f failCalls) Screen(ctx context.Context, pane string, lines int) (string, error) {
	if f.ops["screen"] {
		return "", f.why
	}
	return f.control.Screen(ctx, pane, lines)
}

func (f failCalls) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	if f.ops["process-info"] {
		return port.ProcessInfo{}, f.why
	}
	return f.control.ProcessInfo(ctx, pane)
}

// a give-up carries its evidence: the agent's status, the pane's last non-empty lines, the processes.
func TestGiveUpCarriesEvidence(t *testing.T) {
	t.Parallel()
	stuck := func(x *fx) {
		x.sid("pj-l-01", "sid-1")
		p := x.agent("w1:p3", "pj-l-01", "claude", "done")
		var lines []string
		for i := 1; i <= 20; i++ {
			lines = append(lines, "screen line "+strings.Repeat("x", i%3), "", "   ")
		}
		x.f.Set(p.ID, func(p *mpane) {
			p.OnQuit = "stay"
			p.Screen = strings.Join(lines, "\n") + "\ndraft in the box"
			p.Foreground, p.Procs = 200, []port.Process{{PID: 200, Argv0: "/usr/bin/claude", Name: "claude", Cmdline: "claude --resume sid-1"},
				{PID: 301, Argv0: "/usr/bin/sleep", Cmdline: "/usr/bin/sleep 60"}}
		})
	}
	t.Run("never exits: FAIL line, then status, screen tail, processes; the same block in launch.log", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		stuck(x)
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok {
			t.Fatal("must fail")
		}
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "the one-line result is last")
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
		contains(t, string(log), " pj-l-01 pane w1:p3: pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)\nagent status: done\n", "log head")
		contains(t, string(log), "in the group: 301 /usr/bin/sleep 60", "log carries the processes")
		eqInt(t, strings.Count(string(log), "did not exit"), 1, "one block per failure")
	})
	t.Run("stop: the same evidence", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		stuck(x)
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		contains(t, out, "agent status: done\n", "stop gives up the same way")
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "the one-line result is last")
		contains(t, out, "pane processes:", "processes")
	})
	t.Run("herdr cannot read the screen or the processes: FAIL still printed, the gaps said", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		stuck(x)
		x.wrap = func(h control) control {
			return failCalls{h, map[string]bool{"screen": true}, errors.New("herdr went away")}
		}
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok {
			t.Fatal("must fail")
		}
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "FAIL line last")
		contains(t, out, "(screen unavailable: ", "screen gap")
		contains(t, out, "herdr went away", "why")
		contains(t, out, "foreground: claude", "processes still given")

		x.wrap = func(h control) control {
			return failCalls{h, map[string]bool{"process-info": true}, errors.New("herdr went away")}
		}
		out, _ = x.restart(launch.Request{Agent: "pj-l-01"})
		contains(t, out, "(process info unavailable: ", "process gap")
		contains(t, out, "  draft in the box", "screen still given")
	})
	t.Run("start failure at a startup dialog carries it too", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Once("Start", notReady())
		out, _ := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		contains(t, out, "agent status:", "evidence")
		eq(t, last(out), "FAIL start failed in w1:p3 (pj-l-01 waits at a startup dialog in pane w1:p3: answer it there)", "FAIL last")
		contains(t, out, "pane screen (last 15 non-empty lines):", "screen")
	})
}

// exitHook runs fn after each /exit typed into a pane.
type exitHook struct {
	control
	fn func()
}

func (e exitHook) Write(ctx context.Context, pane, text string, submit bool) error {
	err := e.control.Write(ctx, pane, text, submit)
	if text == "/exit" {
		e.fn()
	}
	return err
}

// procFix sets a pane's process info before each look (the fake's shell pid and foreground group).
type procFix struct {
	control
	fix func(pane string)
}

func (p procFix) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	p.fix(pane)
	return p.control.ProcessInfo(ctx, pane)
}

// procInfo sets pane's process info before each look (its shell pid and foreground group).
func (x *fx) procInfo(fix func(p *mpane)) {
	x.wrap = func(h control) control {
		return procFix{h, func(pane string) { x.f.Set(pane, fix) }}
	}
}

// quitTyped sets quit when /exit is typed into a pane (before the terminal acts on it).
func (x *fx) quitTyped(quit *atomic.Bool) {
	x.f.Before(func(call string) {
		if strings.HasPrefix(call, "Write ") && strings.HasSuffix(call, " text=/exit") {
			quit.Store(true)
		}
	})
}

// after /exit the agent (the foreground group's leader) is gone while its MCP servers keep the group in the
// foreground: the exit is the leader's pid gone; the leftovers get SIGTERM, then SIGKILL once; never the shell's group.
func TestStopLeftoverMCP(t *testing.T) {
	t.Parallel()
	mcp := leftoverMCP
	t.Run("leader exits, shell gets the foreground: no kill", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.agent("w1:p3", "pj-l-01", "claude", "idle")
		out, ok := x.stop("pj-l-01", "", false)
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
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
				x.f.Set(p.ID, func(p *mpane) { p.Foreground = 1 })
			}
		}
		out, ok := x.stop("pj-l-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to pgid 2 only")
		contains(t, out, "stopped 2 leftover process(es) of pj-l-01 (uvx, python)\n", "leftover line")
		contains(t, out, "stopped claude in pane w1:p3", "stopped line")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("TERM ignored: KILL once, then the shell", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := mcp(x)
		x.onKill = func(pgid int, sig syscall.Signal) {
			if sig == syscall.SIGKILL {
				x.f.Set(p.ID, func(p *mpane) { p.Foreground = 1 })
			}
		}
		out, ok := x.stop("pj-l-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		contains(t, out, "stopped 2 leftover process(es) of pj-l-01 (uvx, python)", "leftover line")
	})
	t.Run("TERM and KILL ignored: FAIL with evidence", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		mcp(x)
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated,2 killed", "TERM, then KILL once")
		eq(t, last(out), "FAIL pj-l-01 exited, but its leftover process(es) (uvx, python) kept the foreground after SIGTERM and SIGKILL (pane w1:p3)", "result")
		contains(t, out, "agent status: idle\n", "evidence")
		contains(t, out, "in the group: 300 uvx", "evidence: processes")
		lacks(t, out, "stopped 2 leftover", "no success line")
	})
	// the leftovers exit by themselves while the foreground is still their (now empty) group, before the
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
				x.onExit = func() {
					x.f.Set(p.ID, func(p *mpane) { p.Procs = []port.Process{} })
					x.gone["2"] = true
				}
			}
			x.onKill = func(_ int, sig syscall.Signal) {
				if sig == c.after {
					x.f.Set(p.ID, func(p *mpane) { p.Procs = []port.Process{} })
				}
			}
			out, ok := x.stop("pj-l-01", "", false)
			if !ok {
				t.Fatal(out)
			}
			eqInt(t, p.Foreground, 0, "precondition: the foreground stays group 2")
			contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
			eq(t, strings.Join(x.kills, ","), c.kills, "signals")
			if c.after == 0 {
				lacks(t, out, "leftover", "nothing stopped")
			} else {
				contains(t, out, "stopped 2 leftover process(es) of pj-l-01 (uvx, python)\n", "leftover line")
			}
			eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		})
	}
	t.Run("restart: leftovers stopped, then the start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait // the exit wait is given up on (the leftovers keep the foreground)
		x.up = bound       // the start's outcome, not a give-up: 60ms failed under the gate's load
		x.sid("pj-l-01", "sid-1")
		p := mcp(x)
		x.onKill = func(int, syscall.Signal) { x.f.Set(p.ID, func(p *mpane) { p.Foreground = 1 }) }
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to the group")
		contains(t, out, "stopped 2 leftover process(es) of pj-l-01 (uvx, python)", "leftover line")
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-1")
	})
	t.Run("leader still alive after the wait: FAIL as today, no kill", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.wait = shortWait
		mcp(x)
		x.onExit = nil // the leader never exits: listed and alive throughout
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
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
		x.procInfo(func(p *mpane) {
			if quit.Load() {
				p.Shell, p.Foreground = 2, 9
			}
		})
		out, ok := x.stop("pj-l-01", "", false)
		if ok || !quit.Load() {
			t.Fatalf("must fail after /exit:\n%s", out)
		}
		eq(t, last(out), "FAIL pj-l-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the pane's shell is pid 2, not 1)", "result")
		eqInt(t, len(x.kills), 0, "shell's group not killed")

		// The foreground group is 1 (shell 5): not recorded, the wait is today's.
		y := setup(t)
		y.wait = shortWait
		mcp(y)
		y.gone["1"] = true
		q := &atomic.Bool{}
		y.quitTyped(q)
		y.procInfo(func(p *mpane) { p.Shell, p.Foreground = 5, 1 })
		out, ok = y.stop("pj-l-01", "", false)
		if ok || !q.Load() {
			t.Fatalf("must fail after /exit:\n%s", out)
		}
		eq(t, last(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		eqInt(t, len(y.kills), 0, "group 1 not killed")
	})
}

// leftoverMCP: an idle Claude in w1:p3 leading group 2 (listed, alive) that exits on /exit, its pid gone and no
// longer listed, but leaves uvx and python in its group (one consistent process model).
func leftoverMCP(x *fx) *mpane {
	p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
	x.f.Set(p.ID, func(p *mpane) {
		p.OnQuit = "stay"
		p.Procs = []port.Process{{PID: 2, Argv0: "claude"}, {PID: 300, Argv0: "/usr/local/bin/uvx"}, {PID: 301, Name: "python"}}
	})
	x.onExit = func() {
		x.f.Set(p.ID, func(p *mpane) { p.Procs = p.Procs[1:] })
		x.gone["2"] = true
	}
	return p
}

// no signal to a group not proven to be the exited agent's leftovers. Process info that cannot be
// read proves nothing; the leader still listed, a changed foreground or shell refuse the signal, before TERM and
// before KILL.
func TestStopUnprovenLeftovers(t *testing.T) {
	t.Parallel()
	// unreadable makes herdr's process info fail from then on.
	unreadable := func(x *fx) {
		x.f.Fail("ProcessInfo", termError("process info", "", "denied"))
	}
	// /exit does nothing (claude and uvx stay in group 2), and process info is unreadable after it.
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("process info unreadable during the exit wait, force %v: no kill, FAIL with evidence", force), func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.wait = shortWait
			p := x.agent("w1:p3", "pj-l-01", "claude", "idle")
			x.f.Set(p.ID, func(p *mpane) {
				p.OnQuit = "stay"
				p.Procs = []port.Process{{PID: 2, Argv0: "claude"}, {PID: 300, Argv0: "uvx"}}
			})
			x.onExit = func() { unreadable(x) }
			out, ok := x.stop("pj-l-01", "", force)
			if ok {
				t.Fatal("must fail:\n" + out)
			}
			contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
			want := "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)"
			if force {
				want = "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; not terminated: process info unavailable: "
			}
			contains(t, last(out), want, "result")
			eqInt(t, len(x.kills), 0, "no kill")
			contains(t, out, "pane processes:\n  (process info unavailable: ", "evidence: process info error")
		})
	}
	t.Run("restart, process info unreadable after the exit: no kill, no start", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		leftoverMCP(x)
		exit := x.onExit
		x.onExit = func() { exit(); unreadable(x) }
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		contains(t, strings.Join(x.f.Calls(), "\n"), "Write pane=w1:p3 submit=true text=/exit", "/exit sent")
		contains(t, last(out), "FAIL pj-l-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: process info unavailable: ", "result")
		eqInt(t, len(x.kills), 0, "no kill")
		eq(t, x.startCall(), "", "no start")
	})
	t.Run("leader dead but still listed in process info: no kill", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		x.onExit = func() { x.gone["2"] = true } // the pid is gone, process info still lists it
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, last(out), "FAIL pj-l-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the agent (pid 2) is still in the group)", "result")
		eqInt(t, len(x.kills), 0, "no kill")
		contains(t, out, "agent status: idle", "evidence")
	})
	t.Run("foreground moves to another group after TERM: no KILL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		p := leftoverMCP(x)
		x.onKill = func(_ int, sig syscall.Signal) {
			if sig == syscall.SIGTERM {
				x.f.Set(p.ID, func(p *mpane) {
					p.Foreground, p.Procs = 9, []port.Process{{PID: 9, Argv0: "other-program"}}
				})
			}
		}
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM only")
		eq(t, last(out), "FAIL pj-l-01 exited, but its leftover process(es) (uvx, python) kept the foreground after SIGTERM (pane w1:p3; no SIGKILL: the foreground is group 9, not 2)", "result")
		contains(t, out, "in the group: 9 other-program", "evidence")
	})
	t.Run("process info unreadable after TERM: no KILL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		x.onKill = func(int, syscall.Signal) { unreadable(x) }
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM only")
		contains(t, last(out), "FAIL pj-l-01 exited, but its leftover process(es) (uvx, python) kept the foreground after SIGTERM (pane w1:p3; no SIGKILL: process info unavailable: ", "result")
	})
	// The shell is pid 1 until /exit, then shell (fg stays group 2), and from TERM on afterTerm.
	shellChange := func(x *fx, shell, afterTerm int) {
		var quit, termed atomic.Bool
		x.onKill = func(int, syscall.Signal) { termed.Store(true) }
		x.quitTyped(&quit)
		x.procInfo(func(p *mpane) {
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
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), "FAIL pj-l-01 exited, but the pane's shell did not get the foreground back (pane w1:p3; not signalled: the pane's shell is pid 7, not 1)", "result")
	})
	t.Run("shell identity changes after TERM: no KILL", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		leftoverMCP(x)
		shellChange(x, 1, 7)
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail:\n" + out)
		}
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM only")
		contains(t, last(out), "(pane w1:p3; no SIGKILL: the pane's shell is pid 7, not 1)", "result")
	})
}

// The recorded session is the agent's newest of its own, and a record whose transcript is gone starts fresh whatever
// herdr's live session; the record is never rewritten from herdr. (The record over herdr's live session, herdr's when
// none is recorded and --fresh: TestRestartSessionDecision's rows.)
func TestRestartRecordOverLiveSession(t *testing.T) {
	t.Parallel()
	record := func(x *fx) string { return x.recorded("pj-l-01") + "\n" }
	t.Run("two of its own records, a newer one of another agent: the newest of its own resumed", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		now := time.Now()
		path := x.transcript("sid-2")
		recordSessions(t, x.p.Root,
			store.Session{Agent: "pj-l-01", ID: "sid-1", Started: now.Add(-time.Hour).Unix()},
			store.Session{Agent: "pj-l-01", ID: "sid-2", Transcript: path, Started: now.Unix()},
			store.Session{Agent: "pj-l-02", ID: "sid-3", Started: now.Add(time.Hour).Unix()})
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		restarted(t, last(out), "pj-l-01", "w1:p3", "sid-2")
		if !ok {
			t.Fatal(out)
		}
		contains(t, x.startCall(), "--resume sid-2", "the newest of its own resumed")
	})
	t.Run("the record's transcript is gone, herdr set: fresh, said, the record kept", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.record("pj-l-01", "sid-old")
		x.shell("w1:p3", "pj-l-01", "")
		x.f.Set("w1:p3", func(p *mpane) { p.Session = "sid-live" })
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "recorded session sid-old has no transcript (never prompted): starting fresh", "said")
		restarted(t, last(out), "pj-l-01", "w1:p3", "new")
		eq(t, record(x), "sid-old\n", "record kept")
	})
}

// popup is opencode's slash-command popup: it takes the Enter typed with /exit (nothing happens, the text stays in
// the input), and a separate Enter then runs the quit.
type popup struct {
	control
	enters *int
}

func (p popup) Write(ctx context.Context, pane, text string, submit bool) error {
	if text == "/exit" {
		return nil
	}
	return p.control.Write(ctx, pane, text, submit)
}

func (p popup) SendKeys(ctx context.Context, pane string, keys ...string) error {
	for _, k := range keys {
		if k == "enter" {
			*p.enters++
			return p.control.Write(ctx, pane, "/exit", true)
		}
	}
	return p.control.SendKeys(ctx, pane, keys...)
}

// slowAfterQuit holds the pane's process info past a short wait once the quit is typed: the exit poll's first look
// eats the whole wait, so the second Enter can only come from the give-up.
type slowAfterQuit struct {
	control
	quit *bool
}

func (s slowAfterQuit) Write(ctx context.Context, pane, text string, submit bool) error {
	if text == "/exit" {
		*s.quit = true
	}
	return s.control.Write(ctx, pane, text, submit)
}

func (s slowAfterQuit) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	if *s.quit {
		time.Sleep(2 * shortWait)
	}
	return s.control.ProcessInfo(ctx, pane)
}

// stop for every agent kind. The leader to prove is the program of the kind herdr sees running (not
// a configured kind); the quit is the kind's own (opencode: /exit, then a second Enter for the popup); a kind with
// no quit (no bridge: Cursor's rows went with) goes straight to the proven SIGTERM, without the wait.
func TestStopEveryKind(t *testing.T) {
	t.Parallel()
	// kind: lane L-01's agent of that kind (as herdr sees it); it runs as leader of group 2 and ignores the quit.
	kind := func(t *testing.T, kind string, leader port.Process) (*fx, *mpane) {
		x := setup(t)
		p := x.agent("w1:p3", "pj-l-01", kind, "idle")
		x.f.Set(p.ID, func(p *mpane) {
			p.OnQuit = "stay"
			p.Procs = []port.Process{leader}
		})
		x.onKill = func(_ int, sig syscall.Signal) {
			if sig == syscall.SIGTERM {
				x.f.Set(p.ID, func(p *mpane) {
					p.Kind, p.Name, p.Status, p.Foreground, p.Procs = "", "", "", 1, nil
				})
				x.gone["2"] = true
			}
		}
		return x, p
	}
	t.Run("opencode, quit ignored, --force: /exit and a second Enter, SIGTERM, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "opencode", port.Process{PID: 2, Argv0: "opencode"})
		x.wait = shortWait
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p3 submit=true text=/exit", "opencode's quit typed")
		eqInt(t, len(x.calls("SendKeys keys=[enter] pane=w1:p3")), 1, "one second Enter")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-l-01 (opencode pid 2, SIGTERM): it did not exit in 60ms\n", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("opencode, the popup takes the first Enter, no --force: the second Enter exits it, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "opencode", port.Process{PID: 2, Argv0: "opencode"})
		x.f.Set(p.ID, func(p *mpane) { p.OnQuit = "" })
		enters := 0
		x.wrap = func(h control) control { return popup{h, &enters} }
		out, ok := x.stop("pj-l-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, enters, 1, "the second Enter sent")
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	// Claude Code's slash-command menu (many plugin commands) takes the Enter: the second Enter submits.
	t.Run("claude, the slash-command menu takes the first Enter, no --force: the second Enter exits it, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", port.Process{PID: 2, Argv0: "claude"})
		x.f.Set(p.ID, func(p *mpane) { p.OnQuit = "" })
		enters := 0
		x.wrap = func(h control) control { return popup{h, &enters} }
		out, ok := x.stop("pj-l-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, enters, 1, "the second Enter sent")
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("opencode, a wait shorter than two polls, --force: the second Enter is sent before the give-up", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "opencode", port.Process{PID: 2, Argv0: "opencode"})
		x.wait = shortWait
		quit := false
		x.wrap = func(h control) control { return slowAfterQuit{h, &quit} }
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p3 submit=true text=/exit", "opencode's quit typed")
		eqInt(t, len(x.calls("SendKeys keys=[enter] pane=w1:p3")), 1, "the second Enter, from the give-up")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("leader opencode, herdr kind opencode in a claude lane, --force: proven by herdr's kind, SIGTERM, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", port.Process{PID: 2, Argv0: "opencode"})
		x.wait = shortWait
		// herdr sees opencode in the claude lane's pane
		x.f.Set(p.ID, func(p *mpane) { p.Kind = "opencode" })
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p3 submit=true text=/exit", "opencode's quit typed")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-l-01 (opencode pid 2, SIGTERM)", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	// the proof reads the kind herdr sees running, as the quit does, not the configured one.
	t.Run("retyped lane: configured opus, herdr kind codex, leader codex, quit ignored, --force: SIGTERM, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", port.Process{PID: 2, Argv0: "codex"})
		x.wait = shortWait
		// the old codex worker still runs in the lane now typed opus
		x.f.Set(p.ID, func(p *mpane) { p.Kind = "codex" })
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p3 submit=true text=/quit", "codex's quit typed")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-l-01 (codex pid 2, SIGTERM): it did not exit in 60ms\n", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("retyped lane, leader matching no known kind, --force: refused, no signal", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", port.Process{PID: 2, Argv0: "vim"})
		x.wait = shortWait
		x.f.Set(p.ID, func(p *mpane) { p.Kind = "codex" })
		out, ok := x.stop("pj-l-01", "", true)
		if ok {
			t.Fatal("must fail")
		}
		eq(t, strings.Join(x.calls("Write"), "\n"), "Write pane=w1:p3 submit=true text=/quit", "codex's quit typed")
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), `FAIL pj-l-01 did not exit in 60ms (pane w1:p3; not terminated: the group's leader (pid 2) is "vim", not codex)`, "result")
	})
	t.Run("a kind with no quit in a pane configured as claude: proven by herdr's kind, SIGTERM at once, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", port.Process{PID: 2, Argv0: "gemini"})
		x.f.Set(p.ID, func(p *mpane) { p.Kind = "gemini" })
		x.onExit = func() { t.Error("no quit typed") }
		out, ok := x.stop("pj-l-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, len(x.calls("Write")), 0, "nothing typed")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-l-01 (gemini pid 2, SIGTERM): it has no clean quit\n", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	// a popup kind that gives up leaves no quit in its input for the next typed message.
	for _, c := range []struct{ kind, argv0, clear string }{
		{"opencode", "opencode", "SendKeys keys=[esc] pane=w1:p3\nSendKeys keys=[ctrl+u] pane=w1:p3"},
	} {
		t.Run(c.kind+", the popup stays, no --force: the input cleared, FAIL says so, no signal", func(t *testing.T) {
			t.Parallel()
			x, _ := kind(t, c.kind, port.Process{PID: 2, Argv0: c.argv0})
			x.wait = shortWait
			out, ok := x.stop("pj-l-01", "", false)
			if ok {
				t.Fatal("must fail")
			}
			keys := x.calls("SendKeys")
			eqInt(t, len(keys), 5, "the clear before the quit, the second Enter, the clear again: "+strings.Join(keys, ", "))
			eq(t, strings.Join(keys[:2], "\n"), c.clear, "the clear keys before the quit")
			eq(t, keys[2], "SendKeys keys=[enter] pane=w1:p3", "the second Enter next")
			eq(t, strings.Join(keys[3:], "\n"), c.clear, "the clear keys after the wait")
			eqInt(t, len(x.kills), 0, "no signal")
			eq(t, failLine(out), "FAIL pj-l-01 did not exit in 60ms (pane w1:p3; the input was cleared; --force terminates it)", "result")
		})
	}
	// text a person typed into an idle agent's input is cleared before the quit, or the quit joins it.
	for _, c := range []struct{ kind, argv0, quit, clear string }{
		{"claude", "claude", "/exit", "ctrl+u"},
		{"codex", "codex", "/quit", "ctrl+u"},
		{"opencode", "opencode", "/exit", "esc"},
	} {
		t.Run(c.kind+", idle with text in the input: cleared before the quit, which then exits", func(t *testing.T) {
			t.Parallel()
			x, p := kind(t, c.kind, port.Process{PID: 2, Argv0: c.argv0})
			x.f.Set(p.ID, func(p *mpane) { p.OnQuit, p.Input = "", "t" })
			out, ok := x.stop("pj-l-01", "", false)
			if !ok {
				t.Fatal(out)
			}
			clr, quit := x.index("SendKeys keys=["+c.clear+"] pane=w1:p3"), x.index("Write pane=w1:p3 submit=true text="+c.quit)
			if clr < 0 || quit < 0 || clr > quit {
				t.Errorf("the clear key, then the quit: %q", x.f.Calls())
			}
			if p.Input != "" || p.Kind != "" {
				t.Errorf("input %q, kind %q: the quit must have run on an empty input", p.Input, p.Kind)
			}
			eqInt(t, len(x.calls("Write")), 1, "the quit typed once")
			eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
		})
	}
	t.Run("claude, working, --force: Escape then ctrl+c only, no ctrl+u (unchanged)", func(t *testing.T) {
		t.Parallel()
		x, p := kind(t, "claude", port.Process{PID: 2, Argv0: "claude"})
		x.f.Set(p.ID, func(p *mpane) { p.OnQuit, p.Status = "", "working" })
		out, ok := x.stop("pj-l-01", "", true)
		if !ok {
			t.Fatal(out)
		}
		eq(t, strings.Join(x.calls("SendKeys"), "\n"),
			"SendKeys keys=[esc] pane=w1:p3\nSendKeys keys=[ctrl+c] pane=w1:p3", "the keys of a working agent")
	})
	t.Run("a kind with no quit: nothing typed, SIGTERM at once, STOPPED", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "gemini", port.Process{PID: 2, Argv0: "gemini"})
		x.onExit = func() { t.Error("no quit typed") }
		out, ok := x.stop("pj-l-01", "", false)
		if !ok {
			t.Fatal(out)
		}
		eqInt(t, len(x.calls("Write")), 0, "nothing typed")
		eqInt(t, len(x.calls("SendKeys")), 0, "no clear key either: a kind without a clear sequence is unchanged")
		eq(t, strings.Join(x.kills, ","), "2 terminated", "TERM to group 2")
		contains(t, out, "terminated pj-l-01 (gemini pid 2, SIGTERM): it has no clean quit\n", "said")
		eq(t, last(out), "STOPPED pj-l-01 (pane w1:p3)", "result")
	})
	t.Run("a kind with no quit, leader not its program: refused, no signal", func(t *testing.T) {
		t.Parallel()
		x, _ := kind(t, "gemini", port.Process{PID: 2, Argv0: "node"})
		out, ok := x.stop("pj-l-01", "", false)
		if ok {
			t.Fatal("must fail")
		}
		eqInt(t, len(x.kills), 0, "no signal")
		eq(t, last(out), `FAIL pj-l-01 has no clean quit (pane w1:p3; not terminated: the group's leader (pid 2) is "node", not gemini)`, "result")
	})
}

// Launch reports the pane, kind and session herdr showed for the agent it started: serve's registry row
// of a start (app/registry.go).
func TestLaunchReportsIdentity(t *testing.T) {
	t.Parallel()
	x := setup(t)
	pane := x.shell("w1:p3", "pj-l-01", "")
	x.f.Set(pane.ID, func(p *mpane) { p.Session = "original-task-session" })
	s, err := x.launcher(&bytes.Buffer{}).Launch(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
	if err != nil {
		t.Fatalf("the start: %v", err)
	}
	restarted(t, s.Line, "pj-l-01", "w1:p3", "new")
	eq(t, s.Pane+" "+s.Kind+" "+s.Session, "w1:p3 claude original-task-session", "the identity the start reports")
}

// a fresh start of a kind whose sessions obor names carries --session-id <id>, and the id is in the store
// before the agent runs (the store and a restart know it before the SessionStart hook reports it). A resume carries
// none. A refused record is said, and the start keeps its id.
func TestStartNamesItsSession(t *testing.T) {
	t.Parallel()
	uuidRE := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	t.Run("fresh: the id in the argv, the store has it before the agent runs", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		var stored string
		x.wrap = func(h control) control {
			return startProbe{h, func() {
				s, _ := store.CurrentAt(x.p.Root, "pj-l-01")
				stored = s.ID
			}}
		}
		x.shell("w1:p3", "pj-l-01", "")
		var out bytes.Buffer
		var reported store.Session
		l := x.launcher(&out)
		l.Report = func(_ context.Context, s store.Session) error { // serve's session fact
			reported = s
			recordSessions(t, x.p.Root, s)
			return nil
		}
		started, err := l.Launch(context.Background(), launch.Request{Agent: "pj-l-01", Fresh: true})
		if _, ok := result(t, &out, started.Line, err); !ok {
			t.Fatal(out.String())
		}
		if p := x.shell("w1:p3", "pj-l-01", ""); p.Session != "" {
			t.Fatalf("precondition: herdr reports session %q for the pane, want none", p.Session)
		}
		eq(t, started.Session, reported.ID, "the start's session, herdr reporting none, is the id obor named")
		if reported.Agent != "pj-l-01" || reported.Kind != "claude" || reported.Pane != "w1:p3" || reported.Lane != "L-01" ||
			reported.Source != "launch" || reported.Transcript != "" || reported.Started == 0 {
			t.Errorf("reported %+v, want the agent, kind, pane, lane and no transcript yet", reported)
		}
		if !uuidRE.MatchString(reported.ID) {
			t.Errorf("session id %q is not a uuid", reported.ID)
		}
		eq(t, stored, reported.ID, "the store has the id when the agent starts")
		contains(t, x.startCall(), "--session-id "+reported.ID, "the argv names the session")
		lacks(t, x.startCall(), "--resume", "a fresh start resumes nothing")
	})
	t.Run("resume: the recorded id, no --session-id", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.sid("pj-l-01", "sid-1")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, x.startCall(), "--resume sid-1", "the record")
		lacks(t, x.startCall(), "--session-id", "a resume names no session")
		// The resumed session is recorded as every resume's (sessionOf's record): a repeat of its record, which serve
		// keeps as it is; nothing named before the start.
		if len(x.reported) != 1 || x.reported[0].ID != "sid-1" || x.reported[0].Kind != "claude" || x.reported[0].Started != 0 {
			t.Errorf("reported %+v, want the resumed sid-1 once, after the start", x.reported)
		}
	})
	t.Run("a refused record: said, the start keeps its id", func(t *testing.T) {
		t.Parallel()
		x := setup(t)
		x.reportErr = errors.New("serve is down")
		x.shell("w1:p3", "pj-l-01", "")
		out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "not recorded before the start: serve is down", "said")
		if !regexp.MustCompile(`--session-id [0-9a-f-]{36}`).MatchString(x.startCall()) {
			t.Errorf("the start carries no session id: %q", x.startCall())
		}
	})
}

// startProbe notes what the store holds when the agent's start reaches the terminal.
type startProbe struct {
	control
	atStart func()
}

func (p startProbe) Start(ctx context.Context, s port.AgentStart) (port.Pane, error) {
	p.atStart()
	return p.control.Start(ctx, s)
}

// a role with no configured model still gets its kind's launch defaults: Claude's memory off for
// every role but the lead.
func TestUntypedRoleLaunchDefaults(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ role, agent string }{{"integrator", "pj-int"}, {"reviewer", "pj-rev"}, {"lead", "pj-lead"}} {
		t.Run(c.role, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.roles."+c.role, ""))
			x.shell("w1:p1", c.agent, x.p.Root)
			if out, ok := x.restart(launch.Request{Agent: c.agent, Fresh: true}); !ok {
				t.Fatal(out)
			}
			if got := strings.Contains(x.settingsFile(c.agent), `"autoMemoryEnabled":false`); got != (c.role != "lead") {
				t.Errorf("memory off = %v in the settings file %q of %s", got, x.settingsFile(c.agent), x.startCall())
			}
		})
	}
}

// the restart lock is <state>/restart-<lane>.lock (no lane: the agent's); Held probes it without taking it.
func TestRestartLockHeld(t *testing.T) {
	root := t.TempDir()
	lock := launch.RestartLock(root, "L-01", "pj-l-01")
	if want := filepath.Join(project.StateDir(root), "restart-L-01.lock"); lock.Path != want {
		t.Errorf("path = %q, want %q", lock.Path, want)
	}
	if got := launch.RestartLock(root, "", "pj-int").Path; got != filepath.Join(project.StateDir(root), "restart-pj-int.lock") {
		t.Errorf("no lane: path = %q, want the agent's", got)
	}
	probe := launch.RestartLock(root, "L-01", "")
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
	if launch.RestartLock(root, "L-02", "").Held() {
		t.Error("another lane's lock is held")
	}
	lock.Unlock()
	if probe.Held() {
		t.Error("held after Unlock")
	}
}

// a fresh start without --prompt gets its role's first prompt: a handoff restart (Handoff) the lead's
// /obor:lead handoff, any other start the role's own (/obor:lead), also a lead's fresh start; a kind with no plugin
// slash command gets the plain prompt naming the skill file, handoff with its argument spelled out; --prompt wins, a
// resume carries none, a role without a skill starts bare.
func TestFreshStartRolePrompt(t *testing.T) {
	t.Parallel()
	const plain = "read /kit/skills/lead/SKILL.md and follow it"
	for _, c := range []struct {
		name, agent, role, typ, kind string
		req                          launch.Request
		resume                       bool
		prompt                       string // the expected first prompt ("": none)
	}{
		{"lead, handoff restart (Handoff)", "pj-lead", "lead", "opus", "claude", launch.Request{Fresh: true, Handoff: true}, false,
			"/obor:lead handoff"},
		{"lead, a fresh start without the handoff", "pj-lead", "lead", "opus", "claude", launch.Request{Fresh: true}, false,
			"/obor:lead"},
		{"lead, --prompt wins", "pj-lead", "lead", "opus", "claude", launch.Request{Fresh: true, Prompt: "/obor:lead"}, false,
			"/obor:lead"},
		{"lead, resume carries none", "pj-lead", "lead", "opus", "claude", launch.Request{}, true, ""},
		{"reviewer, no skill in its role", "pj-rev", "reviewer", "opus", "claude", launch.Request{Fresh: true}, false, ""},
		{"lead on a kind without skills: the skill file", "pj-lead", "lead", "luna", "codex", launch.Request{Fresh: true}, false,
			plain},
		{"lead on a kind without skills, handoff", "pj-lead", "lead", "luna", "codex", launch.Request{Fresh: true, Handoff: true}, false,
			plain + ", with the argument handoff"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.roles", `{"lead":"`+c.typ+`","reviewer":"`+c.typ+`"}`))
			a := x.agent("w1:p7", c.agent, c.kind, "idle")
			x.f.Set(a.ID, func(p *mpane) { p.Session = "sid-1" })
			if c.resume {
				x.sid(c.agent, "sid-1") // a Claude resume is its record's, not herdr's session
			}
			c.req.Agent = c.agent
			out, ok := x.restart(c.req)
			if !ok {
				t.Fatal(out)
			}
			call := x.startCall()
			if c.resume && !strings.Contains(call, "sid-1") {
				t.Fatalf("want a resume of sid-1, got %s", call)
			}
			if c.prompt == "" {
				if strings.Contains(call, "/obor:lead") || strings.Contains(call, " x] ") {
					t.Fatalf("want no first prompt, got %s", call)
				}
				return
			}
			if !strings.Contains(call, " "+c.prompt+"] ") {
				t.Fatalf("want first prompt %q last in the args, got %s", c.prompt, call)
			}
		})
	}
}

// startSpy keeps the agent start the terminal was asked for.
type startSpy struct {
	control
	got *port.AgentStart
}

func (s startSpy) Start(ctx context.Context, a port.AgentStart) (port.Pane, error) {
	*s.got = a
	return s.control.Start(ctx, a)
}

// A start hands the terminal the kind's program from the launching config (port.AgentStart.Program): an adapter that
// types the command line looks no kind up.
func TestStartCarriesTheKindsProgram(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.sid("pj-l-01", "sid-1")
	x.shell("w1:p3", "pj-l-01", "")
	var got port.AgentStart
	x.wrap = func(h control) control { return startSpy{h, &got} }
	if out, ok := x.restart(launch.Request{Agent: "pj-l-01"}); !ok {
		t.Fatal(out)
	}
	if got.Pane != "w1:p3" || got.Kind != "claude" {
		t.Fatalf("start = %+v; want the claude start in w1:p3", got)
	}
	eq(t, got.Program, "claude", "the start's program")
}

// restartedRE is the reply's success line: the agent, its pane and its session ("new" for a fresh one).
var restartedRE = regexp.MustCompile(`^RESTARTED (\S+) \(pane (\S+), session (\S+)\)$`)

// restarted asserts that line says the agent was restarted in its pane with the session (or "new"); the line's
// wording is pinned once, by the table of TestRestartWho's lines.
func restarted(t *testing.T, line, agent, pane, session string) {
	t.Helper()
	m := restartedRE.FindStringSubmatch(line)
	if m == nil {
		t.Errorf("not a RESTARTED line: %q", line)
		return
	}
	eq(t, m[1], agent, "restarted agent of "+line)
	eq(t, m[2], pane, "restarted pane of "+line)
	eq(t, m[3], session, "restarted session of "+line)
}

// isRestarted is restarted as a condition, for a test that branches on the outcome.
func isRestarted(line, agent, pane, session string) bool {
	m := restartedRE.FindStringSubmatch(line)
	return m != nil && m[1] == agent && m[2] == pane && m[3] == session
}
