package setup

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
	"obor/internal/launch"
)

// obor init typed at a shell in an hc terminal, then the lead's decision on that terminal (LeadHere, what obor lead
// asks once init is done): one scenario across init's placements, the hc adapter and the herdr-app window, the caller's
// foreground this test process's own process group, as a shell runs a typed command (docs/research/hc-probes.md P9).

// hcInitFx is a bare project for obor init on hc: an in-memory core and herdr-app window, the caller in terminal addr
// (HC_TERMINAL_ID), the one terminal of the first tab of space, its foreground obor init.
type hcInitFx struct {
	*initFx
	core *hctest.Core
	app  *herdrapptest.App
	cl   *hc.Client
	addr string
}

func newHCInitFx(t *testing.T, space func(root string) herdrapptest.Space) *hcInitFx {
	t.Helper()
	k := &hcInitFx{initFx: newFakeGitInitFx(t, true), core: hctest.New(), app: herdrapptest.New()}
	k.app.StartTerminals(k.core.AppTerminals(false))
	k.cl = k.core.Client()
	k.cl.App = k.app.Client()
	sp := space(k.Root)
	k.app.AddSpace(sp)
	k.addr = k.core.AppTerminals(false)(sp.ID, sp.Tabs[0].ID, k.Root)
	k.core.SetForeground(k.addr, hc.Foreground{PID: syscall.Getpgrp(), Name: "obor", Argv: []string{"obor", "init"}, Cwd: k.Root})
	k.env = append(k.env, "HC_TERMINAL_ID="+k.addr)
	return k
}

// unnamedAtRoot is a space herdr-app named itself after the project's folder; home a space someone named home, at
// their home folder.
func unnamedAtRoot(root string) herdrapptest.Space {
	return herdrapptest.Space{ID: "sp1", Name: filepath.Base(root), Folder: root, Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "1"}}}
}

func home(string) herdrapptest.Space {
	return herdrapptest.Space{ID: "sp1", Name: "home", Folder: "/Users/someone", Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "1"}}}
}

// setup is a Setup on the fake core and window, its starter's pane control the core.
func (k *hcInitFx) setup(t *testing.T) (*Setup, *bytes.Buffer) {
	t.Helper()
	s, out := k.newSetup(t)
	s.Dir, s.T = k.Root, k.cl
	s.Launcher = func(cfg *config.Config) port.Launcher {
		return &launch.Launcher{Cfg: cfg, Env: s.Env, Kit: s.Kit, H: k.cl, Out: out, Poll: k.poll, Up: k.up, Where: k.where}
	}
	return s, out
}

// initThenLead runs obor init --terminal hc --name proj, then the lead's decision on the caller's terminal: init's
// output, the pane LeadHere took ("": none) and its line.
func (k *hcInitFx) initThenLead(t *testing.T) (string, string, string) {
	t.Helper()
	s, out := k.setup(t)
	if rc := s.Run(context.Background(), []string{"obor-init", "--terminal", "hc", "--name", "proj"}); rc != 0 {
		t.Fatalf("obor init: exit %d\n%s", rc, out)
	}
	l, _ := k.setup(t)
	pane, line, err := l.LeadHere(context.Background())
	if err != nil {
		t.Fatalf("LeadHere: %v\n%s", err, out)
	}
	return out.String(), pane, line
}

// cfg is the config init wrote.
func (k *hcInitFx) cfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(k.Root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// placed is the agents init places: the two lanes' workers, the integrator and the reviewer.
func (k *hcInitFx) placed(t *testing.T) []string {
	t.Helper()
	cfg := k.cfg(t)
	return []string{cfg.AgentName("worker", "L-01"), cfg.AgentName("worker", "L-02"), cfg.AgentName("integrator", ""),
		cfg.AgentName("reviewer", "")}
}

// spaceOf is the space a terminal labelled agent shows in ("": none).
func (k *hcInitFx) spaceOf(agent string) string {
	for _, r := range k.core.Live() {
		if r.Labels[hc.LabelAgent] == agent {
			return r.Labels[herdrapp.LabelSpace]
		}
	}
	return ""
}

// tabNames is the names of space id's tabs, in order.
func (k *hcInitFx) tabNames(id string) []string {
	for _, s := range k.app.Spaces() {
		if s.ID == id {
			var out []string
			for _, tab := range s.Tabs {
				out = append(out, tab.Name)
			}
			return out
		}
	}
	return nil
}

// spaceNames is the window's spaces by name, in order.
func (k *hcInitFx) spaceNames() []string {
	var out []string
	for _, s := range k.app.Spaces() {
		out = append(out, s.Name)
	}
	return out
}

// precondition: the caller's terminal has obor, not its shell, in the foreground, and no label of the project.
func (k *hcInitFx) precondition(t *testing.T) {
	t.Helper()
	for _, r := range k.core.Live() {
		if r.Address == k.addr && (r.Foreground == nil || r.Foreground.PID == r.PID || r.Labels[hc.LabelProject] != "") {
			t.Fatalf("precondition: obor in the caller's foreground, no project label: %+v", r)
		}
	}
}

// The caller qualifies (an unnamed space at the project's folder, or a named home space elsewhere: an empty folder's
// init): the space init uses is the caller's, named after the project, the lanes and roles placed in it, no second
// space; the lead takes the caller's terminal.
func TestInitPlacesInTheCallersSpaceAndTheLeadTakesIt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		space func(string) herdrapptest.Space
	}{
		{"an unnamed space at the project folder", unnamedAtRoot},
		{"a named home space", home},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newHCInitFx(t, c.space)
			k.precondition(t)
			out, pane, line := k.initThenLead(t)
			if got := k.spaceNames(); !slices.Equal(got, []string{"proj"}) {
				t.Errorf("spaces %q, want the caller's alone, named proj\n%s", got, out)
			}
			for _, a := range k.placed(t) {
				if got := k.spaceOf(a); got != "sp1" {
					t.Errorf("%s placed in space %q, want the caller's sp1\n%s", a, got, out)
				}
			}
			if pane != k.addr {
				t.Fatalf("the lead took %q (%s), want the caller's terminal %s\n%s", pane, line, k.addr, out)
			}
			if got := k.tabNames("sp1"); len(got) < 3 || got[0] != "lead" || !slices.Contains(got, "lanes") ||
				!slices.Contains(got, "roles") {
				t.Errorf("tabs of sp1 %q, want the caller's named lead, then lanes and roles", got)
			}
			if l := k.core.Labels(k.addr); l[hc.LabelAgent] != k.cfg(t).AgentName("lead", "") || l[hc.LabelProject] != "proj" {
				t.Errorf("the caller's labels %v, want the lead's", l)
			}
		})
	}
}

// The caller does not qualify: its foreground another program (an editor), or the project has a space of its own
// already while the caller is elsewhere. The caller's space keeps its name, the lanes go to the project's space, and
// the lead does not take the caller (its own tab follows, obor lead's).
func TestInitLeavesTheCallersSpaceWhenItDoesNotQualify(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, why string
		edit      func(k *hcInitFx)
	}{
		{"an editor in its foreground", "runs a program (vim)", func(k *hcInitFx) {
			k.core.SetForeground(k.addr, hc.Foreground{PID: 99999, Name: "vim", Argv: []string{"vim"}, Cwd: k.Root})
		}},
		{"the project's own space", "the proj group is another than this pane's (home)", func(k *hcInitFx) {
			k.app.AddSpace(herdrapptest.Space{ID: "sp2", Name: "proj", Folder: k.Root, Tabs: []herdrapptest.Tab{{ID: "tb2", Name: "roles"}}})
		}},
		{"another project's terminal in its space", "its group holds pane t2 of another project (other)", func(k *hcInitFx) {
			k.app.AddTab("sp1", herdrapptest.Tab{ID: "tb9", Name: "lanes"})
			k.core.Label(k.core.AppTerminals(false)("sp1", "tb9", "/elsewhere"), map[string]string{hc.LabelProject: "other"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newHCInitFx(t, home)
			c.edit(k)
			if r := k.core.Live()[0]; r.Address != k.addr || r.Foreground == nil || r.Foreground.PID == r.PID {
				t.Fatalf("precondition: the caller's terminal first, a command (not its shell) in its foreground: %+v", r)
			}
			out, pane, line := k.initThenLead(t)
			if pane != "" || !strings.Contains(line, c.why) {
				t.Errorf("the lead took %q (%s), want the caller declined: %s\n%s", pane, line, c.why, out)
			}
			if got := k.spaceNames(); !slices.Equal(got, []string{"home", "proj"}) {
				t.Errorf("spaces %q, want home untouched beside proj\n%s", got, out)
			}
			for _, a := range k.placed(t) {
				if got := k.spaceOf(a); got == "sp1" || got == "" {
					t.Errorf("%s placed in space %q, want the project's, not the caller's\n%s", a, got, out)
				}
			}
			if got := k.tabNames("sp1"); len(got) == 0 || got[0] != "1" || slices.Contains(got, "roles") {
				t.Errorf("tabs of the caller's space %q, want its tab untouched and no roles tab", got)
			}
			if summary(out, "space") != "" {
				t.Errorf("a space step though the caller was declined:\n%s", out)
			}
		})
	}
}
