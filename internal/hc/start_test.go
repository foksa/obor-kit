package hc_test

import (
	"slices"
	"testing"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/testkit"
)

// hc's Start is shellpane's start (the rule's rows are there) over the terminal record: one row that it reads hc's
// facts and acts on them. Up: the quoted command line typed at the shell after the obor.agent and obor.kind labels,
// the pane as hc then shows it; a terminal whose foreground is not up is PaneBusy, nothing typed; a program that
// reports blocked is NotReady and Accepted.
func TestStart(t *testing.T) {
	core := shellCore()
	p, err := core.Client().Start(ctx, port.AgentStart{Pane: "t1", Label: "pj-w1", Kind: "claude", Program: "claude",
		Argv: []string{"--model", "a b's"}})
	if err != nil || p.ID != "t1" || p.Agent != "claude" || p.Label != "pj-w1" {
		t.Errorf("Start = %+v, %v; want t1 running claude labelled pj-w1", p, err)
	}
	if got, want := core.Sent("t1"), []string{`claude --model 'a b'\''s'`}; !slices.Equal(got, want) {
		t.Errorf("typed %q, want %q", got, want)
	}
	if l := core.Labels("t1"); l[hc.LabelAgent] != "pj-w1" || l[hc.LabelKind] != "claude" {
		t.Errorf("labels %v, want obor.agent pj-w1 and obor.kind claude", l)
	}
	snap, _ := (hc.Panes{C: core.Client()}).Snapshot(ctx)
	if len(snap.Panes) != 1 || snap.Panes[0].Group != "proj" {
		t.Errorf("after the start the snapshot %+v; want the pane in proj", snap)
	}

	core = shellCore()
	core.Update("t1", func(r *hc.Record) { r.Foreground = nil })
	if _, err := core.Client().Start(ctx, port.AgentStart{Pane: "t1", Kind: "claude", Program: "claude"}); !port.PaneBusy(err) || len(core.Sent("t1")) != 0 {
		t.Errorf("Start with no foreground = %v, typed %q; want PaneBusy, nothing typed", err, core.Sent("t1"))
	}

	core = shellCore()
	core.Program("claude", &hc.Status{State: "blocked", Kind: "permission", Msg: "trust this folder"})
	if _, err := core.Client().Start(ctx, port.AgentStart{Pane: "t1", Kind: "claude", Program: "claude"}); !port.NotReady(err) || !port.Accepted(err) {
		t.Errorf("Start at a blocked report = %v; want NotReady and Accepted", err)
	}
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
