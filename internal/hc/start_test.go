package hc_test

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/testkit"
)

// A start types the kind's program and its argv, each word quoted, at the pane's shell after labelling the pane with
// the agent and its kind, and is up once the program in the foreground reports a status naming the kind: idle, or
// working on its first prompt. A program that reports blocked waits at a dialog (NotReady at once: Claude's trust
// dialog); one that reports nothing by the bound may wait at a silent one (NotReady at the bound: Codex's trust
// dialog); a shell that never gives the foreground up is TimedOut. Each failure after the typing is Accepted. A
// terminal whose shell is not up is PaneBusy, nothing typed.
func TestStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		program *hc.Status // what claude reports once up (nil: nothing); no program at all when noProgram
		noProg  bool
		noShell bool
		prompt  string
		check   func(err error) bool
		after   time.Duration // when the start returns
	}{
		{name: "up", program: &hc.Status{State: "idle", App: "claude-code"}},
		{name: "working on its first prompt", program: &hc.Status{State: "working", App: "claude-code"}, prompt: "go"},
		{name: "trust dialog", program: &hc.Status{State: "blocked", Kind: "permission", Msg: "trust this folder"},
			check: func(err error) bool { return port.NotReady(err) && port.Accepted(err) }},
		{name: "silent dialog", check: func(err error) bool { return port.NotReady(err) && port.Accepted(err) },
			after: 5 * time.Second},
		{name: "no agent", noProg: true, check: func(err error) bool { return port.TimedOut(err) && port.Accepted(err) },
			after: 5 * time.Second},
		{name: "shell not up", noShell: true, check: func(err error) bool { return port.PaneBusy(err) && !port.Accepted(err) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				core := shellCore()
				if tc.noProg {
					core = agentlessCore()
				} else {
					core.Program("claude", tc.program)
				}
				if tc.noShell {
					core.Update("t1", func(r *hc.Record) { r.Foreground = nil })
				}
				began := time.Now()
				p, err := core.Client().Start(ctx, port.AgentStart{Pane: "t1", Label: "pj-w1", Kind: "claude", Program: "claude",
					Argv: []string{"--model", "a b's"}, Prompt: tc.prompt, Bound: 5 * time.Second, Poll: time.Second})
				if d := time.Since(began); d != tc.after {
					t.Errorf("Start returned after %s, want %s", d, tc.after)
				}
				switch {
				case tc.check == nil && (err != nil || p.ID != "t1" || p.Agent != "claude" || p.Label != "pj-w1"):
					t.Errorf("Start = %+v, %v; want t1 running claude labelled pj-w1", p, err)
				case tc.check != nil && !tc.check(err):
					t.Errorf("Start = %+v, %v; want the %s failure", p, err, tc.name)
				}
				typed := []string{`claude --model 'a b'\''s'`}
				if tc.noShell {
					typed = nil
				}
				if got := core.Sent("t1"); !slices.Equal(got, typed) {
					t.Errorf("typed %q, want %q", got, typed)
				}
				if !tc.noShell {
					pane, _ := core.Client().Pane(ctx, "t1")
					snap, _ := (hc.Panes{C: core.Client()}).Snapshot(ctx)
					if pane.Label != "pj-w1" || len(snap.Panes) != 1 || snap.Panes[0].Group != "proj" {
						t.Errorf("after the start pane %+v, snapshot %+v; want it labelled pj-w1 in proj", pane, snap)
					}
				}
				core.Down()
			})
		})
	}
}

// agentlessCore is shellCore with no program the shell runs: a typed command line leaves the shell in the foreground.
func agentlessCore() *hctest.Core {
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", PID: 41000, Cwd: "/proj", Labels: map[string]string{hc.LabelProject: "proj"},
		Foreground: &hc.Foreground{PID: 41000, Name: "zsh", Cwd: "/proj"}})
	return core
}

// The kind label a start sets names the agent's kind while its status names none it knows (an app no kind claims).
func TestStartKindLabel(t *testing.T) {
	core := shellCore()
	core.Program("claude", &hc.Status{State: "idle", App: "some-app"})
	p, err := core.Client().Start(ctx, port.AgentStart{Pane: "t1", Label: "pj-w1", Kind: "claude", Program: "claude"})
	if err != nil || p.Agent != "claude" {
		t.Errorf("Start = %+v, %v; want claude, named by the label", p, err)
	}
}

// A start reads no kind: it types the program the caller gives, so a plugin kind the client's kinds do not hold yet
// (a config that gained it since the client was built) starts.
func TestStartTakesTheProgramFromTheStart(t *testing.T) {
	core := shellCore()
	core.Program("opencode", &hc.Status{State: "idle", App: "opencode"})
	cl := core.Client()
	cl.Kinds = func() agents.Kinds { return agents.Kinds{} } // the compiled kinds: no opencode
	if _, ok := cl.Kinds().Lookup("opencode"); ok {
		t.Fatal("precondition: the client's kinds hold opencode")
	}
	p, err := cl.Start(ctx, port.AgentStart{Pane: "t1", Label: "pj-w1", Kind: "opencode", Program: "opencode"})
	if err != nil || p.Agent != "opencode" {
		t.Errorf("Start = %+v, %v; want opencode running in t1", p, err)
	}
	if got := core.Sent("t1"); !slices.Equal(got, []string{"opencode"}) {
		t.Errorf("typed %q, want the program alone", got)
	}
}

// A start with no program is refused before anything is typed or labelled: the kind's program is the launcher's to
// give, and no default stands in for it.
func TestStartWithoutProgramIsRefused(t *testing.T) {
	core := shellCore()
	_, err := core.Client().Start(ctx, port.AgentStart{Pane: "t1", Label: "pj-w1", Kind: "claude"})
	if err == nil || !strings.Contains(err.Error(), "no program for agent kind claude") {
		t.Errorf("Start = %v; want the no-program refusal", err)
	}
	if got := core.Sent("t1"); len(got) != 0 {
		t.Errorf("typed %q, want nothing", got)
	}
	if l := core.Labels("t1"); l[hc.LabelKind] != "" {
		t.Errorf("labels %v, want none set", l)
	}
}

// The status-to-kind mapping reads Client.Kinds at each use: a plugin kind added to the config since the client was
// built names the agent of the next snapshot.
func TestStatusKindFollowsTheClientsKinds(t *testing.T) {
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", PID: 41000, Labels: map[string]string{hc.LabelProject: "proj"},
		Foreground: &hc.Foreground{PID: 41001, Name: "opencode"}, Status: &hc.Status{State: "idle", App: "opencode"}})
	kinds := agents.Kinds{}
	cl := core.Client()
	cl.Kinds = func() agents.Kinds { return kinds }
	agentOf := func() string {
		snap, err := (hc.Panes{C: cl}).Snapshot(ctx)
		if err != nil || len(snap.Panes) != 1 {
			t.Fatalf("Snapshot = %+v, %v; want one pane", snap, err)
		}
		return snap.Panes[0].Agent
	}
	if got := agentOf(); got != "" {
		t.Fatalf("precondition: agent %q before the kind is added, want none", got)
	}
	kinds = testkit.Kinds() // the config gained opencode
	if got := agentOf(); got != "opencode" {
		t.Errorf("agent %q after the kind is added, want opencode", got)
	}
}
