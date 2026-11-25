package web

import (
	"context"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/project"
	"obor/internal/view"
)

// overviewOf is /api/projects' one entry for a server whose only project's view is v.
func overviewOf(t *testing.T, v view.Project) ProjectsEntry {
	t.Helper()
	s := &Server{Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: "/work/pj"}} },
		Build: func(context.Context, string) (view.Project, error) { return v, nil }}
	var got []ProjectsEntry
	decode(t, get(t, s.Handler(), "/api/projects"), &got)
	if len(got) != 1 || got[0].Error != "" {
		t.Fatalf("/api/projects: %+v, want one project without an error", got)
	}
	return got[0]
}

// TestOverviewListsAgentsWaitingForYou: a blocked lead, a blocked role agent and a blocked lane worker are the
// entry's three waiting agents, each with its role, since when (serve's age back from now) and its pane's note as
// text (markup kept as written, for the page to escape; none when the pane has none); the needs-you count stays
// serve's records.
func TestOverviewListsAgentsWaitingForYou(t *testing.T) {
	t.Parallel()
	v := view.Project{Serve: view.Serve{Up: true},
		Lead:  &view.Role{Role: "lead", Agent: "pj-lead", Status: "blocked", Age: 21, Note: "permission: approve Bash: ls"},
		Roles: []view.Role{{Role: "reviewer", Agent: "pj-rev", Status: "blocked", Age: 60}},
		Lanes: []view.Lane{{Lane: port.Lane{Name: "L-01", Task: "T-01", Agent: "pj-l1"}, AgentStatus: "blocked",
			AgentNote: "question: <b>Trust</b> this folder?", Column: "working"}},
		Agents:   []wire.AgentStatus{{Name: "pj-l1", Lane: "L-01", Status: "blocked", Age: 300}},
		NeedsYou: []view.NeedsYou{{Task: "T-02", Reason: "held"}}}
	before := time.Now().Unix()
	e := overviewOf(t, v)
	after := time.Now().Unix()
	if e.NeedsYou != 1 {
		t.Errorf("needs-you %d, want 1: serve's records only, the waiting agents counted apart", e.NeedsYou)
	}
	want := []struct {
		agent, role, lane, note string
		age                     int64
	}{{"pj-lead", "lead", "", "permission: approve Bash: ls", 21}, {"pj-rev", "reviewer", "", "", 60},
		{"pj-l1", "worker", "L-01", "question: <b>Trust</b> this folder?", 300}}
	if len(e.Waiting) != len(want) {
		t.Fatalf("waiting %+v, want the lead, the reviewer and L-01's worker", e.Waiting)
	}
	for i, w := range want {
		g := e.Waiting[i]
		if g.Agent != w.agent || g.Role != w.role || g.Lane != w.lane || g.Note != w.note {
			t.Errorf("waiting[%d] %+v, want %s (%s, lane %q, note %q)", i, g, w.agent, w.role, w.lane, w.note)
		}
		if g.Since < before-w.age || g.Since > after-w.age {
			t.Errorf("waiting[%d] since %d, want now less its age %ds (%d..%d)", i, g.Since, w.age, before-w.age, after-w.age)
		}
	}
}

// TestOverviewWaitingSkipsWorkingAndDone: an agent working, done or idle is not waiting: the list is empty (not
// null), and a needs-you record does not make one.
func TestOverviewWaitingSkipsWorkingAndDone(t *testing.T) {
	t.Parallel()
	v := view.Project{Serve: view.Serve{Up: true},
		Lead:     &view.Role{Role: "lead", Agent: "pj-lead", Status: "working", Age: 21},
		Roles:    []view.Role{{Role: "reviewer", Agent: "pj-rev", Status: "done"}, {Role: "advisor", Agent: "pj-adv", Status: "idle"}},
		Lanes:    []view.Lane{{Lane: port.Lane{Name: "L-01", Task: "T-01", Agent: "pj-l1"}, AgentStatus: "working", Column: "working"}},
		Agents:   []wire.AgentStatus{{Name: "pj-l1", Lane: "L-01", Status: "working", Age: 300}},
		NeedsYou: []view.NeedsYou{{Task: "T-01", Reason: "held"}}}
	e := overviewOf(t, v)
	if e.NeedsYou != 1 || e.Working != 1 {
		t.Fatalf("precondition: needs-you %d, working %d; want 1 and the working lead", e.NeedsYou, e.Working)
	}
	if e.Waiting == nil || len(e.Waiting) != 0 {
		t.Errorf("waiting %+v, want an empty list", e.Waiting)
	}
}
