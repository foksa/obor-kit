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

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/service"
	"obor/internal/setup"
)

// terminal is the project's terminal: one value for its panes and events, launch's pane operations with the wakes
// serve types, and setup's placements. cfg names the kind (project.terminal; nil: config.DefaultTerminal), env finds its
// socket. A kind obor does not know is an error naming the ones it does.
func terminal(cfg *config.Config, env project.Env) (port.Terminal, error) {
	switch k := cfg.TerminalKind(); k {
	case config.DefaultTerminal:
		s := &herdr.Socket{Path: herdr.SocketPath(env)}
		return port.Terminal{Panes: herdr.Panes{S: s}, Control: s, Setup: &herdr.Setup{Socket: s, Env: env}}, nil
	case config.TerminalHC:
		cl := hc.New(env)
		return port.Terminal{Panes: hc.Panes{C: cl}, Control: cl, Setup: cl, Check: cl.Check}, nil
	default:
		return port.Terminal{}, fmt.Errorf("terminal.kind %q: obor knows %s", k, strings.Join(config.TerminalKinds, ", "))
	}
}

// serveTerminal is t as serve watches it: on hc, each subscribe first starts a core that is down from serve's own
// environment scrubbed, and refuses a core whose environment holds a HERDR_ or OBOR_ name (hc.Core); obor's other
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
	var names []string
	switch cfg.TerminalKind() {
	case config.DefaultTerminal:
		names = herdr.EnvNames
	case config.TerminalHC:
		names = hc.EnvNames
	}
	var out []service.EnvVar
	for _, name := range names {
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
// (answers) with the HERDR_ and OBOR_ names its environment holds (leaked: a core started from a pane). Both only
// dial; nothing starts a core.
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
// when the project has no config): flag (--terminal) when set; else the one the caller sits in (HC_TERMINAL_ID: hc,
// HERDR_PANE_ID: herdr); both set: herdr, unless hc's core holds a pane's identity (a FAIL); neither: the one that
// answers, herdr when both do (line says how to get hc), no terminal answering is a FAIL.
func detectTerminal(ctx context.Context, env project.Env, flag string, p terminalProbes) (kind, line string, err error) {
	if flag != "" {
		return flag, "", nil
	}
	if k := callerTerminal(env); k != "" {
		return k, "", nil
	}
	if env.Get("HC_TERMINAL_ID") != "" { // both set: HC_TERMINAL_ID was inherited by herdr's server unless the core
		// was started from a herdr pane
		if _, leaked := p.hc(ctx); len(leaked) > 0 {
			return "", "", project.Failf("terminal: HC_TERMINAL_ID and HERDR_PANE_ID are both set: %s (its environment holds %s)",
				hc.LeakLine, strings.Join(leaked, ", "))
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

// callerTerminal is the terminal the caller's pane belongs to by its environment ("": neither, or both).
func callerTerminal(env project.Env) string {
	inHC, inHerdr := env.Get("HC_TERMINAL_ID") != "", env.Get("HERDR_PANE_ID") != ""
	switch {
	case inHC && !inHerdr:
		return config.TerminalHC
	case inHerdr && !inHC:
		return config.DefaultTerminal
	}
	return ""
}
