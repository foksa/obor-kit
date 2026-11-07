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

	"obor/internal/herdr"
)

const (
	socketGo = "internal/herdr/socket.go"
	launchGo = "internal/launch/launch.go"
)

// herdrLive: H-L8, H-L7, H-L4, H-L3, H-L5, H-L9, H-L1 (timeout), H-L6 last (it restarts the session). H-L2 needs a
// known dialog: it runs in the claude session (Claude's trust dialog); H-L1's answer timing is measured on each agent
// kind's start.
func herdrLive(l *lab, _ bool) {
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
	report("SKIP", "H-L2", "runs in the claude session (KIND=claude): Claude's trust dialog is the known dialog it needs",
		at(launchGo, "l.H.Wake(")+", F3.3")
	hL6(l, pane)
}

// hL7: herdr's injected HERDR_* in the pane's shell and in a child of it (the stub, as an MCP server would start).
func hL7(l *lab, pane string) {
	where := "F1.1, " + at("internal/mcp/mcp.go", "HERDR_PANE_ID") + ", CAP §5.1"
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
// stays the way a relabel is seen (T-839: the settled fact, asserted; re-probed on 0.9.5, T-953: still none).
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

// hL9: a cd in a pane's shell and what a stream subscribed to pane.updated sees (T-953: H10's probe; obor's rereadPanes
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
// obor's as H-S2 judges the installed CLI's (RV-103: H-S1/H-S2 read the CLI's bundled schema, not a server).
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
