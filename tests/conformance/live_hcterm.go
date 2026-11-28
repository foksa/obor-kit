//go:build conformance

// The live tier on hc (`make conformance TIER=live TERMINAL=hc KIND=<claude|codex|opencode|agy|pi> [TURN=1]`): each
// kind's worker start through obor's own hc adapter (internal/hc: Write for the env line, Start, Prompt), in a kept,
// labelled terminal of a throwaway hc session (hcLab: HC_SESSION=obor-probe-<kind>, scratch XDG dirs, the core started
// from a scrubbed environment, stopped at the end; never the default session), with the scratch kit's stub as obor
// (lab's makeKit) and the start args obor's launch builds for the model hcModels names. The terminal gets the person's own
// XDG_CONFIG_HOME and XDG_STATE_HOME back (the scratch core's would hide each CLI's config, P4) and every update check
// off (an update prompt is answered by the first key or send: T-1351 upgraded Codex so). hc is experimental: these
// rows do not gate a merge (HD7), and a kind that cannot run SKIPs.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/project"
)

const hcTermPlan = "docs/plan/herdr-core.md step 4"

// hcUpdatesOff are the env names that turn off each kind's update check at its start.
var hcUpdatesOff = map[string]string{"DISABLE_AUTOUPDATER": "1", "OPENCODE_DISABLE_AUTOUPDATE": "1"}

// hcUpdateArgs are a kind's start args that turn off its update check (Codex reads it from its config).
var hcUpdateArgs = map[string][]string{"codex": {"-c", "check_for_update_on_startup=false"}}

// hcModels is the model each kind's start runs: its smallest of obor init's starter list (the lab config's models), and
// agy's and pi's, which the starter list has no entry for (the lab's agy-live and pi-live, the cheapest Anthropic
// model). The hc and Rex kind drivers share it.
var hcModels = map[string]string{"claude": "haiku", "codex": "luna", "opencode": "deepseek", "agy": "agy-live", "pi": "pi-live"}

// liveOnHC runs each kind's hc rows (HC-T1 the start, HC-T3 with TURN=1 a turn, then HC-T2 the identity its obor server
// sees: agy starts that server at its first prompt) in a session of its own.
func liveOnHC(kinds []string, turn bool) {
	hcBin, err := exec.LookPath("hc")
	if err != nil {
		report("SKIP", "HC-T", "hc is not installed", hcTermPlan)
		return
	}
	v, _ := output(15*time.Second, hcBin, "--version")
	out.note("hc " + versionOf(v))
	// /tmp, not TMPDIR: the core's socket path must stay under a unix socket's 104 bytes.
	root, err := os.MkdirTemp("/tmp", "obt-")
	if err != nil {
		report("FAIL", "HC-T", "no scratch dir: "+err.Error(), hcTermPlan)
		return
	}
	defer func() {
		hcStopAll()
		if os.Getenv("KEEP") == "1" {
			out.note("hc: scratch dir kept (KEEP=1): " + root)
		} else {
			os.RemoveAll(root)
		}
	}()
	for _, kind := range kinds {
		if kind == "herdr" {
			report("SKIP", "HC-T", "herdr is a terminal, not an agent kind: TERMINAL=hc runs claude, codex, opencode, agy and pi", hcTermPlan)
			continue
		}
		if _, err := exec.LookPath(kind); err != nil {
			report("SKIP", "HC-T1", kind+" is not installed", hcTermPlan)
			continue
		}
		hcTermKind(hcBin, root, kind, turn)
	}
}

// hcTermKind is one kind's rows: the scratch project and kit, the session, one terminal, the start, the turn, the
// identity, the quit.
func hcTermKind(hcBin, root, kind string, turn bool) {
	dir := filepath.Join(root, kind)
	l := &lab{kind: kind, root: dir, kit: filepath.Join(dir, "kit"), main: filepath.Join(dir, "proj"),
		lane: filepath.Join(dir, "proj-wt", "L-01"), session: "obor-probe-" + kind}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	defer l.cancel()
	if err := l.makeKit(); err != nil {
		report("FAIL", "HC-T1", kind+": scratch kit: "+err.Error(), hcTermPlan)
		return
	}
	if err := l.makeProject(); err != nil {
		report("FAIL", "HC-T1", kind+": scratch project: "+err.Error(), hcTermPlan)
		return
	}
	hl, err := newHCLab(hcBin, root, "s-"+kind)
	if err != nil {
		report("FAIL", "HC-T1", kind+": the scratch session did not start: "+err.Error(), hcTermPlan)
		return
	}
	defer hl.stop()
	cl := &hc.Client{Path: hl.sock, Kinds: l.kinds}
	if err := cl.CheckEnv(context.Background()); err != nil {
		report("FAIL", "HC-T0", kind+": the scratch core's environment: "+err.Error(), at("internal/hc/env.go", "func (cl *Client) CheckEnv"))
		return
	}
	report("PASS", "HC-T0", kind+": the scratch core started from a scrubbed environment passes the env guard (machine.exec of env: no HERDR_/OBOR_ name)",
		at("internal/hc/env.go", "func (cl *Client) CheckEnv")+", "+hcTermPlan)

	home, _ := os.UserHomeDir()
	termEnv := map[string]string{"XDG_CONFIG_HOME": envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config")),
		"XDG_STATE_HOME": envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))}
	for k, v := range hcUpdatesOff {
		termEnv[k] = v
	}
	t, err := hl.term(map[string]any{"keep": true, "no_parent": true, "cwd": l.lane, "env": termEnv,
		"labels": map[string]string{hc.LabelProject: "conf", hc.LabelAgent: "cf-l-01"}})
	if err != nil {
		report("FAIL", "HC-T1", kind+": terminal.run: "+err.Error(), hcTermPlan)
		return
	}
	pane := t.Address
	if !poll(10*time.Second, func() bool { return strings.TrimSpace(hl.screen(pane)) != "" }) {
		report("FAIL", "HC-T1", kind+": the terminal's shell showed no prompt in 10 s", hcTermPlan)
		return
	}

	start, k, err := hcLaunch(l, kind)
	if err != nil {
		report("FAIL", "HC-T1", kind+": the launch cannot be built: "+err.Error(), at("internal/config/models.go", "func (m Model) Launch"))
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane}
	if kind == "agy" { // its project MCP file and hooks name `obor`: the scratch kit's stub is the obor on the pane's PATH
		paneEnv["PATH"] = filepath.Join(l.kit, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	for k, v := range start.Env {
		paneEnv[k] = v
	}
	ctx := context.Background()
	if err := cl.Write(ctx, pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), true); err != nil {
		report("FAIL", "HC-T1", kind+": the env line: "+err.Error(), at("internal/hc/control.go", "func (cl *Client) Write"))
		return
	}
	poll(10*time.Second, func() bool { return hl.get(pane).Foreground != nil && hl.get(pane).Foreground.PID == hl.get(pane).PID })

	where1 := at("internal/hc/start.go", "func (cl *Client) Start") + ", " + hcTermPlan
	t0 := time.Now()
	p, answered, err := startPastDialog(ctx, cl, port.AgentStart{Pane: pane, Label: "cf-l-01", Kind: kind, Program: agents.Programs(k)[0],
		Argv: start.Args, Bound: 45 * time.Second})
	if answered != "" {
		answered = "; " + answered
	}
	took := time.Since(t0)
	term := hl.get(pane)
	if err != nil {
		report("FAIL", "HC-T1", fmt.Sprintf("%s: the start did not come up in %.1fs: %v; status %s; %s", kind, took.Seconds(), err,
			term.state(), tail(hl.screen(pane), 4)), where1)
		hcQuit(cl, hl, k, pane)
		return
	}
	app := ""
	if term.Status != nil {
		app = term.Status.App
	}
	report("PASS", "HC-T1", fmt.Sprintf("%s: up in %.1fs through hc.Client.Start: status %s, app %q, pane agent %s%s", kind,
		took.Seconds(), p.Status, app, p.Agent, answered), where1)

	if turn {
		hcTermTurn(cl, hl, kind, pane)
	} else {
		report("SKIP", "HC-T3", kind+": needs TURN=1 (a model call)", at("internal/hc/control.go", "func (cl *Client) Prompt"))
	}
	if mcpAtPrompt[kind] && !turn {
		report("SKIP", "HC-T2", kind+": starts its MCP servers at its first prompt: needs TURN=1 (a model call)", at("internal/project/pane.go", "func Pane"))
	} else {
		hcTermIdentity(l, kind, pane)
	}
	hcQuit(cl, hl, k, pane)
}

// hcLaunch is a worker's start of the model hcModels names for kind, as obor's launch builds it, with the scratch kit's
// stub as obor and the kind's update check off.
func hcLaunch(l *lab, kind string) (agents.Launch, agents.Kind, error) {
	cfg, err := config.Load(l.main)
	if err != nil {
		return agents.Launch{}, nil, err
	}
	k, ok := cfg.Kind(kind)
	if !ok {
		return agents.Launch{}, nil, fmt.Errorf("no kind %s", kind)
	}
	m, ok := cfg.Model(hcModels[kind])
	if !ok {
		return agents.Launch{}, k, fmt.Errorf("the lab config has no %s model %s", kind, hcModels[kind])
	}
	m.Args = append(m.Args, hcUpdateArgs[kind]...)
	if kind == "pi" { // its sessions stay in the scratch dir, and its startup network work is off
		sessions := filepath.Join(l.root, "pi-sessions")
		if err := os.MkdirAll(sessions, 0o755); err != nil {
			return agents.Launch{}, k, err
		}
		m.Args = append(m.Args, "--offline", "--session-dir", sessions)
	}
	over, err := cfg.Overrides(kind)
	if err != nil {
		return agents.Launch{}, k, err
	}
	launch, err := m.Launch("worker", over, agents.Start{Agent: "cf-l-01", Kit: l.kit, Dir: l.lane, Root: l.main,
		Read: os.ReadFile, Env: os.Getenv, Dirs: project.SubDirs, WriteState: project.StateWriter(l.main), StrictMCP: true})
	return launch, k, err
}

// startPastDialog starts s through control (its AgentStarter). A start that comes back NotReady (a CLI's startup
// dialog in the scratch lane, agy's brief "Sign in required") is answered as a person would, then waited for (45 s):
// Enter, or for Claude's trust dialog, whose default option is "No, exit", down and Enter. answered is that dialog and
// its keys, for the row's fact ("" when the start needed none). The hc and Rex drivers start every agent through it.
func startPastDialog(ctx context.Context, control port.PaneControl, s port.AgentStart) (p port.Pane, answered string, err error) {
	st, ok := control.(port.AgentStarter)
	if !ok {
		return port.Pane{}, "", errors.New("the terminal's PaneControl is no AgentStarter")
	}
	p, err = st.Start(ctx, s)
	if !port.NotReady(err) {
		return p, "", err
	}
	screen, _ := control.Screen(ctx, s.Pane, 0)
	keys := []string{port.KeyEnter}
	if s.Kind == agents.Default && agents.TrustDialog(screen) {
		keys = []string{"down", port.KeyEnter}
	}
	answered = fmt.Sprintf("a startup dialog (%s) answered with %s", firstLine(err.Error()), strings.Join(keys, ", "))
	_ = control.SendKeys(ctx, s.Pane, keys...)
	if poll(45*time.Second, func() bool {
		q, e := control.Pane(ctx, s.Pane)
		p = q
		return e == nil && q.Agent == s.Kind && q.Ready()
	}) {
		err = nil
	}
	return p, answered, err
}

// mcpAtPrompt are the kinds that start their MCP servers at the first prompt of a conversation, not when the CLI
// starts (agy: docs/research/agy-probes.md P2): their identity row needs the turn before it.
var mcpAtPrompt = map[string]bool{"agy": true}

// hcTermIdentity is HC-T2: the obor server the agent started (the stub) runs in the lane with OBOR_PANE and, when the
// kind runs it in the terminal's own process tree, HC_TERMINAL_ID naming the terminal; no HERDR_ name; and obor's pane
// resolver on an hc project reads the pane from that environment, never a herdr variable.
func hcTermIdentity(l *lab, kind, pane string) {
	where := at("internal/project/pane.go", "func Pane") + ", " + hcTermPlan
	rs := l.waitRecords("mcp", "", 30*time.Second)
	if len(rs) == 0 {
		report("FAIL", "HC-T2", kind+": the agent started no obor MCP server in 30 s", where)
		return
	}
	r := rs[len(rs)-1]
	hcCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalHC}}}
	got, err := project.PaneOf(hcCfg, project.Env(func(k string) (string, bool) { v, ok := r.Env[k]; return v, ok }))
	herdr := envNames(r.Env, "^HERDR_")
	fact := fmt.Sprintf("%s: the obor server sees OBOR_PANE=%q HC_TERMINAL_ID=%q, cwd %s, HERDR_ names %v; project.Pane on hc → %q (%v)",
		kind, r.Env[agents.PaneVar], r.Env["HC_TERMINAL_ID"], r.Cwd, herdr, got, orOK(err))
	switch {
	case got != pane || len(herdr) > 0 || !same(r.Cwd, l.lane):
		report("FAIL", "HC-T2", fact+"; want pane "+pane+", the lane, no HERDR_ name", where)
	case r.Env["HC_TERMINAL_ID"] != "" && r.Env["HC_TERMINAL_ID"] != pane:
		report("FAIL", "HC-T2", fact+"; HC_TERMINAL_ID names another terminal", where)
	default:
		report("PASS", "HC-T2", fact, where)
	}
}

// hcTermTurn is HC-T3: one "Reply with exactly OK" through hc.Client.Prompt; the turn ends (the status leaves working)
// and OK is on the screen.
func hcTermTurn(cl *hc.Client, hl *hcLab, kind, pane string) {
	where := at("internal/hc/control.go", "func (cl *Client) Prompt") + ", " + at("internal/hc/control.go", "func (cl *Client) WaitStatus")
	ctx := context.Background()
	t0 := time.Now()
	began, err := cl.Prompt(ctx, pane, "Reply with exactly OK", false)
	if err != nil || !began {
		report("FAIL", "HC-T3", fmt.Sprintf("%s: Prompt: began %v, %v", kind, began, err), where)
		return
	}
	worked := poll(30*time.Second, func() bool { return hl.get(pane).state() == "working" })
	ended := poll(180*time.Second, func() bool {
		s := hl.get(pane).state()
		return s != "working" && s != "null" && okLine(hl.screen(pane))
	})
	fact := fmt.Sprintf("%s: Prompt began; working seen %v; after %.1fs status %s, OK on screen %v", kind, worked,
		time.Since(t0).Seconds(), hl.get(pane).state(), okLine(hl.screen(pane)))
	if !ended {
		var ok []string
		for _, l := range strings.Split(hl.screen(pane), "\n") {
			if strings.Contains(l, "OK") {
				ok = append(ok, strings.TrimSpace(l))
			}
		}
		report("FAIL", "HC-T3", fmt.Sprintf("%s: lines with OK %q; %s", fact, ok, tail(hl.screen(pane), 6)), where)
		return
	}
	report("PASS", "HC-T3", fact, where)
}

// hcQuit ends the agent with its kind's quit recipe, then ctrl+c, so the session stops with no agent in it.
func hcQuit(cl *hc.Client, hl *hcLab, k agents.Kind, pane string) {
	ctx := context.Background()
	if k != nil {
		st := k.Stop()
		_ = cl.SendKeys(ctx, pane, "ctrl+u")
		_ = cl.Write(ctx, pane, st.Quit, true)
		if poll(10*time.Second, func() bool { t := hl.get(pane); return t.Foreground != nil && t.Foreground.PID == t.PID }) {
			return
		}
		_ = cl.SendKeys(ctx, pane, "enter")
	}
	for range 3 {
		if poll(5*time.Second, func() bool { t := hl.get(pane); return t.Foreground != nil && t.Foreground.PID == t.PID }) {
			return
		}
		_ = cl.SendKeys(ctx, pane, "ctrl+c")
	}
}

// okLine: screen has a line reading OK, past the agent's own marks around it (Claude's ⏺, Codex's •, OpenCode's
// gutter and side panel).
func okLine(screen string) bool {
	for _, l := range strings.Split(screen, "\n") {
		// OpenCode draws its side panel on the same line, two spaces or more after the reply
		if t := strings.Trim(strings.TrimSpace(l), "⏺•●>▌┃│ "); t == "OK" || strings.HasPrefix(t, "OK  ") {
			return true
		}
	}
	return false
}

// envOr is os's value of name, else def.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
