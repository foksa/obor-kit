package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/rex"
	"obor/internal/setup"
)

// fakeTerminal is the project's terminal over a fake herdr socket, as terminal builds one for herdr (a test's).
func fakeTerminal(s *herdr.Socket, env project.Env) port.Terminal {
	p := herdr.Panes{S: s}
	return port.Terminal{Panes: p, Control: s, Setup: &herdr.Setup{Socket: s, Env: env}}
}

// The selector builds hc's adapter for project.terminal "hc": its panes, control and setup over one client of the
// env's core, with the env guard as its check; serve's copy holds the core (a start of a core that is down, the
// guard at each subscribe), the other commands' only dial. herdr's has no check and serve's is unchanged.
func TestTerminalSelectsHC(t *testing.T) {
	t.Parallel()
	hcCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalHC}}}
	env := project.Env(func(k string) (string, bool) {
		v, ok := map[string]string{"HC_SOCKET": "/tmp/hc.sock", "HERDR_SOCKET_PATH": "/tmp/herdr.sock"}[k]
		return v, ok
	})
	term, err := terminal(hcCfg, env)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := term.Panes.(hc.Panes)
	cl, cok := term.Setup.(*hc.Client) // the client its Control prompts through, behind port.GuardPrompts
	if !ok || !cok || p.C != cl || cl.Path != "/tmp/hc.sock" || term.Check == nil {
		t.Fatalf("hc's terminal: panes %T, setup %T, check set %v; want hc's over one client of /tmp/hc.sock",
			term.Panes, term.Setup, term.Check != nil)
	}
	if p.Core != nil {
		t.Errorf("the commands' terminal holds the core: only serve's starts one")
	}
	if sp := serveTerminal(hcCfg, term).Panes.(hc.Panes); sp.Core == nil || sp.C != cl {
		t.Errorf("serve's hc terminal: core %v, client %p; want the core held over the same client", sp.Core, sp.C)
	}
	h, err := terminal(nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Panes.(herdr.Panes); !ok || h.Check != nil {
		t.Errorf("the default terminal: panes %T, check set %v; want herdr's, no check", h.Panes, h.Check != nil)
	}
	if _, ok := serveTerminal(nil, h).Panes.(herdr.Panes); !ok {
		t.Errorf("serve's herdr terminal changed")
	}
}

// The selector builds Rex's adapter for project.terminal "rex": its panes, control and setup over one client of the
// rex CLI on PATH, with the CLI's PATH check as its check; serve's copy is the same terminal (no core to hold).
func TestTerminalSelectsRex(t *testing.T) {
	t.Parallel()
	rexCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalRex}}}
	term, err := terminal(rexCfg, project.Env(func(string) (string, bool) { return "", false }))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := term.Panes.(rex.Panes)
	cl, cok := term.Setup.(*rex.Client)
	if !ok || !cok || p.C != cl || cl.Bin != "" || cl.Server != "" || term.Check == nil || term.Control == nil {
		t.Fatalf("Rex's terminal: panes %T, setup %T, check set %v; want Rex's over one client of rex on PATH",
			term.Panes, term.Setup, term.Check != nil)
	}
	if sp, ok := serveTerminal(rexCfg, term).Panes.(rex.Panes); !ok || sp.C != cl {
		t.Errorf("serve's Rex terminal changed: %T", serveTerminal(rexCfg, term).Panes)
	}
}

// A serve launchd starts gets the terminal's own variables the caller has: hc's session names (HC_SOCKET, HC_SESSION,
// the three XDG_ dirs) on hc, herdr's socket on herdr, the CLI's server registry (XDG_RUNTIME_DIR) on Rex; never
// another kind's.
func TestTerminalEnvByKind(t *testing.T) {
	t.Parallel()
	vars := map[string]string{"HC_SOCKET": "/s", "HC_SESSION": "obor", "XDG_RUNTIME_DIR": "/r", "XDG_STATE_HOME": "/st",
		"XDG_CONFIG_HOME": "/c", "HERDR_SOCKET_PATH": "/h", "OBOR_ROLE": "lead", "HC_TERMINAL_ID": "t1"}
	env := project.Env(func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
	names := func(cfg *config.Config) []string {
		var out []string
		for _, v := range terminalEnv(cfg, env) {
			out = append(out, v.Name+"="+v.Value)
		}
		return out
	}
	hcCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalHC}}}
	if got, want := names(hcCfg), []string{"HC_SOCKET=/s", "HC_SESSION=obor", "XDG_RUNTIME_DIR=/r", "XDG_STATE_HOME=/st",
		"XDG_CONFIG_HOME=/c"}; !slices.Equal(got, want) {
		t.Errorf("hc's serve env %q, want %q", got, want)
	}
	if got := names(nil); !slices.Equal(got, []string{"HERDR_SOCKET_PATH=/h"}) {
		t.Errorf("herdr's serve env %q, want its socket only", got)
	}
	rexCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalRex}}}
	if got := names(rexCfg); !slices.Equal(got, []string{"XDG_RUNTIME_DIR=/r"}) {
		t.Errorf("Rex's serve env %q, want the CLI's registry dir only", got)
	}
}

// Serve's adoption of hidden terminals exists for hc, whose setup offers the capability, and not for herdr: nothing is
// called there.
func TestAdopterOnlyWhereTheTerminalOffersIt(t *testing.T) {
	t.Parallel()
	if f := adopter(fakeTerminal(&herdr.Socket{}, nil), t.TempDir()); f != nil {
		t.Errorf("adopter for herdr is set, want nil")
	}
	cl := hc.New(project.Env(func(string) (string, bool) { return "", false }))
	if f := adopter(port.Terminal{Setup: cl}, t.TempDir()); f == nil {
		t.Errorf("adopter for hc is nil, want the capability")
	}
}

// obor init's terminal, §2 of the init flow: --terminal wins; with none, the pane the caller sits in
// (REX_BLOCK: rex, whatever else is set: a block inherits the Rex server's environment; HC_TERMINAL_ID: hc,
// HERDR_PANE_ID: herdr); hc's and herdr's both set: herdr, a FAIL when hc's core holds a pane's identity; none set: the
// one that answers, herdr when both do (with the line that says how to get hc), a FAIL when none does.
func TestDetectTerminal(t *testing.T) {
	t.Parallel()
	const bothLine = "terminal herdr (herdr and hc both run; for hc: obor init --terminal hc, or run it in a herdr-app terminal)"
	for _, c := range []struct {
		name            string
		env             map[string]string
		flag            string
		herdr, hc       bool
		leaked          []string
		kind, line, bad string
	}{
		{name: "flag hc beats herdr's pane", env: map[string]string{"HERDR_PANE_ID": "p1"}, flag: "hc", herdr: true, kind: "hc"},
		{name: "flag herdr beats hc's terminal", env: map[string]string{"HC_TERMINAL_ID": "t1"}, flag: "herdr", hc: true, kind: "herdr"},
		{name: "hc terminal only", env: map[string]string{"HC_TERMINAL_ID": "t1"}, hc: true, kind: "hc"},
		{name: "hc terminal only, herdr up too", env: map[string]string{"HC_TERMINAL_ID": "t1"}, hc: true, herdr: true, kind: "hc"},
		{name: "herdr pane only", env: map[string]string{"HERDR_PANE_ID": "p1"}, herdr: true, kind: "herdr"},
		{name: "herdr pane only, hc up too", env: map[string]string{"HERDR_PANE_ID": "p1"}, herdr: true, hc: true, kind: "herdr"},
		{name: "both set, core clean", env: map[string]string{"HC_TERMINAL_ID": "t1", "HERDR_PANE_ID": "p1"}, herdr: true,
			hc: true, kind: "herdr"},
		{name: "both set, no core", env: map[string]string{"HC_TERMINAL_ID": "t1", "HERDR_PANE_ID": "p1"}, herdr: true,
			kind: "herdr"},
		{name: "both set, core holds herdr names", env: map[string]string{"HC_TERMINAL_ID": "t1", "HERDR_PANE_ID": "p1"},
			herdr: true, hc: true, leaked: []string{"HERDR_PANE_ID"},
			bad: "FAIL terminal: HC_TERMINAL_ID and HERDR_PANE_ID are both set: " + hc.LeakLine + " (its environment holds HERDR_PANE_ID)"},
		{name: "neither, herdr only", herdr: true, kind: "herdr"},
		{name: "neither, hc only", hc: true, kind: "hc"},
		{name: "neither, both answer", herdr: true, hc: true, kind: "herdr", line: bothLine},
		{name: "neither, none answers", bad: "FAIL no terminal runs: start herdr or open herdr-app, then obor init again"},
		{name: "rex block only", env: map[string]string{"REX_BLOCK": "block:1"}, kind: "rex"},
		{name: "rex block only, herdr and hc up", env: map[string]string{"REX_BLOCK": "block:1"}, herdr: true, hc: true,
			kind: "rex"},
		{name: "rex block with an inherited herdr pane", env: map[string]string{"REX_BLOCK": "block:1", "HERDR_PANE_ID": "p1"},
			herdr: true, kind: "rex"},
		{name: "rex block with inherited herdr and hc names", env: map[string]string{"REX_BLOCK": "block:1",
			"HERDR_PANE_ID": "p1", "HC_TERMINAL_ID": "t1"}, herdr: true, hc: true, leaked: []string{"HERDR_PANE_ID"}, kind: "rex"},
		{name: "hc terminal made by a core started from a rex block", env: map[string]string{"HC_TERMINAL_ID": "t7",
			"REX_BLOCK": "block:old"}, hc: true, leaked: []string{"REX_BLOCK", "REX_SESSION"},
			bad: "FAIL terminal: HC_TERMINAL_ID and REX_BLOCK are both set: " + hc.LeakLine + " (its environment holds REX_BLOCK, REX_SESSION)"},
		{name: "hc terminal with a rex block, core clean", env: map[string]string{"HC_TERMINAL_ID": "t7",
			"REX_BLOCK": "block:1"}, hc: true, kind: "rex"},
		{name: "flag herdr beats a rex block", env: map[string]string{"REX_BLOCK": "block:1"}, flag: "herdr", kind: "herdr"},
		{name: "flag rex beats herdr's pane", env: map[string]string{"HERDR_PANE_ID": "p1"}, flag: "rex", herdr: true, kind: "rex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := project.Env(func(k string) (string, bool) { v, ok := c.env[k]; return v, ok })
			p := terminalProbes{
				herdr: func(context.Context) bool { return c.herdr },
				hc:    func(context.Context) (bool, []string) { return c.hc, c.leaked },
			}
			kind, line, err := detectTerminal(context.Background(), env, c.flag, p)
			if c.bad != "" {
				if err == nil || "FAIL "+err.Error() != c.bad {
					t.Fatalf("got kind %q, error %v; want %q", kind, err, c.bad)
				}
				return
			}
			if err != nil || kind != c.kind || line != c.line {
				t.Errorf("got %q, line %q, error %v; want %q, line %q", kind, line, err, c.kind, c.line)
			}
		})
	}
}

// A new project gets the detected kind as init's --terminal (the one given replaced, the other arguments kept); an
// existing config's terminal wins over the env and the flag, and says so in one line only when the caller sits in the
// other terminal (or asked for the other); detection does not run for it.
func TestInitTerminalKind(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	envOf := func(m map[string]string) project.Env {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	probed := 0
	p := terminalProbes{
		herdr: func(context.Context) bool { probed++; return true },
		hc:    func(context.Context) (bool, []string) { probed++; return false, nil },
	}
	run := func(env map[string]string, cfg *config.Config, args ...string) (string, string, []string, error) {
		return initTerminalKind(context.Background(), envOf(env), root, cfg, args, p)
	}
	hcEnv := map[string]string{"HC_TERMINAL_ID": "t1"}

	kind, line, rest, err := run(hcEnv, nil, "--name", "foo", "--terminal", "herdr", "--prefix", "f")
	if want := []string{"--name", "foo", "--prefix", "f", "--terminal", "herdr"}; err != nil || kind != "herdr" || line != "" ||
		!slices.Equal(rest, want) {
		t.Errorf("a new project, --terminal herdr: kind %q, line %q, args %q, error %v; want herdr, %q", kind, line, rest, err, want)
	}
	if kind, _, rest, err = run(hcEnv, nil); err != nil || kind != "hc" || !slices.Equal(rest, []string{"--terminal", "hc"}) {
		t.Errorf("a new project in hc: kind %q, args %q, error %v", kind, rest, err)
	}
	if _, _, _, err = run(hcEnv, nil, "--terminal", "zellij"); err == nil || err.Error() != setup.InitUsage {
		t.Errorf("a terminal obor does not know: error %v, want the usage", err)
	}
	if probed != 0 {
		t.Fatalf("precondition: the probes ran %d times for rows that need none", probed)
	}

	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	hcCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalHC}}}
	rexCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalRex}}}
	for _, c := range []struct {
		name, line string
		env        map[string]string
		cfg        *config.Config
		args       []string
		kind       string
	}{
		{"hc config, in hc", "", hcEnv, hcCfg, nil, "hc"},
		{"hc config, in herdr", "terminal hc (from .obor/config.json; the config wins, change project.terminal to switch)",
			map[string]string{"HERDR_PANE_ID": "p1"}, hcCfg, nil, "hc"},
		{"hc config, --terminal herdr", "terminal hc (from .obor/config.json; the config wins, change project.terminal to switch)",
			nil, hcCfg, []string{"--terminal", "herdr"}, "hc"},
		{"no terminal key (herdr), in hc", "terminal herdr (from .obor/config.json; the config wins, change project.terminal to switch)",
			hcEnv, &config.Config{}, nil, "herdr"},
		{"no terminal key, in herdr", "", map[string]string{"HERDR_PANE_ID": "p1"}, &config.Config{}, nil, "herdr"},
		{"rex config, in a Rex block that inherited a herdr pane", "",
			map[string]string{"REX_BLOCK": "block:1", "HERDR_PANE_ID": "p1"}, rexCfg, nil, "rex"},
		{"hc config, in a Rex block", "terminal hc (from .obor/config.json; the config wins, change project.terminal to switch)",
			map[string]string{"REX_BLOCK": "block:1"}, hcCfg, nil, "hc"},
	} {
		kind, line, rest, err := run(c.env, c.cfg, c.args...)
		if err != nil || kind != c.kind || line != c.line || !slices.Equal(rest, []string{"--terminal", c.kind}) {
			t.Errorf("%s: kind %q, line %q, args %q, error %v; want %q, line %q", c.name, kind, line, rest, err, c.kind, c.line)
		}
	}
	if probed != 0 {
		t.Errorf("detection ran (%d probes) for a project with a config", probed)
	}
}
