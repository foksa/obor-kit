// Package kind is the OpenCode kind as obor's kind protocol answers it (docs/agents/kind-protocol.md): the
// reference plugin kind, built as the binary obor-kind-opencode (kinds/opencode). --prompt, no resume, /exit with its
// popup's second Enter, `opencode run` denied, obor's MCP server and plugin (<kit>/opencode, its hook transport) in each
// start's generated config (OPENCODE_CONFIG), prompts typed. Handle is one request; the binary runs it on its
// stdin, a test binary in process (testkit).
package kind

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
)

// Version is the protocol version this kind speaks.
const Version = 1

// Name is the kind's name (models.<name>.kind, kinds.<name>).
const Name = "opencode"

// ModelVar is the variable obor's plugin (opencode/obor.js) reads the start's model and effort from,
// provider/model[#variant]: it switches each new root session of the start to it.
const ModelVar = "OBOR_OPENCODE_MODEL"

// ConfigVar is the variable that names a start's generated config: a path, so the typed env line stays short
// (herdr refuses over-long lines).
const ConfigVar = "OPENCODE_CONFIG"

// SkillsVar is OpenCode's own switch that leaves the Claude skill dirs unread (~/.claude/skills with the user's synced
// skills, and the project's .claude/skills): "1" on a trimmed start. .agents/skills and .opencode/skills still load.
const SkillsVar = "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"

// ConfigFile is the generated config's name in the project's state dir.
const ConfigFile = "opencode.json"

// efforts are the variant names OpenCode gives a model's reasoning levels. They are per model (opencode 2.2.0:
// `opencode api GET /api/model` lists deepseek-v4.1-flash's as low, high, max; its own fallback list is none, minimal,
// low) and its help lists none, so this is the union obor accepts; a variant the model lacks starts its default.
var efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// request is one call's request: the fields of every call (docs/agents/kind-protocol.md "The calls").
type request struct {
	Version int      `json:"version"`
	Kit     string   `json:"kit"`
	Args    []string `json:"args"`
	Resume  string   `json:"resume"`
	Prompt  string   `json:"prompt"`
	Effort  string   `json:"effort"`
	Trim    bool     `json:"trim_user"`
	Dir     string   `json:"dir"`
	What    string   `json:"what"`
	Error   string   `json:"error"`
	Details string   `json:"details"`
	At      string   `json:"at"`
}

// Main is the binary: call args[0] with the request on stdin, its reply on stdout; a request it cannot read, or an
// unknown call, exits 2 with the reason on stderr.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: obor-kind-opencode <call> < request.json (docs/agents/kind-protocol.md)")
		return 2
	}
	req, err := io.ReadAll(stdin)
	if err == nil {
		var out []byte
		if out, err = Handle(args[0], req); err == nil {
			_, err = stdout.Write(append(out, '\n'))
			if err == nil {
				return 0
			}
		}
	}
	fmt.Fprintf(stderr, "obor-kind-opencode: %s: %v\n", args[0], err)
	return 2
}

// Handle is call's reply to req, as JSON; an error for a request that does not parse or names another version, or a
// call this kind does not answer. A start that cannot carry its effort is a reply with error, not an error.
func Handle(call string, req []byte) ([]byte, error) {
	var in request
	if err := json.Unmarshal(req, &in); err != nil {
		return nil, fmt.Errorf("malformed request: %v", err)
	}
	if in.Version != Version {
		return nil, fmt.Errorf("request version %d, this kind speaks %d", in.Version, Version)
	}
	var reply map[string]any
	switch call {
	case "describe":
		reply = describe()
	case "start":
		reply = start(in)
	case "install":
		reply = map[string]any{"mcp": map[string]any{"where": "start"}}
	case "turn":
		reply = turn(in)
	case "deliver":
		reply = map[string]any{"ok": true, "delivery": map[string]any{"typed": true}}
	case "project-warnings":
		reply = map[string]any{"warnings": ProjectWarnings(in.Dir, os.ReadFile)}
	default:
		return nil, fmt.Errorf("no call %q", call)
	}
	reply["version"] = Version
	return json.Marshal(reply)
}

// describe is the manifest. Caps: hooks through obor's plugin; a session is recorded but never resumed (no resume: obor
// starts an OpenCode agent fresh), and no hook event carries the version, so the marker takes OPENCODE_VERSION;
// status from the terminal's agent detection. Stop: /exit, typed with Enter (live 2026-10-17); its slash-command popup
// takes that Enter, a second one runs it (once the agent has exited it reaches the shell as an empty line). When it did
// not exit: Escape (the popup), then Ctrl+U, so the next typed message does not follow the quit. Headless: `opencode
// run`, as the subcommand or a later word (ValueOpts: opencode --help). No transcript: OpenCode keeps its sessions in a
// database (its plugin reports the session id: opencode/obor.js); no inbox: prompts are typed. MCP: nothing
// user-global: obor's server and plugin reach each start through its generated config.
func describe() map[string]any {
	return map[string]any{
		"name":    Name,
		"status":  "terminal",
		"caps":    map[string]any{"hooks": "plugin", "model_arg": true, "version_var": "OPENCODE_VERSION"},
		"efforts": efforts,
		"vars":    []string{ModelVar, ConfigVar, SkillsVar},
		"stop":    map[string]any{"quit": "/exit", "second_enter": true, "clear_input": []string{"esc", "ctrl+u"}},
		"headless": map[string]any{"subcommands": []string{"run"}, "anywhere": []string{"run"},
			"value_opts": []string{"--server", "-s", "--session", "--prompt", "--completions", "--log-level"}},
		"typed": true,
		"mcp_lane": map[string]any{"registered": true, "how": ConfigVar + " (the start's generated config: " +
			filepath.Join("{kit}", "bin", "obor") + " mcp, plugin " + filepath.Join("{kit}", Name) + ")"},
		"project_warnings": true,
	}
}

// start: the type's args but their --model/-m (OpenCode's TUI takes none: the env carries the model to obor's plugin),
// then --prompt <prompt> (OpenCode has no resume, so a resume adds nothing). Env: ConfigVar, the path of the start's
// generated config (Config of the kit; no kit: none), and ModelVar <provider>/<model>[#<effort>] when the args name a
// model (the effort replaces a #variant the model names), else ModelVar "" (unset: the pane's shell may hold an
// earlier start's), and SkillsVar "1" on a trimmed start (trim_user: every agent but a lead), else "" (unset, the
// same way). OpenCode's interactive TUI has no --model flag (2.2.0: only `opencode run` and `mini` take one) and,
// for a new session, ranks the variant it remembers in its state (model.json) above the config's; obor's plugin switches
// the session in its prompt hook, after the TUI's own pre-submit switch, so the start's model and variant win from the
// first turn (a live probe). obor never guesses a model: args without --model set none, so OpenCode's own choice
// stands, and an effort on them is the start's error.
func start(in request) map[string]any {
	args, model := splitModel(in.Args)
	if in.Effort != "" && model == "" {
		return map[string]any{"error": fmt.Sprintf("effort %s needs a model id (models.<name>.model, provider/model)",
			in.Effort)}
	}
	if model != "" && in.Effort != "" {
		model, _, _ = strings.Cut(model, "#")
		model += "#" + in.Effort
	}
	if in.Prompt != "" && in.Resume == "" {
		args = append(args, "--prompt", in.Prompt)
	}
	env := map[string]string{ModelVar: model, SkillsVar: ""}
	if in.Trim {
		env[SkillsVar] = "1"
	}
	files := []map[string]string{}
	if in.Kit != "" {
		env[ConfigVar] = "{file:" + ConfigFile + "}"
		files = append(files, map[string]string{"name": ConfigFile, "bytes": string(Config(in.Kit))})
	}
	return map[string]any{"args": args, "env": env, "files": files}
}

// splitModel is args without their --model/-m (with its word, or either spelling with =), and the model they named.
func splitModel(args []string) (rest []string, model string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--model" || a == "-m") && i+1 < len(args):
			model = args[i+1]
			i++
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimPrefix(a, "--model=")
		case strings.HasPrefix(a, "-m="):
			model = strings.TrimPrefix(a, "-m=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, model
}

// Config is the generated config of a start from kit: mcp.servers.obor runs the kit's `bin/obor mcp`, pinned to the
// kit (agents.Pin), and plugins names the kit's plugin package dir (<kit>/opencode: opencode loads a package
// dir from an env config, not a plain .js file).
func Config(kit string) []byte {
	b, _ := json.MarshalIndent(map[string]any{
		"mcp": map[string]any{"servers": map[string]any{"obor": map[string]any{
			"type": "local", "command": []string{filepath.Join(kit, "bin", "obor"), "mcp"},
			"environment": map[string]string{agents.Pin: "1"}}}},
		"plugins": []string{"file://" + filepath.Join(kit, Name)},
	}, "", "  ")
	return append(b, '\n')
}

// turn: no turn and no session are read (OpenCode keeps its sessions in a database, not a transcript obor finds).
// Reset: the plugin maps a provider quota failure with a retry time to rate_limit and writes its reset in
// error_details; billing/auth/server errors are not limits.
func turn(in request) map[string]any {
	switch in.What {
	case "reset":
		if in.Error != "rate_limit" {
			return map[string]any{"limited": false, "reset": ""}
		}
		at, _ := time.Parse(time.RFC3339, in.At)
		reset := ""
		if t := agents.ResetIn(in.Details, at); !t.IsZero() {
			reset = t.Format(time.RFC3339)
		}
		return map[string]any{"limited": true, "reset": reset}
	case "last", "session":
		return map[string]any{"ok": false}
	}
	return map[string]any{"error": fmt.Sprintf("turn: no what %q", in.What)}
}

// ProjectFiles are the project config files OpenCode reads in a start dir (opencode.ai/docs/config), relative to it.
var ProjectFiles = []string{"opencode.json", "opencode.jsonc", ".opencode/opencode.json", ".opencode/opencode.jsonc"}

// ProjectWarnings: a project file defining mcp.servers.obor replaces obor's server (OpenCode's project layer
// outranks OPENCODE_CONFIG, docs/research/agent-project-config.md §3), and a file that does not parse. dir "": none.
func ProjectWarnings(dir string, read func(string) ([]byte, error)) []string {
	out := []string{}
	if dir == "" {
		return out
	}
	for _, rel := range ProjectFiles {
		b, err := read(filepath.Join(dir, rel))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var doc struct {
			MCP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"mcp"`
		}
		if err == nil {
			err = json.Unmarshal(plainJSON(b), &doc)
		}
		switch {
		case err != nil:
			out = append(out, rel+": does not parse ("+err.Error()+")")
		case doc.MCP.Servers["obor"] != nil:
			out = append(out, rel+": mcp.servers.obor replaces obor's own server (the project's config wins): remove it")
		}
	}
	return out
}

// plainJSON is JSONC as JSON: its // and /* */ comments and the commas before a closing bracket or brace dropped,
// strings as they are.
func plainJSON(b []byte) []byte {
	out := make([]byte, 0, len(b))
	comma := -1 // the index in out of a comma that may trail
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '"':
			j := i + 1
			for ; j < len(b) && b[j] != '"'; j++ {
				if b[j] == '\\' {
					j++
				}
			}
			j = min(j, len(b)-1)
			out = append(out, b[i:j+1]...)
			i, comma = j, -1
			continue
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			i--
			continue
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := strings.Index(string(b[i+2:]), "*/")
			if end < 0 {
				return out // an open comment: what is left is no JSON
			}
			i += end + 3
			continue
		case c == ',':
			comma = len(out)
		case c == ']' || c == '}':
			if comma >= 0 {
				out = append(out[:comma], out[comma+1:]...)
			}
			comma = -1
		case c != ' ' && c != '\t' && c != '\n' && c != '\r':
			comma = -1
		}
		out = append(out, c)
	}
	return out
}
