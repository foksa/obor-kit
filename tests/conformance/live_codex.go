//go:build conformance

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
)

// codexAllow is the environment Codex gives an MCP server besides env_vars and env (CAP §3.2: DEFAULT_ENV_VARS).
var codexAllow = []string{"HOME", "LOGNAME", "PATH", "SHELL", "USER", "__CF_USER_TEXT_ENCODING", "LANG", "LC_ALL", "TERM", "TMPDIR", "TZ"}

// codexLive: on a scratch CODEX_HOME (auth.json copied, removed after; a config.toml with the model and the scratch
// dirs trusted), a start with --no-daemon and then one without, each with --strict-config: CX-L1 per start, CX-L2,
// H-L1; with TURN=1 one turn on the first (CX-L3); then H-L11 on a third start, whose home holds the user's hooks.json
// (herdr's Codex integration) for that start alone. The per-launch SessionStart hook is not probed (CX-L5 deleted
// until P6's Codex half is a task: the driver sends none).
func codexLive(l *lab, turn bool) {
	k := kindOf("codex")
	src := os.Getenv("CODEX_HOME")
	if src == "" {
		home, _ := os.UserHomeDir()
		src = filepath.Join(home, ".codex")
	}
	auth, err := os.ReadFile(filepath.Join(src, "auth.json"))
	if err != nil {
		skipAll("codex", "no Codex auth to copy ("+filepath.Join(src, "auth.json")+")")
		return
	}
	codexHome := filepath.Join(l.root, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		report("FAIL", "CX-L1", err.Error(), "deep dive §6")
		return
	}
	authCopy := filepath.Join(codexHome, "auth.json")
	l.cleanups = append(l.cleanups, func() { _ = os.Remove(authCopy) })
	if err := os.WriteFile(authCopy, auth, 0o600); err != nil {
		report("FAIL", "CX-L1", err.Error(), "deep dive §6")
		return
	}
	config := ""
	if b, err := os.ReadFile(filepath.Join(src, "config.toml")); err == nil {
		if m := regexp.MustCompile(`(?m)^model\s*=.*$`).Find(b); m != nil {
			config = string(m) + "\n"
		}
	}
	for _, dir := range []string{l.lane, l.main, l.root} {
		config += fmt.Sprintf("\n[projects.%q]\ntrust_level = \"trusted\"\n", dir)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		report("FAIL", "CX-L1", err.Error(), "deep dive §6")
		return
	}

	pane, err := l.workspace("conf", l.lane)
	if err != nil {
		report("FAIL", "CX-L1", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	paneEnv := map[string]string{"CODEX_HOME": codexHome, "OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane,
		"OBOR_CONF_PANE_ONLY": "1"}
	if st, err := l.shell(pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "CX-L1", fmt.Sprintf("the env line failed (status %d, %v)", st, err), at(launchGo, "func envLine"))
		return
	}
	st := k.Stop()
	where2 := at(codexGo, "func (kind) NotReady") + ", " + at(codexGo, "func (kind) Stop") + ", CAP §3.6"
	whereT := at("internal/launch/session.go", "func") + ", F4.5"
	quit := func(report2 bool) {
		code, err := l.quit(pane, st)
		switch {
		case err != nil || code != 0:
			if report2 {
				report("FAIL", "CX-L2", fmt.Sprintf("%s: exit status %d (%v)", st.Quit, code, orOK(err)), where2)
			}
			l.stopAgent(pane)
		case report2:
			report("PASS", "CX-L2", recipeFact(st)+" exits with status 0", where2)
		}
	}

	// 1: --no-daemon (with TURN=1 the turn).
	first, ok := l.codexStart(k, pane, "--no-daemon", []string{"--strict-config", "--no-daemon"})
	switch {
	case !ok:
		report("SKIP", "CX-L3", "codex did not start", whereT)
	case turn:
		cxTurn(l, pane, codexHome, first)
	default:
		report("SKIP", "CX-L3", "needs TURN=1 (a model call)", whereT)
	}
	if ok {
		quit(true)
	}

	// 2: the default (an eligible session may attach to Codex's daemon).
	l.fresh()
	if _, ok := l.codexStart(k, pane, "default (no --no-daemon)", []string{"--strict-config"}); ok {
		quit(false)
		if left := l.stragglers(); len(left) > 0 {
			var ps []string
			for _, p := range left {
				ps = append(ps, fmt.Sprintf("%s(%d)", p.name, p.pid))
			}
			out.note("codex: after /quit these processes still ran on the scratch CODEX_HOME (a daemon?): " + strings.Join(ps, ", "))
		}
	}

	// 3: H-L11 (H7): herdr's agent_session on a fresh --no-daemon start whose CODEX_HOME has the integration.
	hL11Start(l, k, pane, codexHome, src, turn)

	// 4: the repository's own server: CX-L3 reads the first turn's rollout.
	cxL4(l, pane, codexHome, turn)

	// 5: H-L14: a start with a first prompt that keeps codex working, then the same right after its exit.
	l.stopAgent(pane)
	hL14(l, "codex", pane, k.(agents.Split).Start(agents.Start{Kit: l.kit,
		Args: []string{"--strict-config", "--no-daemon", "-a", "never"}, Prompt: hL14Prompt}), turn)
}

// cxL4: codex starts as a worker of the lab's project with the repository's own `obor mcp` (not the stub) and
// one job_next call's rendered reply reaches the session's rollout. The start is the shipped launch's (launchOf), so a
// tool its own discovery cannot reach is a FAIL, not a pass the case's own config bought. A client
// that cannot list the server's tools, or cannot call one, leaves no such result.
func cxL4(l *lab, pane, codexHome string, turn bool) {
	where := at("internal/mcp/mcp.go", "func Serve") + ", F1.1, CAP §3.2, T-898"
	if !turn {
		report("SKIP", "CX-L4", "needs TURN=1 (a model call that uses the tool)", where)
		return
	}
	real, err := l.realKit()
	if err != nil {
		report("FAIL", "CX-L4", "the repository's own obor is not built: "+err.Error(), where)
		return
	}
	if err := l.startStubServe(); err != nil {
		report("FAIL", "CX-L4", err.Error(), where)
		return
	}
	k := kindOf("codex")
	model, ok := liveModel("codex")
	if !ok {
		report("FAIL", "CX-L4", "the config's built-in model table has no codex model", at("internal/config/models.go", "var BuiltinModels")+", T-910")
		return
	}
	start, err := l.launchOf("codex", model, real, nil)
	if err != nil {
		report("FAIL", "CX-L4", "the launch of "+model+" cannot be built: "+err.Error(), at("internal/config/models.go", "func (m Model) Launch")+", T-910")
		return
	}
	argv := k.(agents.Split).Start(agents.Start{Kit: real, Args: start.args})
	l.fresh()
	t0 := time.Now()
	s := l.start("conf-codex-mcp", "codex", pane, argv, 30*time.Second)
	reportStart("codex real mcp", s, l.readyAfter(s, t0, "idle", "blocked"))
	if l.atShell(pane) {
		report("FAIL", "CX-L4", "codex did not start for the real server: "+answer(s)+"; "+tail(l.screen(pane), 4), where)
		return
	}
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": mcpPrompt}, 15*time.Second); err != nil {
		report("FAIL", "CX-L4", "agent.prompt: "+err.Error(), where)
		l.stopAgent(pane)
		return
	}
	text, ok := waitTranscript(180*time.Second, func() string { return rollouts(codexHome) }, mcpJobHead, mcpMarker)
	st := k.Stop()
	if code, err := l.quit(pane, st); err != nil || code != 0 {
		l.stopAgent(pane)
	}
	fact := l.mcpFact("the real `obor mcp`'s job_next result reached a rollout under "+codexHome, text, ok)
	if ok {
		report("PASS", "CX-L4", fact, where)
	} else {
		report("FAIL", "CX-L4", fact, where)
	}
}

// rollouts is the text of every session rollout under the scratch CODEX_HOME: a turn's tool results are its lines.
func rollouts(dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	var b strings.Builder
	for _, f := range files {
		if c, err := os.ReadFile(f); err == nil {
			b.Write(c)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// hL11Start (H7): a fresh codex --no-daemon start in a CODEX_HOME holding herdr's Codex integration (the user's
// hooks.json, whose SessionStart it runs), and when agent_session reaches agent.get: what the start alone gives, and with
// TURN=1 how long after the first turn it arrives (codex dispatches SessionStart with the session's first turn, not at
// TUI startup). The scratch home has neither the user's `[features] hooks = true` nor its hook trust, so the start
// carries --enable hooks and --dangerously-bypass-hook-trust; obor's real launch adds neither (the user's home has
// both). hooks.json is removed after, so the lab's next start is its usual one.
func hL11Start(l *lab, k agents.Kind, pane, codexHome, src string, turn bool) {
	where := at("internal/launch/session.go", "func (l *Launcher) reportRollout") + ", H7, F4.3"
	hooks, err := os.ReadFile(filepath.Join(src, "hooks.json"))
	if err != nil {
		report("SKIP", "H-L11", "herdr's Codex integration is not installed ("+filepath.Join(src, "hooks.json")+"), so no agent_session can be reported", where)
		return
	}
	hooksPath := filepath.Join(codexHome, "hooks.json")
	if err := os.WriteFile(hooksPath, hooks, 0o644); err != nil {
		report("FAIL", "H-L11", err.Error(), where)
		return
	}
	defer func() { _ = os.Remove(hooksPath) }()
	argv := k.(agents.Split).Start(agents.Start{Kit: l.kit, Args: []string{"--strict-config", "--no-daemon", "--enable", "hooks",
		"--dangerously-bypass-hook-trust"}})
	t0 := time.Now()
	s := l.start("conf-codex-session", "codex", pane, argv, 30*time.Second)
	if l.readyAfter(s, t0, "idle", "blocked") == 0 {
		report("FAIL", "H-L11", "codex did not come up for the session probe: "+answer(s)+"; "+tail(l.screen(pane), 4), where)
		l.stopAgent(pane)
		return
	}
	before, _ := l.agent(pane)
	fact := fmt.Sprintf("a fresh codex --no-daemon start with herdr's Codex integration: agent.get reported agent_session %s when the start was ready (%.1fs after it began)",
		sessionFact(before.Session), time.Since(t0).Seconds())
	if turn {
		if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": "Reply with exactly OK"}, 15*time.Second); err != nil {
			report("FAIL", "H-L11", "the probe's first turn: agent.prompt: "+err.Error(), where)
			l.stopAgent(pane)
			return
		}
		prompted := time.Now()
		var a agentInfo
		filled := poll(60*time.Second, func() bool {
			var err error
			a, err = l.agent(pane)
			return err == nil && a.Session != nil
		})
		after := time.Since(prompted)
		switch {
		case !filled:
			fact += fmt.Sprintf("; still none %.1fs after the first prompt", after.Seconds())
		case before.Session != nil:
			fact += "; no change needed after the first turn"
		default:
			fact += fmt.Sprintf("; %s arrived %.1fs after the first prompt", sessionFact(a.Session), after.Seconds())
		}
	} else {
		fact += "; whether it arrives at the first turn needs TURN=1"
	}
	report("PASS", "H-L11", fact, where)
	st := k.Stop()
	if code, err := l.quit(pane, st); err != nil || code != 0 {
		l.stopAgent(pane)
	}
}

// codexUp starts codex in pane with the driver's argv for args (H-L1 per start): the screen once it is up;
// ok false when it did not come up, the screen then herdr's answer and the last lines.
func (l *lab) codexUp(k agents.Kind, pane, mode string, args []string) (string, bool) {
	argv := k.(agents.Split).Start(agents.Start{Kit: l.kit, Args: args})
	t0 := time.Now()
	s := l.start("conf-codex", "codex", pane, argv, 30*time.Second)
	ready := l.readyAfter(s, t0, "idle", "blocked")
	reportStart("codex "+mode, s, ready)
	if l.atShell(pane) {
		return answer(s) + "; " + tail(l.screen(pane), 4), false
	}
	return l.visible(pane), true
}

// codexStart is codexUp and its checks: CX-L2's update screen and CX-L1 for mode. The MCP record of the start; ok
// false when codex did not come up (reported).
func (l *lab) codexStart(k agents.Kind, pane, mode string, args []string) (record, bool) {
	where1 := at(codexGo, "func (kind) Start") + ", F1.1, P3, CAP §3.2"
	screen, ok := l.codexUp(k, pane, mode, args)
	if !ok {
		report("FAIL", "CX-L1", mode+": codex did not start: "+screen, where1)
		return record{}, false
	}
	where2 := at(codexGo, "var updateRE") + ", T-570"
	update := sourceRegexp(codexGo, "updateRE")
	switch {
	case update == nil:
		report("FAIL", "CX-L2", "codex.go has no updateRE to check", where2)
	case update.MatchString(screen) || strings.Contains(strings.ToLower(screen), "update available"):
		report("FAIL", "CX-L2", mode+": an update screen shows with check_for_update_on_startup=false: "+tail(screen, 4), where2)
	default:
		report("PASS", "CX-L2", mode+": no update screen with check_for_update_on_startup=false", where2)
	}
	mcp := l.waitRecords("mcp", "", 20*time.Second)
	if len(mcp) == 0 {
		report("FAIL", "CX-L1", mode+": no obor MCP server ran in 20s; screen: "+tail(screen, 4), where1)
		return record{}, true
	}
	cxL1(l, pane, mode, mcp[0], where1)
	return mcp[0], true
}

// cxL1: the MCP server sees only the allowlist and the forwarded variables, runs in the lane, as a child of the codex
// process in the pane.
func cxL1(l *lab, pane, mode string, r record, where string) {
	var bad []string
	for k, v := range map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane, agents.Pin: "1"} {
		if r.Env[k] != v {
			bad = append(bad, fmt.Sprintf("%s=%q (want %s)", k, r.Env[k], v))
		}
	}
	for _, k := range []string{"OBOR_CONF_PANE_ONLY", "HERDR_SOCKET_PATH", "HERDR_PANE_ID"} {
		if _, ok := r.Env[k]; ok {
			bad = append(bad, k+" leaked (not forwarded)")
		}
	}
	// PWD, SHLVL, _: set by bash, as the kit's bin/obor is a bash shim (the stub's wrapper here, bin/obor in a kit).
	allow := append(append(slices.Clone(codexAllow), agents.EnvVars...), agents.Pin)
	allow = append(allow, "PWD", "SHLVL", "_", "OLDPWD")
	var extra []string
	for _, k := range names(r.Env) {
		if !slices.Contains(allow, k) {
			extra = append(extra, k)
		}
	}
	if !same(r.Cwd, l.lane) {
		bad = append(bad, "cwd "+r.Cwd+", not the lane")
	}
	anc := ancestors(r.PID)
	shell, _ := l.fg(pane)
	inPane := false
	for _, p := range anc {
		inPane = inPane || p.pid == shell
	}
	parentIsCodex := len(anc) > 0 && r.PPID == anc[0].pid && strings.Contains(anc[0].name, "codex")
	if !parentIsCodex || !inPane {
		bad = append(bad, "its parents "+chain(anc, 4)+" are not the pane's codex (a daemon?)")
	}
	slices.Sort(bad)
	fact := fmt.Sprintf("%s: the MCP server's parents %s; env beyond the allowlist and forwarded vars: %s", mode, chain(anc, 3),
		orNone(strings.Join(extra, " ")))
	if len(bad) > 0 {
		report("FAIL", "CX-L1", fact+": "+strings.Join(bad, "; "), where)
		return
	}
	report("PASS", "CX-L1", fact+"; OBOR_ROLE, OBOR_LANE, OBOR_PANE forwarded, pane-only vars (HERDR_PANE_ID too) not, cwd = lane", where)
}

// cxTurn (TURN=1): one prompt typed by herdr; a rollout appears and the codex process holds it open (reportRollout).
func cxTurn(l *lab, pane, codexHome string, mcp record) {
	where := at("internal/launch/session.go", "func") + ", F4.5"
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": "Reply with exactly OK"}, 15*time.Second); err != nil {
		report("FAIL", "CX-L3", "agent.prompt: "+err.Error(), where)
		return
	}
	var rollout string
	poll(120*time.Second, func() bool {
		files, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*.jsonl"))
		if len(files) > 0 {
			rollout = files[0]
		}
		return rollout != ""
	})
	if rollout == "" {
		report("FAIL", "CX-L3", "no rollout under the scratch CODEX_HOME 120s after the prompt", where)
		return
	}
	poll(120*time.Second, func() bool { a, err := l.agent(pane); return err == nil && a.Status == "idle" })
	pid := mcp.PPID
	if _, fg := l.fg(pane); pid == 0 {
		for _, p := range fg {
			if strings.Contains(p.name, "codex") {
				pid = p.pid
			}
		}
	}
	o, _ := output(10*time.Second, "lsof", "-p", fmt.Sprint(pid), "-Fn")
	if !strings.Contains(o, rollout) {
		report("FAIL", "CX-L3", fmt.Sprintf("the codex process %d does not hold %s open (lsof)", pid, filepath.Base(rollout)), where)
		return
	}
	report("PASS", "CX-L3", fmt.Sprintf("after the first turn rollout %s exists and codex (pid %d) holds it open", filepath.Base(rollout), pid), where)
}
