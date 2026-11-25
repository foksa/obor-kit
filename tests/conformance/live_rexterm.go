//go:build conformance

// The live tier on Rex (`make conformance TIER=live TERMINAL=rex KIND=<claude|codex|opencode> [TURN=1]`): each kind's
// worker start through obor's own Rex adapter (internal/rex: Place for the lane's block, Write for the env line,
// Start, the guarded Prompt, WaitStatus), in a block of a throwaway Rex server (rexLab: scratch socket, state, config
// and XDG_RUNTIME_DIR, --autostart=false, stopped at the end; never the person's server), with the scratch kit's stub
// as obor (lab's makeKit) and the start args obor's launch builds for the kind's smallest starter model (hcLaunch). The server is
// started with an obor pane's, herdr's, hc's and a Claude session's identity in its environment, which every block
// inherits from it: the block Place makes must hold none of it (RX-T0), and the agent's obor server must see the env
// line's identity (RX-T2). Rex is experimental: these rows do not gate a merge (D7), and a kind that cannot run SKIPs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/rex"
)

// rexLeak is the identity the scratch server's environment holds (rexLab.leak): what an obor pane, herdr, hc and a
// Claude session would pass to a server started from them.
var rexLeak = []string{"OBOR_ROLE=leak-role", "OBOR_PANE=leak-pane", "HERDR_PANE_ID=leak-pane", "HERDR_ENV=1",
	"HC_TERMINAL_ID=t-leak", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=leak-session", "CLAUDE_CODE_ENTRYPOINT=leak"}

// liveOnRex runs each kind's Rex rows (RX-T0 the scrubbed block, RX-T1 the start, RX-T2 the identity its obor server
// sees, RX-T3 with TURN=1 a turn) on a scratch server of its own.
func liveOnRex(kinds []string, turn bool) {
	bin, err := exec.LookPath("rex")
	if err != nil {
		if _, e := os.Stat(rexBundle); e != nil {
			report("SKIP", "RX-T", "no rex on PATH and no Rex app bundle", rexPlan)
			return
		}
		bin = rexBundle
	}
	v, _ := output(15*time.Second, bin, "version")
	out.note("rex " + firstLine(v))
	// /tmp, not TMPDIR: the socket's path must stay under a unix socket's 104 bytes.
	root, err := os.MkdirTemp("/tmp", "obrt-")
	if err != nil {
		report("FAIL", "RX-T", "no scratch dir: "+err.Error(), rexPlan)
		return
	}
	root, _ = filepath.EvalSymlinks(root)
	defer func() {
		if os.Getenv("KEEP") == "1" {
			out.note("rex: scratch dir kept (KEEP=1): " + root)
		} else {
			os.RemoveAll(root)
		}
	}()
	for _, kind := range kinds {
		if kind == "herdr" {
			report("SKIP", "RX-T", "herdr is a terminal, not an agent kind: TERMINAL=rex runs claude, codex and opencode", rexPlan)
			continue
		}
		if _, err := exec.LookPath(kind); err != nil {
			report("SKIP", "RX-T1", kind+" is not installed", rexPlan)
			continue
		}
		rexTermKind(bin, root, kind, turn)
	}
}

// rexTermKind is one kind's rows: the scratch project and kit, the server, the lane's block, the start, the turn, the
// quit.
func rexTermKind(bin, root, kind string, turn bool) {
	dir := filepath.Join(root, kind)
	l := &lab{kind: kind, root: dir, kit: filepath.Join(dir, "kit"), main: filepath.Join(dir, "proj"),
		lane: filepath.Join(dir, "proj-wt", "L-01"), session: "obor-probe-" + kind}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	defer l.cancel()
	if err := l.makeKit(); err != nil {
		report("FAIL", "RX-T1", kind+": scratch kit: "+err.Error(), rexPlan)
		return
	}
	if err := l.makeProject(); err != nil {
		report("FAIL", "RX-T1", kind+": scratch project: "+err.Error(), rexPlan)
		return
	}
	rl := &rexLab{root: filepath.Join(root, "rx-"+kind), leak: rexLeak}
	rl.sock, rl.state = filepath.Join(rl.root, "s.sock"), filepath.Join(rl.root, "state")
	if err := rl.setup(bin); err != nil {
		report("FAIL", "RX-T1", kind+": scratch server setup: "+err.Error(), rexPlan)
		return
	}
	if err := rl.start(); err != nil {
		report("FAIL", "RX-T1", kind+": no scratch server: "+err.Error(), rexPlan)
		rl.stop()
		return
	}
	defer rl.stop()
	cl := rl.cl
	cl.Kinds = l.kinds
	control := port.GuardPrompts(cl)
	ctx := context.Background()

	env := []string{}
	for k, v := range hcUpdatesOff {
		env = append(env, k+"="+v)
	}
	pane, err := cl.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: l.lane, Env: env,
		Project: port.PlaceProject{Name: "conf", Root: l.main, MaxPanes: 4}})
	if err != nil {
		report("FAIL", "RX-T0", kind+": Place: "+err.Error(), at("internal/rex/place.go", "func (cl *Client) Place"))
		return
	}
	if !rexScrubbed(rl, cl, kind, pane) {
		return
	}

	start, k, err := hcLaunch(l, kind)
	if err != nil {
		report("FAIL", "RX-T1", kind+": the launch cannot be built: "+err.Error(), at("internal/config/models.go", "func (m Model) Launch"))
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane}
	for k, v := range start.Env {
		paneEnv[k] = v
	}
	if err := cl.Write(ctx, pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), true); err != nil {
		report("FAIL", "RX-T1", kind+": the env line: "+err.Error(), at("internal/rex/control.go", "func (cl *Client) Write"))
		return
	}
	poll(10*time.Second, func() bool { p, err := cl.Pane(ctx, pane); return err == nil && p.Status == "" })

	where1 := at("internal/rex/control.go", "func (cl *Client) Start") + ", internal/app/shellpane (Start), " + rexPlan
	t0 := time.Now()
	st := control.(port.AgentStarter)
	p, err := st.Start(ctx, port.AgentStart{Pane: pane, Label: "cf-l-01", Kind: kind, Program: agents.Programs(k)[0], Argv: start.Args,
		Bound: 45 * time.Second})
	dialog := ""
	if port.NotReady(err) { // a startup dialog (the scratch dir is new to the CLI): answered as a person would
		screen, _ := cl.Screen(ctx, pane, 0)
		keys := []string{port.KeyEnter}
		if kind == agents.Default && agents.TrustDialog(screen) { // Claude's default option is "No, exit"
			keys = []string{"down", port.KeyEnter}
		}
		dialog = fmt.Sprintf("; a startup dialog (%s) answered with %s", firstLine(err.Error()), strings.Join(keys, ", "))
		_ = cl.SendKeys(ctx, pane, keys...)
		if poll(45*time.Second, func() bool { q, e := cl.Pane(ctx, pane); p = q; return e == nil && q.Agent == kind && q.Ready() }) {
			err = nil
		}
	}
	took := time.Since(t0)
	if err != nil {
		screen, _ := cl.Screen(ctx, pane, 4)
		report("FAIL", "RX-T1", fmt.Sprintf("%s: the start did not come up in %.1fs: %v; %s", kind, took.Seconds(), err, screen), where1)
		rexQuit(cl, k, pane)
		return
	}
	report("PASS", "RX-T1", fmt.Sprintf("%s: up in %.1fs through rex.Client.Start: status %s, agent %s, label %s%s", kind,
		took.Seconds(), p.Status, p.Agent, p.Label, dialog), where1)

	rexTermIdentity(l, kind, pane)
	if turn {
		rexTermTurn(cl, control, kind, pane)
	} else {
		report("SKIP", "RX-T3", kind+": needs TURN=1 (a model call)", at("internal/rex/control.go", "func (cl *Client) Prompt"))
	}
	rexQuit(cl, k, pane)
}

// rexScrubbed is RX-T0: the block Place made holds none of the server's leaked identity (the server's own environment
// does, rexLeak) and runs its shell in the lane: the block's `env`, written to a file.
func rexScrubbed(rl *rexLab, cl *rex.Client, kind, pane string) bool {
	where := at("internal/rex/env.go", "func (e blockEnv) command") + ", " + at("internal/rex/place.go", "func (cl *Client) Place")
	file := filepath.Join(rl.root, "block-env.txt")
	if err := cl.Write(context.Background(), pane, "env > "+shq(file), true); err != nil {
		report("FAIL", "RX-T0", kind+": "+err.Error(), where)
		return false
	}
	var b []byte
	poll(10*time.Second, func() bool { b, _ = os.ReadFile(file); return len(b) > 0 })
	lines := strings.Split(string(b), "\n")
	var kept []string
	for _, kv := range rexLeak {
		if slices.Contains(lines, kv) {
			kept = append(kept, kv)
		}
	}
	switch {
	case len(b) == 0:
		report("FAIL", "RX-T0", kind+": the block's env was not written in 10 s", where)
		return false
	case len(kept) > 0:
		report("FAIL", "RX-T0", fmt.Sprintf("%s: the block kept the server's %v", kind, kept), where)
		return false
	}
	report("PASS", "RX-T0", fmt.Sprintf("%s: the placed block holds none of the %d identity names the server's environment holds", kind,
		len(rexLeak)), where)
	return true
}

// rexTermIdentity is RX-T2: the obor server the agent started (the stub) runs in the lane with the env line's identity
// (OBOR_PANE the block, OBOR_ROLE worker) and none of the server's (no HERDR_ name, no Claude session marker of its);
// obor's pane resolver on a Rex project (project.PaneOf) reads the block from it, by OBOR_PANE and by REX_BLOCK alone.
func rexTermIdentity(l *lab, kind, pane string) {
	where := at("internal/rex/env.go", "func (e blockEnv) command") + ", " + at("internal/project/pane.go", "func PaneOf") + ", " + rexPlan
	rs := l.waitRecords("mcp", "", 30*time.Second)
	if len(rs) == 0 {
		report("FAIL", "RX-T2", kind+": the agent started no obor MCP server in 30 s", where)
		return
	}
	r := rs[len(rs)-1]
	herdr := envNames(r.Env, "^HERDR_")
	rexCfg := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalRex}}}
	recEnv := project.Env(func(k string) (string, bool) { v, ok := r.Env[k]; return v, ok })
	got, perr := project.PaneOf(rexCfg, recEnv)
	noObor := project.Env(func(k string) (string, bool) { // the fallback alone: the pane variable Rex sets
		if k == agents.PaneVar {
			return "", false
		}
		return recEnv(k)
	})
	fallback, ferr := project.PaneOf(rexCfg, noObor)
	fact := fmt.Sprintf("%s: the obor server sees OBOR_PANE=%q OBOR_ROLE=%q REX_BLOCK=%q, cwd %s, HERDR_ names %v, CLAUDE_CODE_SESSION_ID=%q; "+
		"project.PaneOf on rex → %q (%v), without OBOR_PANE → %q (%v)",
		kind, r.Env[agents.PaneVar], r.Env["OBOR_ROLE"], r.Env["REX_BLOCK"], r.Cwd, herdr, r.Env["CLAUDE_CODE_SESSION_ID"],
		got, orOK(perr), fallback, orOK(ferr))
	if r.Env[agents.PaneVar] != pane || r.Env["OBOR_ROLE"] != "worker" || len(herdr) > 0 || !same(r.Cwd, l.lane) ||
		r.Env["CLAUDE_CODE_SESSION_ID"] == "leak-session" || r.Env["HC_TERMINAL_ID"] != "" || got != pane ||
		r.Env["REX_BLOCK"] != "" && fallback != pane {
		report("FAIL", "RX-T2", fact+"; want the block, worker, the lane, none of the server's identity, the block as the pane "+
			"by either variable", where)
		return
	}
	report("PASS", "RX-T2", fact, where)
}

// rexTermTurn is RX-T3: one "Reply with exactly OK" through the guarded Prompt (paste, Enter); the turn ends
// (WaitStatus) and OK is on the screen within 15 s of it.
func rexTermTurn(cl *rex.Client, control port.PaneControl, kind, pane string) {
	where := at("internal/rex/control.go", "func (cl *Client) Prompt") + ", " + at("internal/rex/control.go", "func (cl *Client) WaitStatus")
	ctx := context.Background()
	t0 := time.Now()
	began, err := control.Prompt(ctx, pane, "Reply with exactly OK", false)
	if err != nil || !began {
		report("FAIL", "RX-T3", fmt.Sprintf("%s: Prompt: began %v, %v", kind, began, err), where)
		return
	}
	worked := poll(30*time.Second, func() bool { p, err := cl.Pane(ctx, pane); return err == nil && p.Status == "working" })
	werr := cl.WaitStatus(ctx, pane, []string{"idle", "done"}, 180*time.Second)
	settled := time.Since(t0)
	// the screen may draw the reply after the status says the turn ended (OpenCode's idle comes first): looked for up
	// to 15 s, as on hc
	var screen string
	ok := poll(15*time.Second, func() bool { screen, _ = cl.Screen(ctx, pane, 0); return okLine(screen) })
	fact := fmt.Sprintf("%s: Prompt began; working seen %v; WaitStatus %s after %.1fs; OK on screen %v after %.1fs", kind, worked,
		orOK(werr), settled.Seconds(), ok, time.Since(t0).Seconds())
	if werr != nil || !ok {
		report("FAIL", "RX-T3", fact+"; "+tail(screen, 6), where)
		return
	}
	report("PASS", "RX-T3", fact, where)
}

// rexQuit ends the agent with its kind's quit recipe, then ctrl+c, so the server stops with no agent in it.
func rexQuit(cl *rex.Client, k agents.Kind, pane string) {
	ctx := context.Background()
	atShell := func() bool { p, err := cl.Pane(ctx, pane); return err == nil && p.Status == "" }
	if k != nil {
		_ = cl.SendKeys(ctx, pane, port.KeyCtrlU)
		_ = cl.Write(ctx, pane, k.Stop().Quit, true)
		if poll(10*time.Second, atShell) {
			return
		}
		_ = cl.SendKeys(ctx, pane, port.KeyEnter)
	}
	for range 3 {
		if poll(5*time.Second, atShell) {
			return
		}
		_ = cl.SendKeys(ctx, pane, port.KeyCtrlC)
	}
}
