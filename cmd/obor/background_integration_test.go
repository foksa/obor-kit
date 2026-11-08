//go:build integration

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/testkit"
	"obor/internal/view"
)

// backgroundView is a project with a lane agent running a recorded background call, a role agent running an unrecorded
// long command (a foreground one, T-727) and the lead running a recorded one with no description, as view.Build joins
// them.
func backgroundView(t *testing.T) (view.Project, string, string) {
	t.Helper()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	lead, integ := cfg.AgentName("lead", ""), cfg.AgentName("integrator", "")
	ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root, Merging: true,
		Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-l1", Lane: "L-01", Status: "working"},
			{Pane: "w1:p2", Name: integ, Status: "idle", Age: 60}, {Pane: "w1:p3", Name: lead, Status: "working", Age: 60}}}}
	v := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), Now: time.Now(),
		Background: map[string][]view.Background{
			"pj-l1": {{PID: 42, Command: "python convert.py", Background: true, Label: "convert maps", Age: 900}},
			integ:   {{PID: 43, Command: "make -s check", Age: 95}},
			lead:    {{PID: 44, Command: "sleep 600", Background: true, Age: 45}}}})
	return v, lead, integ
}

// T-714, T-727: obor status shows each agent's long processes on a line under its own — the lane's under the lane
// line, a role's and the lead's under their agent line — by description, else command, with the age: bg for a
// recorded background call, running for another.
func TestStatusShowsBackground(t *testing.T) {
	t.Parallel()
	v, lead, integ := backgroundView(t)
	var b bytes.Buffer
	writeStatus(&b, v, time.Now())
	text := b.String()
	for _, pair := range [][2]string{{"L-01 ", "  bg: convert maps · 15m"}, {"agent " + integ + ":", "  running make -s check · 1m"},
		{"agent " + lead + ":", "  bg: sleep 600 · 45s"}} {
		i := strings.Index(text, pair[0])
		if i < 0 {
			t.Fatalf("precondition: no line %q in\n%s", pair[0], text)
		}
		rest := text[i:]
		next := rest[strings.Index(rest, "\n")+1:]
		if !strings.HasPrefix(next, pair[1]+"\n") {
			t.Errorf("under %q: %q, want %q in\n%s", pair[0], strings.SplitN(next, "\n", 2)[0], pair[1], text)
		}
	}
}
