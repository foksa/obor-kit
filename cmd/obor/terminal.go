package main

// terminal.go: the one place this binary builds the terminal adapter. Every other caller asks for a port.Terminal,
// so a second terminal is a case here and nothing else.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/rex"
	"obor/internal/service"
	"obor/internal/setup"
)

// terminalKind is one terminal kind's row, the one table of what differs by kind (terminals): PaneVar the variable its
// terminal sets in every pane (config.TerminalPaneVars), Env the variables a caller's serve needs to reach it, Group
// what it calls a pane's group, Build its adapter for env and a function of the project's agent kinds
// (config.Config.Kinds), which the adapter calls at each use; the adapter's Control is the kind's pane operations behind
// port.GuardPrompts, so every prompt on every kind passes obor's blocked-pane rule.
type terminalKind struct {
	Name    string
	PaneVar string
	Env     []string
	Group   string
	Build   func(env project.Env, kinds func() agents.Kinds) port.Terminal
}

var terminals = []terminalKind{
	{Name: config.DefaultTerminal, PaneVar: config.TerminalPaneVars[config.DefaultTerminal], Env: herdr.EnvNames,
		Group: "workspace", Build: func(env project.Env, _ func() agents.Kinds) port.Terminal {
			s := &herdr.Socket{Path: herdr.SocketPath(env)}
			p := herdr.Panes{S: s}
			return port.Terminal{Panes: p, Control: port.GuardPrompts(s), Setup: &herdr.Setup{Socket: s, Env: env},
				Note: p.Note}
		}},
	{Name: config.TerminalHC, PaneVar: config.TerminalPaneVars[config.TerminalHC], Env: hc.EnvNames, Group: "space",
		Build: func(env project.Env, kinds func() agents.Kinds) port.Terminal {
			cl := hc.New(env)
			cl.Kinds = kinds
			p := hc.Panes{C: cl}
			return port.Terminal{Panes: p, Control: port.GuardPrompts(cl), Setup: cl, Check: cl.Check, Note: p.Note}
		}},
	{Name: config.TerminalRex, PaneVar: config.TerminalPaneVars[config.TerminalRex], Env: rex.EnvNames, Group: "session",
		Build: func(_ project.Env, kinds func() agents.Kinds) port.Terminal {
			cl := &rex.Client{Kinds: kinds}
			p := rex.Panes{C: cl}
			return port.Terminal{Panes: p, Control: port.GuardPrompts(cl), Setup: cl, Check: cl.Check, Note: p.Note}
		}},
}

// kindOf is the row of cfg's terminal kind (nil cfg: the default's); ok is false for a kind obor does not know.
func kindOf(cfg *config.Config) (terminalKind, bool) {
	for _, k := range terminals {
		if k.Name == cfg.TerminalKind() {
			return k, true
		}
	}
	return terminalKind{}, false
}

// terminal is the project's terminal: one value for its panes and events, launch's pane operations with the wakes
// serve types, and setup's placements. cfg names the kind (project.terminal; nil: config.DefaultTerminal), env finds its
// socket. A kind obor does not know is an error naming the ones it does.
func terminal(cfg *config.Config, env project.Env) (port.Terminal, error) {
	kinds := cfg.Kinds() // a command's terminal lives as long as the command: the kinds of the config it ran with
	return terminalOn(cfg, env, func() agents.Kinds { return kinds })
}

// terminalOn is terminal with kinds as the adapter's kinds: serve's, which follow its last pass's config (kindCache).
func terminalOn(cfg *config.Config, env project.Env, kinds func() agents.Kinds) (port.Terminal, error) {
	k, ok := kindOf(cfg)
	if !ok {
		return port.Terminal{}, fmt.Errorf("terminal.kind %q: obor knows %s", cfg.TerminalKind(), strings.Join(config.TerminalKinds, ", "))
	}
	return k.Build(env, kinds), nil
}

// serveTerminal is t as serve watches it: on hc, each subscribe first starts a core that is down from serve's own
// environment scrubbed, and refuses a core whose environment holds another terminal's identity (hc.Core); obor's other
// commands only dial. herdr's is t.
func serveTerminal(cfg *config.Config, t port.Terminal) port.Terminal {
	if p, ok := t.Panes.(hc.Panes); ok && cfg.TerminalKind() == config.TerminalHC {
		p.Core = &hc.Core{Environ: os.Environ()}
		t.Panes = p
	}
	return t
}

// terminalEnv is the terminal's environment for a serve launchd starts: the adapter's own variables, read from the
// caller's env (the plist carries the ones set). A kind obor does not know has none.
func terminalEnv(cfg *config.Config, env project.Env) []service.EnvVar {
	k, _ := kindOf(cfg)
	var out []service.EnvVar
	for _, name := range k.Env {
		if v := env.Get(name); v != "" {
			out = append(out, service.EnvVar{Name: name, Value: v})
		}
	}
	return out
}

// adopter is serve's adoption of the project's hidden terminals over term: nil when its setup has no such capability
// (herdr). The project's config is read at each call, so a merged change counts.
func adopter(term port.Terminal, root string) func(ctx context.Context) (int, error) {
	ad, ok := term.Setup.(port.Adopter)
	if !ok {
		return nil
	}
	return func(ctx context.Context) (int, error) {
		cfg, err := config.Load(root)
		if err != nil {
			return 0, err
		}
		return ad.Adopt(ctx, setup.PlaceProjectOf(cfg, root))
	}
}

// terminalProbes are what detectTerminal asks of the machine: whether herdr answers, and whether hc's core does
// (answers) with the identity names of other terminals its environment holds (leaked: a core started from a pane).
// Both only dial; nothing starts a core.
type terminalProbes struct {
	herdr func(ctx context.Context) bool
	hc    func(ctx context.Context) (answers bool, leaked []string)
}

// realProbes are the probes over the terminals terminal builds for env.
func realProbes(env project.Env) terminalProbes {
	return terminalProbes{
		herdr: func(ctx context.Context) bool {
			t, err := terminal(nil, env)
			if err != nil {
				return false
			}
			_, err = t.Setup.Agents(ctx)
			return err == nil
		},
		hc: func(ctx context.Context) (bool, []string) {
			coreEnv, err := hc.New(env).CoreEnv(ctx)
			return !port.NotRunning(err), hc.Leaked(coreEnv)
		},
	}
}

// detectTerminal is the terminal a new project gets (obor init; the caller decides it once, before any write, only
// when the project has no config): flag (--terminal) when set; else the one the caller sits in (callerTerminal:
// REX_BLOCK: rex, HC_TERMINAL_ID: hc, HERDR_PANE_ID: herdr); HC_TERMINAL_ID with REX_BLOCK: rex, unless hc's core holds
// REX_BLOCK (an hc terminal made by a core started from a Rex block: a FAIL); hc's and herdr's both set: herdr, unless
// hc's core holds a pane's identity (a FAIL); none set: the one of herdr and hc that answers, herdr when both do (line
// says how to get hc), neither answering is a FAIL. Rex is never probed: a rex call from outside a block reaches the
// default server.
func detectTerminal(ctx context.Context, env project.Env, flag string, p terminalProbes) (kind, line string, err error) {
	if flag != "" {
		return flag, "", nil
	}
	// An hc terminal inherits its core's environment: a core started from a Rex block gave it that block's REX_BLOCK,
	// which callerTerminal would take for Rex. The block itself, whose own REX_BLOCK Rex set, has a clean core: only the
	// core's names decide that the caller sits in an hc terminal.
	if env.Get(paneVarOf(config.TerminalHC)) != "" && env.Get(paneVarOf(config.TerminalRex)) != "" {
		if _, leaked := p.hc(ctx); slices.Contains(leaked, paneVarOf(config.TerminalRex)) {
			return "", "", project.Failf("terminal: %s and %s are both set: %s (its environment holds %s)",
				paneVarOf(config.TerminalHC), paneVarOf(config.TerminalRex), hc.LeakLine, strings.Join(leaked, ", "))
		}
	}
	if k := callerTerminal(env); k != "" {
		return k, "", nil
	}
	if env.Get(paneVarOf(config.TerminalHC)) != "" { // both set: HC_TERMINAL_ID was inherited by herdr's server unless the core
		// was started from a herdr pane
		if _, leaked := p.hc(ctx); len(leaked) > 0 {
			return "", "", project.Failf("terminal: %s and %s are both set: %s (its environment holds %s)",
				paneVarOf(config.TerminalHC), paneVarOf(config.DefaultTerminal), hc.LeakLine, strings.Join(leaked, ", "))
		}
		return config.DefaultTerminal, "", nil
	}
	herdrUp := p.herdr(ctx)
	hcUp, _ := p.hc(ctx)
	switch {
	case herdrUp && hcUp:
		return config.DefaultTerminal, "terminal herdr (herdr and hc both run; for hc: obor init --terminal hc, " +
			"or run it in a herdr-app terminal)", nil
	case herdrUp:
		return config.DefaultTerminal, "", nil
	case hcUp:
		return config.TerminalHC, "", nil
	}
	return "", "", project.Fail("no terminal runs: start herdr or open herdr-app, then obor init again")
}

// initTerminalKind is the terminal obor init runs on, with init's arguments carrying it as --terminal (the --terminal
// given, if any, replaced): an existing .obor/config.json at root names it (cfg: its load, nil when it does not
// load: herdr) and wins, with line saying so when the caller sits in the other terminal or asked for the other;
// with none, detectTerminal decides, before anything is written.
func initTerminalKind(ctx context.Context, env project.Env, root string, cfg *config.Config, args []string,
	p terminalProbes) (kind, line string, rest []string, err error) {
	flag := ""
	for i := 0; i < len(args); i++ {
		if args[i] != "--terminal" {
			rest = append(rest, args[i])
			continue
		}
		if i+1 == len(args) || !slices.Contains(config.TerminalKinds, args[i+1]) {
			return "", "", nil, project.Fail(setup.InitUsage)
		}
		flag = args[i+1]
		i++
	}
	if root != "" {
		if _, serr := os.Stat(filepath.Join(root, ".obor", "config.json")); serr == nil {
			kind = cfg.TerminalKind()
			if other := callerTerminal(env); flag != "" && flag != kind || flag == "" && other != "" && other != kind {
				line = fmt.Sprintf("terminal %s (from .obor/config.json; the config wins, change project.terminal to "+
					"switch)", kind)
			}
			return kind, line, append(rest, "--terminal", kind), nil
		}
	}
	if kind, line, err = detectTerminal(ctx, env, flag, p); err != nil {
		return "", "", nil, err
	}
	return kind, line, append(rest, "--terminal", kind), nil
}

// callerTerminal is the terminal the caller's pane belongs to by its environment: REX_BLOCK set is Rex whatever else
// is (a block inherits the Rex server's whole environment, another terminal's pane variable with it:
// docs/research/rex-probes.md R4); else the one kind whose variable is set ("": none, or both).
func callerTerminal(env project.Env) string {
	if env.Get(paneVarOf(config.TerminalRex)) != "" {
		return config.TerminalRex
	}
	in := ""
	for _, k := range terminals {
		if env.Get(k.PaneVar) == "" {
			continue
		}
		if in != "" {
			return ""
		}
		in = k.Name
	}
	return in
}

// paneVarOf is kind's pane variable (the terminals table).
func paneVarOf(kind string) string {
	for _, k := range terminals {
		if k.Name == kind {
			return k.PaneVar
		}
	}
	return ""
}
