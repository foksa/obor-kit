// Package claude is the Claude Code bridge (agents.Kind): its start argv (--settings, obor's --mcp-config on a strict
// start, --resume), the settings file and the MCP file of a start that trims the user's settings, its quit, its
// headless flag, its plugin manifest, its transcripts and its session inbox.
package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
)

func init() { agents.Register(kind{}) }

type kind struct{}

const name = "claude"

func (kind) Name() string { return name }

// StrictFlag is Claude's flag that hides every MCP server the command line does not name: the plugin manifest's obor
// server included.
const StrictFlag = "--strict-mcp-config"

// MCPConfig is the --mcp-config JSON of this kit's obor server, as the plugin manifest declares it but with the kit's
// path spelled out (a strict start has no ${CLAUDE_PLUGIN_ROOT}) and the pin, so bin/obor runs this kit. The
// OBOR_* variables need no entry: Claude passes its own environment to the server.
func MCPConfig(kit string) string {
	b, _ := json.Marshal(map[string]any{serversKey: map[string]any{"obor": oborServer(kit)}})
	return string(b)
}

// oborServer is the kit's obor server as an mcpServers entry.
func oborServer(kit string) map[string]any {
	return map[string]any{
		"command": filepath.Join(kit, "bin", "obor"),
		"args":    []string{"mcp"},
		"env":     map[string]string{agents.Pin: "1"},
	}
}

// serversKey is the map of MCP server name → definition, in ~/.claude.json, a .mcp.json and an --mcp-config file.
const serversKey = "mcpServers"

// Start is the type's args (with in.Effort: their --effort replaced by --effort <effort>), then
// --strict-mcp-config for a lane's agent (in.StrictMCP: once, also when the args name it) or `--mcp-config
// <in.MCPFile> --strict-mcp-config` for a start with Prepare's MCP file (the file's servers alone), then the
// settings: --settings <in.Settings> as inline JSON, or for a start with Prepare's settings file `--setting-sources
// project,local --settings <in.SettingsFile>` (Claude loads the project's settings and that file, never the
// user's own, so what the file leaves out of the user's settings is off; --setting-sources as the args name it,
// when they do), then --session-id <in.Session> (a fresh start obor names) or --resume <sid>, with obor's
// --mcp-config pair spliced in before the strict flag (the flag stops --mcp-config's variadic value), then one `--`
// when a config flag could take the prompt, then the prompt. The prompt is positional, never a config value; a
// resume carries none. The only `--` is obor's own (config validate refuses one in the args).
//
// The project's committed .claude/settings.json in in.Dir outranks obor's policy: a project that opts into its
// .mcp.json servers (enableAllProjectMcpServers true, or a non-empty enabledMcpjsonServers, which also approves them,
// so an unattended lane does not sit on the approval prompt) starts without the strict flag, obor's server then the
// plugin manifest's as for a role (a start with an MCP file is strict all the same: the file holds those servers); a
// settings key the file sets is left out of --settings (no --settings when it sets
// all). A file that does not parse counts as absent (config validate warns).
func (kind) Start(in agents.Start) []string {
	proj, _ := projectSettings(in)
	args := withEffort(in.Args, in.Effort)
	switch {
	case in.MCPFile != "":
		args = withStrict(append(args, "--mcp-config", in.MCPFile))
	case in.StrictMCP && !proj.mcpOptIn():
		args = withStrict(args)
	}
	if in.SettingsFile != "" {
		if !hasFlag(args, "--setting-sources") {
			args = append(args, "--setting-sources", "project,local")
		}
		args = append(args, "--settings", in.SettingsFile)
	} else if settings := proj.unset(in.Settings); len(settings) > 0 {
		args = append(args, "--settings", compact(settings))
	}
	prompt := in.Prompt
	if in.Resume != "" {
		args = append(args, "--resume", in.Resume)
		prompt = ""
	} else if in.Session != "" {
		args = append(args, "--session-id", in.Session)
	}
	p := parse(args)
	out := make([]string, 0, len(args)+4)
	if p.needsObor(in.Kit, in.Read) {
		out = append(out, args[:p.strict]...)
		out = append(out, "--mcp-config", MCPConfig(in.Kit))
		out = append(out, args[p.strict:]...)
		p.hasConf = true
	} else {
		out = append(out, args...)
	}
	if p.hasConf && prompt != "" {
		out = append(out, "--")
	}
	if prompt != "" {
		out = append(out, prompt)
	}
	return out
}

// SettingsFile is the project's committed settings file, relative to the start dir.
const SettingsFile = ".claude/settings.json"

// settings are the keys of a project's settings file (nil: no file, or one that does not parse).
type settings map[string]json.RawMessage

// projectSettings is the start dir's SettingsFile; err: it is there but does not read or parse (nil settings then).
func projectSettings(in agents.Start) (settings, error) { return projectFile(in, SettingsFile) }

// projectFile is the settings file rel of the start's dir; nil settings: no dir, no reader, no such file, or one
// that does not read or parse (err then).
func projectFile(in agents.Start, rel string) (settings, error) {
	if in.Dir == "" {
		return nil, nil
	}
	return settingsFile(in.Read, filepath.Join(in.Dir, rel))
}

// mcpOptIn: the project opts into its .mcp.json servers and approves them (enableAllProjectMcpServers true, or a
// non-empty enabledMcpjsonServers).
func (s settings) mcpOptIn() bool {
	var names []json.RawMessage
	return s.approvesAll() || json.Unmarshal(s["enabledMcpjsonServers"], &names) == nil && len(names) > 0
}

// approvesAll: enableAllProjectMcpServers is true.
func (s settings) approvesAll() bool {
	var all bool
	return json.Unmarshal(s["enableAllProjectMcpServers"], &all) == nil && all
}

// approves: the project approves its .mcp.json server name (approvesAll, or enabledMcpjsonServers names it).
func (s settings) approves(name string) bool {
	var names []string
	return s.approvesAll() || json.Unmarshal(s["enabledMcpjsonServers"], &names) == nil && slices.Contains(names, name)
}

// unset is want without the keys s sets.
func (s settings) unset(want map[string]any) map[string]any {
	out := make(map[string]any, len(want))
	for k, v := range want {
		if _, set := s[k]; !set {
			out[k] = v
		}
	}
	return out
}

// LocalSettingsFile is the project's own uncommitted settings file, relative to the start dir.
const LocalSettingsFile = ".claude/settings.local.json"

// StateSettings is the settings file of agent's start, as a name in the project's state dir; StateMCP its MCP file.
func StateSettings(agent string) string { return "settings/" + agent + ".json" }
func StateMCP(agent string) string      { return "mcp/" + agent + ".json" }

// SettingsOverride and MCPOverride are Claude's override files (agents.OverrideDir): the first is merged over the
// user's settings.json, the second's mcpServers over the MCP servers of the user's ~/.claude.json.
const (
	SettingsOverride = "settings_override.json"
	MCPOverride      = "claude_override.json"
)

func (kind) OverrideFiles() []string { return []string{SettingsOverride, MCPOverride} }

// Prepare (agents.Preparer) is a start that trims the user's settings (in.TrimUser: every agent but a lead) made
// ready: its settings file (merged, in.Settings in it) written as StateSettings(in.Agent) with in.WriteState,
// replacing the agent's last one, and named in in.SettingsFile for Start; and, when MCPOverride names mcpServers, its
// MCP file (servers) written as StateMCP(in.Agent) and named in in.MCPFile. Any other start is in as given. An error:
// the start names no agent or no state writer, or a file cannot be written (it names the file).
func (kind) Prepare(in agents.Start) (agents.Start, error) {
	if !in.TrimUser {
		return in, nil
	}
	if in.Agent == "" || in.WriteState == nil {
		return in, errors.New("claude: a start that trims the user's settings names no agent or no state dir for its settings file")
	}
	write := func(name string, doc map[string]any) (string, error) {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return "", fmt.Errorf("claude: the file %s: %v", name, err)
		}
		path, err := in.WriteState(name, b.Bytes())
		if err != nil {
			return "", fmt.Errorf("claude: writing the file %s of the state dir: %v", name, err)
		}
		return path, nil
	}
	settingsFile, err := write(StateSettings(in.Agent), merged(in))
	if err != nil {
		return in, err
	}
	if patch, ok := in.Override(MCPOverride)[serversKey].(map[string]any); ok {
		if in.MCPFile, err = write(StateMCP(in.Agent), map[string]any{serversKey: servers(in, patch)}); err != nil {
			return in, err
		}
	}
	in.SettingsFile, in.Settings = settingsFile, nil
	return in, nil
}

// pluginsKey is the settings map of plugin id → enabled.
const pluginsKey = "enabledPlugins"

// userDir is the user's Claude config dir: $CLAUDE_CONFIG_DIR, default ~/.claude ("": the start names neither).
func userDir(in agents.Start) string { return agents.UserDir(in, "CLAUDE_CONFIG_DIR", ".claude") }

// merged is the settings file of a start that trims the user's settings, built in this order:
//   - the user's settings file (userDir: settings.json; none, or one that does not parse: empty);
//   - the default: every plugin it enables set false (obor's own stays);
//   - every entry the project's settings set (in.Dir's SettingsFile and LocalSettingsFile) dropped (dropSet): the file
//     ranks above the project's, the user's settings rank below them;
//   - SettingsOverride laid over it (agents.MergePatch): what it sets stays, whatever the project's settings set;
//   - in.Settings, obor's own keys, without those the project's settings set.
//
// The user's skills and the skills Claude syncs from claude.ai do not load without the user source.
func merged(in agents.Start) map[string]any {
	user := map[string]any{}
	if dir := userDir(in); dir != "" {
		user = objectFile(in.Read, filepath.Join(dir, "settings.json"))
	}
	if plugins, ok := user[pluginsKey].(map[string]any); ok {
		for id, v := range plugins {
			if v != false && !isObor(id) {
				plugins[id] = false
			}
		}
	}
	own := agents.MergePatch(in.Settings, nil)
	if in.Dir != "" {
		for _, rel := range []string{SettingsFile, LocalSettingsFile} {
			set := objectFile(in.Read, filepath.Join(in.Dir, rel))
			dropSet(user, set)
			dropSet(own, set)
		}
	}
	return agents.MergePatch(agents.MergePatch(user, in.Override(SettingsOverride)), own)
}

// servers are the MCP servers of a start whose MCPOverride names mcpServers (patch), by name: the user's own (the
// top-level mcpServers of the user's .claude.json, then those it holds for the project's root, in.Root) with patch
// laid over them (agents.MergePatch: a null removes a server), then the servers of in.Dir's .mcp.json the project's
// settings approve (enableAllProjectMcpServers: all; else those enabledMcpjsonServers names), then obor's own (with
// a kit). The start is strict, so these are all it loads.
func servers(in agents.Start, patch map[string]any) map[string]any {
	user := map[string]any{}
	if file := userFile(in); file != "" {
		doc := objectFile(in.Read, file)
		user, _ = doc[serversKey].(map[string]any)
		projects, _ := doc["projects"].(map[string]any)
		if project, ok := projects[in.Root].(map[string]any); ok && in.Root != "" {
			local, _ := project[serversKey].(map[string]any)
			user = agents.MergePatch(user, local)
		}
	}
	out := agents.MergePatch(user, patch)
	if in.Dir != "" {
		proj, _ := projectSettings(in)
		own, _ := objectFile(in.Read, filepath.Join(in.Dir, ".mcp.json"))[serversKey].(map[string]any)
		for name, def := range own {
			if proj.approves(name) {
				out[name] = def
			}
		}
	}
	if in.Kit != "" {
		out["obor"] = oborServer(in.Kit)
	}
	return out
}

// userFile is the user's .claude.json, where Claude keeps the user's MCP servers: in $CLAUDE_CONFIG_DIR when the
// start's environment sets it, else in $HOME ("": it names neither).
func userFile(in agents.Start) string {
	if in.Env == nil {
		return ""
	}
	dir := in.Env("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = in.Env("HOME")
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ".claude.json")
}

// OverrideWarnings (agents.OverrideChecker): a key of MCPOverride other than mcpServers (nothing reads it).
func (kind) OverrideWarnings(in agents.Start) []string {
	var out []string
	doc := in.Override(MCPOverride)
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		if k != serversKey {
			out = append(out, MCPOverride+": "+k+": only "+serversKey+" applies")
		}
	}
	return out
}

// dropSet drops from file every entry set sets: where both hold an object under a key, entry by entry the same way
// (an object left empty goes); where both hold a list the file's stays (Claude adds the lists of its sources up);
// any other value of file's goes.
func dropSet(file, set map[string]any) {
	for k, sv := range set {
		fv, ok := file[k]
		if !ok {
			continue
		}
		fm, fIsMap := fv.(map[string]any)
		sm, sIsMap := sv.(map[string]any)
		_, fIsList := fv.([]any)
		_, sIsList := sv.([]any)
		switch {
		case fIsMap && sIsMap:
			if dropSet(fm, sm); len(fm) == 0 {
				delete(file, k)
			}
		case fIsList && sIsList:
		default:
			delete(file, k)
		}
	}
}

// objectFile is the JSON object of the file at path, read with read, its numbers as written; an empty one: no reader,
// no such file, or one that does not read or parse as an object.
func objectFile(read agents.ReadFile, path string) map[string]any {
	out := map[string]any{}
	if read == nil {
		return out
	}
	b, err := read(path)
	if err != nil {
		return out
	}
	m, err := agents.JSONObject(b)
	if err != nil {
		return out
	}
	return m
}

// isObor: id (<plugin>@<marketplace>) is obor's own plugin, from any marketplace.
func isObor(id string) bool {
	plugin, _, _ := strings.Cut(id, "@")
	return plugin == "obor"
}

// settingsFile is the settings file at path, read with read; nil settings: no reader, no such file, or one that does
// not read or parse (err then, unless it is absent).
func settingsFile(read agents.ReadFile, path string) (settings, error) {
	if read == nil {
		return nil, nil
	}
	b, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return s, nil
}

// ProjectWarnings: a settings file that does not parse (obor's start flags then apply as without it), and
// disableAllHooks true (it turns off the plugin's hooks, obor's guards among them).
func (kind) ProjectWarnings(dir string, read agents.ReadFile) []string {
	s, err := projectSettings(agents.Start{Dir: dir, Read: read})
	if err != nil {
		return []string{SettingsFile + ": does not parse (" + err.Error() + "): obor's start flags apply as if it were absent"}
	}
	var off bool
	if json.Unmarshal(s["disableAllHooks"], &off) == nil && off {
		return []string{SettingsFile + ": disableAllHooks is true: it turns off obor's guard hooks (the plugin's hooks)"}
	}
	return nil
}

// efforts are Claude Code's --effort values, as `claude --help` lists them (2.x, 2026-11-02: "Effort level for the
// current session (low, medium, high, xhigh, max)").
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

func (kind) Efforts() []string { return efforts }

// StartEnv: none; the effort is a flag.
func (kind) StartEnv(agents.Start) (map[string]string, error) { return nil, nil }

// Vars: none (StartEnv sets none).
func (kind) Vars() []string { return nil }

// WithModel: --model <model> in place of the args' own.
func (kind) WithModel(args []string, model string) []string {
	return agents.SetFlag(args, []string{"--model"}, model)
}

// withEffort is args without their --effort (either spelling), then --effort effort; args as they are for effort "".
func withEffort(args []string, effort string) []string {
	if effort == "" {
		return append([]string(nil), args...)
	}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--effort" && i+1 < len(args):
			i++
		case strings.HasPrefix(a, "--effort="):
		default:
			out = append(out, a)
		}
	}
	return append(out, "--effort", effort)
}

// withStrict is args with the strict flag last, unless they already name it (either spelling): a lane's agent starts
// with obor's MCP server alone.
func withStrict(args []string) []string {
	if hasFlag(args, StrictFlag) {
		return args
	}
	return append(args, StrictFlag)
}

// hasFlag: args name flag, either spelling (`flag` or `flag=<value>`).
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// compact is v as one line of JSON, HTML characters as they are (config.Compact).
func compact(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// args is a start's args parsed once: where --strict-mcp-config sits, whether an --mcp-config flag is there and every
// value of them.
type args struct {
	strict  int      // index of --strict-mcp-config (either spelling), -1 when none
	hasConf bool     // an --mcp-config flag is there
	configs []string // the --mcp-config values, in either spelling
}

// parse reads a once. --mcp-config takes several values, so every value after a flag counts up to the next option
// `--mcp-config=<v>` takes the same variadic tail.
func parse(a []string) args {
	p := args{strict: -1}
	for i := 0; i < len(a); i++ {
		w := a[i]
		if w == StrictFlag || strings.HasPrefix(w, StrictFlag+"=") {
			if p.strict < 0 {
				p.strict = i
			}
			continue
		}
		v, eq := strings.CutPrefix(w, "--mcp-config=")
		if !eq && w != "--mcp-config" {
			continue
		}
		p.hasConf = true
		if eq {
			p.configs = append(p.configs, v)
		}
		for i+1 < len(a) && !strings.HasPrefix(a[i+1], "-") {
			i++
			p.configs = append(p.configs, a[i])
		}
	}
	return p
}

// needsObor: the start is strict, has a kit to name and its configs name no obor server (an explicit one wins).
func (p args) needsObor(kit string, read agents.ReadFile) bool {
	if kit == "" || p.strict < 0 {
		return false
	}
	for _, value := range p.configs {
		if namesObor(value, read) {
			return false
		}
	}
	return true
}

// namesObor: value, a --mcp-config value (inline JSON, or the path of a file of it), registers a server named obor.
// A value that is neither JSON nor a file read can read names none.
func namesObor(value string, read agents.ReadFile) bool {
	b := []byte(value)
	if !json.Valid(b) {
		var err error
		if read == nil {
			return false
		}
		if b, err = read(value); err != nil {
			return false
		}
	}
	var f struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(b, &f) != nil {
		return false
	}
	_, ok := f.Servers["obor"]
	return ok
}

func (kind) NotReady(string) string { return "" }

// Stop: /exit. Escape puts Claude's interrupted prompt back into the input box, and /exit typed after it would be
// sent as part of that text (live 2026-10-13): Ctrl+C clears the whole input (it only arms "press again to exit").
// With background tasks running, /exit opens a dialog ("Exit and stop tasks / Move to background / Stay"), its
// options numbered. SecondEnter: with many slash commands the menu over "/exit" can take the Enter (owner
// 2026-11-02); a second Enter after the first poll submits it (never into the dialog).
func (kind) Stop() agents.Stop {
	return agents.Stop{Quit: "/exit", SecondEnter: true, AfterEscape: []string{"ctrl+c"}, ClearInput: []string{"ctrl+u"}, Dialog: &exitDialog}
}

var exitDialog = agents.Dialog{Option: "Exit and stop tasks", Key: regexp.MustCompile(`([1-9])[.)] *Exit and stop tasks`)}

// Headless: claude has no headless subcommand; its headless form is -p / --print.
func (kind) Headless() agents.Headless { return agents.Headless{PrintFlag: true} }

// Install: the kit's plugin manifest registers the server (mcpServers.obor: `bash ${CLAUDE_PLUGIN_ROOT}/bin/obor mcp`,
// so the shipped manifest never names the kit's path), which every obor Claude pane loads with the plugin; a start with
// --strict-mcp-config hides it, so obor names its own on that start — a lane's agent always is such a start
func (kind) Install(kit string, _ func(string) string) agents.Install {
	lane := func(a []string, read agents.ReadFile) (string, bool, bool) {
		p := parse(a)
		switch {
		case p.strict < 0:
			return "", true, true
		case kit == "":
			return StrictFlag + " and no obor kit to point at", false, false
		case p.needsObor(kit, read):
			return StrictFlag + " hides the plugin manifest's server; obor adds --mcp-config " + MCPConfig(kit), true, false
		}
		return "the type's args name an obor server in --mcp-config", true, false
	}
	shim := []string{"${CLAUDE_PLUGIN_ROOT}/bin/obor", "mcp"}
	return agents.Install{MCP: agents.MCPRegistration{Where: agents.Manifest,
		Path: filepath.Join(kit, ".claude-plugin", "plugin.json"), Key: []string{"mcpServers", "obor"},
		Entry:    map[string]any{"command": "bash", "args": shim, "env": map[string]string{agents.Pin: "1"}},
		Unpinned: map[string]any{"command": "bash", "args": shim}, Lane: lane}}
}

// Transcript: $CLAUDE_CONFIG_DIR (default ~/.claude)/projects/<dir>/<sid>.jsonl with any project dir (*): Claude
// derives the dir from its start cwd as it sees fit (resolved symlinks, a hash past 200 characters), so only the
// session id names the file and the path recorded by the session itself is the true one.
func (kind) Transcript(env func(string) string, sid string) string {
	dir := env("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(env("HOME"), ".claude")
	}
	return filepath.Join(dir, "projects", "*", sid+".jsonl")
}

// LastTurn: none read; the StopFailure hook reports a stop.
func (kind) LastTurn([]byte) (agents.Turn, bool) { return agents.Turn{}, false }

// Session: none read; the SessionStart hook reports the session id and its transcript_path.
func (kind) Session([]byte) (agents.Session, bool) { return agents.Session{}, false }

// Reset: StopFailure's error rate_limit is a limit (billing_error, overloaded, … are not); its error_details name
// the reset when they carry an epoch ("…|<epoch>") or a clock time ("resets 6pm"; agents.ResetIn).
func (kind) Reset(errText, details string, at time.Time) (time.Time, bool) {
	if errText != "rate_limit" {
		return time.Time{}, false
	}
	return agents.ResetIn(details, at), true
}

// Deliver posts to the session's inbox, as the SessionStart hook recorded it ({"socket", "token"}): the two
// lines of the binary's own sender, {"type":"auth","token":…} (when the record has a token) then
// {"type":"user","message":{"role":"user","content":<text>}}. Written against Claude Code 2.1.312 (cross-session
// messaging needs ≥ 2.1.224; code.claude.com/docs/en/cross-session-messaging.md: an idle receiving session starts a
// new turn with the message). ok false: the record names no socket.
func (kind) Deliver(text string, record []byte) (agents.Delivery, bool) {
	var in inbox
	if json.Unmarshal(record, &in) != nil || in.Socket == "" {
		return agents.Delivery{}, false
	}
	type auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type user struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
	}
	d := agents.Delivery{Socket: in.Socket}
	if in.Token != "" {
		b, _ := json.Marshal(auth{"auth", in.Token})
		d.Lines = append(d.Lines, string(b))
	}
	b, _ := json.Marshal(user{"user", message{"user", text}})
	d.Lines = append(d.Lines, string(b))
	return d, true
}

func (kind) Caps() agents.Caps {
	return agents.Caps{Hooks: agents.HooksNative, NamedSession: true, ProjectPlugin: true, Inbox: true, Resume: true,
		Skills: true, Transcript: true, Status: agents.StatusTerminal}
}

// inbox is the record's part Deliver reads and InboxRecord writes.
type inbox struct {
	Socket string `json:"socket"`
	Token  string `json:"token,omitempty"`
}

// InboxRecord: the session's messaging socket and token, as Claude Code sets them in its hooks' environment
// (CLAUDE_CODE_MESSAGING_SOCKET, _TOKEN); nil when messaging is off.
func (kind) InboxRecord(env func(string) string) []byte {
	sock := env("CLAUDE_CODE_MESSAGING_SOCKET")
	if sock == "" {
		return nil
	}
	b, _ := json.Marshal(inbox{sock, env("CLAUDE_CODE_MESSAGING_TOKEN")})
	return b
}
