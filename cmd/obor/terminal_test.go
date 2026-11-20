package main

import (
	"slices"
	"testing"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/herdr"
	"obor/internal/project"
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
	cl, cok := term.Control.(*hc.Client)
	if !ok || !cok || p.C != cl || cl.Path != "/tmp/hc.sock" || term.Setup == nil || term.Check == nil {
		t.Fatalf("hc's terminal: panes %T, control %T, setup %T, check set %v; want hc's over one client of /tmp/hc.sock",
			term.Panes, term.Control, term.Setup, term.Check != nil)
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

// A serve launchd starts gets the terminal's own variables the caller has: hc's session names (HC_SOCKET, HC_SESSION,
// the three XDG_ dirs) on hc, herdr's socket on herdr; never the other kind's.
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
}
