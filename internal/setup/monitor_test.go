package setup

// obor setup monitor (T-658): `obor monitor` in a tab labelled "system" of the project's workspace, the pane labelled
// <prefix>-monitor; the tab is reused when the project has one, and a monitor that runs is left alone.

import (
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
)

// TestSetupMonitor: the command makes the pane in a new "system" tab of the project's workspace, cwd the root,
// labelled <prefix>-monitor, running obor monitor; a monitor that runs there is reused ("already", nothing typed), a
// pane left at its shell gets the command again.
func TestSetupMonitor(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	out, rc := x.run(t, "monitor")
	if rc != 0 || !strings.Contains(out, "OK monitor: pj-monitor started in pane ") {
		t.Fatalf("obor setup monitor: exit %d\n%s", rc, out)
	}
	reqs := strings.Join(x.reqs(), "\n")
	if !strings.Contains(reqs, "tab.create cwd="+x.Root+" focus=false label=system workspace_id=w1") {
		t.Errorf("no system tab in the project's workspace:\n%s", reqs)
	}
	mon := x.monitorPane(t)
	if !strings.Contains(reqs, "pane.rename label=pj-monitor pane_id="+mon.ID) {
		t.Errorf("the monitor pane %s is not labelled pj-monitor:\n%s", mon.ID, reqs)
	}
	if !strings.Contains(reqs, "pane.send_input keys=[Enter] pane_id="+mon.ID+" text=obor monitor") {
		t.Errorf("obor monitor not typed in pane %s:\n%s", mon.ID, reqs)
	}
	if x.h.TabLabels[mon.Tab] != "system" {
		t.Errorf("the monitor's tab %s is labelled %q, want system", mon.Tab, x.h.TabLabels[mon.Tab])
	}

	// A monitor runs there: a rerun types nothing and makes no tab.
	x.reset()
	mon.Foreground = 2
	out, rc = x.run(t, "monitor")
	if rc != 0 || !strings.Contains(out, "OK monitor: pj-monitor runs in pane "+mon.ID+" (already)") {
		t.Errorf("rerun: exit %d\n%s", rc, out)
	}
	if reqs := x.reqs(); len(reqs) != 0 {
		for _, r := range reqs {
			for _, w := range []string{"tab.create", "pane.split", "pane.rename", "pane.send_input", "agent.start"} {
				if strings.HasPrefix(r, w) {
					t.Errorf("rerun sent %s, want no pane change", r)
				}
			}
		}
	}

	// The pane is back at its shell (the monitor ended): the command is typed again.
	x.reset()
	mon.Foreground = 0
	out, rc = x.run(t, "monitor")
	if rc != 0 || !strings.Contains(out, "pj-monitor started in pane "+mon.ID) {
		t.Errorf("after the monitor ended: exit %d\n%s", rc, out)
	}
	if reqs := strings.Join(x.reqs(), "\n"); !strings.Contains(reqs, "text=obor monitor") ||
		strings.Contains(reqs, "tab.create") {
		t.Errorf("a monitor started again in the same pane and no tab:\n%s", reqs)
	}
}

// TestSetupMonitorReusesSystemTab: a "system" tab of the project's workspace (a pane in the root) gets the monitor's
// pane beside its own pane; no second tab is made.
func TestSetupMonitorReusesSystemTab(t *testing.T) {
	t.Parallel()
	x := newFx(t)
	x.h.Tab("w1:t3", 200, 50, "w1:p9")
	x.h.Set("w1:p9", func(p *herdrtest.Pane) { p.Workspace, p.Cwd = "w1", x.Root })
	x.h.TabLabels = map[string]string{"w1:t3": "system"}
	out, rc := x.run(t, "monitor")
	if rc != 0 || !strings.Contains(out, "OK monitor: pj-monitor started in pane ") {
		t.Fatalf("obor setup monitor: exit %d\n%s", rc, out)
	}
	reqs := strings.Join(x.reqs(), "\n")
	if strings.Contains(reqs, "tab.create") {
		t.Errorf("a new tab, though the project has its system tab:\n%s", reqs)
	}
	if !strings.Contains(reqs, "pane.split") || !strings.Contains(reqs, "target_pane_id=w1:p9") {
		t.Errorf("no pane split beside the system tab's pane:\n%s", reqs)
	}
	if mon := x.monitorPane(t); x.h.TabLabels[mon.Tab] != "system" {
		t.Errorf("the monitor's tab %s is labelled %q, want the system tab", mon.Tab, x.h.TabLabels[mon.Tab])
	}
}

// monitorPane is the pane the runs labelled with the project's monitor label.
func (x *fx) monitorPane(t *testing.T) *herdrtest.Pane {
	t.Helper()
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range x.h.Panes {
		if p.Label == cfg.MonitorLabel() {
			return p
		}
	}
	t.Fatalf("no pane labelled %s in %+v", cfg.MonitorLabel(), x.h.Panes)
	return nil
}
