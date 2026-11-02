// Package opencode is the OpenCode bridge (agents.Kind): --prompt, no resume, /exit with its popup's second Enter,
// `opencode run` denied, obor's MCP server in the user config and obor's plugin (<kit>/opencode/obor.js, its hook
// transport) linked into it, prompts typed.
package opencode

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
)

func init() { agents.Register(kind{}) }

type kind struct{}

const name = "opencode"

func (kind) Name() string { return name }

// Start: the type's args but their --model/-m (OpenCode's TUI takes none: StartEnv carries the model), then
// --prompt <prompt>. OpenCode takes no settings and has no resume. Its sessions live in its data dir
// (<XDG_DATA_HOME|~/.local/share>/opencode), which StartEnv leaves as it is (T-696).
func (kind) Start(in agents.Start) []string {
	out, _ := splitModel(in.Args)
	if in.Prompt != "" && in.Resume == "" {
		out = append(out, "--prompt", in.Prompt)
	}
	return out
}

func (kind) NotReady(string) string { return "" }

// ConfigVar is OpenCode's variable holding config JSON layered over its config files: a start's model and effort
// reach the TUI through it (T-695).
const ConfigVar = "OPENCODE_CONFIG_CONTENT"

// efforts are the variant names OpenCode gives a model's reasoning levels. They are per model (opencode 2.2.0:
// `opencode api GET /api/model` lists deepseek-v4.1-flash's as low, high, max; its own fallback list is none, minimal,
// low) and its help lists none, so this is the union obor accepts; a variant the model lacks starts its default.
var efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func (kind) Efforts() []string { return efforts }

// StateVar is the variable naming OpenCode's state dir (<XDG_STATE_HOME|~/.local/state>/opencode): its model.json
// keeps the TUI's recent models and each model's remembered variant, which beats the config's (T-696).
const StateVar = agents.StateHomeVar

// StateDir: <Home>/opencode-state/<Project>/<Agent>, the agent's own XDG_STATE_HOME (T-696), so the variant a start
// passes is not beaten by one remembered in the user's state, and obor never writes the user's own
// (~/.local/state/opencode). The project and agent elements are path-escaped (pathElement), so any configured name
// isolates. "" when Home, Project or Agent is empty (StartEnv refuses such a start, T-697).
func (kind) StateDir(in agents.Start) string {
	if in.Home == "" || in.Project == "" || in.Agent == "" {
		return ""
	}
	return filepath.Join(in.Home, "opencode-state", pathElement(in.Project), pathElement(in.Agent))
}

// pathElement is s as one path element: url.PathEscape (a separator, / or \, becomes %2F or %5C; a literal % becomes
// %25, so two names never share an element), and the dots-only names . and .. with their dots escaped.
func pathElement(s string) string {
	s = url.PathEscape(s)
	if s == "." || s == ".." {
		return strings.ReplaceAll(s, ".", "%2E")
	}
	return s
}

// CheckEffort: an effort needs the type's --model, as obor never guesses OpenCode's model (T-697).
func (kind) CheckEffort(args []string, effort string) error {
	if _, model := splitModel(args); effort != "" && model == "" {
		return fmt.Errorf("opencode: effort %s needs a model: add --model provider/model to the type's args", effort)
	}
	return nil
}

// StartEnv: StateVar the agent's own state dir (StateDir, T-696; a start without one is refused: it would run on the
// owner's state), and ConfigVar {"model":"<provider>/<model>[#<effort>]"} when the type's args name a model, else
// ConfigVar unset (the pane's shell may hold an earlier start's). OpenCode's interactive TUI has no --model flag
// (2.2.0: only `opencode run` and `mini` take --model provider/model#variant), but its config's model takes the same
// form, and the TUI starts with the config's model and variant (live 2026-11-02: the footer showed "· max"). obor
// never guesses a model (T-697): a type without --model sets none, so OpenCode's own precedence (project config,
// global config, recent) stands, and an effort on it is an error. The TUI's own remembered variant of a model (picked
// in it, kept in model.json) wins over the config's: in the agent's own state dir, emptied on a fresh start, there is
// none.
func (k kind) StartEnv(in agents.Start) (map[string]string, error) {
	state := k.StateDir(in)
	if state == "" {
		return nil, fmt.Errorf("opencode: no state dir for the agent (home %q, project %q, agent %q): refusing to start on the owner's state",
			in.Home, in.Project, in.Agent)
	}
	if err := k.CheckEffort(in.Args, in.Effort); err != nil {
		return nil, err
	}
	out := map[string]string{StateVar: state, ConfigVar: ""}
	if _, model := splitModel(in.Args); model != "" {
		if in.Effort != "" {
			model, _, _ = strings.Cut(model, "#")
			model += "#" + in.Effort
		}
		b, _ := json.Marshal(map[string]string{"model": model})
		out[ConfigVar] = string(b)
	}
	return out, nil
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

// Stop: /exit, typed with Enter (live 2026-10-17); its slash-command popup takes that Enter, a second one runs it (once
// the agent has exited it reaches the shell as an empty line). When it did not exit: Escape (the popup), then Ctrl+U,
// so the next typed message does not follow the quit.
func (kind) Stop() agents.Stop {
	return agents.Stop{Quit: "/exit", SecondEnter: true, ClearInput: []string{"esc", "ctrl+u"}}
}

// Headless: `opencode run`, as the subcommand or a later word. ValueOpts: opencode --help.
func (kind) Headless() agents.Headless {
	return agents.Headless{Subcommands: []string{"run"}, Anywhere: []string{"run"},
		ValueOpts: []string{"--server", "-s", "--session", "--prompt", "--completions", "--log-level"}}
}

// Install: OpenCode 2's mcp.servers.obor in the user config (<XDG_CONFIG_HOME|~/.config>/opencode/opencode.json, or
// opencode.jsonc when that is the only one): a local server, the kit's shim with the one argument, pinned to the kit
// (T-420); and obor's plugin linked as plugins/obor.js there (T-306; user-global, once per user).
func (kind) Install(kit string, env func(string) string) agents.Install {
	conf := env("XDG_CONFIG_HOME")
	if conf == "" {
		conf = filepath.Join(env("HOME"), ".config")
	}
	conf = filepath.Join(conf, name)
	shim := []string{filepath.Join(kit, "bin", "obor"), "mcp"}
	return agents.Install{
		MCP: agents.MCPRegistration{Where: agents.Config,
			Path: filepath.Join(conf, "opencode.json"), AltPath: filepath.Join(conf, "opencode.jsonc"),
			Key:      []string{"mcp", "servers", "obor"},
			Entry:    map[string]any{"type": "local", "command": shim, "environment": map[string]string{agents.Pin: "1"}},
			Unpinned: map[string]any{"type": "local", "command": shim},
			Lane:     func([]string, agents.ReadFile) (string, bool, bool) { return "", true, true }},
		Plugin: &agents.Link{From: filepath.Join(kit, name, "obor.js"), To: filepath.Join(conf, "plugins", "obor.js")},
	}
}

func (kind) Transcript(func(string) string, string, string) string { return "" }

// LastTurn: none read; OpenCode keeps its sessions in a database, not a transcript obor finds.
func (kind) LastTurn([]byte) (agents.Turn, bool) { return agents.Turn{}, false }

// Session: no transcript, so none (its plugin reports the session id: opencode/obor.js).
func (kind) Session([]byte) (agents.Session, bool) { return agents.Session{}, false }

// Reset: the plugin maps a provider quota failure with a retry time to rate_limit and writes its reset in
// error_details; billing/auth/server errors are not limits.
func (kind) Reset(errText, details string, at time.Time) (time.Time, bool) {
	if errText != "rate_limit" {
		return time.Time{}, false
	}
	return agents.ResetIn(details, at), true
}

func (kind) Deliver(string, []byte) (agents.Delivery, bool) {
	return agents.Delivery{Typed: true}, true
}

func (kind) InboxRecord(func(string) string) []byte { return nil }

// Caps: hooks through obor's plugin; a session is recorded but never resumed (Resume false: obor starts an OpenCode
// agent fresh, T-581), and no hook event carries the version, so the marker takes OPENCODE_VERSION.
func (kind) Caps() agents.Caps {
	return agents.Caps{Hooks: agents.HooksPlugin, Resume: false, VersionVar: "OPENCODE_VERSION"}
}
