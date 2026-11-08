//go:build conformance

package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"obor/internal/agents"
	"obor/internal/store"
)

// The variables the OpenCode kind's start sets (kinds/opencode/kind: a tool may not import a kind's package).
const (
	ocConfigVar = "OPENCODE_CONFIG"
	ocModelVar  = "OBOR_OPENCODE_MODEL"
)

// opencodeLive: on scratch XDG dirs holding nothing of obor's (T-809: `obor setup agents` installs nothing global; an
// old entry and link are removed by hand, T-835), one --standalone start in the lane with the env the kind's start (the
// real kinds/opencode binary through obor's plugin package, T-1046) gives it (OPENCODE_CONFIG naming its generated config, under the scratch dir): OC-L1, H-L1, OC-L3 (the start's own
// mcp.servers.obor runs), OC-L4 (no global link; obor.js set up once, from the config's package dir), OC-L5's quit.
// With TURN=1 a role agent in the main checkout takes one turn (OC-L2, OC-L5).
func opencodeLive(l *lab, turn bool) {
	k := kindOf("opencode")
	xdg := map[string]string{}
	for _, d := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		dir := filepath.Join(l.root, "xdg", strings.ToLower(d))
		_ = os.MkdirAll(dir, 0o755)
		xdg["XDG_"+d+"_HOME"] = dir
	}
	home, _ := os.UserHomeDir()
	inst := k.Install(l.kit, func(n string) string {
		if n == "HOME" {
			return home
		}
		return xdg[n]
	})
	where3, where4 := at(opencodeGo, `case "install"`)+", P1, CAP §4.2", at(opencodeGo, "func Config")+", P1, CAP §4.3"
	if inst.MCP.Where != agents.PerStart || inst.Plugin != nil {
		report("FAIL", "OC-L3", fmt.Sprintf("Install still registers globally (%s, plugin %v)", inst.MCP.Where, inst.Plugin != nil), where3)
		return
	}
	withKit, err := agents.StartOf(k, agents.Start{Kit: l.kit, WriteState: func(name string, data []byte) (string, error) {
		path := filepath.Join(l.root, "state", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		return path, os.WriteFile(path, data, 0o644)
	}})
	vars := withKit.Env
	if err != nil || vars[ocConfigVar] == "" {
		report("FAIL", "OC-L3", fmt.Sprintf("the start names no generated config (%v, %v)", vars, err), at(opencodeGo, "func start("))
		return
	}
	pane, err := l.workspace("conf", l.lane)
	if err != nil {
		report("FAIL", "OC-L1", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane, "OBOR_CONF_PANE_ONLY": "1"}
	for k, v := range xdg {
		paneEnv[k] = v
	}
	for k, v := range vars {
		paneEnv[k] = v
	}
	if st, err := l.shell(pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "OC-L1", fmt.Sprintf("the env line failed (status %d, %v)", st, err), at(launchGo, "func envLine"))
		return
	}
	st := k.Stop()
	where5 := at(opencodeGo, `"stop"`) + ", CAP §4.6"
	bare, err := agents.StartOf(k, agents.Start{Args: []string{"--standalone"}})
	if err != nil {
		report("FAIL", "OC-L1", "the start: "+err.Error(), at(opencodeGo, "func start("))
		return
	}
	argv := bare.Args

	l.fresh()
	if l.opencodeStart(pane, argv, "as obor starts it") {
		mcp := l.waitRecords("mcp", "", 30*time.Second)
		ocL1(l, pane, mcp)
		ocL3(l.serverLabels(), where3)
		ocL4(l, filepath.Join(xdg["XDG_CONFIG_HOME"], "opencode", "plugins", "obor.js"), where4)
		if code, err := l.quit(pane, st); err != nil || code != 0 {
			report("FAIL", "OC-L5", fmt.Sprintf("%s + a second Enter: exit status %d (%v)", st.Quit, code, orOK(err)), where5)
			l.stopAgent(pane)
		} else {
			report("PASS", "OC-L5", recipeFact(st)+" + a second Enter exits with status 0", where5)
		}
	}

	if !turn {
		report("SKIP", "OC-L2", "needs TURN=1: obor.js runs a hook only for a session's events, which need a turn", "F1.3, P5")
		report("SKIP", "OC-L5", "session.execution.* reaching the plugin and OBOR_OPENCODE_MODEL's switch need TURN=1", at(opencodeGo, "const ModelVar"))
		report("SKIP", "OC-L6", "needs TURN=1 (a model call that uses the tool)", at("internal/mcp/mcp.go", "func Serve")+", T-898")
		return
	}
	l.opencodeAuth(xdg)
	ocTurn(l, pane, argv, xdg, st)
	ocL6(l, pane, xdg, st)
}

// ocL3: the start's generated config's mcp.servers.obor is the one obor server that runs (its `obor mcp`, no label).
func ocL3(labels []string, where string) {
	fact := "OPENCODE_CONFIG's mcp.servers.obor, nothing global: servers run " + orNone(strings.Join(labels, ", "))
	switch {
	case slices.Equal(labels, []string{"start"}):
		report("PASS", "OC-L3", fact+": the per-start entry runs, alone", where)
	case slices.Contains(labels, "start"):
		report("FAIL", "OC-L3", fact+": another obor server runs beside it", where)
	default:
		report("FAIL", "OC-L3", fact+": the per-start entry does not run", where)
	}
}

// ocL4: no global obor.js link (what an obor before v0.25 wrote; setup no longer removes it, T-835) and obor.js set up
// exactly once, from the config's package dir (<kit>/opencode, its main obor.js).
func ocL4(l *lab, link, where string) {
	if _, err := os.Lstat(link); err == nil {
		report("FAIL", "OC-L4", "a global "+link+" is there: opencode would set up that copy, not the start's", where)
		return
	}
	js := filepath.Join(l.kit, "opencode", "obor.js")
	loads := l.markerLoads()
	n := countURL(loads, js)
	fact := fmt.Sprintf("no global plugins/obor.js; the start's plugins (file://<kit>/opencode) set up obor.js %d time(s)%s, %d setup(s) in all",
		n, l.pluginWarning(filepath.Join(l.kit, "opencode")), len(loads))
	switch {
	case n == 1 && len(loads) == 1:
		report("PASS", "OC-L4", fact+": the per-start plugin, once", where)
	case n > 1:
		report("FAIL", "OC-L4", fact+": set up more than once", where)
	default:
		report("FAIL", "OC-L4", fact, where)
	}
}

// pluginWarning is opencode's log line about the configured plugin at path (scratch XDG_DATA_HOME's log), as
// " (opencode: <message>)"; "" when it logged none.
func (l *lab) pluginWarning(path string) string {
	b, _ := os.ReadFile(l.opencodeLog())
	re := regexp.MustCompile(`message="([^"]*)" target=` + regexp.QuoteMeta(path))
	if m := re.FindSubmatch(b); m != nil {
		return " (opencode: " + string(m[1]) + ")"
	}
	return ""
}

type markerLoad struct {
	PID int    `json:"pid"`
	URL string `json:"url"`
	Dir string `json:"dir"`
}

// markerLoads are the marker's setups so far (after a settle: plugins set up lazily).
func (l *lab) markerLoads() []markerLoad {
	time.Sleep(3 * time.Second)
	f, err := os.Open(filepath.Join(l.kit, "out", "plugin-marker.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var loads []markerLoad
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m markerLoad
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			loads = append(loads, m)
		}
	}
	return loads
}

func countURL(loads []markerLoad, path string) int {
	n := 0
	for _, m := range loads {
		if strings.TrimPrefix(m.URL, "file://") == path || same(strings.TrimPrefix(m.URL, "file://"), path) {
			n++
		}
	}
	return n
}

// serverLabels are the labels of the MCP servers the start ran ("start" for the generated config's `obor mcp`), after the
// first one shows and a settle.
func (l *lab) serverLabels() []string {
	l.waitRecords("mcp", "*", 30*time.Second)
	time.Sleep(3 * time.Second)
	var labels []string
	for _, r := range l.records("mcp", "*") {
		label := r.Name
		if label == "" {
			label = "start"
		}
		labels = append(labels, label)
	}
	slices.Sort(labels)
	return slices.Compact(labels)
}

// opencodeStart starts opencode in pane (H-L1); false when it did not come up (reported).
func (l *lab) opencodeStart(pane string, argv []string, mode string) bool {
	t0 := time.Now()
	s := l.start("conf-opencode", "opencode", pane, argv, 30*time.Second)
	ready := l.readyAfter(s, t0, "idle")
	reportStart("opencode "+mode, s, ready)
	if l.atShell(pane) {
		report("FAIL", "OC-L1", mode+": opencode did not start: "+tail(l.screen(pane), 6), at(opencodeGo, "func start("))
		return false
	}
	return true
}

// ocL1: under --standalone the obor server of the start's config is the TUI's grandchild, sees the pane env, runs in the lane.
func ocL1(l *lab, pane string, mcp []record) {
	where := "F1.1, " + at("internal/config/models.go", "--standalone") + ", CAP §4.2"
	if len(mcp) == 0 {
		report("FAIL", "OC-L1", "the start's mcp.servers.obor entry ran no server in 30s", where)
		return
	}
	r := mcp[0]
	anc := ancestors(r.PID)
	_, fg := l.fg(pane)
	grandchild := false
	if len(anc) > 1 {
		for _, p := range fg {
			grandchild = grandchild || p.pid == anc[1].pid
		}
	}
	var bad []string
	if !grandchild {
		bad = append(bad, "not the TUI's grandchild")
	}
	for k, v := range map[string]string{agents.PaneVar: pane, "OBOR_ROLE": "worker", "OBOR_LANE": "L-01", "OBOR_CONF_PANE_ONLY": "1"} {
		if r.Env[k] != v {
			bad = append(bad, fmt.Sprintf("%s=%q (want %s)", k, r.Env[k], v))
		}
	}
	if !same(r.Cwd, l.lane) {
		bad = append(bad, "cwd "+r.Cwd+", not the lane")
	}
	slices.Sort(bad)
	fact := "--standalone: the obor server's parents " + chain(anc, 3)
	if len(bad) > 0 {
		report("FAIL", "OC-L1", fact+": "+strings.Join(bad, "; "), where)
		return
	}
	report("PASS", "OC-L1", fact+": the TUI's grandchild, with the pane's env (OBOR_PANE, OBOR_*, a pane-only var), cwd = lane", where)
}

// ocTurn (TURN=1): a role agent (OBOR_ROLE=researcher) in the main checkout, OBOR_OPENCODE_MODEL from
// CONF_OPENCODE_MODEL, one turn: what obor.js's hooks see (OC-L2), the execution events reaching it (OC-L5).
func ocTurn(l *lab, pane string, argv []string, xdg map[string]string, st agents.Stop) {
	where2, where5 := "F1.3, P5, opencode/obor.js", at(opencodeGo, "const ModelVar")+", opencode/obor.js"
	model := os.Getenv("CONF_OPENCODE_MODEL")
	if model == "" {
		report("SKIP", "OC-L2", "TURN=1 needs CONF_OPENCODE_MODEL=<provider/model> (a model the copied opencode auth can call)", where2)
		report("SKIP", "OC-L5", "TURN=1 needs CONF_OPENCODE_MODEL=<provider/model>", where5)
		return
	}
	l.fresh()
	line := "cd " + shq(l.main) + " && unset OBOR_LANE && export OBOR_ROLE=researcher " + ocModelVar + "=" + shq(model)
	if _, err := l.shell(pane, line, 10*time.Second); err != nil || !l.opencodeStart(pane, argv, "role agent, main checkout") {
		report("SKIP", "OC-L2", "opencode did not start for the turn", where2)
		report("SKIP", "OC-L5", "opencode did not start for the turn", where5)
		return
	}
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": "Reply with exactly OK"}, 15*time.Second); err != nil {
		report("FAIL", "OC-L5", "agent.prompt: "+err.Error(), where5)
	}
	stops := l.waitRecords("hook", "stop", 120*time.Second)
	hooks := l.records("hook", "*")
	if len(hooks) == 0 {
		report("FAIL", "OC-L2", "no obor hook ran for the turn", where2)
	} else if role := hooks[0].Env["OBOR_ROLE"]; role != "researcher" {
		report("FAIL", "OC-L2", fmt.Sprintf("obor.js's hook (%s) sees OBOR_ROLE=%q: a role agent's guard runs as role none in the main checkout", hooks[0].Name, role), where2)
	} else {
		report("PASS", "OC-L2", "obor.js's hooks see OBOR_ROLE=researcher for a role agent in the main checkout", where2)
	}
	var seen []string
	for _, h := range hooks {
		seen = append(seen, h.Name)
	}
	slices.Sort(seen)
	if len(stops) == 0 {
		report("FAIL", "OC-L5", "no stop hook (session.execution.succeeded) in 120s; hooks run: "+orNone(strings.Join(slices.Compact(seen), " ")), where5)
	} else {
		report("PASS", "OC-L5", "session.execution.* reach obor.js (hooks "+strings.Join(slices.Compact(seen), " ")+")", where5)
		report("SKIP", "OC-L5", "the model switch ("+ocModelVar+") is not read back: the turn's answer does not name the model it ran on", where5)
	}
	if _, err := l.quit(pane, st); err != nil {
		l.stopAgent(pane)
	}
}

// opencodeAuth copies the user's opencode credentials into the scratch XDG_DATA_HOME's store (a turn needs one; none to
// copy: the turn fails on its own). opencode 2.2 keeps them in its database's credential table, not auth.json.
func (l *lab) opencodeAuth(xdg map[string]string) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	src := filepath.Join(data, "opencode", "opencode.db")
	dst := filepath.Join(xdg["XDG_DATA_HOME"], "opencode", "opencode.db")
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Stat(dst); err != nil {
		return // the scratch store exists once the first start wrote it
	}
	from, err := store.OpenRO(src)
	if err != nil {
		return
	}
	defer from.Close()
	rows, err := from.Query("SELECT id, integration_id, label, value, connector_id, method_id, active, time_created, time_updated FROM credential")
	if err != nil {
		return
	}
	defer rows.Close()
	to, err := sql.Open("sqlite", "file:"+dst)
	if err != nil {
		return
	}
	defer to.Close()
	for rows.Next() {
		var id, integration, label, value string
		var connector, method sql.NullString
		var active sql.NullInt64
		var created, updated int64
		if rows.Scan(&id, &integration, &label, &value, &connector, &method, &active, &created, &updated) != nil {
			continue
		}
		_, _ = to.Exec("INSERT OR REPLACE INTO credential (id, integration_id, label, value, connector_id, method_id, active, time_created, time_updated) VALUES (?,?,?,?,?,?,?,?,?)",
			id, integration, label, value, connector, method, active, created, updated)
	}
}

// ocL6 (T-898): opencode starts as a worker of the lab's project with its generated config naming the repository's own
// `obor mcp` (not the stub), and one job_next call's rendered reply reaches the session's transcript — opencode's own
// store, the database its plugin reports as transcript_path (opencode 2.x keeps a session's messages there,
// session_message). The start is the shipped launch's (launchOf: its generated config and model env included), so a tool
// the start does not wire is a FAIL, not a pass the case's own flags bought (RV-122 #1, T-910). A client that cannot
// list the server's tools, or cannot call one, leaves no such result. Runs after ocTurn, so it sets the lane worker's
// environment again (ocTurn left the main checkout as a researcher).
func ocL6(l *lab, pane string, xdg map[string]string, st agents.Stop) {
	where := at("internal/mcp/mcp.go", "func Serve") + ", " + at(opencodeGo, "func Config") + ", T-898"
	real, err := l.realKit()
	if err != nil {
		report("FAIL", "OC-L6", "the repository's own obor is not built: "+err.Error(), where)
		return
	}
	if err := l.startStubServe(); err != nil {
		report("FAIL", "OC-L6", err.Error(), where)
		return
	}
	model, ok := liveModel("opencode")
	if !ok {
		report("FAIL", "OC-L6", "the config's built-in model table has no opencode model", at("internal/config/models.go", "var BuiltinModels")+", T-910")
		return
	}
	start, err := l.launchOf("opencode", model, real, func(name string, data []byte) (string, error) {
		path := filepath.Join(l.root, "state", "mcp-"+name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		return path, os.WriteFile(path, data, 0o644)
	})
	if err != nil || start.env[ocConfigVar] == "" {
		report("FAIL", "OC-L6", fmt.Sprintf("the launch of %s names no generated config (%v, %v)", model, start.env, err),
			at("internal/config/models.go", "func (m Model) Launch")+", T-910")
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", agents.PaneVar: pane}
	for name, v := range start.env {
		paneEnv[name] = v
	}
	for name, v := range xdg {
		paneEnv[name] = v
	}
	l.stopAgent(pane)
	if code, err := l.shell(pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), 10*time.Second); err != nil || code != 0 {
		report("FAIL", "OC-L6", fmt.Sprintf("the env line failed (status %d, %v)", code, err), at(launchGo, "func envLine"))
		return
	}
	argv := start.args // the launch's own argv: a plugin kind's start is one call (launchOf)
	t0 := time.Now()
	s := l.start("conf-opencode-mcp", "opencode", pane, argv, 30*time.Second)
	reportStart("opencode real mcp", s, l.readyAfter(s, t0, "idle"))
	if l.atShell(pane) {
		report("FAIL", "OC-L6", "opencode did not start for the real server: "+tail(l.screen(pane), 6), where)
		return
	}
	if _, err := l.call("agent.prompt", map[string]any{"target": pane, "text": mcpPrompt}, 15*time.Second); err != nil {
		report("FAIL", "OC-L6", "agent.prompt: "+err.Error(), where)
		l.stopAgent(pane)
		return
	}
	dir := filepath.Join(xdg["XDG_DATA_HOME"], "opencode")
	var readErr error
	text, ok := waitTranscript(180*time.Second, func() string {
		var s string
		s, readErr = opencodeParts(dir)
		return s
	}, mcpJobHead, mcpMarker)
	poll(60*time.Second, func() bool { a, err := l.agent(pane); return err == nil && a.Status == "idle" })
	if code, err := l.quit(pane, st); err != nil || code != 0 {
		l.stopAgent(pane)
	}
	fact := l.mcpFact("the real `obor mcp`'s job_next result reached opencode's session database", text, ok)
	if !ok && readErr != nil {
		fact += "; the store could not be read: " + readErr.Error()
	}
	if ok {
		report("PASS", "OC-L6", fact, where)
	} else {
		report("FAIL", "OC-L6", fact, where)
	}
}

// opencodeParts is every stored message of the session databases under dir (opencode 2.x: session_message, one JSON
// value per message whose content holds the assistant's tool parts; the plugin reports that database as the session's
// transcript_path), and why none could be read when the store could not be.
func opencodeParts(dir string) (string, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.db"))
	if len(files) == 0 {
		return "", fmt.Errorf("no database under %s", dir)
	}
	var b strings.Builder
	var firstErr error
	for _, f := range files {
		db, err := store.OpenRO(f)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rows, err := db.Query("SELECT data FROM session_message")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			db.Close()
			continue
		}
		for rows.Next() {
			var data string
			if rows.Scan(&data) == nil {
				b.WriteString(data)
				b.WriteString("\n")
			}
		}
		rows.Close()
		db.Close()
	}
	return b.String(), firstErr
}

// opencodeLog is the scratch XDG_DATA_HOME's opencode log.
func (l *lab) opencodeLog() string {
	return filepath.Join(l.root, "xdg", "data", "opencode", "log", "opencode.log")
}
