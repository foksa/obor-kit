package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"syscall"
	"testing"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/herdr"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
	"obor/internal/testkit"
)

// obor lead with no lead pane, typed in a herdr pane: the caller's pane becomes the lead's when it qualifies.

// herdrCaller is a fixture whose caller sits in herdr pane w1:p1 (HERDR_PANE_ID), a shell in the project root alone in
// tab w1:tc of workspace w1 (labelled "shell"), with obor lead in its foreground.
func herdrCaller(t *testing.T) *leadFx {
	t.Helper()
	x := newLeadFx(t)
	x.f.Tab("w1:tc", 80, 24, "w1:p1")
	x.f.Shell("w1:p1", "", x.p.Root)
	x.f.Group("w1", "shell")
	x.env = []string{"HERDR_PANE_ID=w1:p1"}
	x.srv.Reply("pane.process_info", map[string]any{"type": "pane_process_info", "process_info": map[string]any{
		"pane_id": "w1:p1", "shell_pid": 1, "foreground_process_group_id": 77,
		"foreground_processes": []any{map[string]any{"pid": 77, "argv0": "obor"}}}})
	return x
}

// methods is every request method herdr was asked, in order.
func (x *leadFx) methods() []string {
	var out []string
	for _, r := range x.srv.Requests() {
		out = append(out, r.Method)
	}
	return out
}

// A qualifying herdr pane: labelled the lead's, its tab labelled lead, its workspace the project's name; the lead's
// start is scheduled there (once obor lead exits) and no tab or workspace is made.
func TestLeadTakesTheCallersHerdrPane(t *testing.T) {
	t.Parallel()
	x := herdrCaller(t)
	if p := x.f.Pane("w1:p1"); p == nil || p.Label != "" || x.f.TabLabels["w1:tc"] != "" {
		t.Fatalf("precondition: an unlabelled pane w1:p1 in an unlabelled tab, got %+v, tab %q", p, x.f.TabLabels["w1:tc"])
	}
	out, rc := x.run(t)
	if rc != 0 || lastLine(out) != "STARTING pj-lead in this pane (w1:p1) once obor lead exits" {
		t.Fatalf("exit %d, want the start scheduled in the caller's pane:\n%s", rc, out)
	}
	if !strings.Contains(out, "lead pane: pane w1:p1 labelled pj-lead, tab w1:tc labelled lead, workspace w1 labelled proj") {
		t.Errorf("no line saying what was taken:\n%s", out)
	}
	if p := x.f.Pane("w1:p1"); p.Label != "pj-lead" {
		t.Errorf("pane w1:p1 labelled %q, want pj-lead", p.Label)
	}
	if got := x.f.TabLabels["w1:tc"]; got != "lead" {
		t.Errorf("tab w1:tc labelled %q, want lead", got)
	}
	if i := slices.IndexFunc(x.f.Workspaces, func(w herdr.Workspace) bool { return w.ID == "w1" }); i < 0 || x.f.Workspaces[i].Label != "proj" {
		t.Errorf("workspaces %+v, want w1 labelled proj", x.f.Workspaces)
	}
	if len(x.spawn) != 1 || strings.Join(x.spawn[0], " ") != "restart pj-lead --pane w1:p1 --fresh" {
		t.Errorf("detached runs %v, want one fresh restart of pj-lead in w1:p1", x.spawn)
	}
	m := x.methods()
	if slices.Contains(m, "tab.create") || slices.Contains(m, "workspace.create") || slices.Contains(m, "pane.split") ||
		slices.Contains(m, "agent.start") {
		t.Errorf("requests %q: want no new tab, workspace or split, and no start while obor lead has the foreground", m)
	}
}

// The caller's pane stays as it is, and the lead gets a new lead tab whose line names workspace, tab and pane, when
// the pane shares its tab, the caller is an agent (OBOR_ROLE), or its workspace holds another project's pane.
func TestLeadNewTabWhenTheCallersPaneDoesNotQualify(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, why, last string
		edit            func(x *leadFx)
	}{
		{"a sibling in its tab", "pane w1:p1 shares its tab with 1 other pane(s)",
			"OK pj-lead runs in workspace shell, tab lead (pane w1:n1)",
			func(x *leadFx) { x.f.Tab("w1:tc", 80, 24, "w1:p2"); x.f.Shell("w1:p2", "", x.p.Root) }},
		{"an agent's pane", "this pane is a worker's (OBOR_ROLE is set)",
			"OK pj-lead runs in workspace shell, tab lead (pane w1:n1)",
			func(x *leadFx) { x.env = append(x.env, "OBOR_ROLE=worker") }},
		{"a foreign workspace", "its workspace holds pane w1:p9 of another project (/elsewhere)",
			"OK pj-lead runs in workspace proj, tab lead (pane wn1:n2)",
			func(x *leadFx) { x.f.Tab("w1:t9", 80, 24, "w1:p9"); x.f.Shell("w1:p9", "", "/elsewhere") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := herdrCaller(t)
			c.edit(x)
			x.srv.Clear("pane.process_info") // the new tab's shell is at its prompt
			out, rc := x.run(t)
			if rc != 0 || lastLine(out) != c.last {
				t.Fatalf("exit %d, want the last line %q:\n%s", rc, c.last, out)
			}
			if !strings.Contains(out, "this pane stays as it is: "+c.why) {
				t.Errorf("no line saying why the pane stays (%s):\n%s", c.why, out)
			}
			if p := x.f.Pane("w1:p1"); p.Label != "" || x.f.TabLabels["w1:tc"] != "" {
				t.Errorf("the caller's pane %+v, tab %q: want both untouched", p, x.f.TabLabels["w1:tc"])
			}
			m := x.methods()
			if !slices.Contains(m, "tab.create") || slices.Contains(m, "tab.rename") || slices.Contains(m, "workspace.rename") {
				t.Errorf("requests %q, want a tab.create and no rename of a tab or workspace", m)
			}
			if len(x.spawn) != 0 {
				t.Errorf("detached runs %v, want none (the start runs in the new tab)", x.spawn)
			}
		})
	}
}

// hcCaller is an hc project whose caller sits in terminal addr (HC_TERMINAL_ID), a shell herdr-app started in the
// project root in tab tb1 of space "work", obor lead in its foreground: this test process's own process group, as a
// shell runs a typed command (docs/research/hc-probes.md P9).
type hcCaller struct {
	*leadFx
	core *hctest.Core
	app  *herdrapptest.App
	cl   *hc.Client
	addr string
}

func newHCCaller(t *testing.T) *hcCaller {
	t.Helper()
	x := newLeadFx(t, testkit.Set("project.terminal", "hc"))
	k := &hcCaller{leadFx: x, core: hctest.New(), app: herdrapptest.New()}
	k.app.StartTerminals(k.core.AppTerminals(false))
	k.cl = k.core.Client()
	k.cl.App = k.app.Client()
	k.app.AddSpace(herdrapptest.Space{ID: "sp1", Name: "work", Folder: x.p.Root, Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "1"}}})
	k.addr = k.core.AppTerminals(false)("sp1", "tb1", x.p.Root)
	k.core.SetForeground(k.addr, hc.Foreground{PID: syscall.Getpgrp(), Name: "obor", Argv: []string{"obor", "lead"}, Cwd: x.p.Root})
	x.env = []string{"HC_TERMINAL_ID=" + k.addr}
	return k
}

func (k *hcCaller) run(t *testing.T) (string, int) {
	t.Helper()
	var out bytes.Buffer
	r := leadRun{dir: k.p.Root, kit: k.p.Base, env: k.p.Env(k.env...), out: &out, tune: k.launcher,
		t: port.Terminal{Panes: hc.Panes{C: k.cl}, Control: k.cl, Setup: k.cl}}
	rc := r.run(context.Background(), nil)
	return out.String(), rc
}

// tabName is the name of tab id in the window ("" when none).
func (k *hcCaller) tabName(id string) string {
	for _, s := range k.app.Spaces() {
		for _, tab := range s.Tabs {
			if tab.ID == id {
				return tab.Name
			}
		}
	}
	return ""
}

// A qualifying hc terminal: labelled as the lead's (agent, project, kind), kept, its tab named lead and its space
// named after the project; the lead's start is scheduled there and no tab or space is made.
func TestLeadTakesTheCallersHCTerminal(t *testing.T) {
	t.Parallel()
	k := newHCCaller(t)
	if l := k.core.Labels(k.addr); l[hc.LabelAgent] != "" || len(k.app.Spaces()) != 1 {
		t.Fatalf("precondition: an unlabelled terminal in the one space, got %v, %d spaces", l, len(k.app.Spaces()))
	}
	if r := k.core.Live()[0]; r.Foreground == nil || r.Foreground.PID == r.PID {
		t.Fatalf("precondition: obor lead, not the shell, in the caller's foreground: %+v", r)
	}
	out, rc := k.run(t)
	want := "STARTING pj-lead in this pane (" + k.addr + ") once obor lead exits"
	if rc != 0 || lastLine(out) != want {
		t.Fatalf("exit %d, want %q:\n%s", rc, want, out)
	}
	l := k.core.Labels(k.addr)
	if l[hc.LabelAgent] != "pj-lead" || l[hc.LabelProject] != "proj" || l[hc.LabelPlace] != "lead" ||
		l[herdrapp.LabelTab] != "tb1" || !k.core.Kept(k.addr) {
		t.Errorf("terminal %s labels %v, kept %v: want the lead's labels in tb1, kept", k.addr, l, k.core.Kept(k.addr))
	}
	if got := k.tabName("tb1"); got != "lead" {
		t.Errorf("tab tb1 named %q, want lead", got)
	}
	spaces := k.app.Spaces()
	if len(spaces) != 1 || spaces[0].Name != "proj" || len(spaces[0].Tabs) != 1 {
		t.Errorf("spaces %+v, want the one space named proj with its one tab", spaces)
	}
	if len(k.spawn) != 1 || strings.Join(k.spawn[0], " ") != "restart pj-lead --pane "+k.addr+" --fresh" {
		t.Errorf("detached runs %v, want one fresh restart of pj-lead in %s", k.spawn, k.addr)
	}
	if n := len(k.core.Live()); n != 1 {
		t.Errorf("%d live terminals, want the caller's alone (no new tab's)", n)
	}
}

// An hc terminal whose foreground is another program than obor lead (an editor) stays as it is: the lead gets a new
// lead tab, the caller's terminal and tab untouched.
func TestLeadNewTabWhenTheCallersHCTerminalRunsAProgram(t *testing.T) {
	t.Parallel()
	k := newHCCaller(t)
	k.core.SetForeground(k.addr, hc.Foreground{PID: 99999, Name: "vim", Argv: []string{"vim"}, Cwd: k.p.Root})
	k.core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
	out, rc := k.run(t)
	if rc != 0 || !strings.HasPrefix(lastLine(out), "OK pj-lead runs in space proj, tab lead (pane ") {
		t.Fatalf("exit %d, want the lead in a new tab:\n%s", rc, out)
	}
	if !strings.Contains(out, "this pane stays as it is: terminal "+k.addr+" runs a program (vim)") {
		t.Errorf("no line saying why the terminal stays:\n%s", out)
	}
	if l := k.core.Labels(k.addr); l[hc.LabelAgent] != "" || l[hc.LabelProject] != "" {
		t.Errorf("terminal %s labels %v: want it untouched", k.addr, l)
	}
	if got := k.tabName("tb1"); got != "1" {
		t.Errorf("tab tb1 named %q, want it untouched", got)
	}
	if len(k.spawn) != 0 {
		t.Errorf("detached runs %v, want none", k.spawn)
	}
}

// An hc terminal sharing its tab stays as it is: the lead gets a new lead tab in the project's space, the last line
// naming space, tab and terminal.
func TestLeadNewTabWhenTheCallersHCTerminalHasASibling(t *testing.T) {
	t.Parallel()
	k := newHCCaller(t)
	other := k.core.AppTerminals(false)("sp1", "tb1", k.p.Root)
	k.core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
	out, rc := k.run(t)
	if rc != 0 || !strings.HasPrefix(lastLine(out), "OK pj-lead runs in space proj, tab lead (pane ") {
		t.Fatalf("exit %d, want the lead in a new tab:\n%s", rc, out)
	}
	if !strings.Contains(out, "this pane stays as it is: terminal "+k.addr+" shares its tab with 1 other terminal(s)") {
		t.Errorf("no line saying why the terminal stays:\n%s", out)
	}
	for _, a := range []string{k.addr, other} {
		if l := k.core.Labels(a); l[hc.LabelAgent] != "" || l[hc.LabelProject] != "" {
			t.Errorf("terminal %s labels %v: want it untouched", a, l)
		}
	}
	if got := k.tabName("tb1"); got != "1" {
		t.Errorf("tab tb1 named %q, want it untouched", got)
	}
	if len(k.spawn) != 0 {
		t.Errorf("detached runs %v, want none", k.spawn)
	}
}

// An hc terminal whose space is not the project's while another space already is (the project's tabs are there)
// stays as it is: the lead gets a new lead tab in the project's space, and the caller's space keeps its name.
func TestLeadNewTabWhenTheProjectsSpaceIsAnother(t *testing.T) {
	t.Parallel()
	k := newHCCaller(t)
	k.app.AddSpace(herdrapptest.Space{ID: "sp2", Name: "proj", Folder: k.p.Root, Tabs: []herdrapptest.Tab{{ID: "tb2", Name: "roles"}}})
	k.core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
	out, rc := k.run(t)
	if rc != 0 || !strings.HasPrefix(lastLine(out), "OK pj-lead runs in space proj, tab lead (pane ") {
		t.Fatalf("exit %d, want the lead in a new tab:\n%s", rc, out)
	}
	if !strings.Contains(out, "this pane stays as it is: the proj space is another than this terminal's (work)") {
		t.Errorf("no line saying why the terminal stays:\n%s", out)
	}
	if l := k.core.Labels(k.addr); l[hc.LabelAgent] != "" {
		t.Errorf("terminal %s labels %v: want it untouched", k.addr, l)
	}
	for _, s := range k.app.Spaces() {
		if s.ID == "sp1" && (s.Name != "work" || s.Tabs[0].Name != "1") {
			t.Errorf("the caller's space %+v: want it and its tab untouched", s)
		}
	}
}
