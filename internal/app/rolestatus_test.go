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
	all := append([]port.Pane{{ID: "w:lead", Group: "proj", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"}},
		panes...)
	a.connect(context.Background(), port.Snapshot{Panes: all})
	return a, p
}

// shell is a pane labelled agent that runs no agent: the pane launch.Stop leaves, whether serve or the lead stopped it.
func shell(root, id, agent string) port.Pane {
	return port.Pane{ID: id, Group: "proj", Cwd: root, Label: agent}
}

// T-677: status calls a role agent stopped only when herdr shows exactly one labelled pane of it at its shell — whoever
// stopped it (serve after its work, or the lead's obor restart <agent> --stop), for every role and with no role.stop
// result in the log. Two labelled panes (which runs it is unknown), herdr down and no pane are not stopped.
func TestStoppedRoles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		panes func(root string) []port.Pane
		want  []string
		check func(t *testing.T, a *App, p *proj)
	}{
		{"the lead stopped the advisor and the deep dive", func(root string) []port.Pane {
			return []port.Pane{shell(root, "w:adv", "pj-advisor"), shell(root, "w:dd", "pj-deep-dive")}
		}, []string{"pj-advisor", "pj-deep-dive"}, nil},
		{"reviewer and integrator at a shell", func(root string) []port.Pane {
			return []port.Pane{shell(root, "w:int", "pj-int"), shell(root, "w:rev", "pj-rev")}
		}, []string{"pj-int", "pj-rev"}, func(t *testing.T, a *App, p *proj) {
			if !p.roleAgent("pj-int") || p.roleAgentOf("pj-int") {
				t.Errorf("pj-int: role agent %v, brief starts it %v; want true, false",
					p.roleAgent("pj-int"), p.roleAgentOf("pj-int"))
			}
			if !p.roleAgentOf("pj-rev") {
				t.Error("pj-rev: a brief does not start the stopped reviewer")
			}
		}},
		{"two panes of one agent", func(root string) []port.Pane {
			return []port.Pane{shell(root, "w:adv1", "pj-advisor"), shell(root, "w:adv2", "pj-advisor")}
		}, nil, func(t *testing.T, a *App, p *proj) {
			if w := a.whereIs(p, "pj-advisor"); w.State != Unavailable || w.Why == notConnected {
				t.Errorf("pj-advisor %+v, want no identified pane while herdr is connected", w)
			}
		}},
		{"herdr down", func(root string) []port.Pane {
			return []port.Pane{shell(root, "w:adv", "pj-advisor")}
		}, []string{"pj-advisor"}, func(t *testing.T, a *App, p *proj) {
			a.mu.Lock()
			a.connected = false
			a.mu.Unlock()
			st := a.Status()
			if st.Connected {
				t.Fatal("status still says herdr is connected")
			}
			if got := st.Project.Stopped; len(got) != 0 {
				t.Errorf("status stopped %q with herdr down, want unknown (not stopped)", got)
			}
		}},
		{"no pane", nil, nil, func(t *testing.T, a *App, p *proj) {
			if !p.roleAgent("pj-advisor") {
				t.Fatal("pj-advisor is not a configured role agent")
			}
			if w := a.whereIs(p, "pj-advisor"); w.State != Unavailable || w.Why == notConnected {
				t.Errorf("pj-advisor %+v, want unavailable while herdr is connected", w)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := stoppedRoot(t)
			var panes []port.Pane
			if c.panes != nil {
				panes = c.panes(root)
			}
			a, p := stoppedApp(t, root, panes...)
			for _, rec := range readLog(t, p.root) {
				if rec.Task == event.RoleTask {
					t.Fatalf("precondition: a role stop is recorded: %s %s", rec.Task, rec.Step)
				}
			}
			if got := a.Status().Project.Stopped; !slices.Equal(got, c.want) {
				t.Errorf("status stopped %q, want %q", got, c.want)
			}
			if c.check != nil {
				c.check(t, a, p)
			}
		})
	}
}
