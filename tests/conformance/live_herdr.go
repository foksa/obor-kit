//go:build conformance

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"bytes"
	"obor/internal/agents"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/testkit"
)

const (
	socketGo = "internal/herdr/socket.go"
	launchGo = "internal/launch/launch.go"
)

// herdrLive: H-L8, H-L7, H-L4, H-L3, H-L5, H-L9, H-L1 (timeout), the contract (H-C), H-L12, H-L13, H-L14 (claude;
// TURN=1), H-L6 last (it restarts the session). H-L2 needs a
// known dialog: it runs in the claude session (Claude's trust dialog); H-L1's answer timing is measured on each agent
// kind's start.
func herdrLive(l *lab, turn bool) {
	hL8(l)
	pane, err := l.workspace("conf", l.lane)
	if err != nil {
		report("FAIL", "H-L7", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	hL7(l, pane)
	hL4(l, pane)
	hL3(l, pane)
	hL5(l, pane)
	hL9(l, pane)
	hL1Timeout(l, pane)
	hContract(l, pane, "conf", l.lane)
	hL12(l, pane)
	hL13(l, pane)
	if _, err := exec.LookPath("claude"); err != nil {
		report("SKIP", "H-L14", "a first-prompt start: claude is not installed", "internal/launch/start.go")
	} else {
		hL14(l, "claude", pane, kindOf("claude").(agents.Split).Start(agents.Start{
			Args: []string{"--allowedTools", "Bash(perl:*)", "--model", "haiku"}, Prompt: hL14Prompt}), turn)
	}
	report("SKIP", "H-L2", "runs in the claude session (KIND=claude): Claude's trust dialog is the known dialog it needs",
		at(launchGo, "l.H.Wake(")+", F3.3")
	hL6(l, pane)
}

// The contract's rows are herdr's live rows too, for the SKIP lines when herdr cannot run.
func init() {
	for _, row := range testkit.Contract {
		liveIDs["herdr"] = append(liveIDs["herdr"], "H-C "+row.Name)
	}
}

// hContract: the terminal contract (testkit.Contract) against herdr's adapter on the throwaway session, one
// H-C row each, on pane (a shell in the workspace labelled group, working in cwd). A row that starts an agent is the
// kind sessions' (each proves a start there): SKIP here.
func hContract(l *lab, pane, group, cwd string) {
	where := "internal/testkit/terminal.go, the same rows as internal/herdr's TestTerminalContract"
	s := &herdr.Socket{Path: l.sock, Timeout: 10 * time.Second}
	t := testkit.Terminal{Control: s, Panes: herdr.Panes{S: s},
		Setup: &herdr.Setup{Socket: s, Env: project.OSEnv}, Group: group, Cwd: cwd}
	for _, row := range testkit.Contract {
		id := "H-C " + row.Name
		if row.Agent {
			report("SKIP", id, "starts an agent CLI: the kind sessions (KIND=claude, codex, opencode) prove a start", where)
			continue
		}
		if err := row.Run(l.ctx, t, pane); err != nil {
			report("FAIL", id, err.Error(), where)
			continue
		}
		report("PASS", id, "herdr's adapter keeps the row on a live session", where)
	}
}

// hL7: herdr's injected HERDR_* in the pane's shell and in a child of it (the stub, as an MCP server would start).
func hL7(l *lab, pane string) {
	where := "F1.1, " + at("internal/project/pane.go", "terminalPaneVar =") + " (the resolver's fallback, T-1037), CAP §5.1"
	envFile := filepath.Join(l.root, "pane-env.txt")
	if st, err := l.shell(pane, "env > "+shq(envFile), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "H-L7", fmt.Sprintf("env in the pane failed (status %d, %v)", st, err), where)
		return
	}
	paneEnv := map[string]string{}
	b, _ := os.ReadFile(envFile)
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			paneEnv[k] = v
		}
	}
	if st, err := l.shell(pane, shq(filepath.Join(l.kit, "bin", "obor"))+" hook pane-child </dev/null", 10*time.Second); err != nil || st != 0 {
		report("FAIL", "H-L7", fmt.Sprintf("the stub as the pane's child failed (status %d, %v)", st, err), where)
		return
	}
	rs := l.records("hook", "pane-child")
	if len(rs) == 0 {
		report("FAIL", "H-L7", "the stub run in the pane wrote no record", where)
		return
	}
	child := rs[0].Env
	var bad []string
	for name, env := range map[string]map[string]string{"pane": paneEnv, "child": child} {
		if env["HERDR_PANE_ID"] != pane {
			bad = append(bad, fmt.Sprintf("%s HERDR_PANE_ID=%q, not %s", name, env["HERDR_PANE_ID"], pane))
		}
		if !same(env["HERDR_SOCKET_PATH"], l.sock) {
			bad = append(bad, fmt.Sprintf("%s HERDR_SOCKET_PATH=%q, not the session's socket", name, env["HERDR_SOCKET_PATH"]))
		}
		if env["HERDR_ENV"] != "1" {
			bad = append(bad, name+" HERDR_ENV is not 1")
		}
	}
	slices.Sort(bad)
	fact := fmt.Sprintf("pane shell has %s; its child has %s", strings.Join(envNames(paneEnv, "^HERDR_"), " "),
		strings.Join(envNames(child, "^HERDR_"), " "))
	if len(bad) > 0 {
		report("FAIL", "H-L7", fact+": "+strings.Join(bad, "; "), where)
		return
	}
	report("PASS", "H-L7", fact+"; HERDR_PANE_ID is the pane, HERDR_SOCKET_PATH the session's socket", where)
}

// hL4: pane.wait_for_output matches a marker already on the screen at once, and one printed later when it comes.
func hL4(l *lab, pane string) {
	where := at(launchGo, "func (l *Launcher) setEnv") + ", P7, CAP §5.4"
	if _, err := l.shell(pane, `echo "EARLY_$((6*7))"`, 10*time.Second); err != nil {
		report("FAIL", "H-L4", "the early marker was not printed: "+err.Error(), where)
		return
	}
	t0 := time.Now()
	errEarly := l.waitOutput(pane, "EARLY_42", false, 5*time.Second)
	early := time.Since(t0)
	done := make(chan error, 1)
	t1 := time.Now()
	go func() { done <- l.waitOutput(pane, "LATE_42", false, 15*time.Second) }()
	time.Sleep(500 * time.Millisecond)
	_ = l.typeLine(pane, `sleep 2; echo "LATE_$((6*7))"`)
	errLate := <-done
	late := time.Since(t1)
	fact := fmt.Sprintf("a marker on the screen matched after %.1fs (%v); one printed ~2.5s later matched after %.1fs (%v)",
		early.Seconds(), orOK(errEarly), late.Seconds(), orOK(errLate))
	switch {
	case errEarly != nil || errLate != nil:
		report("FAIL", "H-L4", fact, where)
	case early > 2*time.Second || late < 2*time.Second:
		report("FAIL", "H-L4", fact+": not the documented immediate check then poll", where)
	default:
		report("PASS", "H-L4", fact+": the env-line wait can be pane.wait_for_output", where)
	}
}

func orOK(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// paneEvent is one event line's pane payload: the event name and the pane's id, label and cwds (H9's relabel, H10's cd).
type paneEvent struct {
	name, pane, label, cwd, foreground string
}

// paneEventOf parses an event line's pane payload; both the event's data.pane and a flat data.pane_id are read.
func paneEventOf(line []byte) paneEvent {
	var e struct {
		Event string `json:"event"`
		Data  struct {
			PaneID string `json:"pane_id"`
			Pane   struct {
				PaneID     string `json:"pane_id"`
				Label      string `json:"label"`
				Cwd        string `json:"cwd"`
				Foreground string `json:"foreground_cwd"`
			} `json:"pane"`
		} `json:"data"`
	}
	_ = json.Unmarshal(line, &e)
	p := e.Data.Pane
	id := p.PaneID
	if id == "" {
		id = e.Data.PaneID
	}
	return paneEvent{e.Event, id, p.Label, p.Cwd, p.Foreground}
}

// paneEvents collects the pane events of pane (or naming no pane) seen on s within d.
func paneEvents(s *sub, pane string, d time.Duration) []paneEvent {
	var got []paneEvent
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		line := s.next(time.Until(deadline))
		if line == nil {
			break
		}
		if e := paneEventOf(line); e.pane == "" || e.pane == pane {
			got = append(got, e)
		}
	}
	return got
}

// hL3: pane.rename emits no event on a stream subscribed to pane.updated and the pane's status: the stream's reread
// stays the way a relabel is seen (the settled fact, asserted; re-probed: still none).
func hL3(l *lab, pane string) {
	where := at(socketGo, "pane.rename emits no event") + ", F3.2, H9"
	s, err := l.subscribe(map[string]any{"type": "pane.updated"}, map[string]any{"type": "pane.agent_status_changed", "pane_id": pane})
	if err != nil {
		report("FAIL", "H-L3", err.Error(), where)
		return
	}
	defer s.close()
	if _, err := l.call("pane.rename", map[string]any{"pane_id": pane, "label": "conf-renamed"}, 10*time.Second); err != nil {
		report("FAIL", "H-L3", "pane.rename: "+err.Error(), where)
		return
	}
	var label string
	if r, err := l.call("pane.get", map[string]any{"pane_id": pane}, 10*time.Second); err == nil {
		get(r, "pane.label", &label)
	}
	if label != "conf-renamed" {
		report("FAIL", "H-L3", fmt.Sprintf("the rename did not take: the pane's label reads %q, not conf-renamed", label), where)
		return
	}
	got := paneEvents(s, pane, 3*time.Second)
	if len(got) > 0 {
		var seen []string
		for _, e := range got {
			seen = append(seen, fmt.Sprintf("%s (label %q)", e.name, e.label))
		}
		report("FAIL", "H-L3", "pane.rename emits "+strings.Join(seen, ", ")+" in 3s: a relabel arrives as an event, so the stream's reread is not the only way it is seen (H9's workaround can go)", where)
		return
	}
	report("PASS", "H-L3", "pane.rename to conf-renamed (pane.get reads the new label) emits no event in 3s on a stream subscribed to pane.updated and the pane's status: the stream's reread stays the way a relabel is seen", where)
}

// hL5: the lifecycle events' spelling on the wire (pane_created or pane.created) for a split, a close and a shell that
// exits; the stream maps both spellings (eventName).
func hL5(l *lab, pane string) {
	where := at(socketGo, "func eventName") + ", CAP §5.6"
	s, err := l.subscribe(map[string]any{"type": "pane.created"}, map[string]any{"type": "pane.closed"}, map[string]any{"type": "pane.exited"})
	if err != nil {
		report("FAIL", "H-L5", err.Error(), where)
		return
	}
	defer s.close()
	// wait collects the event names for target within d.
	wait := func(target string, d time.Duration) []string {
		var got []string
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			line := s.next(time.Until(deadline))
			if line == nil {
				break
			}
			if name, p := event(line); p == target || p == "" {
				got = append(got, name)
			}
		}
		return got
	}
	results := map[string][]string{}
	if p, err := l.split(pane, l.root); err == nil {
		results["split"] = wait(p, 3*time.Second)
		if _, err := l.call("pane.close", map[string]any{"pane_id": p}, 10*time.Second); err == nil {
			results["pane.close"] = wait(p, 3*time.Second)
		}
	}
	if p, err := l.split(pane, l.root); err == nil {
		_ = s.drain()
		_ = l.typeLine(p, "exit")
		results["shell exit"] = wait(p, 5*time.Second)
	}
	var parts []string
	ok := true
	for _, action := range []string{"split", "pane.close", "shell exit"} {
		got := results[action]
		parts = append(parts, action+": "+orNone(strings.Join(got, " ")))
		known := false
		for _, name := range got {
			if strings.HasPrefix(name, "pane_") || strings.HasPrefix(name, "pane.") {
				known = true
			}
		}
		ok = ok && known
	}
	fact := strings.Join(parts, "; ")
	if !ok {
		report("FAIL", "H-L5", fact+": an action brought no pane lifecycle event", where)
		return
	}
	report("PASS", "H-L5", fact+" (eventName maps the underscore spelling to the dotted one)", where)
}

// hL9: a cd in a pane's shell and what a stream subscribed to pane.updated sees (H10's probe; obor's rereadPanes
// exists because 0.9.3 gave no event for a cd). Records the event, its cwd and the pane's own read, and passes either way:
// obor's reread is correct whichever answer herdr gives, and which proposal the answer settles is the probe doc's call.
func hL9(l *lab, pane string) {
	where := at("internal/app/panes.go", "a cd in the pane gives no event") + ", H10, F3.2"
	dir := filepath.Join(l.root, "cwd-probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		report("FAIL", "H-L9", "the probe dir: "+err.Error(), where)
		return
	}
	probe, err := l.split(pane, l.root)
	if err != nil {
		report("FAIL", "H-L9", "split: "+err.Error(), where)
		return
	}
	defer func() { _, _ = l.call("pane.close", map[string]any{"pane_id": probe}, 10*time.Second) }()
	s, err := l.subscribe(map[string]any{"type": "pane.updated"})
	if err != nil {
		report("FAIL", "H-L9", err.Error(), where)
		return
	}
	defer s.close()
	_ = s.drain()
	if err := l.typeLine(probe, "cd "+shq(dir)); err != nil {
		report("FAIL", "H-L9", "the cd: "+err.Error(), where)
		return
	}
	got := paneEvents(s, probe, 3*time.Second)
	var cwd, foreground string
	poll(3*time.Second, func() bool {
		r, err := l.call("pane.get", map[string]any{"pane_id": probe}, 10*time.Second)
		var p struct {
			Cwd        string `json:"cwd"`
			Foreground string `json:"foreground_cwd"`
		}
		if err != nil || !get(r, "pane", &p) {
			return false
		}
		cwd, foreground = p.Cwd, p.Foreground
		return same(cwd, dir) || same(foreground, dir)
	})
	if !same(cwd, dir) && !same(foreground, dir) {
		report("FAIL", "H-L9", fmt.Sprintf("the cd to %s did not take: pane.get reads cwd %q, foreground_cwd %q, and %d pane.updated event(s) came",
			dir, cwd, foreground, len(got)), where)
		return
	}
	var events []string
	withCwd := false
	for _, e := range got {
		events = append(events, fmt.Sprintf("%s (cwd %q, foreground_cwd %q)", e.name, e.cwd, e.foreground))
		withCwd = withCwd || same(e.cwd, dir) || same(e.foreground, dir)
	}
	seen := "no pane.updated event in 3s"
	if len(got) > 0 {
		seen = fmt.Sprintf("%d pane.updated event(s) in 3s: %s", len(got), strings.Join(events, ", "))
	}
	fact := fmt.Sprintf("a cd to %s in a fresh pane: %s; pane.get then reads cwd %q (foreground_cwd %q)", dir, seen, cwd, foreground)
	switch {
	case len(got) == 0:
		fact += "; a cd gives no event (H10 has nothing to use, the reread stays)"
	case withCwd:
		fact += "; the event carries the new cwd (H10's event exists)"
	default:
		fact += "; no event carries the new cwd (H10's event alone does not say where the shell went)"
	}
	report("PASS", "H-L9", fact, where)
}

// drain empties what the stream holds now.
func (s *sub) drain() int {
	n := 0
	for s.next(300*time.Millisecond) != nil {
		n++
	}
	return n
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// hL1Timeout: agent.start honours timeout_ms when the agent never comes up (the CLI exits on an unknown flag).
func hL1Timeout(l *lab, pane string) {
	where := at(socketGo, "func (s *Socket) Start") + ", F3.4, CAP §5.4"
	kind := ""
	for _, k := range []string{"claude", "codex", "opencode"} {
		if _, err := exec.LookPath(k); err == nil {
			kind = k
			break
		}
	}
	if kind == "" {
		report("SKIP", "H-L1", "timeout_ms: no agent CLI installed to start", where)
		return
	}
	const timeout = 4 * time.Second
	t0 := time.Now()
	s := l.start("conf-timeout", kind, pane, []string{"--obor-conformance-no-such-flag"}, timeout)
	fact := fmt.Sprintf("agent.start of %s with a flag it refuses (it exits at once), timeout_ms %d: answered after %.1fs with %s",
		kind, timeout.Milliseconds(), s.took.Seconds(), answer(s))
	ended, end := s.took, answer(s)
	if s.err == nil && s.info.LaunchPending { // answered at once: follow the pending launch
		poll(timeout+10*time.Second, func() bool {
			a, err := l.agent(s.name)
			switch {
			case err != nil:
				end = "agent.get " + err.Error()
			case !a.LaunchPending:
				end = "status " + a.Status + " (no longer pending)"
			default:
				return false
			}
			return true
		})
		ended = time.Since(t0)
		fact += fmt.Sprintf("; the pending launch ended after %.1fs: %s", ended.Seconds(), end)
	}
	l.stopAgent(pane)
	switch {
	case ended > timeout+3*time.Second:
		report("FAIL", "H-L1", fact+": timeout_ms not honoured", where)
	case s.err == nil && !s.info.LaunchPending:
		report("FAIL", "H-L1", fact+": an agent that exited counted as started", where)
	default:
		report("PASS", "H-L1", fact, where)
	}
}

// answer is an agent.start answer for a fact.
func answer(s started) string {
	if s.err != nil {
		return s.err.Error()
	}
	return fmt.Sprintf("status %s, launch_pending %t, interactive_ready %t", orNone(s.info.Status), s.info.LaunchPending, s.info.Ready)
}

// hL6: a stop and start of the named session: its panes come back as shells with their labels and cwd; which
// plugins (user-global registry) would run a startup hook, and what their log shows (no plugin is linked by the run).
func hL6(l *lab, pane string) {
	where := "internal/launch/restore.go, P9, CAP §5.6"
	other := filepath.Join(l.root, "other")
	_ = os.MkdirAll(other, 0o755)
	second, err := l.split(pane, other)
	if err != nil {
		report("FAIL", "H-L6", "split: "+err.Error(), where)
		return
	}
	want := map[string]string{"conf-keep": l.lane, "conf-two": other}
	for p, label := range map[string]string{pane: "conf-keep", second: "conf-two"} {
		if _, err := l.call("pane.rename", map[string]any{"pane_id": p, "label": label}, 10*time.Second); err != nil {
			report("FAIL", "H-L6", "pane.rename: "+err.Error(), where)
			return
		}
	}
	plugins := l.pluginIDs()
	l.stopServer()
	if err := l.startServer(); err != nil {
		report("FAIL", "H-L6", "the session did not start again: "+err.Error(), where)
		return
	}
	type pinfo struct {
		ID    string `json:"pane_id"`
		Label string `json:"label"`
		Cwd   string `json:"cwd"`
		Agent string `json:"agent"`
	}
	var panes []pinfo
	poll(15*time.Second, func() bool {
		r, err := l.call("pane.list", nil, 10*time.Second)
		panes = nil
		if err == nil {
			get(r, "panes", &panes)
		}
		return len(panes) >= 2
	})
	var got, bad []string
	for _, p := range panes {
		got = append(got, fmt.Sprintf("%s %q", p.ID, p.Label))
		dir, ok := want[p.Label]
		switch {
		case !ok:
			continue
		case !same(p.Cwd, dir):
			bad = append(bad, fmt.Sprintf("%s cwd %s, not %s", p.Label, p.Cwd, dir))
		case p.Agent != "":
			bad = append(bad, p.Label+" came back with agent "+p.Agent)
		default:
			_ = l.waitPrompt(p.ID)
			if !l.atShell(p.ID) {
				bad = append(bad, p.Label+" is not at a shell")
			}
		}
		delete(want, p.Label)
	}
	for label := range want {
		bad = append(bad, label+" did not come back")
	}
	slices.Sort(bad)
	logs := l.pluginLog()
	fact := fmt.Sprintf("after a stop/start: panes %s; registered plugins (user-global) %s; plugin log %s",
		orNone(strings.Join(got, ", ")), orNone(strings.Join(plugins, " ")), orNone(logs))
	if len(bad) > 0 {
		report("FAIL", "H-L6", fact+": "+strings.Join(bad, "; "), where)
		return
	}
	report("PASS", "H-L6", fact+": both came back as shells with their labels and cwd", where)
}

// pluginIDs is plugin.list's ids (and whether each is enabled).
func (l *lab) pluginIDs() []string {
	r, err := l.call("plugin.list", nil, 10*time.Second)
	if err != nil {
		return []string{"(plugin.list: " + err.Error() + ")"}
	}
	var ps []struct {
		ID      string `json:"plugin_id"`
		Enabled *bool  `json:"enabled"`
	}
	if !get(r, "plugins", &ps) {
		return []string{"(plugin.list: " + firstLine(string(r)) + ")"}
	}
	var ids []string
	for _, p := range ps {
		id := p.ID
		if p.Enabled != nil && !*p.Enabled {
			id += "(disabled)"
		}
		ids = append(ids, id)
	}
	return ids
}

// pluginLog is plugin.log.list's entries as "plugin:event status", or why it has none.
func (l *lab) pluginLog() string {
	r, err := l.call("plugin.log.list", map[string]any{"limit": 20}, 10*time.Second)
	if err != nil {
		return "(plugin.log.list: " + err.Error() + ")"
	}
	var entries []map[string]json.RawMessage
	for _, key := range []string{"logs", "entries", "plugin_logs"} {
		if get(r, key, &entries) {
			break
		}
	}
	if len(entries) == 0 {
		return "empty"
	}
	var parts []string
	for _, e := range entries {
		var plugin, ev string
		_ = json.Unmarshal(e["plugin_id"], &plugin)
		_ = json.Unmarshal(e["event"], &ev)
		if ev == "" {
			_ = json.Unmarshal(e["kind"], &ev)
		}
		parts = append(parts, plugin+":"+ev)
	}
	return fmt.Sprintf("%d entries: %s", len(entries), strings.Join(parts, ", "))
}

// hL8: ping to the throwaway session's running server: its version, protocol and capabilities, its protocol against
// obor's as H-S2 judges the installed CLI's (H-S1/H-S2 read the CLI's bundled schema, not a server).
func hL8(l *lab) {
	where := at(socketGo, "func checkProtocol") + ", CAP §5.1"
	r, err := l.call("ping", nil, 10*time.Second)
	if err != nil {
		report("FAIL", "H-L8", "ping to the running server: "+err.Error(), where)
		return
	}
	var p struct {
		Version      string                     `json:"version"`
		Protocol     int                        `json:"protocol"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(r, &p) != nil || p.Protocol == 0 {
		report("FAIL", "H-L8", "ping answered no protocol: "+firstLine(string(r)), where)
		return
	}
	var caps []string
	for _, k := range names(p.Capabilities) {
		caps = append(caps, k+"="+string(p.Capabilities[k]))
	}
	fact := fmt.Sprintf("running server (session %s): herdr %s, protocol %d (obor needs >= %d); capabilities: %s",
		l.session, p.Version, p.Protocol, herdr.Protocol, orNone(strings.Join(caps, ", ")))
	if cli := herdrVersion(l.herdr); cli != p.Version {
		fact += "; the installed CLI is " + cli
	}
	switch {
	case p.Protocol < herdr.Protocol:
		report("FAIL", "H-L8", fact, where)
	case p.Protocol > herdr.Protocol:
		report("WARN", "H-L8", fact+"; newer than obor's, accepted (H-S1 guards what obor sends)", where)
	default:
		report("PASS", "H-L8", fact, where)
	}
}

// hL12: does agent.get say when the launched process is the one listed? A claude start in pane
// (agent.get sampled every 200 ms from the start until it is ready), its exit (sampled for 3 s after the pane is its
// shell again), and a second start right after that exit (sampled until ready): each change of status,
// launch_pending and interactive_ready, with the raw agent.get answer while the second start was pending. The start
// rule (internal/launch/start.go) reads launch_pending/interactive_ready only if they are there and change.
func hL12(l *lab, pane string) {
	where := at("internal/launch/start.go", "func (l *Launcher) started") + ", " + at(socketGo, "func (w Wire) pane") + ", H6"
	if _, err := exec.LookPath("claude"); err != nil {
		report("SKIP", "H-L12", "agent.get across a start: claude is not installed", where)
		return
	}
	var trusted time.Time
	fact0 := ""
	// follow samples agent.get on pane until done holds or d passes: the transitions, and the raw answer at each (none: an error).
	follow := func(t0 time.Time, d time.Duration, done func(agentInfo, error) bool) (seen, raws []string, sawPending, sawReady, readyAfterPending bool) {
		last := ""
		poll(d, func() bool {
			r, err := l.call("agent.get", map[string]any{"target": pane}, 10*time.Second)
			var a agentInfo
			if err == nil {
				get(r, "agent", &a)
			}
			cur := "agent.get " + orNone(errCode(err))
			if err == nil {
				cur = fmt.Sprintf("%s/%s pending=%t ready=%t", orNone(a.Agent), orNone(a.Status), a.LaunchPending, a.Ready)
			}
			if cur != last {
				seen = append(seen, fmt.Sprintf("%.1fs %s", time.Since(t0).Seconds(), cur))
				raws = append(raws, string(r))
				last = cur
			}
			sawPending = sawPending || err == nil && a.LaunchPending
			sawReady = sawReady || err == nil && a.Ready
			readyAfterPending = readyAfterPending || sawPending && err == nil && !a.LaunchPending && a.Ready
			// down, enter: "Yes, I trust this folder" (the default is "No, exit"); once per scratch dir, again every 2 s
			// while the dialog shows (keys sent while it renders are lost, and the start then never comes up).
			if time.Since(trusted) > 2*time.Second && strings.Contains(strings.ToLower(l.visible(pane)), "trust this folder") {
				fact0 = "trust dialog: " + tail(l.visible(pane), 6)
				_, _ = l.call("pane.send_input", map[string]any{"pane_id": pane, "keys": []string{"down", "enter"}}, 10*time.Second)
				trusted = time.Now()
			}
			return done(a, err)
		})
		return
	}
	upNow := func(a agentInfo, err error) bool {
		return err == nil && !a.LaunchPending && a.Ready && a.Status == "idle"
	}
	args := []string{"--model", "haiku"}

	t0 := time.Now()
	s := l.start("conf-ready", "claude", pane, args, 30*time.Second)
	first, _, p1, r1, ok1 := follow(t0, 40*time.Second, upNow)
	fact := "first start: answer " + answer(s) + "; agent.get " + strings.Join(first, ", ")

	k := kindOf("claude")
	code, qerr := l.quit(pane, k.Stop())
	t1 := time.Now()
	exited, exitRaws, _, _, _ := follow(t1, 3*time.Second, func(agentInfo, error) bool { return false })
	fact += fmt.Sprintf("; after its exit (status %d, %s): agent.get %s", code, orOK(qerr), strings.Join(exited, ", "))

	t2 := time.Now()
	s2 := l.start("conf-ready", "claude", pane, args, 30*time.Second)
	second, raws, p2, r2, ok2 := follow(t2, 40*time.Second, upNow)
	fact += "; second start right after the exit: answer " + answer(s2) + "; agent.get " + strings.Join(second, ", ")
	fact += "; agent.get answers after the exit: " + strings.Join(exitRaws, " | ") + "; agent.get answers of the second start: " + strings.Join(raws, " | ")
	if !ok2 {
		fact += "; screen: " + tail(l.visible(pane), 6)
	}
	if fact0 != "" {
		fact += "; " + fact0
	}
	if _, err := l.quit(pane, k.Stop()); err != nil {
		l.stopAgent(pane)
	}
	switch {
	case !p1 && !p2:
		report("FAIL", "H-L12", fact+": agent.get never carried launch_pending true", where)
	case !r1 && !r2:
		report("FAIL", "H-L12", fact+": agent.get never carried interactive_ready true", where)
	case !ok1 || !ok2:
		report("FAIL", "H-L12", fact+": a start did not go from pending to ready in agent.get", where)
	default:
		report("PASS", "H-L12", fact+": agent.get's launch_pending leaves and interactive_ready turns true when the launched agent is up", where)
	}
}

// hL13: obor's own start (launch.Launcher.Start, the one start of restart, serve and setup) of claude in pane
// on the live session, then a second right after that agent's exit (the pane waited for, as a restart does): each
// must return only once herdr says the launch is over (agent.get: launch_pending gone, interactive_ready), never on
// the pending listing herdr answers at once. Run after H-L12 (the scratch dir is trusted by then).
func hL13(l *lab, pane string) {
	where := at("internal/launch/start.go", "func (t attempt) counts") + ", " + at(socketGo, "Launched: !w.LaunchPending")
	if _, err := exec.LookPath("claude"); err != nil {
		report("SKIP", "H-L13", "obor's start: claude is not installed", where)
		return
	}
	var out bytes.Buffer
	ln := &launch.Launcher{H: &herdr.Socket{Path: l.sock, Timeout: 10 * time.Second}, Out: &out, Poll: 200 * time.Millisecond,
		Up: 30 * time.Second, Install: func() agents.InstallHost { return agents.InstallHost{} }}
	k := kindOf("claude")
	var facts []string
	ok := true
	for i, await := range []bool{false, true} {
		if i > 0 {
			if code, err := l.quit(pane, k.Stop()); err != nil {
				report("FAIL", "H-L13", fmt.Sprintf("the first agent did not exit (%d, %v): %s", code, err, strings.Join(facts, "; ")), where)
				l.stopAgent(pane)
				return
			}
		}
		t0 := time.Now()
		err := ln.Start(l.ctx, launch.Start{Agent: fmt.Sprintf("conf-obor-%d", l.seq.Add(1)), Kind: "claude", Pane: pane,
			Label: "conf-obor", Args: []string{"--model", "haiku"}, Shell: 20 * time.Second, Await: await})
		took := time.Since(t0)
		a, gerr := l.agent(pane)
		fact := fmt.Sprintf("start %d (%s): returned %s after %.1fs; agent.get then %s/%s pending=%t ready=%t (%s)",
			i+1, map[bool]string{false: "a shell", true: "right after the exit"}[await], orOK(err), took.Seconds(),
			orNone(a.Agent), orNone(a.Status), a.LaunchPending, a.Ready, orOK(gerr))
		facts = append(facts, fact)
		if err != nil || gerr != nil || a.LaunchPending || !a.Ready || a.Agent != "claude" {
			ok = false
		}
	}
	if _, err := l.quit(pane, k.Stop()); err != nil {
		l.stopAgent(pane)
	}
	fact := strings.Join(facts, "; ")
	if !ok {
		report("FAIL", "H-L13", fact+"; obor said: "+tail(out.String(), 6), where)
		return
	}
	report("PASS", "H-L13", fact+": obor's start returned only once herdr's launch was over", where)
}

// hL14Prompt is H-L14's first prompt: one shell command that keeps the agent working past 60 s (a 70 s sleep through
// its shell tool, the one tool argv allows without asking; perl, as Claude Code refuses a long bare `sleep`).
const hL14Prompt = "Run the shell command `perl -e 'sleep 70'` exactly once in the foreground (not in the background), " +
	"with a timeout of at least 120 seconds, then reply with exactly OK."

// hL14: a start with a first prompt that keeps the agent working, sent as obor sends it (agent.start with no
// timeout_ms, the call left open up to 90 s): when herdr answers and with what, and each change of agent.get (kind,
// status, launch_pending, interactive_ready) over 60 s from t0 (the first call; agent_pane_busy retried); then the
// agent is stopped (halt) and the same start runs right after its exit in the pane; last, obor's own start of it
// (hL14Obor). argv is kind's command line with the prompt. A model call: TURN=1 only.
func hL14(l *lab, kind, pane string, argv []string, turn bool) {
	where := at("internal/launch/start.go", "func (l *Launcher) started") + ", " + at(socketGo, "func (s *Socket) Start")
	if !turn {
		report("SKIP", "H-L14", kind+": a first-prompt start needs TURN=1 (a model call)", where)
		return
	}
	var facts []string
	worked, trusted := 0, time.Time{}
	for _, when := range []string{"fresh pane", "right after the old agent's exit"} {
		halt(l, kind, pane) // the first run: whatever ran before; the second: the first run's agent
		if !l.waitShell(pane, 20*time.Second) {
			report("FAIL", "H-L14", kind+": the pane is not a shell: "+strings.Join(facts, "; ")+"; screen: "+tail(l.visible(pane), 4), where)
			return
		}
		name := fmt.Sprintf("conf-prompt-%d", l.seq.Add(1))
		type reply struct {
			r    json.RawMessage
			err  error
			at   time.Duration // the answer, from t0
			sent time.Duration // the call answered, from t0 (after busy retries)
		}
		t0 := time.Now()
		res := make(chan reply, 1)
		go func() { // a pane whose agent just exited reads busy until herdr sees its shell again: retried, as obor does
			for {
				t := time.Now()
				r, err := l.call("agent.start", map[string]any{"name": name, "kind": kind, "pane_id": pane, "args": argv}, 90*time.Second)
				if errCode(err) != "agent_pane_busy" || time.Since(t0) > 20*time.Second {
					res <- reply{r, err, time.Since(t0), t.Sub(t0)}
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
		var seen []string
		ans, last, sawWorking, workedPending := "unanswered after 60s", "", false, false
		answered := false
		poll(60*time.Second, func() bool {
			select {
			case r := <-res:
				answered = true
				ans = fmt.Sprintf("sent at %.1fs, answered at %.1fs: %s", r.sent.Seconds(), r.at.Seconds(), startAnswer(r.r, r.err))
			default:
			}
			r, err := l.call("agent.get", map[string]any{"target": pane}, 10*time.Second)
			var a agentInfo
			if err == nil {
				get(r, "agent", &a)
			}
			cur := "agent.get " + orNone(errCode(err))
			if err == nil {
				cur = fmt.Sprintf("%s/%s pending=%t ready=%t", orNone(a.Agent), orNone(a.Status), a.LaunchPending, a.Ready)
			}
			if cur != last {
				seen = append(seen, fmt.Sprintf("%.1fs %s", time.Since(t0).Seconds(), cur))
				last = cur
			}
			// Claude's trust dialog of this start (its kind listed after the call, the dialog on screen), answered as H-L12
			// does ("Yes, I trust this folder"), again every 2 s while it shows: keys sent while it renders are lost.
			if err == nil && a.Agent == kind && time.Since(trusted) > 2*time.Second &&
				strings.Contains(strings.ToLower(l.visible(pane)), "trust this folder") {
				_, kerr := l.call("pane.send_input", map[string]any{"pane_id": pane, "keys": []string{"down", "enter"}}, 10*time.Second)
				trusted = time.Now()
				seen = append(seen, fmt.Sprintf("%.1fs trust dialog answered (%s)", time.Since(t0).Seconds(), orOK(kerr)))
			}
			if err == nil && a.Agent == kind && a.Status == "working" {
				sawWorking = true
				workedPending = workedPending || a.LaunchPending
			}
			return false
		})
		screen := tail(l.visible(pane), 3)
		if !answered {
			halt(l, kind, pane)
			select {
			case r := <-res:
				ans += fmt.Sprintf(" (answered %.1fs after the start, once the agent was stopped: %s)", r.at.Seconds(), startAnswer(r.r, r.err))
			case <-time.After(30 * time.Second):
				ans += " (still unanswered 30 s after the agent was stopped)"
			}
		}
		if sawWorking {
			worked++
		}
		facts = append(facts, fmt.Sprintf("%s %s: agent.start %s; agent.get %s; working seen %t (with launch_pending %t); screen at 60s: %s",
			kind, when, ans, strings.Join(seen, ", "), sawWorking, workedPending, screen))
	}
	halt(l, kind, pane)
	if !l.waitShell(pane, 20*time.Second) {
		report("FAIL", "H-L14", kind+": the pane is not a shell for obor's start: "+strings.Join(facts, "; "), where)
		return
	}
	obor, started := hL14Obor(l, kind, pane, argv)
	facts = append(facts, obor)
	halt(l, kind, pane)
	fact := strings.Join(facts, "; ")
	switch {
	case worked < 2:
		report("FAIL", "H-L14", fact+": an agent never worked on its first prompt (the probe's premise)", where)
	case !started:
		report("FAIL", "H-L14", fact+": obor's start of a first-prompt agent that works was not started within its bound", where)
	default:
		report("PASS", "H-L14", fact, where)
	}
}

// hL14Obor is H-L14's last leg: obor's own start (launch.Launcher.Start, the first-prompt start rule) of the first-prompt agent in
// pane, right after the previous agent's exit, with obor's 30 s Up: the fact, and whether it returned started while
// herdr lists the agent working (its launch still pending, if H-L14's raw runs hold).
func hL14Obor(l *lab, kind, pane string, argv []string) (string, bool) {
	var out bytes.Buffer
	ln := &launch.Launcher{H: &herdr.Socket{Path: l.sock, Timeout: 10 * time.Second}, Out: &out, Poll: 200 * time.Millisecond,
		Up: 30 * time.Second, Install: func() agents.InstallHost { return agents.InstallHost{} },
		Background: func(func()) {}} // no session search: the start's result is what is read
	t0 := time.Now()
	err := ln.Start(l.ctx, launch.Start{Agent: fmt.Sprintf("conf-obor-%d", l.seq.Add(1)), Kind: kind, Pane: pane,
		Label: "conf-obor", Args: argv, Prompt: hL14Prompt, Shell: 20 * time.Second, Await: true})
	took := time.Since(t0)
	a, gerr := l.agent(pane)
	fact := fmt.Sprintf("%s obor's start with the first prompt: returned %s after %.1fs; agent.get then %s/%s pending=%t ready=%t (%s)",
		kind, orOK(err), took.Seconds(), orNone(a.Agent), orNone(a.Status), a.LaunchPending, a.Ready, orOK(gerr))
	if err != nil {
		fact += "; obor said: " + tail(out.String(), 4)
	}
	return fact, err == nil && took < 30*time.Second && gerr == nil && a.Agent == kind && a.Status == "working"
}

// startAnswer is an agent.start answer r (or its error) for a fact.
func startAnswer(r json.RawMessage, err error) string {
	if err != nil {
		return "error " + orNone(errCode(err)) + " (" + err.Error() + ")"
	}
	var a agentInfo
	get(r, "agent", &a)
	return fmt.Sprintf("%s/%s, launch_pending %t, interactive_ready %t", orNone(a.Agent), orNone(a.Status), a.LaunchPending, a.Ready)
}

// halt ends a kind's agent in pane that may be working: its turn interrupted (esc), then its quit recipe, then
// ctrl+c (stopAgent) if it still runs. A pane at its shell is left as it is.
func halt(l *lab, kind, pane string) {
	if l.atShell(pane) {
		return
	}
	for i := 0; i < 2; i++ {
		_ = l.keys(pane, "esc")
		time.Sleep(time.Second)
	}
	if _, err := l.quit(pane, kindOf(kind).Stop()); err != nil {
		l.stopAgent(pane)
	}
}
