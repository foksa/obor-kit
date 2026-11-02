package view

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// T-714: ps's lines (pid ppid etime command) parse into rows, every etime form included; a header or a broken line is
// skipped.
func TestParseProcs(t *testing.T) {
	out := "  PID  PPID ELAPSED COMMAND\n" +
		"  100     1   1-02:03:04 /usr/local/bin/claude --resume x\n" +
		"  101   100    01:02:03 /bin/zsh -c   eval 'sleep 600'\n" +
		"  102   101       00:45 sleep 600\n" +
		"  103   101      bad sleep\n"
	got := ParseProcs(out)
	want := []Proc{{100, 1, 26*time.Hour + 3*time.Minute + 4*time.Second, "/usr/local/bin/claude --resume x"},
		{101, 100, time.Hour + 2*time.Minute + 3*time.Second, "/bin/zsh -c eval 'sleep 600'"},
		{102, 101, 45 * time.Second, "sleep 600"}}
	if len(got) != len(want) {
		t.Fatalf("procs %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("proc %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// fakeTable is an agent (pid 100, up an hour) with: an MCP server started with it, and that server's own long child, a
// shell-wrapped MCP server started with it, a caffeinate it started later (helpers: left out); obor's MCP server
// restarted later (left out); a background Bash shell running a long python
// (shown, the shell passed through); a shell running a short sleep (too young); a long process that is no descendant.
func fakeTable() []Proc {
	s := time.Second
	return []Proc{
		{1, 0, 7200 * s, "/sbin/launchd"},
		{100, 1, 3600 * s, "claude"},
		{101, 100, 3598 * s, "node /opt/mcp/server.js"},
		{107, 100, 120 * s, "caffeinate -i -t 300"},
		{108, 100, 3599 * s, "/bin/sh -c npx some-mcp"},
		{109, 108, 3599 * s, "node some-mcp"},
		{110, 101, 3000 * s, "node /opt/mcp/worker.js"},
		{102, 100, 1000 * s, "/bin/bash /kit/bin/obor mcp"},
		{103, 100, 900 * s, `/bin/zsh -c -l source /tmp/snap.sh && eval 'python convert.py --name '\''a b'\''' < /dev/null`},
		{104, 103, 899 * s, "python3 convert.py --name a b"},
		{105, 100, 10 * s, "/bin/zsh -c eval 'sleep 5'"},
		{106, 105, 10 * s, "sleep 5"},
		{200, 1, 5000 * s, "python convert.py --name a b"},
	}
}

// T-714: an agent's background processes are its descendants that ran longer than the threshold — a shell with
// children passed through, the long child shown with its wrapping shell — and never its helpers: a direct child that is
// no shell (caffeinate, an MCP server), one that started with it (a shell-wrapped MCP server, with what it started) or
// obor's own MCP server.
func TestDescendants(t *testing.T) {
	found := NewProcIndex(fakeTable()).Descendants(100, BackgroundMin)
	if len(found) != 1 {
		t.Fatalf("found %+v, want only the long python", found)
	}
	f := found[0]
	if f.PID != 104 || len(f.Chain) != 1 || !strings.HasPrefix(f.Chain[0], "/bin/zsh -c -l") {
		t.Errorf("found %+v, want pid 104 under its zsh", f)
	}
	// Each exclusion holds on its own: with a 0 threshold the short sleep shows, the helpers still do not.
	pids := map[int]bool{}
	for _, f := range NewProcIndex(fakeTable()).Descendants(100, 0) {
		pids[f.PID] = true
	}
	if !pids[104] || !pids[106] || pids[101] || pids[110] || pids[102] || pids[107] || pids[108] || pids[109] || pids[200] {
		t.Errorf("no threshold: pids %v, want 104 and 106 only", pids)
	}
	if got := NewProcIndex(fakeTable()).Descendants(999, 0); got != nil {
		t.Errorf("an agent not in the table: %+v, want none", got)
	}
}

// T-714, T-727: BackgroundOf takes each agent's process from its pane's process info (its foreground leader), marks a
// process that runs a recorded background call (matched by command, through the shell's quoting) as background with
// the call's description, and gives the command shortened and the age; another agent's record does not mark it; an
// agent at its shell has none; an unrecorded long child is a running foreground command, no bg.
func TestBackgroundOf(t *testing.T) {
	root := t.TempDir()
	t0 := fixClock(t)
	started := t0.Unix() - 899 - 2 // the call, two seconds before pid 104 started
	project.AppendBackground(root, project.BackgroundCall{At: started, Agent: "w1", Session: "s1",
		Command: "python convert.py --name 'a b'", Description: "convert maps"})
	project.AppendBackground(root, project.BackgroundCall{At: started, Agent: "other", Session: "s1",
		Command: "python convert.py --name 'a b'", Description: "not mine"})
	info := func(_ context.Context, pane string) (port.ProcessInfo, error) {
		switch pane {
		case "p1":
			return port.ProcessInfo{ShellPID: 50, FgPGID: 100}, nil
		case "p2":
			return port.ProcessInfo{ShellPID: 60, FgPGID: 60}, nil // at its shell
		}
		return port.ProcessInfo{}, errors.New("no pane")
	}
	agents := []wire.AgentStatus{{Name: "w1", Pane: "p1", Session: "s1"}, {Name: "w2", Pane: "p2"}, {Name: "w3", Pane: "p3"}}
	got := BackgroundOf(context.Background(), root, agents, fakeTable(), info)
	if len(got) != 1 || len(got["w1"]) != 1 {
		t.Fatalf("background %+v, want one process of w1", got)
	}
	want := Background{PID: 104, Command: "python3 convert.py --name a b", Background: true, Label: "convert maps", Age: 899}
	if got["w1"][0] != want {
		t.Errorf("w1: %+v, want %+v", got["w1"][0], want)
	}
	if line := got["w1"][0].Line("15m"); line != "bg: convert maps · 15m" {
		t.Errorf("recorded line %q, want bg", line)
	}
	// No record of w1's (only another agent's): a long foreground command, the command alone, running.
	other := t.TempDir()
	project.AppendBackground(other, project.BackgroundCall{At: started, Agent: "other", Session: "s1", Command: "python convert.py --name 'a b'"})
	got = BackgroundOf(context.Background(), other, agents[:1], fakeTable(), info)
	if b := got["w1"]; len(b) != 1 || b[0].Background || b[0].Label != "" ||
		b[0].Line("15m") != "running python3 convert.py --name a b · 15m" {
		t.Errorf("unrecorded: %+v, want running with the command, no bg", b)
	}
}

// fixClock fixes the clock BackgroundOf reads for the test and returns it.
func fixClock(t *testing.T) time.Time {
	t0 := time.Unix(1_800_000_000, 0)
	old := now
	now = func() time.Time { return t0 }
	t.Cleanup(func() { now = old })
	return t0
}

// RV-85 verify #3: a record marks a process bg only when it is of the agent's current session and was recorded just
// before the process started: a newer foreground run of the same command (the old record, long before its start),
// a previous session's record and a record after the start are `running`; the matching one is bg.
func TestBackgroundOfMatchesTheStartingCall(t *testing.T) {
	t0 := fixClock(t)
	start := t0.Unix() - 899 // pid 104 started now minus its age
	cmd := "python convert.py --name 'a b'"
	info := func(context.Context, string) (port.ProcessInfo, error) {
		return port.ProcessInfo{ShellPID: 50, FgPGID: 100}, nil
	}
	agents := []wire.AgentStatus{{Name: "w1", Pane: "p1", Session: "s2"}}
	run := func(c project.BackgroundCall) Background {
		root := t.TempDir()
		c.Agent, c.Command = "w1", cmd
		project.AppendBackground(root, c)
		got := BackgroundOf(context.Background(), root, agents, fakeTable(), info)["w1"]
		if len(got) != 1 || got[0].PID != 104 {
			t.Fatalf("precondition: the long python: %+v", got)
		}
		return got[0]
	}
	if b := run(project.BackgroundCall{At: start - 3, Session: "s2", Description: "d"}); !b.Background || b.Label != "d" {
		t.Errorf("same session, recorded just before the start: %+v, want bg", b)
	}
	if b := run(project.BackgroundCall{At: start - 3600, Session: "s2", Description: "d"}); b.Background || b.Label != "" {
		t.Errorf("old record, newer foreground run of the same command: %+v, want running", b)
	}
	if b := run(project.BackgroundCall{At: start + 60, Session: "s2", Description: "d"}); b.Background {
		t.Errorf("record after the start: %+v, want running", b)
	}
	if b := run(project.BackgroundCall{At: start - 3, Session: "s1", Description: "d"}); b.Background || b.Label != "" {
		t.Errorf("a previous session's record: %+v, want running", b)
	}
}

// T-727: one scan builds one process index, whatever the number of agents: two agents with a long process each, one
// NewProcIndex; an agent with no pane or at its shell builds none.
func TestBackgroundOfOneIndexPerScan(t *testing.T) {
	s := time.Second
	procs := append(fakeTable(),
		Proc{300, 1, 3600 * s, "claude"},
		Proc{301, 300, 100 * s, "/bin/zsh -c eval 'go test ./...'"},
		Proc{302, 301, 99 * s, "go test ./..."})
	info := func(_ context.Context, pane string) (port.ProcessInfo, error) {
		switch pane {
		case "p1":
			return port.ProcessInfo{ShellPID: 50, FgPGID: 100}, nil
		case "p2":
			return port.ProcessInfo{ShellPID: 70, FgPGID: 300}, nil
		}
		return port.ProcessInfo{ShellPID: 60, FgPGID: 60}, nil
	}
	agents := []wire.AgentStatus{{Name: "w1", Pane: "p1"}, {Name: "w2", Pane: "p2"}}
	before := indexBuilds.Load()
	got := BackgroundOf(context.Background(), t.TempDir(), agents, procs, info)
	if len(got["w1"]) != 1 || len(got["w2"]) != 1 || got["w2"][0].PID != 302 {
		t.Fatalf("precondition: a process for each agent: %+v", got)
	}
	if n := indexBuilds.Load() - before; n != 1 {
		t.Errorf("%d indexes built for two agents, want one", n)
	}
	before = indexBuilds.Load()
	if got := BackgroundOf(context.Background(), t.TempDir(), []wire.AgentStatus{{Name: "w3", Pane: "p3"}}, procs, info); len(got) != 0 {
		t.Errorf("an agent at its shell: %+v, want none", got)
	}
	if n := indexBuilds.Load() - before; n != 0 {
		t.Errorf("%d indexes built with no agent process, want none", n)
	}
}

// T-714: a long command is cut with an ellipsis, its argv0 without its path.
func TestShorten(t *testing.T) {
	if got := shorten("/usr/bin/python3   x.py"); got != "python3 x.py" {
		t.Errorf("shorten = %q", got)
	}
	long := shorten("go " + strings.Repeat("x", 100))
	if r := []rune(long); len(r) != commandMax || !strings.HasSuffix(long, "…") {
		t.Errorf("long: %q (%d runes), want %d with an ellipsis", long, len(r), commandMax)
	}
}

// T-714: Build puts each agent's background processes on its lane, its role row and the lead's row, and --json carries
// them as background.
func TestBuildBackground(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	lead, integ := cfg.AgentName("lead", ""), cfg.AgentName("integrator", "")
	if lead == "" || integ == "" {
		t.Fatalf("precondition: the config names a lead and an integrator: %q %q", lead, integ)
	}
	ss := wire.Status{Project: &wire.ProjectStatus{Root: p.Root, Agents: []wire.AgentStatus{
		{Name: "w1", Pane: "p1", Lane: "O-01", Status: "working"}, {Name: integ, Pane: "p2", Status: "idle"},
		{Name: lead, Pane: "p3", Status: "working"}}}}
	bg := map[string][]Background{"w1": {{PID: 1, Command: "a", Age: 40}}, integ: {{PID: 2, Command: "b", Age: 50}},
		lead: {{PID: 3, Command: "c", Background: true, Label: "l", Age: 60}}}
	v := Build(Input{Status: ss, Lanes: checks.NewLaneTable(cfg), Now: time.Now(), Background: bg})
	if len(v.Lanes) != 1 || len(v.Lanes[0].Background) != 1 || v.Lanes[0].Background[0].PID != 1 {
		t.Errorf("lane: %+v, want w1's process", v.Lanes)
	}
	var role *Role
	for i := range v.Roles {
		if v.Roles[i].Agent == integ {
			role = &v.Roles[i]
		}
	}
	if role == nil || len(role.Background) != 1 || role.Background[0].PID != 2 {
		t.Errorf("integrator: %+v, want its process", role)
	}
	if v.Lead == nil || v.Lead.Agent != lead || len(v.Lead.Background) != 1 || v.Lead.Background[0].PID != 3 {
		t.Errorf("lead: %+v, want its row with its process", v.Lead)
	}
	if n := v.BackgroundCount(); n != 1 {
		t.Errorf("count %d, want 1: only the lead's recorded background call, not the two running commands", n)
	}
	if got := v.AgentBackground(lead); len(got) != 1 || got[0].PID != 3 {
		t.Errorf("AgentBackground(lead) = %+v", got)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"background":[{"pid":1,"command":"a","background":false,"age":40}]`) ||
		!strings.Contains(string(b), `"background":true,"label":"l"`) {
		t.Errorf("json has no background per agent: %s", b)
	}
}
