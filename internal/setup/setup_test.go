package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/claude"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/testkit"
)

func TestMain(m *testing.M) {
	// obor init commits a fresh repository's files (T-629): git needs an identity in the tests' config-less environment.
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "obor test", "GIT_AUTHOR_EMAIL": "obor-test@example.invalid",
		"GIT_COMMITTER_NAME": "obor test", "GIT_COMMITTER_EMAIL": "obor-test@example.invalid"} {
		os.Setenv(k, v)
	}
	// A start reports its session to the project's serve, which no test here runs: the call ends in 0.2 s, not after
	// retrying for the report's 2 s (T-685's knob; T-1009: 1.8 s per pane start).
	testkit.Isolate() // before the Setenv: it clears OBOR_*
	os.Setenv("OBOR_CALL_DEADLINE", "0.2")
	os.Exit(testkit.Run(m))
}

// kit is this checkout: the templates and githooks setup reads.
var kit = func() string { d, _ := filepath.Abs("../.."); return d }()

type fx struct {
	*testkit.Project
	h      *herdrtest.Fake
	srv    *herdrtest.Server // the herdr socket setup talks to
	mark   int               // reqs starts here (reset)
	env    []string          // more env for the runs ("VAR=value")
	screen string            // what pane read shows of every pane
	kit    string            // the kit setup reads ("": this checkout)
	// mcpCheck is obor mcp --check (nil: a handshake that works)
	mcpCheck func(project.Env) (string, error)
	rollout  time.Duration // a fresh Codex start's rollout search (0: 20 ms)
	// open is the files processes hold open (launch.Launcher.Open; nil: none)
	open func(pids []int) []string
	// serve is Setup.Serve (obor init's serve install; nil: none)
	serve func(context.Context, *config.Config) (string, string, error)
	// installed is Setup.Installed (the serve LaunchAgent's plist when installed; nil: none is)
	installed func(*config.Config) (string, bool)
	// install is Setup.Install (the machine of the doctor's install checks; nil: an empty one, so a test never reads
	// this machine's CLIs)
	install func() agents.InstallHost
	// shellWait is Setup.ShellWait (0: 10 s), for tests that count on the bound
	shellWait time.Duration
	// poll and up are the launcher's Poll and Up (0: its defaults, 1 s and 30 s), for a start that polls (T-1058)
	poll, up time.Duration
}

// newFx is a project with a config (prefix pj, lanes_dir ../proj-wt, the built-in models; edits on top) and a fake
// herdr: tab w1:t1 with pane w1:p1, tab w1:t2 with w1:q1 (200×50 each),
// both shells of this project (workspace w1, cwd the root: the project's own tabs, T-632). The project is plain
// (testkit.NewPlain, T-1021): its role worktrees are laid out as files and no git process runs, so a lane comes from
// x.lane; a test of what git does with what setup installs uses newGitFx.
func newFx(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	x := bare(t)
	x.Config(edits...)
	return x
}

// newGitFx is newFx in a git repository (testkit.New): for the integration tests, whose subject is the git state setup
// leaves (lane worktrees, core.hooksPath, obor init's commit).
func newGitFx(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	x := bareGit(t)
	x.Config(edits...)
	return x
}

// bare is newFx without a config.
func bare(t *testing.T) *fx {
	t.Helper()
	return withHerdr(t, testkit.NewPlain(t))
}

// bareGit is newGitFx without a config.
func bareGit(t *testing.T) *fx {
	t.Helper()
	return withHerdr(t, testkit.New(t))
}

// lane is `obor setup lane <name>` in a plain project: the lane's worktree is laid out as files first
// (testkit.PlainLane), so setup finds it and runs no git; the config edit, links and lane_setup are setup's.
func (x *fx) lane(t *testing.T, name string) string {
	t.Helper()
	x.PlainLane(name, "")
	return x.ok(t, "lane", name)
}

// withHerdr is p with the fake herdr of newFx.
func withHerdr(t *testing.T, p *testkit.Project) *fx {
	t.Helper()
	h := herdrtest.New()
	h.Tab("w1:t1", 200, 50, "w1:p1")
	h.Tab("w1:t2", 200, 50, "w1:q1")
	x := &fx{Project: p, h: h, srv: herdrtest.Serve(t, h)}
	for _, p := range h.Panes {
		p.Cwd = x.Root // in workspace w1, its id's
	}
	// A screen applies to every pane read, whatever pane it names (the fake's per-pane Screen is empty here).
	x.srv.Before(func(r herdrtest.Request) {
		if x.screen == "" || r.Method != "pane.read" {
			return
		}
		var p struct {
			Pane string `json:"pane_id"`
		}
		if json.Unmarshal(r.Params, &p) == nil {
			x.h.Set(p.Pane, func(p *herdrtest.Pane) { p.Screen = x.screen })
		}
	})
	return x
}

// run is `obor setup <args>` in the main checkout: its output and exit code.
func (x *fx) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return x.runIn(t, x.Root, args...)
}

// runIn is `obor setup <args>` with dir as the caller's cwd: its output and exit code.
func (x *fx) runIn(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	s, out := x.newSetup(t)
	s.Dir = dir
	rc := s.Run(context.Background(), args)
	return out.String(), rc
}

// newSetup is the Setup runIn builds, with its own output buffer (nil: none), for a start `obor setup` does not run
// itself: the owner's drive agent (obor drive, StartDrive).
func (x *fx) newSetup(t *testing.T) (*Setup, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	k := kit
	if x.kit != "" {
		k = x.kit
	}
	check := x.mcpCheck
	if check == nil {
		check = func(project.Env) (string, error) { return "MCP OK (-: no tools)", nil }
	}
	rollout := x.rollout
	if rollout == 0 {
		rollout = 20 * time.Millisecond
	}
	open := x.open
	if open == nil {
		open = func([]int) []string { return nil }
	}
	install := x.install
	if install == nil {
		install = func() agents.InstallHost { return agents.InstallHost{} }
	}
	env, h := x.Env(x.env...), x.srv.Client()
	return &Setup{Kit: k, Env: env, T: &herdr.Setup{Socket: h, Env: env}, Out: &out, McpCheck: check, ShellWait: x.shellWait,
		Starter: func(cfg *config.Config) port.Starter {
			return &launch.Launcher{Cfg: cfg, Env: env, Kit: k, H: h, Out: &out, Rollout: rollout, Open: open, Poll: x.poll, Up: x.up}
		}, Serve: x.serve, Installed: x.installed, Install: install}, &out
}

// reqs is the requests the fake herdr socket got since reset, each as req's one line, e.g. "pane.split cwd=/w
// direction=right env={OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=worker,OPENCODE_CONFIG=} target_pane_id=w1:p1".
func (x *fx) reqs() []string {
	all := x.srv.Requests()
	var out []string
	for _, r := range all[min(x.mark, len(all)):] {
		out = append(out, req(r))
	}
	return out
}

// reset starts the request log over: reqs then shows only later requests.
func (x *fx) reset() { x.mark = len(x.srv.Requests()) }

// req is one request as one line: its method, then its params by name (a map's keys and a list's items sorted, so a
// line is the same however herdr's JSON orders them).
func req(r herdrtest.Request) string {
	var m map[string]any
	if len(r.Params) == 0 || json.Unmarshal(r.Params, &m) != nil || len(m) == 0 {
		return r.Method
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+param(m[k]))
	}
	return r.Method + " " + strings.Join(parts, " ")
}

// param is one request param as a log line reads it: a string bare, a list as [a b] and a map as {k=v,…}, else JSON.
func param(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		items := make([]string, 0, len(v))
		for _, x := range v {
			items = append(items, param(x))
		}
		return "[" + strings.Join(items, " ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		items := make([]string, 0, len(keys))
		for _, k := range keys {
			items = append(items, k+"="+param(v[k]))
		}
		return "{" + strings.Join(items, ",") + "}"
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprint(v)
}

// noAgents: herdr runs no agent (tab panes stay, at their shells, labels kept; agent-only panes go).
func (x *fx) noAgents() {
	var kept []*herdrtest.Pane
	for _, p := range x.h.Panes {
		if p.Tab != "" {
			p.Kind, p.Name, p.Status = "", "", ""
			kept = append(kept, p)
		}
	}
	x.h.Panes = kept
}

// running: herdr runs these agents (name, cwd), each in a pane of its own; no other.
func (x *fx) running(agents ...[2]string) {
	x.noAgents()
	for i, a := range agents {
		x.h.Agent(fmt.Sprintf("w1:x%d", i+1), a[0], "idle", a[1])
	}
}

// projectStep is the project step of `obor init` (initProject calls it too): the files it writes, its lines, or the
// error.
func (x *fx) projectStep(t *testing.T, name, prefix string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	k := kit
	if x.kit != "" {
		k = x.kit
	}
	s := &Setup{Kit: k, Dir: x.Root, Env: x.Env(), T: &herdr.Setup{Socket: x.srv.Client(), Env: x.Env()}, Out: &out}
	if err := s.findRoot(); err != nil {
		t.Fatal(err)
	}
	err := s.initProject(name, prefix)
	return out.String(), err
}

// ok runs and wants exit 0.
func (x *fx) ok(t *testing.T, args ...string) string {
	t.Helper()
	out, rc := x.run(t, args...)
	if rc != 0 {
		t.Fatalf("setup %v: exit %d\n%s", args, rc, out)
	}
	return out
}

// failing runs and wants exit 1 and the FAIL line want last.
func (x *fx) failing(t *testing.T, want string, args ...string) string {
	t.Helper()
	out, rc := x.run(t, args...)
	if rc != 1 || last(out) != want {
		t.Fatalf("setup %v: exit %d, last line %q, want %q\n%s", args, rc, last(out), want, out)
	}
	return out
}

func last(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

// named hides the session id obor names a fresh Claude start with (T-813): it is random, so the expectations write
// it NEW.
var namedRE = regexp.MustCompile(`--session-id [0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)

func named(text string) string { return namedRE.ReplaceAllString(text, "--session-id NEW") }

func has(t *testing.T, text, want, what string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("%s: no %q in\n%s", what, want, text)
	}
}

func hasNot(t *testing.T, text, unwanted, what string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Errorf("%s: %q in\n%s", what, unwanted, text)
	}
}

func count(text, line string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

// --- init -------------------------------------------------------------------------------------------------------------

// bash setup.test.sh case_init_never_overwrites_and_is_idempotent
func TestInitNeverOverwritesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	x := bare(t)
	x.Write("docs/agents/lead.md", "mine\n")
	x.Write(".gitignore", x.Read(".gitignore")+".obor/state/") // no trailing newline: appended lines must not glue on
	out, err := x.projectStep(t, "", "pj")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	has(t, out, "skip docs/agents/lead.md (exists)", "existing doc said")
	if x.Read("docs/agents/lead.md") != "mine\n" {
		t.Error("existing role doc kept")
	}
	gi := x.Read(".gitignore")
	if count(gi, ".obor/local.json") != 1 || count(gi, ".obor/state/") != 1 {
		t.Errorf("no repeat, new line after the unterminated one:\n%s", gi)
	}

	x.Write("docs/agents/worker.md", "edited\n")
	x.Write(".obor/config.json", strings.Replace(x.Read(".obor/config.json"), `"name": "proj"`, `"name": "kept"`, 1))
	if !strings.Contains(x.Read(".obor/config.json"), `"name": "kept"`) {
		t.Fatal("precondition: name edited")
	}
	out, err = x.projectStep(t, "other", "zz")
	if err != nil {
		t.Fatalf("project step: %v\n%s", err, out)
	}
	has(t, out, "skip .obor/config.json (exists)", "config skipped")
	has(t, out, "skip BACKLOG.md (exists)", "backlog skipped")
	has(t, out, "skip docs/decisions.md (exists)", "decisions skipped")
	cfg, _ := config.Load(x.Root)
	if cfg.Data.Project.Name != "kept" || cfg.Data.Project.AgentPrefix != "pj" {
		t.Errorf("config not overwritten: %q %q", cfg.Data.Project.Name, cfg.Data.Project.AgentPrefix)
	}
	if x.Read("docs/agents/worker.md") != "edited\n" {
		t.Error("role doc not overwritten")
	}
	if x.Read(".gitignore") != gi {
		t.Errorf("gitignore unchanged on rerun:\n%s", x.Read(".gitignore"))
	}
	hasNot(t, out, "created", "nothing created on rerun")
}

// bash setup.test.sh case_init_refuses_a_bad_prefix
func TestInitRefusesABadPrefix(t *testing.T) {
	t.Parallel()
	x := bare(t)
	_, err := x.projectStep(t, "", "Bad_Prefix")
	if err == nil || err.Error() != "bad prefix Bad_Prefix (must match ^[a-z][a-z0-9-]{0,7}$)" {
		t.Fatalf("err %v", err)
	}
	if x.Exists(".obor/config.json") {
		t.Error("no config written")
	}
}

func TestDefaultPrefix(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"obor-kit": "obor", "My Proj": "mypr", "2048 game": "game", "42": "obor",
		"axis-alies": "axis"} {
		if got := defaultPrefix(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

// T-609: `obor setup`'s init subcommand is gone (obor init sets a project up): the usage line names no init, and
// the old init subcommand is unknown.
func TestSetupUsageNamesNoInit(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, "FAIL "+Usage(), "init", "--prefix", "pj")
	hasNot(t, Usage(), "init", "the usage line")
}

// --- lane -------------------------------------------------------------------------------------------------------------

// T-765: lanes are L-01 … L-NN in order: setup lane takes a configured lane or the next one, nothing else, and one
// name only (no type).
func TestLaneTakesOnlyTheNextLNN(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	for _, name := range []string{"O-01", "L-02", "review"} {
		x.failing(t, "FAIL lanes are L-01 … L-NN in order (lanes: 0): the next one is L-01", "lane", name)
		if x.Exists(x.LaneDir(name)) {
			t.Errorf("worktree %s made", name)
		}
	}
	x.lane(t, "L-01")
	x.failing(t, "FAIL lanes are L-01 … L-NN in order (lanes: 1): the next one is L-02", "lane", "L-03")
	x.lane(t, "L-02")
	if cfg, _ := config.Load(x.Root); cfg.LaneCount() != 2 {
		t.Errorf("lanes = %d, want 2", cfg.LaneCount())
	}
	for _, args := range [][]string{{"lane"}, {"lane", "L-03", "opus"}} {
		if out, rc := x.run(t, args...); rc != 1 || !strings.Contains(last(out), "usage: obor setup lane <L-NN> |") {
			t.Errorf("%q: exit %d, %q", args, rc, last(out))
		}
	}
}

// T-765: a lane pane without --start is labelled and starts nothing; with --start it runs the default kind with no
// model (no dispatch names one), as obor restart does.
func TestPaneOnALaneStartsTheDefaultKindWithoutADispatch(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	x.reset()
	out := x.ok(t, "pane", "L-01", "--tab", "w1:t1")
	has(t, out, "no agent started", "a pane without --start")
	got := strings.Join(x.reqs(), "\n")
	has(t, got, "pane.split", "the pane is made")
	has(t, got, "OBOR_LANE=L-01", "the lane's env")
	if strings.Contains(got, "agent.start") {
		t.Errorf("no agent starts: %s", got)
	}
	x.reset()
	x.ok(t, "pane", "L-01", "--pane", "w1:n1", "--start")
	has(t, named(strings.Join(x.reqs(), "\n")), "agent.start args=["+laneArgs+" --session-id NEW] kind=claude name=pj-l-01", "the default kind, no model")
}

// T-770: the owner's drive agent (role driver) is a lane's agent too: its Claude start is strict — obor's MCP server
// alone, the user's MCP servers out of its context on every job.
func TestDriveStartIsStrict(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	x.reset()
	s, out := x.newSetup(t)
	s.Dir = x.Root
	if err := s.StartDrive(context.Background(), Drive{Agent: "pj-l-01-drive", Lane: "L-01", Cwd: x.LaneDir("L-01"),
		Prompt: "drive T-1"}); err != nil {
		t.Fatalf("StartDrive: %v\n%s", err, out)
	}
	reqs := strings.Join(x.reqs(), "\n")
	has(t, reqs, "OBOR_ROLE=driver", "the drive agent's role")
	has(t, named(reqs), "agent.start args=["+laneArgs+" --session-id NEW -- drive T-1] kind=claude name=pj-l-01-drive", "a strict start, obor's server alone")
}

// bash setup.test.sh case_lane_refuses_unknown_type_type_change_and_failing_setup: a failing lane_setup fails the lane.
func TestLaneRefusesAFailingSetup(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lanes.setup", "echo boom; exit 3"))
	x.PlainLane("L-01", "") // the worktree is git's (TestLaneAddsConfigWorktreeLinksAndRunsLaneSetup): here, files
	out := x.failing(t, "FAIL lane_setup exited 3 in "+x.LaneDir("L-01")+": echo boom; exit 3", "lane", "L-01")
	has(t, out, "boom", "setup output shown")
}

// --- pane -------------------------------------------------------------------------------------------------------------

const settings = `--settings {"autoMemoryEnabled":false,"remoteControlAtStartup":false}`

// laneArgs is a lane agent's start args before its first prompt (T-770): obor's MCP server, then the strict flag that
// hides every other one, then the settings.
var laneArgs = "--mcp-config " + claude.MCPConfig(kit) + " --strict-mcp-config " + settings

// bash setup.test.sh case_pane_splits_the_tab_and_starts_the_agent_in_order
func TestPaneSplitsTheTabAndStartsTheAgentInOrder(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	lane := x.LaneDir("L-01")
	out := x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	if last(out) != "OK L-01: pj-l-01 in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	// T-1037: the new pane's id is herdr's answer to the split, so its shell, once up, gets the env line with OBOR_PANE
	// before the start.
	want := []string{"agent.list", "pane.list", "pane.layout pane_id=w1:p1",
		"pane.split cwd=" + lane + " direction=right env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_LANE_CPUS=2,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=worker,OPENCODE_CONFIG=} target_pane_id=w1:p1",
		"pane.process_info pane_id=w1:n1", "pane.process_info pane_id=w1:n1",
		"pane.send_input keys=[Enter] pane_id=w1:n1 text=cd " + lane + " && unset OBOR_AGENT OBOR_OPENCODE_MODEL OPENCODE_CONFIG && export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:n1 OBOR_LANE_CPUS=2 && echo obor-env-M || echo obor-env-M",
		"pane.wait_for_output match={type=regex,value=obor-env-M|obor-env-M} pane_id=w1:n1 source=recent_unwrapped timeout_ms=20000",
		"pane.rename label=pj-l-01 pane_id=w1:n1",
		"agent.start args=[" + laneArgs + " --session-id NEW] kind=claude name=pj-l-01 pane_id=w1:n1"}
	marks := regexp.MustCompile(`obor-env-"?[0-9a-z]+"?`)
	if got := marks.ReplaceAllString(named(strings.Join(x.reqs(), "\n")), "obor-env-M"); got != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// T-1041: a lane's pane gets the generic fact OBOR_LANE_CPUS (lanes.cpus) and the project's lanes.env, its
// {cpus} replaced, in the split's env and the env line (setup is one of the three consumers of config.LaneEnv).
func TestPaneLaneEnv(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lanes.cpus", 3), testkit.Set("lanes.env",
		`{"GOMAXPROCS": "{cpus}", "GOFLAGS": "-p={cpus} -trimpath"}`))
	x.lane(t, "L-01")
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "env={GOFLAGS=-p=3 -trimpath,GOMAXPROCS=3,OBOR_AGENT=,OBOR_LANE=L-01,OBOR_LANE_CPUS=3,"+
		"OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=worker,OPENCODE_CONFIG=}", "the split's env")
	has(t, calls, "export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:n1 GOFLAGS='-p=3 -trimpath' GOMAXPROCS=3 "+
		"OBOR_LANE_CPUS=3", "the env line")
}

// STATUS 2026-10-13: hk-int got a 1-line pane: the largest pane is split (not the last), right when wide, else down.
// T-557: panes of the same area go to the last one, as before (50×20 and 100×10 are both 1000 cells).
func TestPaneSplitsTheLargestPane(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		panes  [][3]int // w, h and the pane's number: pane w1:p<n>
		target int      // the pane's number split
		dir    string   // the split's direction
	}{
		{"the big one, not the last one: tall → down", [][3]int{{100, 50, 1}, {100, 2, 2}}, 1, "down"},
		{"the big one, not the last one: wide → right", [][3]int{{50, 20, 1}, {150, 20, 2}}, 2, "right"},
		{"equal areas: the last of the tie, not the first", [][3]int{{50, 20, 1}, {100, 10, 2}}, 2, "right"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t)
			x.lane(t, "L-01")
			x.h.Panes = nil
			for _, p := range c.panes {
				x.h.Tab("w1:t1", p[0], p[1], fmt.Sprintf("w1:p%d", p[2]))
			}
			if len(x.h.Panes) != len(c.panes) {
				t.Fatalf("precondition: %d panes, want %d", len(x.h.Panes), len(c.panes))
			}
			a, b := c.panes[0], c.panes[1]
			area := func(p [3]int) int { return p[0] * p[1] }
			if c.target == b[2] && area(b) < area(a) || c.target == a[2] && area(a) <= area(b) {
				t.Fatalf("precondition: pane %d (area %d) and pane %d (area %d) do not make pane %d the split's target",
					a[2], area(a), b[2], area(b), c.target)
			}
			x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
			has(t, strings.Join(x.reqs(), "\n"), "direction="+c.dir+
				" env={OBOR_AGENT=,OBOR_LANE=L-01,OBOR_LANE_CPUS=2,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=worker,OPENCODE_CONFIG=} "+
				fmt.Sprintf("target_pane_id=w1:p%d", c.target), c.name)
		})
	}
}

// bash setup.test.sh case_pane_refuses_a_full_tab
func TestPaneRefusesAFullTab(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	x.h.Tab("w1:t1", 200, 50, "w1:p2", "w1:p3")
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	has(t, strings.Join(x.reqs(), "\n"), "pane.split ", "precondition: 3 of 4 panes is not full")

	x.noAgents()
	x.h.Pane("w1:n1").Label = ""
	x.reset()
	x.failing(t, "FAIL tab w1:t1 is full (4 of 4 panes; --new-tab <label>)", "pane", "--start", "L-01", "--tab", "w1:t1")
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "pane.split", "no split")
	hasNot(t, calls, "agent.start", "no agent start")
}

// R-28 §2: the new pane is labelled the agent name before the start; a label another pane holds, or --pane labelled
// for another agent → FAIL before any split or start; a failing rename → FAIL, no start.
// bash setup.test.sh case_pane_label_set_and_refused
func TestPaneLabelSetAndRefused(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	out := x.ok(t, "pane", "--start", "L-01", "--pane", "w1:p1")
	has(t, out, "labelled pane w1:p1 pj-l-01", "said")
	if x.h.Pane("w1:p1").Label != "pj-l-01" {
		t.Error("label set in herdr")
	}
	has(t, named(strings.Join(x.reqs(), "\n")), "pane.rename label=pj-l-01 pane_id=w1:p1\nagent.start args=["+laneArgs+" --session-id NEW] kind=claude name=pj-l-01 pane_id=w1:p1",
		"rename, then the start")

	x.noAgents()
	if out := x.ok(t, "pane", "--start", "L-01", "--pane", "w1:p1"); last(out) != "OK L-01: pj-l-01 in pane w1:p1" {
		t.Errorf("own label on --pane → fine: %q", last(out))
	}

	x.noAgents()
	x.reset()
	x.failing(t, "FAIL pane w1:p1 is already labelled pj-l-01 (obor restart pj-l-01, or herdr pane rename w1:p1 --clear)",
		"pane", "--start", "L-01", "--tab", "w1:t1")
	calls := strings.Join(x.reqs(), "\n")
	hasNot(t, calls, "pane.split", "held label → no split")
	hasNot(t, calls, "agent.start", "held label → no start")

	x.h.Pane("w1:p1").Label = ""
	x.h.Pane("w1:q1").Label = "advisor"
	x.reset()
	x.failing(t, "FAIL pane w1:q1 is labelled advisor, not pj-l-01 (herdr pane rename w1:q1 --clear to reuse it)",
		"pane", "--start", "L-01", "--pane", "w1:q1")
	hasNot(t, strings.Join(x.reqs(), "\n"), "agent.start", "foreign label on --pane → no start")

	x.srv.Fail("pane.rename", "pane_not_found", "gone")
	x.reset()
	x.failing(t, "FAIL herdr pane rename w1:p1 pj-l-01 failed", "pane", "--start", "L-01", "--pane", "w1:p1")
	hasNot(t, strings.Join(x.reqs(), "\n"), "agent.start", "rename failed → no start")
}

// bash setup.test.sh case_pane_skips_a_lane_whose_agent_runs
func TestPaneSkipsALaneWhoseAgentRuns(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.lane(t, "L-01")
	x.running([2]string{"someone", x.LaneDir("L-01")})
	out := x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	if last(out) != "OK L-01: someone already running" {
		t.Errorf("last line %q", last(out))
	}
	if got := x.reqs(); strings.Join(got, ",") != "agent.list" {
		t.Errorf("no split, no start: %v", got)
	}
}

// A failed start reads the pane (T-256: only then, the one launch.Start): a trust dialog or a Codex self-update names
// its fix; herdr's agent_not_ready alone says the agent waits; any other failure is herdr's.
// bash setup.test.sh case_pane_trust_dialog_fails_with_hint, case_pane_codex_self_update_fails_with_hint
func TestPaneNotReadyHints(t *testing.T) {
	t.Parallel()
	notReady := herdrtest.Failure{Code: "agent_not_ready", Message: "blocked during startup"}
	exited := herdrtest.Failure{Code: "agent_exited", Message: "the agent exited"}
	for _, c := range []struct {
		screen string
		fail   herdrtest.Failure
		want   string
	}{
		{"> You are in /x\n  Do you trust the contents of this directory?\n  Trust this folder? 1. Yes  2. No\n", notReady,
			"FAIL agent not ready: a 'Trust this folder?' dialog in pane w1:n1: answer it there (Enter = trust; once per repo)"},
		{"Updating Codex via npm install -g @openai/codex\nUpdated to 0.161.0. Restart Codex.\n$ \n", exited,
			"FAIL agent not ready: codex updated itself and exited in pane w1:n1: run herdr agent start pj-l-01 --kind codex --pane w1:n1 again"},
		{"", notReady, "FAIL agent not ready: pj-l-01 waits at a startup dialog in pane w1:n1: answer it there"},
		{"$ \n", exited, "FAIL herdr agent start pj-l-01 failed (pane w1:n1)"},
	} {
		x := newFx(t)
		x.lane(t, "L-01")
		x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
		x.screen = c.screen
		x.srv.Fail("agent.start", c.fail.Code, c.fail.Message)
		out := x.failing(t, c.want, "pane", "--start", "L-01", "--tab", "w1:t1")
		has(t, strings.Join(x.reqs(), "\n"), "kind=codex name=pj-l-01 pane_id=w1:n1", "precondition: agent start ran")
		has(t, out, "("+c.fail.Code+")", "herdr's error printed")
	}
	x := newFx(t)
	x.lane(t, "L-01")
	x.State("L-01", map[string]any{"task": "T-1", "type": "luna"})
	x.screen = "Trust this folder?"
	x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t1")
	hasNot(t, strings.Join(x.reqs(), "\n"), "pane.read", "a start herdr took: no screen read")
}

// bash setup.test.sh case_pane_refuses_an_unknown_lane_and_ambiguous_placement
func TestPaneRefusesUnknownLaneAndAmbiguousPlacement(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, "FAIL unknown lane L-09 (lanes: 0; obor setup lane L-09 first)", "pane", "L-09", "--tab", "w1:t1")
	x.lane(t, "L-01")
	x.failing(t, "FAIL pass at most one of --tab, --new-tab, --pane", "pane", "--start", "L-01", "--tab", "w1:t1", "--new-tab", "x")
	x.lane(t, "L-02")
	x.ok(t, "pane", "--start", "L-02") // not in a herdr pane: the project's tab with most panes, the first of equals (T-632)
	has(t, strings.Join(x.reqs(), "\n"), "target_pane_id=w1:p1", "no caller: the project's first tab")
	x.reset()
	x.env = []string{"OBOR_PANE=w1:q1"}
	x.ok(t, "pane", "--start", "L-01")
	has(t, strings.Join(x.reqs(), "\n"), "target_pane_id=w1:q1", "no --tab: the caller's tab")
}

// --- role -------------------------------------------------------------------------------------------------------------

// envLines is the text typed at a shell since reset (pane.send_input), one per line.
func (x *fx) envLines() []string {
	var out []string
	for _, r := range x.reqs() {
		if _, text, ok := strings.Cut(r, " text="); ok && strings.HasPrefix(r, "pane.send_input") {
			out = append(out, text)
		}
	}
	return out
}

// A reused pane (--pane) gets the env line a restart types, so a shell that holds the env of the agent that ran there
// before (a stale OBOR_AGENT, OBOR_LANE) starts the new agent with its own: cd, exports and unsets, before the start.
func TestSetupPaneTypesTheEnvAtAReusedPane(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("agents.roles", `{"integrator": "opus"}`))
	x.lane(t, "L-01")
	x.reset()
	x.ok(t, "role", "integrator", "--pane", "w1:p1", "--start")
	lines := x.envLines()
	// T-853: the integrator's cwd is its role worktree.
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "cd "+x.LaneDir("integrator")+" && unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OPENCODE_CONFIG && export OBOR_ROLE=integrator OBOR_PANE=w1:p1 && echo obor-env-") {
		t.Fatalf("typed %q, want one env line: cd its role worktree, unset every registry var it does not set (a stale OBOR_AGENT, OBOR_LANE, another kind's), export integrator and its pane, then the marks", lines)
	}
	calls := strings.Join(x.reqs(), "\n")
	if strings.Index(calls, "pane.send_input") > strings.Index(calls, "agent.start") || !strings.Contains(calls, "agent.start") {
		t.Errorf("the env line comes before the start:\n%s", calls)
	}

	x.noAgents()
	x.h.Pane("w1:p1").Label = ""
	x.reset()
	x.ok(t, "pane", "--start", "L-01", "--pane", "w1:p1")
	lines = x.envLines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "cd "+x.LaneDir("L-01")+" && unset OBOR_AGENT OBOR_OPENCODE_MODEL OPENCODE_CONFIG && export OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:p1 OBOR_LANE_CPUS=2 && echo obor-env-") {
		t.Errorf("typed %q, want a lane's env: its worktree, worker, L-01, its pane (no OBOR_AGENT)", lines)
	}

	// a new pane gets its env from herdr at creation (OBOR_PANE empty: its id is herdr's answer), then the env line
	// with its id once its shell is up (T-1037)
	x.noAgents()
	x.h.Pane("w1:p1").Label = ""
	x.reset()
	out := x.ok(t, "pane", "--start", "L-01", "--tab", "w1:t2")
	has(t, strings.Join(x.reqs(), "\n"), "OBOR_PANE=,", "precondition: a new pane, made without a pane id")
	if lines := x.envLines(); len(lines) != 1 || !strings.Contains(lines[0], " OBOR_PANE=w1:n1 ") ||
		!strings.Contains(last(out), "pane w1:n1") {
		t.Errorf("a new pane (%s) got typed %q; want one env line exporting OBOR_PANE=w1:n1", last(out), lines)
	}
}

// T-601 (RV-64 #3): setup role --pane types the env line through the same setEnv as restart. A cd that fails echoes the
// line's failure word, not its success word, so the agent is not started with the shell's old cwd and OBOR_* env.
func TestSetupPaneStopsWhenTheEnvLineFailed(t *testing.T) {
	t.Parallel()
	t.Run("cd fails: the failure mark, no start, FAIL", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, testkit.Set("agents.roles", `{"integrator": "opus"}`))
		x.h.Tab("w1:t1", 200, 50, "w1:p3")
		pane := x.h.Pane("w1:p3")
		pane.OnRun = "fail"
		x.failing(t, "FAIL the env line in w1:p3 failed (nothing started; check the pane)", "role", "integrator", "--pane", "w1:p3", "--start")
		lines := x.envLines()
		if len(lines) != 1 {
			t.Fatalf("one env line typed, got %q", lines)
		}
		okWord, badWord := envWords(t, lines[0])
		if strings.Contains(pane.Screen, okWord) || !strings.Contains(pane.Screen, badWord) {
			t.Errorf("the screen shows the failure mark only, want %q:\n%s", badWord, pane.Screen)
		}
		hasNot(t, strings.Join(x.reqs(), "\n"), "agent.start", "no start after a failed env line")
	})
	t.Run("the line runs: the success mark, the agent starts", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, testkit.Set("agents.roles", `{"integrator": "opus"}`))
		x.h.Tab("w1:t1", 200, 50, "w1:p3")
		pane := x.h.Pane("w1:p3")
		if out := x.ok(t, "role", "integrator", "--pane", "w1:p3", "--start"); last(out) != "OK integrator: pj-int in pane w1:p3" {
			t.Errorf("last line %q", last(out))
		}
		lines := x.envLines()
		if len(lines) != 1 {
			t.Fatalf("one env line typed, got %q", lines)
		}
		okWord, badWord := envWords(t, lines[0])
		if !strings.Contains(pane.Screen, okWord) || strings.Contains(pane.Screen, badWord) {
			t.Errorf("the screen shows the success mark only, want %q:\n%s", okWord, pane.Screen)
		}
		has(t, named(strings.Join(x.reqs(), "\n")), "agent.start args=[--permission-mode auto --model opus "+settings+" --session-id NEW] kind=claude name=pj-int pane_id=w1:p3",
			"the agent starts after the env line succeeded")
	})
}

// envMarksRE is the env line's two words as typed (T-601): its success word, then its failure word.
var envMarksRE = regexp.MustCompile(` && echo obor-env-"([0-9a-z]+)" \|\| echo obor-env-"([0-9a-z]+)"$`)

// envWords is the success and failure words the typed env line echoes (T-601), quotes removed.
func envWords(t *testing.T, line string) (ok, bad string) {
	t.Helper()
	m := envMarksRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("the env line has no success and failure marks: %q", line)
	}
	return "obor-env-" + m[1], "obor-env-" + m[2]
}

// bash setup.test.sh case_role_lead_new_tab_gets_role_and_agent_env
func TestRoleLeadNewTabGetsRoleEnv(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("agents.roles", `{"lead": "opus"}`))
	if out := x.ok(t, "role", "lead", "--new-tab", "lead", "--start"); last(out) != "OK lead: pj-lead in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+x.Root+" env={OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=lead,OPENCODE_CONFIG=} focus=false label=lead", "env")
	has(t, named(calls), "agent.start args=[--permission-mode auto --model opus --session-id NEW /obor:lead] kind=claude name=pj-lead pane_id=w1:n1", "the agent named by its derived name, no Remote Control default (T-280)")
}

// T-280: a lead of a type without settings keeps Remote Control; a worker does not. (Setup role lead, the one lead.)
func TestLeadKeepsRemoteControl(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("agents.roles", `{"lead": "opus"}`))
	if out := x.ok(t, "role", "lead", "--new-tab", "lead", "--start"); last(out) != "OK lead: pj-lead in pane w1:n1" {
		t.Fatalf("setup role lead: %q", out)
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, named(calls), "agent.start args=[--permission-mode auto --model opus --session-id NEW /obor:lead] kind=claude name=pj-lead pane_id=w1:n1", "lead: type args, no --settings")
	hasNot(t, calls, "remoteControlAtStartup", "lead")
}

// bash setup.test.sh case_role_triage_runs_in_the_root_with_agent_env_only_and_creates_bugs_md; T-796: OBOR_ROLE=triage,
// so its identity is derived like every other role's.
func TestRoleTriageRunsInTheRootWithItsRoleEnvAndCreatesBugsMd(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, `FAIL roles.triage is not set (a model ref, e.g. <model>[:<effort>])`, "role", "triage", "--new-tab", "triage", "--start")
	if x.Exists("docs/BUGS.md") {
		t.Fatal("precondition: no BUGS.md after the FAIL")
	}
	x.Config(testkit.Set("agents.roles", `{"triage": "opus"}`))
	if out := x.ok(t, "role", "triage", "--new-tab", "triage", "--start"); last(out) != "OK triage: pj-triage in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+x.Root+" env={OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=triage,OPENCODE_CONFIG=} focus=false label=triage", "the triage role env (T-796)")
	has(t, x.Read("docs/BUGS.md"), "Severity", "BUGS.md from the template")
	x.Write("docs/BUGS.md", "mine\n")
	x.noAgents()
	x.h.Pane("w1:n1").Label = ""
	x.ok(t, "role", "triage", "--new-tab", "triage", "--start")
	if x.Read("docs/BUGS.md") != "mine\n" {
		t.Error("BUGS.md not overwritten")
	}
}

// T-801: obor setup role places a custom role like any other (its name with - or _), in the main checkout with
// OBOR_ROLE=<name>, its agent <prefix>-<name, _ → ->; it seeds nothing (only a default row with Seeds does); a name with
// no row is the usage line.
func TestRoleCustomPlaced(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.failing(t, "FAIL "+Usage(), "role", "ux_buddy", "--new-tab", "ux", "--start")
	x.Write("docs/agents/ux_buddy.md", "# Role: ux_buddy\n")
	x.Config(testkit.Set("agents.roles", `{"ux_buddy": {"model": "opus", "write": ["docs/ux/"]}}`))
	if out := x.ok(t, "role", "ux-buddy", "--new-tab", "ux", "--start"); last(out) != "OK ux_buddy: pj-ux-buddy in pane w1:n1" {
		t.Errorf("last line %q", last(out))
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "tab.create cwd="+x.Root+" env={OBOR_AGENT=,OBOR_LANE=,OBOR_OPENCODE_MODEL=,OBOR_PANE=,OBOR_ROLE=ux_buddy,OPENCODE_CONFIG=} focus=false label=ux", "the custom role's env")
	has(t, calls, "name=pj-ux-buddy", "the custom role's agent")
	if x.Exists("docs/BUGS.md") || x.Exists("docs/ux") {
		t.Error("a custom role seeded a file")
	}
	if !strings.Contains(Usage(), "advisor|researcher|triage|<custom role>") {
		t.Errorf("usage names the default rows and custom roles: %s", Usage())
	}
}

// --- githooks ---------------------------------------------------------------------------------------------------------
// githooks_integration_test.go: what setup githooks leaves in git config, and the hooks git runs (T-1021).

// --- herdr-plugin (R-28 §3, T-1117: removed) ---------------------------------------------------------------------

// T-1117: `obor setup herdr-plugin` no longer links the obor.events plugin (obor serve restarts its own panes): the
// one an older obor linked is unlinked once, and a machine without one has nothing to remove. Either way herdr's
// resume hint is said once, unless herdr's config sets it.
func TestHerdrPluginRemovesTheOldLinkAndHints(t *testing.T) {
	t.Parallel()
	x := bare(t)
	conf := x.Path("herdr-config.toml")
	x.env = []string{"HERDR_CONFIG_PATH=" + conf}
	old := x.Path("old-kit/herdr-plugin")
	x.h.Plugins = []herdrtest.Plugin{{ID: "obor.events", Root: old}, {ID: "other", Root: "/o"}}
	out := x.ok(t, "herdr-plugin")
	has(t, strings.Join(x.reqs(), "\n"), "plugin.unlink plugin_id=obor.events", "linked → the old plugin unlinked")
	has(t, out, "herdr-plugin: obor.events unlinked (was "+old+"; obor serve restarts its own panes now)", "unlinked said")
	has(t, out, "hint: obor restarts its panes after a herdr restart: set resume_agents_on_restore = false under [session] in "+conf,
		"no setting → the resume line")
	if last(out) != "OK herdr-plugin" {
		t.Errorf("last line %q", last(out))
	}
	if len(x.h.Plugins) != 1 || x.h.Plugins[0].ID != "other" {
		t.Errorf("herdr's plugins after the removal: %+v, want only the other", x.h.Plugins)
	}
	// A rerun (or a machine that never linked it): nothing to unlink, and it says so.
	x.reset()
	out = x.ok(t, "herdr-plugin")
	has(t, out, "herdr-plugin: obor.events is not linked", "not linked said")
	hasNot(t, strings.Join(x.reqs(), "\n"), "plugin.unlink", "nothing linked → no unlink")
	hasNot(t, strings.Join(x.reqs(), "\n"), "plugin.link", "never linked again")

	x.Write(conf, "[session]\nresume_agents_on_restore = true\n")
	has(t, x.ok(t, "herdr-plugin"), "set resume_agents_on_restore = false", "true → the resume line")
	text := "[ui]\nx = 1\n[session]\nresume_agents_on_restore = false # obor\n"
	x.Write(conf, text)
	hasNot(t, x.ok(t, "herdr-plugin"), "hint:", "false → no hint at all")
	if x.Read(conf) != text {
		t.Error("herdr's config never edited")
	}
	x.Write(conf, "[other]\nresume_agents_on_restore = false\n")
	has(t, x.ok(t, "herdr-plugin"), "set resume_agents_on_restore", "false outside [session] → the line")
	os.Remove(conf)
}

// T-613 #4: herdr cannot list its plugins: the removal FAILs with herdr's error (never "not linked") and unlinks
// nothing; a failed unlink is a WARN and a FAIL too.
func TestHerdrPluginRemovalFailures(t *testing.T) {
	t.Parallel()
	x := bare(t)
	x.h.Plugins = []herdrtest.Plugin{{ID: "obor.events", Root: x.Path("old-kit/herdr-plugin")}}
	x.srv.Fail("plugin.list", "invalid_request", "boom")
	x.failing(t, "FAIL cannot read herdr's plugin list: herdr plugin.list: failed: boom (invalid_request)", "herdr-plugin")
	if len(x.h.Plugins) != 1 {
		t.Errorf("a plugin list error changed herdr's plugins: %v", x.h.Plugins)
	}
	hasNot(t, strings.Join(x.reqs(), "\n"), "plugin.unlink", "a list error unlinked nothing")
	x.srv.Clear("plugin.list")

	x.reset()
	x.srv.Fail("plugin.unlink", "plugin_invalid", "boom")
	out := x.failing(t, "FAIL herdr-plugin", "herdr-plugin")
	has(t, out, "WARN herdr-plugin: herdr plugin.unlink: failed: boom (plugin_invalid)", "a failed unlink said")
	x.srv.Clear("plugin.unlink")

	x.h.Plugins = nil
	x.reset()
	if out := x.ok(t, "herdr-plugin"); last(out) != "OK herdr-plugin" {
		t.Errorf("nothing linked: last line %q", last(out))
	}
}

// --- agents (T-306, DD-v0.13 B1, T-809) -----------------------------------------------------------------------------

// `obor setup mcp` says per kind whether the server is registered, then runs the handshake: all OK, a broken handshake
// a WARN and a FAIL.
func TestSetupMCPDoctor(t *testing.T) {
	x := newFx(t, testkit.Set("agents.roles.reviewer", "luna"))
	x.env = []string{"XDG_CONFIG_HOME=" + filepath.Join(x.Base, "xdg")}
	out := x.ok(t, "mcp")
	has(t, out, "mcp claude: registered", "claude: the kit's plugin.json")
	has(t, out, "mcp codex: registered per start", "codex: launch args")
	has(t, out, "env OBOR_NO_SELF_HOST=1", "codex: the pin")
	if strings.Contains(out, "without the OBOR_NO_SELF_HOST pin") {
		t.Errorf("a pinned claude manifest warns about the pin:\n%s", out)
	}
	has(t, out, `env_vars=["OBOR_ROLE","OBOR_LANE","OBOR_PANE","OBOR_STATE_HOME"]`, "codex: forwarded variables")
	has(t, out, "mcp opencode: registered per start (OPENCODE_CONFIG", "opencode: its generated config (T-809)")
	has(t, out, "MCP OK (", "the handshake line of obor mcp --check")
	has(t, last(out), "OK mcp", "result")

	x.mcpCheck = func(project.Env) (string, error) { return "", errors.New("the server answered nothing") }
	out, rc := x.run(t, "mcp")
	has(t, out, "WARN mcp check: the server answered nothing", "a broken handshake")
	if rc != 1 || last(out) != "FAIL mcp" {
		t.Errorf("broken handshake: exit %d, last %q", rc, last(out))
	}
}

// The plugin manifest's mcpServers: the obor server is `bash ${CLAUDE_PLUGIN_ROOT}/bin/obor mcp`, nothing else.
func TestKitManifestMCPServers(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(kit, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		McpServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	h := got.McpServers["obor"]
	if len(got.McpServers) != 1 || h.Command != "bash" || strings.Join(h.Args, " ") != "${CLAUDE_PLUGIN_ROOT}/bin/obor mcp" ||
		len(h.Env) != 1 || h.Env["OBOR_NO_SELF_HOST"] != "1" {
		t.Errorf("plugin.json mcpServers: %s", b)
	}
	// DD-v0.13 B1: the bridge's entry is the shipped one.
	k, _ := agents.Lookup("claude")
	m := k.Install(kit, nil).MCP
	if state, path, err := registration(m); state != entryPinned || err != nil || path != filepath.Join(kit, ".claude-plugin", "plugin.json") {
		t.Errorf("registration of the kit's manifest = %d, %s, %v", state, path, err)
	}
	m.Path = filepath.Join(kit, "nope.json")
	if state, _, err := registration(m); state != entryNone || err != nil {
		t.Errorf("registration of a missing manifest = %d, %v", state, err)
	}
}

// T-582 (RV-62 #3), T-588: obor setup pane and setup role start Codex through launch's one start path, so the rollout
// the fresh start's process holds open (the fake herdr's agent: pid 2) is recorded as the agent's session: serve down
// here, so it is said and nothing is recorded (no .sid file: T-599).
func TestCodexSetupStartRecordsItsSession(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, agent, wt string
		args            []string
		edit            func(map[string]any)
	}{
		{"setup pane", "pj-l-01", "L-01", []string{"pane", "--start", "L-01", "--tab", "w1:t1"}, func(map[string]any) {}},
		{"setup role", "pj-rev", "review", []string{"role", "reviewer", "--tab", "w1:t1", "--start"},
			testkit.Set("agents.roles", `{"reviewer": "luna"}`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, c.edit)
			x.rollout = 10 * time.Second
			if c.args[0] == "pane" {
				x.lane(t, c.wt)
				x.State(c.wt, map[string]any{"task": "T-1", "type": "luna"})
			}
			cwd := x.LaneDir(c.wt)
			rollout := filepath.Join(x.Base, "home", ".codex", "sessions", "2026", "10", "30", "rollout-cx-new.jsonl")
			x.open = func(pids []int) []string {
				if slices.Contains(pids, 2) {
					return []string{rollout}
				}
				return nil
			}
			x.srv.Before(func(r herdrtest.Request) {
				if r.Method != "agent.start" {
					return
				}
				at := time.Now().UTC()
				x.Write(rollout,
					`{"timestamp":"`+at.Format(time.RFC3339Nano)+`","type":"session_meta","payload":{"id":"cx-new","timestamp":"`+
						at.Format(time.RFC3339Nano)+`","cwd":"`+cwd+`"}}`+"\n")
			})
			out := x.ok(t, c.args...)
			has(t, strings.Join(x.reqs(), "\n"), "kind=codex name="+c.agent, "precondition: a codex start")
			has(t, out, "session cx-new (", "the new session found")
			has(t, out, "session cx-new of "+c.agent+" not recorded", "serve down: said, nothing recorded")
			if _, err := os.Stat(filepath.Join(x.Env().Get("OBOR_STATE_HOME"), "agents", c.agent+".sid")); err == nil {
				t.Errorf("a .sid file written\n%s", out)
			}
		})
	}
}

// T-1058 (RV-151 #1): setup's direct starter goes through the one start: herdr accepts it at once with launch_pending
// and lists the start's kind with a stale idle, still pending, for three reads: OK only once the new agent is up
// (interactive_ready); a replacement never up is a FAIL, not OK.
func TestSetupStartWaitsForTheLaunch(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		reads int
		ok    bool
	}{
		{"up after three pending reads: OK", 3, true},
		{"pending past the bound: FAIL", 1 << 30, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t)
			x.poll, x.up = time.Millisecond, 100*time.Millisecond
			x.lane(t, "L-01")
			x.h.Set("w1:p1", func(p *herdrtest.Pane) { p.StartPending = c.reads })
			x.reset()
			out, rc := x.run(t, "pane", "--start", "L-01", "--pane", "w1:p1")
			reqs := named(strings.Join(x.reqs(), "\n"))
			has(t, reqs, "agent.start ", "precondition: the start was sent")
			reads := strings.Count(reqs[strings.LastIndex(reqs, "agent.start "):], "agent.get target=w1:p1")
			if !c.ok {
				if rc == 0 || strings.HasPrefix(last(out), "OK ") {
					t.Fatalf("a start whose agent never came up said OK (exit %d):\n%s", rc, out)
				}
				has(t, out, "no claude agent came up in w1:p1 within 100ms", "why")
				return
			}
			if rc != 0 || last(out) != "OK L-01: pj-l-01 in pane w1:p1" {
				t.Fatalf("exit %d:\n%s", rc, out)
			}
			p := x.h.Pane("w1:p1")
			x.h.Locked(func() {
				if p.StartPending != 0 || !p.InteractiveReady {
					t.Errorf("pending reads left %d, interactive_ready %t: want the launch read until up", p.StartPending, p.InteractiveReady)
				}
			})
			if reads < 4 {
				t.Errorf("%d reads of w1:p1 after the start: want the 3 pending ones and the one up", reads)
			}
		})
	}
}
