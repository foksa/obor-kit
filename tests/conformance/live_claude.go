//go:build conformance

package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
)

// sourceRegexp is the regexp a driver file compiles as `var <name> = regexp.MustCompile(` + "`…`" + `)`: the check
// uses the driver's own pattern, not a copy. nil when the file no longer has it.
func sourceRegexp(file, name string) *regexp.Regexp {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	m := regexp.MustCompile(`var ` + name + " = regexp.MustCompile\\(`([^`]*)`\\)").FindSubmatch(b)
	if m == nil {
		return nil
	}
	re, err := regexp.Compile(string(m[1]))
	if err != nil {
		return nil
	}
	return re
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// payload is a hook's stdin, the fields the checks read.
type payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Source         string `json:"source"`
	LastMessage    string `json:"last_assistant_message"`
}

func (r record) payload() payload {
	var p payload
	_ = json.Unmarshal([]byte(r.Stdin), &p)
	return p
}

// hookCmd is a Claude or Codex hook entry that runs the stub as hook name.
func (l *lab) hookCmd(name string) map[string]any {
	return map[string]any{"type": "command", "command": shq(filepath.Join(l.kit, "bin", "obor")) + " hook " + name, "timeout": 10}
}

// claudeLive: one strict start in the lane through a symlinked path (CC-L1, CC-L2, CC-L3, CC-L5, H-L1, H-L2 at the
// trust dialog), with TURN=1 one turn through the inbox (CC-L4, CC-L6) and a resume (CC-L1); then a start without the
// strict flag as the control for the hidden manifest server.
func claudeLive(l *lab, turn bool) {
	k := kindOf("claude")
	pane, err := l.workspace("conf", l.root)
	if err != nil {
		report("FAIL", "CC-L1", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	link := filepath.Join(l.root, "lane-link") // CC-L2: the session starts in a symlinked dir
	if err := os.Symlink(l.lane, link); err != nil {
		report("FAIL", "CC-L1", "symlink: "+err.Error(), "deep dive §6")
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", "OBOR_CONF_PANE_ONLY": "1"}
	if st, err := l.shell(pane, "cd "+shq(link)+" && "+exports(paneEnv), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "CC-L1", fmt.Sprintf("the env line failed (status %d, %v)", st, err), at(launchGo, "func envLine"))
		return
	}
	plugin, err := l.manifestPlugin()
	if err != nil {
		report("FAIL", "CC-L1", "the --plugin-dir plugin: "+err.Error(), "deep dive §6")
		return
	}
	sid := uuid()
	settings := map[string]any{
		"env": map[string]string{"OBOR_CONF_X": "1"},
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{l.hookCmd("SessionStart")}}},
			"Stop":         []any{map[string]any{"hooks": []any{l.hookCmd("Stop")}}},
		},
	}
	base := []string{"--model", "haiku", "--permission-mode", "auto", "--plugin-dir", plugin}
	argv := k.Start(agents.Start{Kit: l.kit, Args: base, Session: sid, Settings: settings, StrictMCP: true})
	where1 := at(claudeGo, "func (kind) Start") + ", " + at(claudeGo, "func MCPConfig") + ", F1.1, CAP §2.2"

	t0 := time.Now()
	s := l.start("conf-claude", "claude", pane, argv, 30*time.Second)
	trustShown := trustDialog(l, pane, s)
	ready := l.readyAfter(s, t0, "idle")
	reportStart("claude (trust dialog on screen)", s, 0)
	if !trustShown && ready == 0 {
		report("FAIL", "CC-L1", "claude did not come up: "+tail(l.visible(pane), 6), where1)
		l.stopAgent(pane)
		return
	}

	mcp := l.waitRecords("mcp", "", 20*time.Second)
	hooks := l.waitRecords("hook", "SessionStart", 20*time.Second)
	hidden := len(l.records("mcp", "manifest")) == 0
	ccL1(l, pane, mcp, where1)
	ccL2(sid, hooks)
	ccL3(mcp, hooks)

	if turn {
		ccTurn(l, k, hooks)
	} else {
		report("SKIP", "CC-L4", "needs TURN=1 (a model call)", at(claudeGo, "func (kind) Deliver")+", F4.2")
		report("SKIP", "CC-L6", "needs TURN=1 (a model call)", "internal/guard/stop.go")
	}

	st := k.Stop()
	where5 := at(claudeGo, "func (kind) Stop") + ", CAP §2.6"
	if code, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil || code != 0 {
		report("FAIL", "CC-L5", fmt.Sprintf("%s + the recipe: exit status %d (%v)", st.Quit, code, orOK(err)), where5)
		l.stopAgent(pane)
	} else {
		report("PASS", "CC-L5", st.Quit+" (a second Enter after a poll) exits with status 0", where5)
	}
	report("SKIP", "CC-L5", "the exit dialog (Key "+st.Dialog.Key.String()+") needs a background task, which needs model turns", at(claudeGo, "var exitDialog"))

	ccResume(l, k, pane, base, settings, sid, turn)

	// The control: without --strict-mcp-config the --plugin-dir manifest's server starts; with it (above) it did not.
	l.fresh()
	argv = k.Start(agents.Start{Kit: l.kit, Args: base, Settings: settings})
	t0 = time.Now()
	s = l.start("conf-claude2", "claude", pane, argv, 30*time.Second)
	ready = l.readyAfter(s, t0, "idle")
	reportStart("claude", s, ready)
	shown := len(l.waitRecords("mcp", "manifest", 20*time.Second)) > 0
	where := at(claudeGo, "const StrictFlag") + ", T-445, T-770"
	switch {
	case !shown:
		report("FAIL", "CC-L1", "the control start without "+"--strict-mcp-config ran no --plugin-dir manifest server, so the hidden one proves nothing", where)
	case !hidden:
		report("FAIL", "CC-L1", "--strict-mcp-config did not hide the plugin manifest's server", where)
	default:
		report("PASS", "CC-L1", "--strict-mcp-config hides the plugin manifest's server (a start without it runs it)", where)
	}
	if code, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil || code != 0 {
		l.stopAgent(pane)
	}
	out.note("claude: ~/.claude.json keeps a trust entry for " + link + " (" + l.lane + "); Claude's trust dialog writes it (known leftover, deep dive §6)")
}

// manifestPlugin is a plugin dir for --plugin-dir whose manifest declares a second MCP server: the stub as
// `mcp manifest`.
func (l *lab) manifestPlugin() (string, error) {
	dir := filepath.Join(l.root, "plugin")
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(map[string]any{
		"name": "obor-conformance", "version": "0.0.1", "description": "obor conformance: a manifest MCP server",
		"mcpServers": map[string]any{"conf-manifest": map[string]any{
			"command": filepath.Join(l.kit, "bin", "obor"), "args": []string{"mcp", "manifest"}}},
	}, "", "  ")
	return dir, os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), b, 0o644)
}

// trustDialog: CC-L5's trust dialog against the driver's trustRE, and H-L2 there (agent.prompt refused, nothing typed;
// pane.send_input typed, which answers the dialog). Whether a trust dialog showed.
func trustDialog(l *lab, pane string, s started) bool {
	whereT := at("internal/launch/start.go", "var trustRE") + ", CAP §2.1"
	whereB := at(launchGo, "l.H.Wake(") + ", " + at(socketGo, "func (s *Socket) Wake") + ", F3.3"
	trust := sourceRegexp("internal/launch/start.go", "trustRE")
	if trust == nil {
		report("FAIL", "CC-L5", "internal/launch/start.go has no trustRE to check", whereT)
		return false
	}
	var screen string
	shown := poll(10*time.Second, func() bool {
		screen = l.visible(pane)
		return strings.Contains(strings.ToLower(screen), "trust")
	})
	if !shown {
		report("SKIP", "CC-L5", "no trust dialog showed (the scratch dir is trusted already?): "+tail(screen, 3), whereT)
		report("SKIP", "H-L2", "no known dialog to prompt into", whereB)
		return false
	}
	if !trust.MatchString(screen) {
		report("FAIL", "CC-L5", "the trust dialog no longer matches trustRE "+trust.String()+": "+tail(screen, 8), whereT)
	} else {
		report("PASS", "CC-L5", "the trust dialog matches trustRE (agent.start answered "+orOK(s.err)+")", whereT)
	}
	// The wake targets the pane (launch.go); while herdr's launch is pending the pane has no named agent yet, so the
	// agent's name is tried too.
	poll(15*time.Second, func() bool {
		a, err := l.agent(s.name)
		return err == nil && !a.LaunchPending && a.Status == "blocked"
	})
	before, _ := l.agent(pane)
	var answers []string
	refused := true
	for _, target := range []string{pane, s.name} {
		_, perr := l.call("agent.prompt", map[string]any{"target": target, "text": "obor-conformance"}, 10*time.Second)
		answers = append(answers, target+": "+orOK(perr))
		refused = refused && perr != nil
	}
	time.Sleep(time.Second)
	after := l.visible(pane)
	untouched := trust.MatchString(after) && !l.atShell(pane)
	_, ierr := l.call("pane.send_input", map[string]any{"pane_id": pane, "keys": []string{"down", "enter"}}, 10*time.Second)
	answered := poll(10*time.Second, func() bool { return !trust.MatchString(l.visible(pane)) })
	fact := fmt.Sprintf("Claude's trust dialog (the pane's agent: status %s, launch_pending %t): agent.prompt answered %s, the dialog %s; pane.send_input (down, enter) answered %v and %s",
		orNone(before.Status), before.LaunchPending, strings.Join(answers, "; "), map[bool]string{true: "untouched", false: "changed"}[untouched], orOK(ierr),
		map[bool]string{true: "answered the dialog", false: "did not answer it"}[answered])
	switch {
	case !refused || !untouched:
		report("FAIL", "H-L2", fact+": agent.prompt does not refuse a blocked agent", whereB)
	case ierr != nil || !answered:
		report("FAIL", "H-L2", fact, whereB)
	default:
		report("PASS", "H-L2", fact+": a wake through agent.prompt cannot type into a dialog", whereB)
	}
	return true
}

// ccL1: the strict start's obor server sees the identity env and runs in the lane.
func ccL1(l *lab, pane string, mcp []record, where string) {
	if len(mcp) == 0 {
		report("FAIL", "CC-L1", "the strict start ran no obor MCP server (the kit's bin/obor mcp) in 20s", where)
		return
	}
	r := mcp[0]
	var bad []string
	for k, v := range map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", "HERDR_PANE_ID": pane, agents.Pin: "1"} {
		if r.Env[k] != v {
			bad = append(bad, fmt.Sprintf("%s=%q (want %s)", k, r.Env[k], v))
		}
	}
	if !same(r.Cwd, l.lane) {
		bad = append(bad, "cwd "+r.Cwd+", not the lane")
	}
	slices.Sort(bad)
	if len(bad) > 0 {
		report("FAIL", "CC-L1", "the obor server: "+strings.Join(bad, ", "), where)
		return
	}
	report("PASS", "CC-L1", fmt.Sprintf("strict start: the obor server sees OBOR_ROLE, OBOR_LANE, HERDR_PANE_ID, %s and runs in the lane (cwd %s)", agents.Pin, r.Cwd), where)
}

// ccL2: SessionStart reports the id --session-id gave (obor's choice, T-813) and a transcript_path of its own — the
// realpath Claude resolved, under its config dir — and that path is the file of that id: obor records it and derives
// none.
func ccL2(sid string, hooks []record) {
	where := "internal/guard/session.go, " + at(claudeGo, "func (kind) Start") + ", F4.3, F4.4"
	if len(hooks) == 0 {
		report("FAIL", "CC-L2", "no SessionStart hook ran in 20s (--settings hooks)", where)
		return
	}
	r := hooks[0]
	p := r.payload()
	fi, statErr := os.Stat(p.TranscriptPath)
	fact := fmt.Sprintf("SessionStart: session_id %s, source %s, cwd %s, transcript_path %s", p.SessionID, p.Source, p.Cwd, p.TranscriptPath)
	switch {
	case p.SessionID != sid:
		report("FAIL", "CC-L2", fact+": not the --session-id given ("+sid+")", where)
	case p.TranscriptPath == "":
		report("FAIL", "CC-L2", fact+": no transcript_path: obor would have none to record", where)
	case filepath.Base(p.TranscriptPath) != sid+".jsonl":
		report("FAIL", "CC-L2", fact+": not the file of that id: the path obor records must be the session's own", where)
	case !filepath.IsAbs(p.TranscriptPath):
		report("FAIL", "CC-L2", fact+": not an absolute path", where)
	case statErr != nil || !fi.Mode().IsRegular():
		report("FAIL", "CC-L2", fact+": not an existing file: a restart could not resume it", where)
	default:
		report("PASS", "CC-L2", fact+": the id given and its own path, the one obor records", where)
	}
}

// ccL3: --settings env reaches the hooks and the MCP server.
func ccL3(mcp, hooks []record) {
	where := at(claudeGo, `"--settings"`) + ", P8, CAP §2.3"
	if len(mcp) == 0 || len(hooks) == 0 {
		report("FAIL", "CC-L3", fmt.Sprintf("no records to read (%d MCP, %d SessionStart)", len(mcp), len(hooks)), where)
		return
	}
	h, m := hooks[0].Env["OBOR_CONF_X"], mcp[0].Env["OBOR_CONF_X"]
	if h != "1" || m != "1" {
		report("FAIL", "CC-L3", fmt.Sprintf(`--settings {"env":{"OBOR_CONF_X":"1"}}: the hook sees %q, the MCP server %q`, h, m), where)
		return
	}
	report("PASS", "CC-L3", `--settings {"env":{"OBOR_CONF_X":"1"}} reaches the SessionStart hook and the MCP server`, where)
}

// ccTurn (TURN=1): one "Reply with exactly OK" posted to the session's inbox in the driver's envelope (Deliver):
// it starts a turn (CC-L4) and the Stop hook carries last_assistant_message (CC-L6).
func ccTurn(l *lab, k agents.Kind, hooks []record) {
	where4 := at(claudeGo, "func (kind) Deliver") + ", F4.2"
	where6 := "internal/guard/stop.go, CAP §2.3"
	if len(hooks) == 0 {
		report("FAIL", "CC-L4", "no SessionStart record to read the inbox from", where4)
		report("SKIP", "CC-L6", "no turn", where6)
		return
	}
	rec := k.InboxRecord(func(n string) string { return hooks[0].Env[n] })
	d, ok := k.Deliver("Reply with exactly OK", rec)
	if !ok {
		report("FAIL", "CC-L4", "the SessionStart hook env names no messaging socket (CLAUDE_CODE_MESSAGING_SOCKET)", where4)
		report("SKIP", "CC-L6", "no turn", where6)
		return
	}
	if err := post(d.Socket, d.Lines); err != nil {
		report("FAIL", "CC-L4", "posting to the inbox: "+err.Error(), where4)
		report("SKIP", "CC-L6", "no turn", where6)
		return
	}
	stops := l.waitRecords("hook", "Stop", 120*time.Second)
	if len(stops) == 0 {
		report("FAIL", "CC-L4", "the envelope (auth + user line) started no turn in 120s under --permission-mode auto", where4)
		report("SKIP", "CC-L6", "no turn", where6)
		return
	}
	report("PASS", "CC-L4", "the envelope (auth + user line) started a turn under --permission-mode auto (the plain-text line and crossSessionInbound are not sent: one model call per kind)", where4)
	p := stops[0].payload()
	if strings.TrimSpace(p.LastMessage) == "" {
		report("FAIL", "CC-L6", "the Stop payload has no last_assistant_message", where6)
		return
	}
	report("PASS", "CC-L6", fmt.Sprintf("the Stop payload carries last_assistant_message %q (StopFailure stays docs-only)", p.LastMessage), where6)
}

// post writes lines to the unix socket path.
func post(path string, lines []string) error {
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	for _, line := range lines {
		if _, err := c.Write([]byte(line + "\n")); err != nil {
			return err
		}
	}
	time.Sleep(500 * time.Millisecond) // let the receiver read before the close
	return nil
}

// ccResume: a resume's argv keeps --settings and obor's --mcp-config; with TURN=1 (a session has no transcript before
// its first turn) the resume runs and its SessionStart (source resume) and MCP server are seen.
func ccResume(l *lab, k agents.Kind, pane string, base []string, settings map[string]any, sid string, turn bool) {
	where := at(claudeGo, "func (kind) Start") + ", F1.1"
	argv := k.Start(agents.Start{Kit: l.kit, Args: base, Settings: settings, StrictMCP: true, Resume: sid})
	if !slices.Contains(argv, "--settings") || !slices.Contains(argv, "--mcp-config") || !slices.Contains(argv, "--resume") {
		report("FAIL", "CC-L1", "the resume argv drops --settings or --mcp-config: "+strings.Join(argv, " "), where)
		return
	}
	if !turn {
		report("PASS", "CC-L1", "the resume argv keeps --settings and --mcp-config (run live with TURN=1: no transcript to resume before a turn)", where)
		return
	}
	l.fresh()
	s := l.start("conf-claude-resume", "claude", pane, argv, 30*time.Second)
	hooks := l.waitRecords("hook", "SessionStart", 30*time.Second)
	mcp := l.waitRecords("mcp", "", 20*time.Second)
	st := k.Stop()
	defer func() {
		if _, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil {
			l.stopAgent(pane)
		}
	}()
	switch {
	case len(hooks) == 0:
		report("FAIL", "CC-L1", "the resume ran no SessionStart hook (start answered "+orOK(s.err)+")", where)
	case hooks[0].payload().Source != "resume" || hooks[0].payload().SessionID != sid:
		report("FAIL", "CC-L1", fmt.Sprintf("the resume's SessionStart: source %s, session %s (want resume, %s)",
			hooks[0].payload().Source, hooks[0].payload().SessionID, sid), where)
	case len(mcp) == 0 || mcp[0].Env["OBOR_CONF_X"] != "1":
		report("FAIL", "CC-L1", "the resume ran no obor MCP server with the settings env", where)
	default:
		report("PASS", "CC-L1", "--resume keeps --settings (SessionStart source resume, its env) and --mcp-config (the obor server runs)", where)
	}
}
