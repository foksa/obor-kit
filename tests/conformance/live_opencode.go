//go:build conformance

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/opencode"
)

// opencodeLive: on scratch XDG dirs holding what `obor setup agents` writes (the global mcp.servers.obor entry and the
// obor.js link, from the driver's Install), three --standalone starts in the lane: as set up (OC-L1, H-L1, OC-L5's
// quit), with OPENCODE_CONFIG_CONTENT (OC-L3, OC-L4) and with OPENCODE_CONFIG while a marker plugin is also linked
// globally (OC-L3, OC-L4). With TURN=1 a role agent in the main checkout takes one turn (OC-L2, OC-L5).
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
	if err := writeKey(inst.MCP.Path, inst.MCP.Key, inst.MCP.Entry); err != nil {
		report("FAIL", "OC-L1", "the scratch global config: "+err.Error(), "deep dive §6")
		return
	}
	if inst.Plugin != nil {
		_ = os.MkdirAll(filepath.Dir(inst.Plugin.To), 0o755)
		if err := os.Symlink(inst.Plugin.From, inst.Plugin.To); err != nil {
			report("FAIL", "OC-L1", "the scratch obor.js link: "+err.Error(), "deep dive §6")
			return
		}
	}
	marker, err := l.markerPlugin()
	if err != nil {
		report("FAIL", "OC-L4", "the marker plugin: "+err.Error(), "deep dive §6")
		return
	}
	pane, err := l.workspace("conf", l.lane)
	if err != nil {
		report("FAIL", "OC-L1", "no workspace in the throwaway session: "+err.Error(), "deep dive §6")
		return
	}
	paneEnv := map[string]string{"OBOR_ROLE": "worker", "OBOR_LANE": "L-01", "OBOR_CONF_PANE_ONLY": "1"}
	for k, v := range xdg {
		paneEnv[k] = v
	}
	if st, err := l.shell(pane, "cd "+shq(l.lane)+" && "+exports(paneEnv), 10*time.Second); err != nil || st != 0 {
		report("FAIL", "OC-L1", fmt.Sprintf("the env line failed (status %d, %v)", st, err), at(launchGo, "func envLine"))
		return
	}
	st := k.Stop()
	where5 := at(opencodeGo, "func (kind) Stop") + ", CAP §4.6"
	where4 := "P1, CAP §4.3"
	argv := k.Start(agents.Start{Args: []string{"--standalone"}})

	// 1: as obor sets it up.
	if l.opencodeStart(pane, argv, "as set up") {
		mcp := l.waitRecords("mcp", "", 30*time.Second)
		ocL1(l, pane, mcp)
		if code, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil || code != 0 {
			report("FAIL", "OC-L5", fmt.Sprintf("%s + a second Enter: exit status %d (%v)", st.Quit, code, orOK(err)), where5)
			l.stopAgent(pane)
		} else {
			report("PASS", "OC-L5", st.Quit+" + a second Enter exits with status 0", where5)
		}
	}

	// 2: OPENCODE_CONFIG_CONTENT naming another obor command and the marker as a file and as a package dir.
	content := envConfig(l.kit, "content", "file://"+marker.file, "file://"+marker.dir)
	l.fresh()
	if _, err := l.shell(pane, "export OPENCODE_CONFIG_CONTENT="+shq(content), 10*time.Second); err == nil && l.opencodeStart(pane, argv, "OPENCODE_CONFIG_CONTENT") {
		ocL3("OPENCODE_CONFIG_CONTENT", "content", l.serverLabels())
		loads := l.markerLoads()
		fileN, dirN := countURL(loads, marker.file), countURL(loads, filepath.Join(marker.dir, "index.js"))
		fact := fmt.Sprintf(`"plugins" in OPENCODE_CONFIG_CONTENT under --standalone: file://…/conf-marker.js set up %d time(s)%s; file://<package dir> set up %d time(s)`,
			fileN, l.pluginWarning(marker.file), dirN)
		switch {
		case fileN > 0:
			report("PASS", "OC-L4", fact+": a plain .js file loads", where4)
		case dirN > 0:
			report("WARN", "OC-L4", fact+": only a package dir loads, so P1 ships obor.js in a dir with a package.json", where4)
		default:
			report("FAIL", "OC-L4", fact+": no plugin from the env config loads", where4)
		}
		if _, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil {
			l.stopAgent(pane)
		}
	}

	// 3: OPENCODE_CONFIG (a file) naming the package dir, and the same plugin (its id) also in the global plugins dir,
	// as setup links obor.js there.
	file := filepath.Join(l.root, "opencode-conf.json")
	global := filepath.Join(xdg["XDG_CONFIG_HOME"], "opencode", "plugins", "conf-marker.js")
	l.fresh()
	logFrom := l.opencodeLogSize()
	js, _ := os.ReadFile(marker.file)
	if err := os.WriteFile(file, []byte(envConfig(l.kit, "file", "file://"+marker.dir)), 0o644); err != nil {
		report("FAIL", "OC-L3", err.Error(), "deep dive §6")
	} else if err := os.WriteFile(global, js, 0o644); err != nil {
		report("FAIL", "OC-L4", err.Error(), "deep dive §6")
	} else if _, err := l.shell(pane, "unset OPENCODE_CONFIG_CONTENT && export OPENCODE_CONFIG="+shq(file), 10*time.Second); err == nil && l.opencodeStart(pane, argv, "OPENCODE_CONFIG") {
		ocL3("OPENCODE_CONFIG", "file", l.serverLabels())
		loads := l.markerLoads()
		dirN, globalN := countURL(loads, filepath.Join(marker.dir, "index.js")), countURL(loads, global)
		log := l.opencodeLogSince(logFrom)
		loading := func(id string) int { return strings.Count(log, `msg="loading plugin" id=`+id+" ") }
		fact := fmt.Sprintf("one plugin id in OPENCODE_CONFIG's plugins (package dir) and in the global plugins dir: "+
			"opencode logs loading the package %d time(s), the global file %d time(s); setup ran %d time(s) from the package, %d from the global file",
			loading(marker.dir), loading(global), dirN, globalN)
		switch {
		case dirN > 0 && globalN > 0:
			report("WARN", "OC-L4", fact+": set up twice, so P1 removes the global link", where4)
		case globalN == 1 && dirN == 0:
			report("WARN", "OC-L4", fact+": one setup per plugin id and the global copy wins, so P1 must remove the global link or the per-launch plugin never runs", where4)
		case dirN == 1 && globalN == 0:
			report("PASS", "OC-L4", fact+": one setup per plugin id, the env config's", where4)
		default:
			report("FAIL", "OC-L4", fact, where4)
		}
		if _, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil {
			l.stopAgent(pane)
		}
		_, _ = l.shell(pane, "unset OPENCODE_CONFIG", 10*time.Second)
	}
	_ = os.Remove(global)

	if !turn {
		report("SKIP", "OC-L2", "needs TURN=1: obor.js runs a hook only for a session's events, which need a turn", "F1.3, P5")
		report("SKIP", "OC-L5", "session.execution.* reaching the plugin and OBOR_OPENCODE_MODEL's switch need TURN=1", at(opencodeGo, "const ModelVar"))
		return
	}
	ocTurn(l, pane, argv, xdg, st)
}

// writeKey writes {key[0]: {key[1]: … value}} as the JSON file path (what setup merges into a user config).
func writeKey(path string, key []string, value any) error {
	v := value
	for i := len(key) - 1; i >= 0; i-- {
		v = map[string]any{key[i]: v}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// envConfig is an opencode config that registers obor as the stub labelled label and loads plugins.
func envConfig(kit, label string, plugins ...string) string {
	b, _ := json.Marshal(map[string]any{
		"mcp":     map[string]any{"servers": map[string]any{"obor": map[string]any{"type": "local", "command": []string{filepath.Join(kit, "bin", "obor"), "mcp", label}}}},
		"plugins": plugins,
	})
	return string(b)
}

// marker is a server plugin that appends {pid, url, dir} to out/plugin-marker.jsonl from its setup: as a plain file
// (marker/conf-marker.js) and as a package dir (marker/pkg: package.json and index.js), one plugin id.
type markerPaths struct{ file, dir string }

func (l *lab) markerPlugin() (markerPaths, error) {
	m := markerPaths{file: filepath.Join(l.root, "marker", "conf-marker.js"), dir: filepath.Join(l.root, "marker", "pkg")}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return m, err
	}
	js := fmt.Sprintf(`// obor conformance: records each setup of this plugin.
import fs from "node:fs";

export default {
  id: "obor.conformance-marker",
  setup(ctx) {
    fs.appendFileSync(%q, JSON.stringify({ pid: process.pid, url: import.meta.url, dir: ctx?.location?.directory ?? "" }) + "\n");
  },
};
`, filepath.Join(l.kit, "out", "plugin-marker.jsonl"))
	pkg := `{"name": "obor-conformance-marker", "version": "0.0.1", "type": "module", "main": "index.js", "exports": {".": "./index.js"}}` + "\n"
	for path, text := range map[string]string{m.file: js, filepath.Join(m.dir, "index.js"): js, filepath.Join(m.dir, "package.json"): pkg} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return m, err
		}
	}
	return m, nil
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

// serverLabels are the labels of the MCP servers the start ran ("" for the global entry's `obor mcp`), after the
// first one shows and a settle.
func (l *lab) serverLabels() []string {
	l.waitRecords("mcp", "*", 30*time.Second)
	time.Sleep(3 * time.Second)
	var labels []string
	for _, r := range l.records("mcp", "*") {
		label := r.Name
		if label == "" {
			label = "global"
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
		report("FAIL", "OC-L1", mode+": opencode did not start: "+tail(l.screen(pane), 6), at(opencodeGo, "func (kind) Start"))
		return false
	}
	return true
}

// ocL1: under --standalone the obor server set up globally is the TUI's grandchild, sees the pane env, runs in the lane.
func ocL1(l *lab, pane string, mcp []record) {
	where := "F1.1, " + at("internal/config/models.go", "--standalone") + ", CAP §4.2"
	if len(mcp) == 0 {
		report("FAIL", "OC-L1", "the global mcp.servers.obor entry ran no server in 30s", where)
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
	for k, v := range map[string]string{"HERDR_PANE_ID": pane, "OBOR_ROLE": "worker", "OBOR_LANE": "L-01", "OBOR_CONF_PANE_ONLY": "1"} {
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
	report("PASS", "OC-L1", fact+": the TUI's grandchild, with the pane's env (HERDR_PANE_ID, OBOR_*, a pane-only var), cwd = lane", where)
}

// ocL3: which obor command runs when the env config names one and the global config another.
func ocL3(how, label string, labels []string) {
	where := at(opencodeGo, "func (kind) Install") + ", P1, CAP §4.2"
	fact := fmt.Sprintf("%s's mcp.servers.obor over the global one: servers run %s", how, orNone(strings.Join(labels, ", ")))
	switch {
	case slices.Equal(labels, []string{label}):
		report("PASS", "OC-L3", fact+": the env config's command replaces the global entry", where)
	case slices.Contains(labels, label):
		report("WARN", "OC-L3", fact+": both commands run", where)
	case slices.Contains(labels, "global"):
		report("FAIL", "OC-L3", fact+": the global entry wins, the env config's is ignored", where)
	default:
		report("FAIL", "OC-L3", fact+": no obor server ran", where)
	}
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
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	if b, err := os.ReadFile(filepath.Join(data, "opencode", "auth.json")); err == nil {
		dst := filepath.Join(xdg["XDG_DATA_HOME"], "opencode", "auth.json")
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		l.cleanups = append(l.cleanups, func() { _ = os.Remove(dst) })
		_ = os.WriteFile(dst, b, 0o600)
	}
	l.fresh()
	line := "cd " + shq(l.main) + " && unset OBOR_LANE && export OBOR_ROLE=researcher " + opencode.ModelVar + "=" + shq(model)
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
		report("PASS", "OC-L5", "session.execution.* reach obor.js (hooks "+strings.Join(slices.Compact(seen), " ")+"); the model switch is not read back", where5)
	}
	if _, err := l.quit(pane, st.Quit, st.SecondEnter); err != nil {
		l.stopAgent(pane)
	}
}

// opencodeLog is the scratch XDG_DATA_HOME's opencode log.
func (l *lab) opencodeLog() string {
	return filepath.Join(l.root, "xdg", "data", "opencode", "log", "opencode.log")
}

func (l *lab) opencodeLogSize() int64 {
	fi, err := os.Stat(l.opencodeLog())
	if err != nil {
		return 0
	}
	return fi.Size()
}

// opencodeLogSince is the log after byte off.
func (l *lab) opencodeLogSince(off int64) string {
	b, _ := os.ReadFile(l.opencodeLog())
	if off > int64(len(b)) {
		return ""
	}
	return string(b[off:])
}
