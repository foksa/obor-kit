package app

// T-677: a role agent whose only labelled pane is at its shell is stopped in status, whoever stopped it — serve once its
// work was done (T-672) or the lead's obor restart <agent> --stop — for every role, not only the two serve stops.

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// stoppedRoot is a project naming every role the lead may stop: the integrator, the reviewer, the advisor and the deep
// dive (the last two optional roles, which count only when configured).
func stoppedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":"","reviewer":"","advisor":"","deep_dive":""}}`
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// stoppedApp is an app on root, loaded and connected to one snapshot: the lead's pane and panes.
func stoppedApp(t *testing.T, root string, panes ...port.Pane) (*App, *proj) {
	t.Helper()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a := New(Options{Now: func() time.Time { return now }, Loop: func(string) (*Loop, error) { return &Loop{}, nil }},
		&fakePanes{}, &fakeLanes{})
	p, err := a.load(root)
	if err != nil {
		t.Fatal(err)
	}
	a.p = p
	t.Cleanup(func() { p.log.Close(); a.closeLog() })
	all := append([]port.Pane{{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"}},
		panes...)
	a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: all})
	return a, p
}

// shell is a pane labelled agent that runs no agent: the pane launch.Stop leaves, whether serve or the lead stopped it.
func shell(root, id, agent string) port.Pane {
	return port.Pane{ID: id, Workspace: "w", Cwd: root, Label: agent}
}

// The advisor and the deep dive, which serve never stops, are stopped in status when the lead's stop left their pane at
// a shell: no role.stop result is in the log at all.
func TestStoppedRolesLeadStopped(t *testing.T) {
	t.Parallel()
	root := stoppedRoot(t)
	a, p := stoppedApp(t, root, shell(root, "w:adv", "pj-advisor"), shell(root, "w:dd", "pj-deep-dive"))
	for _, rec := range readLog(t, p.root) {
		if rec.Task == event.RoleTask {
			t.Fatalf("precondition: a role stop is recorded: %s %s", rec.Task, rec.Step)
		}
	}
	got := a.Status().Project.Stopped
	if want := []string{"pj-advisor", "pj-deep-dive"}; !slices.Equal(got, want) {
		t.Errorf("status stopped %q, want %q", got, want)
	}
}

// The reviewer and the integrator at a shell show stopped with no role.stop result either (an integration branch's
// review never closed, a verdict never recorded): it is the pane that says so. The integrator stays no brief's to
// start, though.
func TestStoppedRolesAtShellWithoutServeStop(t *testing.T) {
	t.Parallel()
	root := stoppedRoot(t)
	a, p := stoppedApp(t, root, shell(root, "w:int", "pj-int"), shell(root, "w:rev", "pj-rev"))
	for _, rec := range readLog(t, p.root) {
		if rec.Task == event.RoleTask {
			t.Fatalf("precondition: a role stop is recorded: %s %s", rec.Task, rec.Step)
		}
	}
	if got, want := a.Status().Project.Stopped, []string{"pj-int", "pj-rev"}; !slices.Equal(got, want) {
		t.Errorf("status stopped %q, want %q", got, want)
	}
	if !p.roleAgent("pj-int") || p.roleAgentOf("pj-int") {
		t.Errorf("pj-int: role agent %v, brief starts it %v; want true, false",
			p.roleAgent("pj-int"), p.roleAgentOf("pj-int"))
	}
	if !p.roleAgentOf("pj-rev") {
		t.Error("pj-rev: a brief does not start the stopped reviewer")
	}
}

// Two panes labelled with one agent: which runs it is unknown, so it is not called stopped.
func TestStoppedRolesTwoLabelledPanesUnknown(t *testing.T) {
	t.Parallel()
	root := stoppedRoot(t)
	a, p := stoppedApp(t, root, shell(root, "w:adv1", "pj-advisor"), shell(root, "w:adv2", "pj-advisor"))
	w := a.whereIs(p, "pj-advisor")
	if w.State != Unavailable || w.Why == notConnected {
		t.Fatalf("precondition: pj-advisor %+v, want no identified pane while herdr is connected", w)
	}
	if got := a.Status().Project.Stopped; len(got) != 0 {
		t.Errorf("status stopped %q with two panes labelled pj-advisor, want unknown (not stopped)", got)
	}
}

// Herdr down: serve knows no pane, so a stop it cannot see is not called stopped either.
func TestStoppedRolesHerdrDownUnknown(t *testing.T) {
	t.Parallel()
	root := stoppedRoot(t)
	a, _ := stoppedApp(t, root, shell(root, "w:adv", "pj-advisor"))
	if got := a.Status().Project.Stopped; !slices.Equal(got, []string{"pj-advisor"}) {
		t.Fatalf("precondition: connected, status stopped %q, want [pj-advisor]", got)
	}
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()
	st := a.Status()
	if st.Connected {
		t.Fatal("precondition: status still says herdr is connected")
	}
	if got := st.Project.Stopped; len(got) != 0 {
		t.Errorf("status stopped %q with herdr down, want unknown (not stopped)", got)
	}
}

// A configured role agent with no labelled pane is "no pane", not stopped: nothing was stopped, no pane is there.
func TestStoppedRolesNoPane(t *testing.T) {
	t.Parallel()
	root := stoppedRoot(t)
	a, p := stoppedApp(t, root)
	if !p.roleAgent("pj-advisor") {
		t.Fatal("precondition: pj-advisor is not a configured role agent")
	}
	if w := a.whereIs(p, "pj-advisor"); w.State != Unavailable || w.Why == notConnected {
		t.Fatalf("precondition: pj-advisor %+v, want unavailable while herdr is connected", w)
	}
	if got := a.Status().Project.Stopped; len(got) != 0 {
		t.Errorf("status stopped %q with no pane of pj-advisor, want none (no pane)", got)
	}
}
