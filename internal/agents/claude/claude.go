// Package claude is the Claude Code bridge (agents.Kind): its start argv (--settings, obor's --mcp-config on a strict
// start, --resume), its quit, its headless flag, its plugin manifest, its transcripts and its session inbox.
package claude

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"obor/internal/agents"
)

func init() { agents.Register(kind{}) }

type kind struct{}

const name = "claude"

func (kind) Name() string { return name }

// StrictFlag is Claude's flag that hides every MCP server the command line does not name: the plugin manifest's obor
// server included (T-445).
const StrictFlag = "--strict-mcp-config"

// MCPConfig is the --mcp-config JSON of this kit's obor server, as the plugin manifest declares it but with the kit's
// path spelled out (a strict start has no ${CLAUDE_PLUGIN_ROOT}) and the pin, so bin/obor runs this kit (T-420). The
// OBOR_* variables need no entry: Claude passes its own environment to the server.
func MCPConfig(kit string) string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"obor": map[string]any{
		"command": filepath.Join(kit, "bin", "obor"),
		"args":    []string{"mcp"},
		"env":     map[string]string{agents.Pin: "1"},
	}}})
	return string(b)
}

// Start is the type's args (with in.Effort: their --effort replaced by --effort <effort>, T-695), then
// --strict-mcp-config for a lane's agent (in.StrictMCP, T-770: once, also when the args name it), then --settings
// <settings>, then --session-id <in.Session> (a fresh start obor names, T-813) or --resume <sid>, with obor's
// --mcp-config pair spliced in before the strict flag (the flag stops --mcp-config's variadic value), then one `--`
// when a config flag could take the prompt, then the prompt. The prompt is positional, never a config value; a
// resume carries none. The only `--` is obor's own (config validate refuses one in the args).
func (kind) Start(in agents.Start) []string {
	args := withEffort(in.Args, in.Effort)
	if in.StrictMCP {
		args = withStrict(args)
	}
	if in.Settings != nil {
		args = append(args, "--settings", compact(in.Settings))
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

// efforts are Claude Code's --effort values, as `claude --help` lists them (2.x, 2026-11-02: "Effort level for the
// current session (low, medium, high, xhigh, max)").
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

func (kind) Efforts() []string { return efforts }

// StartEnv: none; the effort is a flag.
func (kind) StartEnv(agents.Start) (map[string]string, error) { return nil, nil }

// Vars: none (StartEnv sets none).
func (kind) Vars() []string { return nil }

// WithModel: --model <model> in place of the args' own (T-753).
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
// with obor's MCP server alone (T-770).
func withStrict(args []string) []string {
	for _, a := range args {
		if a == StrictFlag || strings.HasPrefix(a, StrictFlag+"=") {
			return args
		}
	}
	return append(args, StrictFlag)
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
// (RV-47); `--mcp-config=<v>` takes the same variadic tail.
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
// options numbered. SecondEnter: with many slash commands the menu over "/exit" can take the Enter (T-712, owner
// 2026-11-02); a second Enter after the first poll submits it (never into the dialog).
func (kind) Stop() agents.Stop {
	return agents.Stop{Quit: "/exit", SecondEnter: true, AfterEscape: []string{"ctrl+c"}, ClearInput: []string{"ctrl+u"}, Dialog: &exitDialog}
}

var exitDialog = agents.Dialog{Option: "Exit and stop tasks", Key: regexp.MustCompile(`([1-9])[.)] *Exit and stop tasks`)}

// Headless: claude has no headless subcommand; its headless form is -p / --print (T-470).
func (kind) Headless() agents.Headless { return agents.Headless{PrintFlag: true} }

// Install: the kit's plugin manifest registers the server (mcpServers.obor: `bash ${CLAUDE_PLUGIN_ROOT}/bin/obor mcp`,
// so the shipped manifest never names the kit's path), which every obor Claude pane loads with the plugin; a start with
// --strict-mcp-config hides it, so obor names its own on that start (T-445) — a lane's agent always is such a start
// (T-770).
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
// session id names the file and the path recorded by the session itself is the true one (T-813).
func (kind) Transcript(env func(string) string, sid string) string {
	dir := env("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(env("HOME"), ".claude")
	}
	return filepath.Join(dir, "projects", "*", sid+".jsonl")
}

// LastTurn: none read; the StopFailure hook reports a stop (T-532).
func (kind) LastTurn([]byte) (agents.Turn, bool) { return agents.Turn{}, false }

// Session: none read; the SessionStart hook reports the session id and its transcript_path (T-577).
func (kind) Session([]byte) (agents.Session, bool) { return agents.Session{}, false }

// Reset: StopFailure's error rate_limit is a limit (billing_error, overloaded, … are not); its error_details name
// the reset when they carry an epoch ("…|<epoch>") or a clock time ("resets 6pm"; agents.ResetIn).
func (kind) Reset(errText, details string, at time.Time) (time.Time, bool) {
	if errText != "rate_limit" {
		return time.Time{}, false
	}
	return agents.ResetIn(details, at), true
}

// Deliver posts to the session's inbox (T-485), as the SessionStart hook recorded it ({"socket", "token"}): the two
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
		Skills: true, Transcript: true}
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
