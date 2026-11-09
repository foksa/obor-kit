package background

import (
	"strings"
	"testing"
	"time"

	"obor/internal/project"
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

// T-1142: a recorded call is live while a process runs its command, one that started just after the call was recorded
// (a shell that wraps it, its quoting undone, included); its process gone, an old record (a newer run of the same
// command), a record after the start, and a fragment too short to tell are not. The newest live call is the one.
func TestLive(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	call := func(secsAgo int64, cmd string) project.BackgroundCall {
		return project.BackgroundCall{At: at.Add(-time.Duration(secsAgo) * time.Second).Unix(), Session: "s1",
			Agent: "w1", Description: "convert maps", Command: cmd}
	}
	// the agent (pid 500) runs each call in a shell of its own, the call's process under it
	table := func(age time.Duration, command string) []Proc {
		return []Proc{{PID: 500, PPID: 1, Age: time.Hour, Command: "claude"},
			{PID: 899, PPID: 500, Age: age, Command: "/bin/zsh -c x"}, {PID: 900, PPID: 899, Age: age, Command: command}}
	}
	zsh := `/bin/zsh -c -l source /tmp/snap.sh && eval 'python convert.py --name '\''a b'\''' < /dev/null`
	if c, ok := Live([]project.BackgroundCall{call(5, "python convert.py --name 'a b'")}, table(5*time.Second, zsh), 500, at); !ok ||
		c.Description != "convert maps" {
		t.Errorf("the shell wrapper: %+v %v, want the call", c, ok)
	}
	if _, ok := Live([]project.BackgroundCall{call(5, "go test ./...")}, nil, 500, at); ok {
		t.Error("no process: live, want ended")
	}
	if _, ok := Live([]project.BackgroundCall{call(3600, "sleep 600")}, table(5*time.Second, "sleep 600"), 500, at); ok {
		t.Error("a record from an hour before the start: live, want none (a newer run of the same command)")
	}
	if _, ok := Live([]project.BackgroundCall{call(-60, "sleep 600")}, table(5*time.Second, "sleep 600"), 500, at); ok {
		t.Error("a record after the start: live, want none")
	}
	if _, ok := Live([]project.BackgroundCall{call(5, "make check ./...")}, table(5*time.Second, "make"), 500, at); ok {
		t.Error("a 4-character fragment: live, want none")
	}
	older, newer := call(5, "sleep 600"), call(4, "sleep 600")
	if c, ok := Live([]project.BackgroundCall{older, newer}, table(5*time.Second, "sleep 600"), 500, at); !ok || c.At != newer.At {
		t.Errorf("two live calls: %+v %v, want the newer (%d)", c, ok, newer.At)
	}
}

// T-1143 (RV-169 #1): a call is live only in the stopping agent's own processes: its descendant running the recorded
// command blocks; another agent's process running the same command (same start) does not, with the agent's own run
// alive or ended; an agent pid not in the table finds none.
func TestLiveOnlyTheAgentsDescendants(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	call := project.BackgroundCall{At: at.Add(-5 * time.Second).Unix(), Session: "s1", Agent: "w1",
		Description: "bench", Command: "go test -bench . ./..."}
	calls := []project.BackgroundCall{call}
	agents := []Proc{{PID: 500, PPID: 1, Age: time.Hour, Command: "claude"},
		{PID: 800, PPID: 1, Age: time.Hour, Command: "claude"}}
	mine := []Proc{{PID: 600, PPID: 500, Age: 5 * time.Second, Command: "/bin/zsh -c eval 'go test -bench . ./...'"},
		{PID: 601, PPID: 600, Age: 5 * time.Second, Command: "go test -bench . ./..."}}
	theirs := []Proc{{PID: 810, PPID: 800, Age: 5 * time.Second, Command: "/bin/zsh -c eval 'go test -bench . ./...'"},
		{PID: 811, PPID: 810, Age: 5 * time.Second, Command: "go test -bench . ./..."}}
	join := func(ps ...[]Proc) []Proc {
		var out []Proc
		for _, p := range ps {
			out = append(out, p...)
		}
		return out
	}
	if c, ok := Live(calls, join(agents, mine, theirs), 500, at); !ok || c.Description != "bench" {
		t.Errorf("the agent's own descendant: %+v %v, want the call live", c, ok)
	}
	if _, ok := Live(calls, join(agents, mine), 800, at); ok {
		t.Error("another agent's process runs the command: live for agent 800, want none of its")
	}
	if _, ok := Live(calls, join(agents, theirs), 800, at); !ok {
		t.Fatal("precondition: agent 800's own run is live for it")
	}
	if _, ok := Live(calls, join(agents, theirs), 500, at); ok {
		t.Error("own run ended, another agent's same command alive: live, want ended")
	}
	if _, ok := Live(calls, join(agents, mine), 0, at); ok {
		t.Error("no agent pid: live, want none")
	}
	if _, ok := Live(calls, join(agents, mine), 700, at); ok {
		t.Error("an agent pid not in the table: live, want none")
	}
}

// T-1143: a hook's agent is its first ancestor that is no shell (the hook command's shell passed through); a hook
// whose pid is not in the table, or whose ancestry ends at pid 1 through shells only or a pid not in the table, has
// none.
func TestAgentOf(t *testing.T) {
	procs := []Proc{{PID: 500, PPID: 1, Command: "/usr/local/bin/claude --resume x"},
		{PID: 600, PPID: 500, Command: "/bin/sh -c OBOR_NO_SELF_HOST=1 bash obor hook stop"},
		{PID: 601, PPID: 600, Command: "/Users/x/.obor/bin/obor hook stop"},
		{PID: 700, PPID: 1, Command: "-zsh"}, {PID: 701, PPID: 700, Command: "obor hook stop"},
		{PID: 801, PPID: 800, Command: "obor hook stop"}}
	if got := AgentOf(procs, 601); got != 500 {
		t.Errorf("through the hook's shell: agent %d, want 500 (claude)", got)
	}
	if got := AgentOf(procs, 600); got != 500 {
		t.Errorf("the shell's own agent: %d, want 500", got)
	}
	if got := AgentOf(procs, 701); got != 0 {
		t.Errorf("only a login shell up to pid 1: agent %d, want none", got)
	}
	if got := AgentOf(procs, 801); got != 0 {
		t.Errorf("the parent not in the table: agent %d, want none", got)
	}
	if got := AgentOf(procs, 999); got != 0 {
		t.Errorf("self not in the table: agent %d, want none", got)
	}
}

// T-1142: a call belongs to an agent by its session when one is known, and by its name, or (naming none) its pane,
// inside that; another session's, another agent's and no session's are none of its.
func TestMine(t *testing.T) {
	calls := []project.BackgroundCall{
		{Command: "a", Agent: "w1", Session: "s1", Pane: "p1"},
		{Command: "b", Agent: "w2", Session: "s1", Pane: "p1"},
		{Command: "c", Agent: "w1", Session: "s0", Pane: "p1"},
		{Command: "d", Agent: "", Session: "s1", Pane: "p1"},
		{Command: "e", Agent: "", Session: "s1", Pane: "p2"},
	}
	got := func(mine []project.BackgroundCall) string {
		var cmds []string
		for _, c := range mine {
			cmds = append(cmds, c.Command)
		}
		return strings.Join(cmds, "")
	}
	if g := got(Mine(calls, "w1", "s1", "p1")); g != "ad" {
		t.Errorf("w1 in s1/p1: %q, want a's and d's", g)
	}
	if g := got(Mine(calls, "w1", "", "p1")); g != "acd" {
		t.Errorf("no session known: %q, want the name's calls and its pane's (%q)", g, "acd")
	}
}

// T-1142: a call is named by its description, else its command as a view shows it.
func TestText(t *testing.T) {
	if got := Text(project.BackgroundCall{Description: "convert maps", Command: "python x.py"}); got != "convert maps" {
		t.Errorf("Text = %q, want the description", got)
	}
	if got := Text(project.BackgroundCall{Command: "/usr/bin/python3  x.py"}); got != "python3 x.py" {
		t.Errorf("Text = %q, want the shortened command", got)
	}
}

// T-714: a long command is cut with an ellipsis, its argv0 without its path.
func TestShorten(t *testing.T) {
	if got := Shorten("/usr/bin/python3   x.py"); got != "python3 x.py" {
		t.Errorf("Shorten = %q", got)
	}
	long := Shorten("go " + strings.Repeat("x", 100))
	if r := []rune(long); len(r) != commandMax || !strings.HasSuffix(long, "…") {
		t.Errorf("long: %q (%d runes), want %d with an ellipsis", long, len(r), commandMax)
	}
}
