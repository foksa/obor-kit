//go:build conformance

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
	"obor/kinds/agy/kind"
)

const (
	agyGo        = "kinds/agy/kind/kind.go"    // the agy kind's source
	agyHookGo    = "kinds/agy/kind/hook.go"    // its hook transport
	agyDeliverGo = "kinds/agy/kind/deliver.go" // its inbox: deliver through agentapi
)

// agyModel is the model id the agy rows start (CONF_AGY_MODEL; default the probes' cheap model).
func agyModel() string {
	if m := os.Getenv("CONF_AGY_MODEL"); m != "" {
		return m
	}
	return "gemini-3.8-flash-low"
}

// agyStatic: `agy --help` still lists every flag and subcommand the kind's start and headless forms use.
func agyStatic() {
	where := at(agyGo, "func start(") + ", " + at(agyGo, `"headless"`)
	path, _, ok := tool("agy", map[string]string{"AG-S1": where})
	if !ok {
		return
	}
	help, err := output(30*time.Second, path, "--help")
	if err != nil {
		report("FAIL", "AG-S1", "agy --help failed: "+firstLine(help+err.Error()), where)
		return
	}
	var gone []string
	for _, f := range []string{"--settings", "--model", "--effort", "--conversation", "--prompt-interactive", "--print",
		"--prompt ", "--add-dir", "acp"} {
		if !strings.Contains(help, f) {
			gone = append(gone, strings.TrimSpace(f))
		}
	}
	if len(gone) > 0 {
		report("FAIL", "AG-S1", "agy --help no longer lists "+strings.Join(gone, ", "), where)
		return
	}
	report("PASS", "AG-S1", "agy --help lists --settings, --model, --effort, --conversation, --prompt-interactive, --print, --prompt, --add-dir and acp", where)
}

// agyLive: a worker of the lab's project started as obor's launch builds it (config.Model.Launch: the agy kind obor
// links into itself, through obor's plugin package), in the lane, with the project's .agents/mcp_config.json and .agents/hooks.json
// as obor setup agents writes them and the repository's own obor (and its agy kind, the hook transport) first on PATH
// (the files name obor by PATH), a stub serve on the project's socket. AG-L11..AG-L13: the calls agy's tool set can make
// that the guard must refuse, each run through the project's hooks entry's command (a deny stops the tool and agy shows
// the model its reason). AG-L1 the start (no trust dialog, no wrong-model notice, idle), AG-L7 the lab's bogus model
// refused by the launcher while that agent runs, AG-L4 the quit. With TURN=1: AG-L2 the first prompt in the argv,
// AG-L8 the session record (the SessionStart hook reports the new conversation to serve), AG-L3 an obor MCP tool call
// with no prompt (job_next's reply in the transcript), AG-L6 the context read of that transcript, AG-L9 a command
// obor's guard denies (a worker's git commit in its lane: the PreToolUse hook's deny, final), AG-L10 the inbox (the
// agent's own obor command records it; the kind's deliver through agentapi starts a turn in the idle session), AG-L14
// P13's enumeration against the transport's table (one WARN per tool it has no row for), AG-L5 the
// resume by the recorded conversation id, and P10 (agy-probes.md): with command(obor), command(go test) and
// read_url(proxy.golang.org) allowed, whether `obor status`, a test that opens a unix socket and a module download run
// in auto mode with no prompt.
//
// agy trusts a folder by exact path and the kind adds a lane only when the project's root is in the user's saved
// trustedWorkspaces, which the lab must not write: so the lab adds its scratch dirs to each start's settings file
// (trustLab), standing in for the owner's trust of the project.
func agyLive(l *lab, turn bool) {
	k := l.kindOf("agy")
	whereStart, whereMCP := at(agyGo, "func start("), at(agyGo, `case "install"`)
	real, err := l.realKit()
	if err != nil {
		report("FAIL", "AG-L1", "the repository's own obor is not built: "+err.Error(), whereMCP)
		return
	}
	if err := l.startStubServe(); err != nil {
		report("FAIL", "AG-L1", err.Error(), whereMCP)
		return
	}
	inst, err := agents.InstallOf(k, real, l.lane, nil)
	if err != nil || inst.MCP.Where != agents.Project {
		report("FAIL", "AG-L3", fmt.Sprintf("install: %+v, %v; want where project", inst.MCP, err), whereMCP)
		return
	}
	if err := writeEntry(inst.MCP); err != nil {
		report("FAIL", "AG-L3", "the project file: "+err.Error(), whereMCP)
		return
	}
	whereHooks := at(agyHookGo, "func Hook(")
	if inst.Hooks.Where != agents.Project {
		report("FAIL", "AG-L9", fmt.Sprintf("install: hooks %+v; want where project", inst.Hooks), whereHooks)
		return
	}
	if err := writeEntry(inst.Hooks); err != nil {
		report("FAIL", "AG-L9", "the hooks file: "+err.Error(), whereHooks)
		return
	}
	pane, err := l.workspace("conf", l.lane)
	if err != nil {
		report("FAIL", "AG-L1", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane,
		"PATH": filepath.Join(real, "bin") + string(os.PathListSeparator) + l.envOf("PATH")}
	if st, err := l.shell(pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "AG-L1", fmt.Sprintf("the env line failed (status %d, %v)", st, err), at(launchGo, "func envLine"))
		return
	}
	l.agyDenies(pane)
	stop := k.Stop()
	whereStop := at(agyGo, `"stop"`)
	if !turn {
		if s, ok := l.agyStart(k, pane, agents.Start{}, "no prompt"); ok {
			l.agyWrongModel(s, pane)
			agyQuit(l, pane, stop, whereStop)
		} else {
			report("SKIP", "AG-L7", "no agent runs in the pane (AG-L1's start did not come up): nothing to restart with the lab's bogus model", whereStart)
		}
		for _, id := range []string{"AG-L2", "AG-L3", "AG-L5", "AG-L6", "AG-L8", "AG-L9", "AG-L10", "AG-L14", "P10", "P10-control"} {
			report("SKIP", id, "needs TURN=1 (a model call)", whereStart)
		}
		return
	}
	const first = "AGY-FIRST-OK"
	s, ok := l.agyStart(k, pane, agents.Start{Prompt: "Reply with exactly " + first}, "first prompt")
	if !ok {
		report("SKIP", "AG-L7", "no agent runs in the pane (AG-L1's start did not come up): nothing to restart with the lab's bogus model", whereStart)
		return
	}
	l.agyWrongModel(s, pane)
	sid, text := l.agyConversation(k, s, first)
	if sid == "" {
		report("FAIL", "AG-L2", "the first prompt's reply is in no conversation's transcript in 120s; screen: "+tail(l.screen(pane), 4), whereStart)
		l.stopAgent(pane)
		return
	}
	report("PASS", "AG-L2", "-i <prompt>: the reply "+first+" is in conversation "+sid+"'s transcript.jsonl", whereStart)
	poll(30*time.Second, func() bool { return slices.Contains(l.app.recorded(), sid) })
	if rec, ok := l.app.session(sid); ok && rec.Kind == "agy" && rec.Agent != "" {
		report("PASS", "AG-L8", fmt.Sprintf("the SessionStart hook (obor kind agy hook → obor hook session-start) reported conversation %s of %s, kind %s, to serve",
			sid, rec.Agent, rec.Kind), whereHooks)
	} else if ok {
		report("FAIL", "AG-L8", fmt.Sprintf("the session request for %s: %+v; want kind agy and an agent", sid, rec), whereHooks)
	} else {
		report("FAIL", "AG-L8", fmt.Sprintf("no session request for %s; serve recorded %q, was asked %q", sid, l.app.recorded(), l.app.asked()), whereHooks)
	}

	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": mcpPrompt}, 15*time.Second); err != nil {
		report("FAIL", "AG-L3", "agent.prompt: "+err.Error(), whereMCP)
	} else {
		asked := ""
		text, ok = waitTranscript(180*time.Second, func() string {
			if m := agyAskRE.FindString(l.screen(pane)); m != "" && asked == "" {
				asked = m
			}
			return l.agyTranscript(k, sid)
		}, mcpJobHead, mcpMarker)
		fact := l.mcpFact("the real `obor mcp` (mcpServers.obor of .agents/mcp_config.json, obor on PATH) answered job_next into agy's transcript", text, ok)
		switch {
		case asked != "":
			report("FAIL", "AG-L3", fact+"; agy asked: "+asked+" (mcp(obor/*) in the start's settings should allow it)", whereMCP)
		case ok:
			report("PASS", "AG-L3", fact+"; no permission prompt", whereMCP)
		default:
			report("FAIL", "AG-L3", fact+"; screen: "+tail(l.screen(pane), 4), whereMCP)
		}
	}
	c, cok := k.Context([]byte(text))
	if whereCtx := at(agyGo, "func Context("); cok && c.Tokens > 0 {
		report("PASS", "AG-L6", fmt.Sprintf("turn what: context reads %d tokens at %s from the transcript", c.Tokens, c.At.Format(time.RFC3339)), whereCtx)
	} else {
		report("FAIL", "AG-L6", fmt.Sprintf("turn what: context read %+v, %v from the transcript: %s", c, cok, tail(text, 2)), whereCtx)
	}
	poll(60*time.Second, func() bool { a, err := l.agent(s.name); return err == nil && a.Status == "idle" })
	l.agyToolDrift(k, pane, sid)
	poll(60*time.Second, func() bool { a, err := l.agent(s.name); return err == nil && a.Status == "idle" })
	l.agyPush(k, pane, sid, s.name)
	poll(60*time.Second, func() bool { a, err := l.agent(s.name); return err == nil && a.Status == "idle" })
	l.agyDenied(k, pane, sid, whereHooks)
	poll(60*time.Second, func() bool { a, err := l.agent(s.name); return err == nil && a.Status == "idle" })
	agyQuit(l, pane, stop, whereStop)

	const resumed = "AGY-RESUMED-OK"
	if l.agyRelaunch(sid, pane, whereStart) {
		if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": "Reply with exactly " + resumed}, 15*time.Second); err != nil {
			report("FAIL", "AG-L5", "agent.prompt: "+err.Error(), whereStart)
		} else if text, ok := waitTranscript(120*time.Second, func() string { return l.agyTranscript(k, sid) }, first, resumed); ok {
			report("PASS", "AG-L5", "obor's launcher (obor restart, no --fresh, the agent exited) resumed "+sid+
				" from the SessionStart hook's record: it answered "+resumed+" into the same transcript", whereStart)
		} else {
			report("FAIL", "AG-L5", "--conversation "+sid+": no "+resumed+" in its transcript in 120s: "+tail(text, 2)+"; screen: "+tail(l.screen(pane), 4), whereStart)
		}
		poll(60*time.Second, func() bool { a, err := l.agent(pane); return err == nil && a.Status == "idle" })
		if _, err := l.quit(pane, stop); err != nil {
			l.stopAgent(pane)
		}
	}
	agyP10(l, k, pane, stop, true)
	agyP10(l, k, pane, stop, false)
}

// agyRelaunch restarts the agent of conversation sid in pane as obor restart does after its exit (launch.Launcher.Launch,
// no --fresh): the session it resumes is obor's own record, the one the SessionStart hook reported to serve (the stub
// serve's), never an id handed to the kind. The lab's dirs are trusted through the project's override file (the
// launcher writes the start's settings file itself). False (AG-L5 reported) when the record is missing, the launcher
// fails, or it started another session than sid.
func (l *lab) agyRelaunch(sid, pane, where string) bool {
	rec, ok := l.app.session(sid)
	if !ok {
		report("FAIL", "AG-L5", "no SessionStart record of "+sid+" to resume from", where)
		return false
	}
	over := filepath.Join(l.main, ".obor", "agy", "settings_override.json")
	trust, _ := json.Marshal(map[string]any{"trustedWorkspaces": []string{l.main, l.lane}})
	if err := os.MkdirAll(filepath.Dir(over), 0o755); err == nil {
		err = os.WriteFile(over, trust, 0o644)
	}
	defer os.Remove(over)
	var out bytes.Buffer
	ln := l.launcher(&out, func(agent string) (store.Session, bool) { return rec, agent == rec.Agent })
	_, err := ln.Launch(l.ctx, launch.Request{Agent: rec.Agent, Pane: pane})
	said := out.String()
	switch {
	case err != nil:
		report("FAIL", "AG-L5", "obor's launcher: "+err.Error()+"; said: "+tail(said, 4), where)
		return false
	case !strings.Contains(said, "resuming "+sid):
		report("FAIL", "AG-L5", "obor's launcher did not resume "+sid+": "+tail(said, 4), where)
		l.stopAgent(pane)
		return false
	}
	return true
}

// launcher is obor's one start path in the lab's project: its config and the repository's kit, the throwaway session's
// terminal, its stub serve's where answer, and out for its progress lines. sessions is its recorded-session lookup.
func (l *lab) launcher(out io.Writer, sessions func(string) (store.Session, bool)) *launch.Launcher {
	env := func(key string) (string, bool) { v := l.envOf(key); return v, v != "" }
	return &launch.Launcher{Cfg: l.config(), Env: env, Kit: l.real, H: &herdr.Socket{Path: l.sock, Timeout: 10 * time.Second},
		Out: out, Poll: 200 * time.Millisecond, Up: 60 * time.Second, Background: func(func()) {},
		Install:  func() agents.InstallHost { return agents.InstallHost{} },
		Sessions: sessions,
		Report:   func(context.Context, store.Session) error { return nil }, Started: noStarted,
		Where: func(context.Context, wire.WhereReq) (wire.WhereReply, error) {
			return wire.WhereReply{State: wire.WhereNone, Why: "the lab's stub serve"}, nil
		}}
}

// agyDenied is AG-L9: the agent runs a commit in its lane as a run_command; obor's guard denies it through the
// PreToolUse hook (agy's error names the pre-tool hook and obor's line) and the lane's branch has no such commit.
func (l *lab) agyDenied(k agents.Kind, pane, sid, where string) {
	const msg, done = "conf-agy-denied", "AGY-DENY-DONE"
	cmd := "git commit --allow-empty -m " + msg
	// tagged as obor's prompts are: the worker's wrong-tab guard (user-prompt-submit, now live) asks back on any other
	prompt := "[obor conf] Run exactly this shell command as one run_command call, once, and do not retry or change it: " + cmd +
		". Then reply " + done + "."
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": prompt}, 15*time.Second); err != nil {
		report("FAIL", "AG-L9", "agent.prompt: "+err.Error(), where)
		return
	}
	// the reply, not the prompt, which names the marker too
	text, ok := waitTranscript(180*time.Second, func() string { return l.agyTranscript(k, sid) }, `"content":"`+done)
	denied := agyDenyRE.FindString(text)
	log, _ := output(10*time.Second, "git", "-C", l.lane, "log", "--format=%s", "-5")
	switch {
	case strings.Contains(log, msg):
		report("FAIL", "AG-L9", cmd+" committed in the lane: the guard did not stop it", where)
	case denied != "":
		report("PASS", "AG-L9", cmd+": "+strings.TrimSpace(strings.ReplaceAll(denied, `\n`, " "))+"; no such commit in the lane", where)
	case !ok:
		report("FAIL", "AG-L9", "no "+done+" in the transcript in 180s: "+tail(text, 3)+"; screen: "+tail(l.screen(pane), 4), where)
	default:
		report("FAIL", "AG-L9", cmd+": no pre-tool hook denial in the transcript: "+tail(text, 3), where)
	}
}

// agyDenyRE is agy's error for a call a PreToolUse hook denied, with obor's line (agy-probes.md P3).
var agyDenyRE = regexp.MustCompile(`tool call denied by pre-tool hook: obor:[^"]*`)

// agyDenies are AG-L11..AG-L13: the calls agy's tool set can make that the guard must refuse — a tool the transport has
// no row for, run_workflow, and an MCP call to a server the project does not declare. Each is run through the project's
// hooks entry's command (kind.HookCommand, the real obor and its guard) in the pane's lane env: a deny stops the tool
// and agy shows the model its reason (P13). A model call cannot make the first (agy offers no such tool).
func (l *lab) agyDenies(pane string) {
	where := at(agyHookGo, "func Hook(") + ", " + at(agyHookGo, "var tools") + ", " +
		at("internal/guard/guard.go", "func (g *guard) unguarded")
	const server = "conf-undeclared"
	if declaresServer(filepath.Join(l.main, agents.ProjectMCPFile), server) {
		report("FAIL", "AG-L13", "precondition: the project's "+agents.ProjectMCPFile+" declares "+server+", the server this row calls", where)
		return
	}
	event := func(tool string, args map[string]any) string {
		b, _ := json.Marshal(map[string]any{"hookEventName": "PreToolUse", "conversationId": "conf-deny", "cwd": l.lane,
			"toolCall": map[string]any{"name": tool, "args": args}})
		return string(b)
	}
	cases := []struct{ id, name, event, want string }{
		{"AG-L11", "an agy tool the transport has no row for (conf_unknown_tool)", event("conf_unknown_tool", map[string]any{}),
			"obor has no rule for agy tool conf_unknown_tool: update obor"},
		{"AG-L12", "run_workflow (a script no hook sees)", event("run_workflow", map[string]any{"Script": "print(1)"}),
			"obor cannot guard a workflow script; run commands with run_command"},
		{"AG-L13", "an MCP call to a server the project does not declare (" + server + ")",
			event("call_mcp_tool", map[string]any{"ServerName": server, "ToolName": "echo"}),
			"obor lanes use the project's MCP servers only"},
	}
	for i, c := range cases {
		path := filepath.Join(l.kit, "out", fmt.Sprintf("agy-deny-%d.json", i))
		line := "printf '%s' " + shq(c.event) + " | " + kind.HookCommand + " > " + shq(path)
		if st, err := l.shell(pane, line, 60*time.Second); err != nil || st != 0 {
			report("FAIL", c.id, fmt.Sprintf("obor kind agy hook in the pane failed (status %d, %v); screen: %s", st, err,
				tail(l.screen(pane), 3)), where)
			continue
		}
		b, err := os.ReadFile(path)
		var a struct{ Decision, Reason string }
		switch {
		case err != nil:
			report("FAIL", c.id, "the transport wrote no answer: "+err.Error(), where)
		case json.Unmarshal(b, &a) != nil:
			report("FAIL", c.id, "the transport's answer is not JSON: "+tail(string(b), 2), where)
		case a.Decision != "deny":
			report("FAIL", c.id, c.name+": the transport answers "+strconv.Quote(strings.TrimSpace(string(b)))+"; want a deny", where)
		case !strings.Contains(a.Reason, c.want):
			report("FAIL", c.id, c.name+": denied "+strconv.Quote(a.Reason)+"; want one naming "+strconv.Quote(c.want), where)
		default:
			report("PASS", c.id, c.name+": the guard denies it: "+strconv.Quote(a.Reason), where)
		}
	}
}

// agyToolAsk is the enumeration's instruction: the prompt carries it, and it is how the answer's place in the
// transcript is found (no other prompt obor sends has it).
const agyToolAsk = "List every tool (function) you can call in this session"

// agyToolDrift is AG-L14: P13's enumeration — the model lists the tools it can call in this session — against the
// transport's table (var tools), one WARN per name the table has no row for: a call of such a tool goes to the guard as
// unguarded and is denied, so this is a classify-me list, never a failure. agy prints no tool list of its own, so the
// answer is the model's report (docs/research/agy-probes.md P13); the nightly runs the tier at a new `agy --version`.
func (l *lab) agyToolDrift(k agents.Kind, pane, sid string) {
	const marker = "TOOL-LIST-DONE"
	where := at(agyHookGo, "var tools") + ", docs/research/agy-probes.md P13"
	prompt := "[obor conf] Do not call any tool. " + agyToolAsk + ", by its exact function name as declared to you, one " +
		"per line, no descriptions, in the order they are declared. Then reply " + marker + "."
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": prompt}, 15*time.Second); err != nil {
		report("FAIL", "AG-L14", "agent.prompt: "+err.Error(), where)
		return
	}
	// the marker ends the answer, so its presence means the whole list is in the record
	text, ok := waitTranscript(180*time.Second, func() string {
		if a, found := answerAfter(l.agyTranscript(k, sid), agyToolAsk); found && strings.Contains(a, marker) {
			return a
		}
		return ""
	}, marker)
	if !ok {
		report("SKIP", "AG-L14", "the enumeration's answer did not land in 180s: "+tail(l.agyTranscript(k, sid), 3), where)
		return
	}
	names := toolNamesIn(text)
	if len(names) == 0 {
		report("SKIP", "AG-L14", "the enumeration's answer named no tool: "+tail(text, 3), where)
		return
	}
	unknown := noRow(names, kind.HasRow)
	if len(unknown) == 0 {
		report("PASS", "AG-L14", fmt.Sprintf("P13's enumeration: agy offers %d tools, each with a row in the transport's table", len(names)), where)
		return
	}
	for _, name := range unknown {
		report("WARN", "AG-L14", "agy offers "+name+", which the transport's table has no row for: the guard decides a call of it as unguarded (a deny for obor's agents); classify it", where)
	}
}

// declaresServer reports whether the committed MCP config at path names server under mcpServers.
func declaresServer(path, server string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	return json.Unmarshal(b, &doc) == nil && doc.Servers[server] != nil
}

// agyPush is AG-L10 (docs/design.md "agy's inbox", probes P7): the agent runs `obor whoami` (a start check), which
// records its inbox from the command's environment (agy's agentapi variables) under its slot, L-01; once the agent is
// idle, the kind's deliver with that record (agentapi send-message) starts a turn: the reply is in the conversation's
// transcript (the marker twice: the message, as a system message, and the reply).
func (l *lab) agyPush(k agents.Kind, pane, sid, name string) {
	where := at(agyDeliverGo, "func deliver(") + ", " + at("internal/guard/session.go", "func CommandRecord(")
	const ran = "Run exactly this shell command and nothing else: obor whoami  Then reply with exactly: WHOAMI-RAN"
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": ran}, 15*time.Second); err != nil {
		report("FAIL", "AG-L10", "agent.prompt: "+err.Error(), where)
		return
	}
	var in project.Inbox
	asked := ""
	recorded := poll(120*time.Second, func() bool {
		if m := agyAskRE.FindString(l.screen(pane)); m != "" && asked == "" {
			asked = m
		}
		r, err := project.ReadInbox(l.config(), "L-01")
		in = r
		return err == nil && r.Sends
	})
	if !recorded {
		report("FAIL", "AG-L10", fmt.Sprintf("obor whoami recorded no inbox for L-01 in 120s (asked: %q); screen: %s", asked,
			tail(l.screen(pane), 4)), where)
		return
	}
	poll(60*time.Second, func() bool { a, err := l.agent(name); return err == nil && a.Status == "idle" })
	const push = "AGY-PUSH-OK"
	d, ok := k.Deliver("Reply with exactly "+push, []byte(in.Record))
	if !ok || !d.Sent {
		report("FAIL", "AG-L10", fmt.Sprintf("deliver with the record: %+v, %v", d, ok), where)
		return
	}
	text, ok := waitTranscript(120*time.Second, func() string {
		if t := l.agyTranscript(k, sid); strings.Count(t, push) >= 2 {
			return t
		}
		return ""
	}, push)
	if !ok {
		report("FAIL", "AG-L10", "agentapi send-message went out, but no "+push+" reply in the transcript in 120s: "+
			tail(l.agyTranscript(k, sid), 3)+"; screen: "+tail(l.screen(pane), 4), where)
		return
	}
	report("PASS", "AG-L10", fmt.Sprintf("the agent's obor whoami recorded its inbox (asked: %q); deliver's agentapi "+
		"send-message woke the idle session: %s", asked, tail(text, 1)), where)
}

// agyAskRE is a prompt of agy's on the screen: a permission request, a trust dialog.
var agyAskRE = regexp.MustCompile(`Allow calling this tool\?|Requesting permission for[^\n]*|Do you trust the contents of this project\?`)

// writeEntry writes m.Entry at m.Key of m.Path, as obor setup agents does for a project (here the lane's copy, as a
// commit would give it).
func writeEntry(m agents.MCPRegistration) error {
	doc := []byte("{}")
	out, err := config.EditJSON(doc, m.Key, m.Entry)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(m.Path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(m.Path, out, 0o644)
	}
	return err
}

// agyStart starts agy in pane as obor's launch builds a worker's start of the lab's agy model (in: the start's prompt
// or resume), its settings file trusting the lab's dirs (trustLab): AG-L1 (H-L1's fact) when it comes up idle with no
// prompt of agy's and no wrong-model notice; false (reported) when it does not.
func (l *lab) agyStart(k agents.Kind, pane string, in agents.Start, mode string) (started, bool) {
	where := at(agyGo, "func start(") + ", " + at(agyGo, "const NotReadyPattern")
	s, ready, launch, screen, err := l.agyUp(k, pane, "agy-live", in)
	if err != nil {
		report("FAIL", "AG-L1", mode+": the start: "+err.Error(), where)
		return started{}, false
	}
	reportStart("agy "+mode, s, ready)
	switch {
	case l.atShell(pane):
		report("FAIL", "AG-L1", mode+": agy did not start: "+tail(screen, 6), where)
		return s, false
	case agyAskRE.MatchString(screen):
		report("FAIL", "AG-L1", mode+": agy asks on its start screen: "+agyAskRE.FindString(screen), where)
		l.stopAgent(pane)
		return s, false
	case k.NotReady(screen) != "":
		report("FAIL", "AG-L1", mode+": "+k.NotReady(screen)+": "+tail(screen, 4), where)
		l.stopAgent(pane)
		return s, false
	}
	report("PASS", "AG-L1", fmt.Sprintf("%s: `agy %s` came up with no prompt of agy's and no wrong-model notice", mode,
		strings.Join(redactPrompt(launch.Args), " ")), where)
	return s, true
}

// agyWrongModel is AG-L7: obor's launcher starts the agent running in pane again with the lab's bogus model. The kind
// checks a start's model against `agy models` before agy runs, so the launcher refuses it before anything is stopped:
// the agent in the pane keeps running (the same foreground processes) and the start's first prompt is never typed.
func (l *lab) agyWrongModel(s started, pane string) {
	where := at(agyGo, "func start(")
	const first = "AGY-L7-FIRST"
	shell, before := l.fg(pane)
	var out bytes.Buffer
	ln := l.launcher(&out, func(string) (store.Session, bool) { return store.Session{}, false })
	_, err := ln.Launch(l.ctx, launch.Request{Agent: s.name, Pane: pane, Type: "agy-bogus", Prompt: first})
	_, after := l.fg(pane)
	said := l.screen(pane)
	atShell := shell != 0 && len(before) == 1 && before[0].pid == shell
	switch {
	case len(before) == 0 || atShell:
		report("FAIL", "AG-L7", "precondition: an agent process runs in pane "+pane+" before its restart: foreground "+pidsFact(before)+"; screen: "+tail(said, 4), where)
	case err == nil:
		report("FAIL", "AG-L7", "model agy-bogus (gemini-9-bogus): the launcher started it: "+tail(out.String(), 3)+"; screen: "+tail(said, 5), where)
	case !strings.Contains(err.Error(), "gemini-9-bogus") || !strings.Contains(err.Error(), "agy models"):
		report("FAIL", "AG-L7", "model agy-bogus: the launcher's error names neither the model nor agy models: "+firstLine(err.Error()), where)
	case !samePids(before, after):
		report("FAIL", "AG-L7", "model agy-bogus: the refusal came back but the agent the pane ran was stopped: foreground "+
			pidsFact(before)+" became "+pidsFact(after), where)
	case strings.Contains(said, first):
		report("FAIL", "AG-L7", "model agy-bogus: the start's first prompt reached the pane: "+tail(said, 4), where)
	default:
		report("PASS", "AG-L7", "model agy-bogus: obor's launcher refuses it before stopping anything ("+firstLine(err.Error())+
			"), pane "+pane+" still runs "+pidsFact(after)+", first prompt "+first+" never typed", where)
	}
}

// pidsFact is procs as "name(pid) …" for a fact line ("none": nothing in the foreground).
func pidsFact(ps []proc) string {
	if len(ps) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s(%d)", p.name, p.pid))
	}
	return strings.Join(parts, " ")
}

// samePids is whether every process of before still runs in after: a pane's agent stopped and restarted is not it.
func samePids(before, after []proc) bool {
	now := map[int]bool{}
	for _, p := range after {
		now[p.pid] = true
	}
	for _, p := range before {
		if !now[p.pid] {
			return false
		}
	}
	return len(before) > 0
}

// agyUp starts agy in pane as obor's launch builds a worker's start of model (the lab config's), its settings file
// trusting the lab's dirs (trustLab), and waits until herdr reports it idle (or 30 s): the start, when it took input
// (readyAfter), its launch, and the screen then (a few seconds on, for a notice).
func (l *lab) agyUp(k agents.Kind, pane, model string, in agents.Start) (started, time.Duration, agents.Launch, string, error) {
	cfg := l.config()
	m, ok := cfg.Model(model)
	if !ok {
		return started{}, 0, agents.Launch{}, "", fmt.Errorf("the lab project's config has no model %s", model)
	}
	over, err := cfg.Overrides("agy")
	if err != nil {
		return started{}, 0, agents.Launch{}, "", err
	}
	in.Dir, in.Root, in.Env, in.Read = l.lane, l.main, l.envOf, os.ReadFile
	in.WriteState = func(name string, data []byte) (string, error) {
		path := filepath.Join(l.root, "state", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		return path, os.WriteFile(path, data, 0o644)
	}
	launch, err := m.LaunchOf(k, "worker", over, in)
	if err == nil {
		err = l.trustLab(launch.Args)
	}
	if err != nil {
		return started{}, 0, launch, "", err
	}
	_, _ = l.shell(pane, "clear", 10*time.Second) // the last start's screen (a notice) is not this one's
	t0 := time.Now()
	s := l.start("conf-agy", "agy", pane, launch.Args, 60*time.Second)
	ready := l.readyAfter(s, t0, "idle", "done")
	time.Sleep(3 * time.Second) // the model resolves after sign-in: its notice comes a moment after the input shows
	return s, ready, launch, l.visible(pane), nil
}

// redactPrompt is args with the settings file's path cut to its name.
func redactPrompt(args []string) []string {
	out := slices.Clone(args)
	for i := range out {
		if i > 0 && out[i-1] == "--settings" {
			out[i] = "<state>/" + filepath.Base(out[i])
		}
	}
	return out
}

// trustLab adds the lab's main checkout and lane to the trustedWorkspaces of the settings file args name.
func (l *lab) trustLab(args []string) error {
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		return fmt.Errorf("the start names no --settings file: %q", args)
	}
	b, err := os.ReadFile(args[i+1])
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	tw, _ := doc["trustedWorkspaces"].([]any)
	doc["trustedWorkspaces"] = append(tw, l.main, l.lane)
	out, _ := json.MarshalIndent(doc, "", "  ")
	return os.WriteFile(args[i+1], out, 0o644)
}

// agyConversation is the conversation of s whose transcript carries marker (herdr's agent_session, else the newest
// conversation that has it), and its transcript; "" after 120 s.
func (l *lab) agyConversation(k agents.Kind, s started, marker string) (string, string) {
	var sid, text string
	poll(120*time.Second, func() bool {
		if a, err := l.agent(s.name); err == nil && a.Session != nil && a.Session.Value != "" {
			id := strings.TrimSuffix(filepath.Base(a.Session.Value), filepath.Ext(a.Session.Value))
			if t := l.agyTranscript(k, id); strings.Contains(t, marker) {
				sid, text = id, t
				return true
			}
		}
		dirs, _ := filepath.Glob(filepath.Join(l.envOf("HOME"), ".gemini", "antigravity-cli", "brain", "*"))
		for _, d := range dirs {
			if fi, err := os.Stat(d); err != nil || time.Since(fi.ModTime()) > 10*time.Minute {
				continue
			}
			if t := l.agyTranscript(k, filepath.Base(d)); strings.Contains(t, marker) {
				sid, text = filepath.Base(d), t
				return true
			}
		}
		return false
	})
	return sid, text
}

// agyTranscript is conversation sid's transcript.jsonl (the kind's glob).
func (l *lab) agyTranscript(k agents.Kind, sid string) string {
	return transcripts(k.Transcript(l.envOf, sid))
}

// agyQuit is AG-L4: the kind's quit recipe ends agy with status 0.
func agyQuit(l *lab, pane string, st agents.Stop, where string) {
	if code, err := l.quit(pane, st); err != nil || code != 0 {
		report("FAIL", "AG-L4", fmt.Sprintf("%s: exit status %d (%v)", recipeFact(st), code, orOK(err)), where)
		l.stopAgent(pane)
		return
	}
	report("PASS", "AG-L4", recipeFact(st)+" exits with status 0", where)
}

// p10Commands are P10's commands, each run as its own tool call: obor's own command (it dials serve's unix socket: the
// lab's stub serve records the request), a test that opens a unix socket, a module download from the Go proxy, and a
// control, a host no rule names.
var p10Commands = []string{"obor status", "go test ./sockprobe/", "go mod download golang.org/x/text@v0.3.0",
	"curl -sS -o /dev/null https://example.com"}

// agyP10 answers P10 (docs/research/agy-probes.md): the project's settings_override.json allows command(obor),
// command(go test) and read_url(proxy.golang.org) (rules; else P10-control: an empty override, the start's own rules
// alone); a fresh start in auto mode runs p10Commands. Per command: whether
// agy asked (its prompt answered with its default, Yes, so the next command runs), its exit code and first line.
// The module cache is a new dir of the lane per run (a workspace dir), the sum database off, the proxy proxy.golang.org alone.
func agyP10(l *lab, k agents.Kind, pane string, st agents.Stop, rules bool) {
	where := "docs/research/agy-probes.md P10, " + at(agyGo, "func start(")
	id := "P10"
	allow := `{"permissions": {"allow": ["command(obor)", "command(go test)", "read_url(proxy.golang.org)"]}}`
	if !rules {
		id, allow = "P10-control", `{}`
	}
	files := map[string]string{
		filepath.Join(l.main, ".obor", "agy", "settings_override.json"): allow + "\n",
		filepath.Join(l.lane, "go.mod"):                                 "module p10\n\ngo 1.22\n",
		filepath.Join(l.lane, "sockprobe", "sock_test.go"): `package sockprobe

import (
	"net"
	"path/filepath"
	"testing"
)

func TestUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() { c, err := ln.Accept(); if err == nil { c.Write([]byte("x")); c.Close() } }()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	b := make([]byte, 1)
	if _, err := c.Read(b); err != nil || b[0] != 'x' {
		t.Fatalf("read: %q %v", b, err)
	}
	t.Log("P10-SOCKET-OK")
}
`,
	}
	for path, text := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = os.WriteFile(path, []byte(text), 0o644)
		}
	}
	env := map[string]string{"GOMODCACHE": filepath.Join(l.lane, ".modcache-"+id), "GOSUMDB": "off", "GOPROXY": "https://proxy.golang.org",
		"GOFLAGS": "-modcacherw"}
	if st, err := l.shell(pane, exports(env), 10*time.Second); err != nil || st != 0 {
		report("FAIL", id, fmt.Sprintf("the env line failed (status %d, %v)", st, err), where)
		return
	}
	var list []string
	for i, c := range p10Commands {
		list = append(list, fmt.Sprintf("%d) %s", i+1, c))
	}
	const done = "P10-DONE"
	prompt := "[obor conf] Run these shell commands, each as its own run_command call, exactly as written, one after another, " +
		"even when one fails: " + strings.Join(list, " ") + ". Do not retry or change them. Then reply " + done + "."
	before := len(l.app.asked())
	s, ok := l.agyStart(k, pane, agents.Start{Prompt: prompt}, id)
	if !ok {
		report("FAIL", id, "agy did not start with the override file", where)
		return
	}
	var asked []string
	sid, text := "", ""
	poll(300*time.Second, func() bool {
		if m := agyAskRE.FindString(l.visible(pane)); m != "" {
			asked = append(asked, m)
			_ = l.keys(pane, "enter")
			time.Sleep(2 * time.Second)
		}
		if sid == "" {
			if a, err := l.agent(s.name); err == nil && a.Session != nil && a.Session.Value != "" {
				sid = strings.TrimSuffix(filepath.Base(a.Session.Value), filepath.Ext(a.Session.Value))
			}
		}
		if sid != "" {
			text = l.agyTranscript(k, sid)
		}
		return strings.Contains(text, `"content":"`+done)
	})
	if sid == "" {
		sid, text = l.agyConversation(k, s, done)
	}
	results := p10Results(text)
	var parts []string
	for _, c := range p10Commands {
		r, ran := results[c]
		if !ran {
			parts = append(parts, c+": not run")
			continue
		}
		parts = append(parts, c+": "+r)
	}
	fact := strings.Join(parts, "; ")
	fact += fmt.Sprintf("; the stub serve got %s from obor status", orNone(strings.Join(l.app.asked()[before:], " ")))
	if len(asked) > 0 {
		fact += "; agy asked: " + strings.Join(asked, " / ")
	} else {
		fact += "; agy asked nothing"
	}
	if len(results) < len(p10Commands) {
		report("FAIL", id, fact+"; transcript end: "+tail(text, 3), where)
	} else {
		report("PASS", id, fact, where)
	}
	if _, err := l.quit(pane, st); err != nil {
		l.stopAgent(pane)
	}
	for path := range files {
		os.Remove(path)
	}
}

// p10Results are the commands the transcript ran (a PLANNER_RESPONSE's run_command, then its GENERIC result), each
// as "exit <n>: <its first output line>" (the sandbox's refusal shows as "operation not permitted" there).
func p10Results(text string) map[string]string {
	out := map[string]string{}
	pending := ""
	for _, line := range strings.Split(text, "\n") {
		var l struct {
			Type      string `json:"type"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Name string `json:"name"`
				Args struct {
					CommandLine string `json:"CommandLine"`
				} `json:"args"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal([]byte(line), &l) != nil {
			continue
		}
		for _, c := range l.ToolCalls {
			if c.Name == "run_command" {
				var cmd string
				if json.Unmarshal([]byte(c.Args.CommandLine), &cmd) != nil {
					cmd = c.Args.CommandLine
				}
				pending = strings.TrimSpace(cmd)
			}
		}
		if l.Type == "GENERIC" && pending != "" {
			out[pending] = p10Outcome(l.Content)
			pending = ""
		}
	}
	return out
}

var exitRE = regexp.MustCompile(`Command exited with code (\d+)`)

// p10Outcome is a run_command result as "exit <n>: <the line that tells>".
func p10Outcome(content string) string {
	code := "?"
	if m := exitRE.FindStringSubmatch(content); m != nil {
		code = m[1]
	}
	tell := ""
	for _, line := range strings.Split(content, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "not permitted") || strings.Contains(low, "p10-socket-ok") || strings.Contains(low, "denied") ||
			strings.HasPrefix(line, "ok ") || strings.HasPrefix(line, "FAIL") || strings.Contains(low, "error") ||
			strings.Contains(low, "serve") {
			tell = strings.TrimSpace(line)
			break
		}
	}
	if len(tell) > 160 {
		tell = tell[:160]
	}
	return "exit " + code + ": " + orNone(tell)
}
