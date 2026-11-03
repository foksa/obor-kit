// Package opencode is the OpenCode bridge (agents.Kind): --prompt, no resume, /exit with its popup's second Enter,
// `opencode run` denied, obor's MCP server in the user config and obor's plugin (<kit>/opencode/obor.js, its hook
// transport) linked into it, prompts typed.
package opencode

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
)

func init() { agents.Register(kind{}) }

type kind struct{}

const name = "opencode"

// Name is the kind's name (models.<name>.kind).
const Name = name

func (kind) Name() string { return name }

// Start: the type's args but their --model/-m (OpenCode's TUI takes none: StartEnv carries the model to obor's plugin),
// then --prompt <prompt>. OpenCode takes no settings and has no resume.
func (kind) Start(in agents.Start) []string {
	out, _ := splitModel(in.Args)
	if in.Prompt != "" && in.Resume == "" {
		out = append(out, "--prompt", in.Prompt)
	}
	return out
}

func (kind) NotReady(string) string { return "" }

// ModelVar is the variable obor's plugin (opencode/obor.js) reads the start's model and effort from,
// provider/model[#variant]: it switches each new root session of the start to it (T-703).
const ModelVar = "OBOR_OPENCODE_MODEL"

// efforts are the variant names OpenCode gives a model's reasoning levels. They are per model (opencode 2.2.0:
// `opencode api GET /api/model` lists deepseek-v4.1-flash's as low, high, max; its own fallback list is none, minimal,
// low) and its help lists none, so this is the union obor accepts; a variant the model lacks starts its default.
var efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func (kind) Efforts() []string { return efforts }

// CheckEffort: an effort needs the type's --model, as obor never guesses OpenCode's model (T-697).
func (kind) CheckEffort(args []string, effort string) error {
	if _, model := splitModel(args); effort != "" && model == "" {
		return fmt.Errorf("opencode: effort %s needs a model id (models.<name>.model, provider/model)", effort)
	}
	return nil
}

// StartEnv: ModelVar <provider>/<model>[#<effort>] when the type's args name a model (the effort replaces a #variant
// the model names), else ModelVar unset (the pane's shell may hold an earlier start's). OpenCode's interactive TUI has
// no --model flag (2.2.0: only `opencode run` and `mini` take one) and, for a new session, ranks the variant it
// remembers in its state (model.json) above the config's; obor's plugin switches the session in its prompt hook, after
// the TUI's own pre-submit switch, so the start's model and variant win from the first turn (T-703 probe). obor never
// guesses a model (T-697): a type without --model sets none, so OpenCode's own choice stands, and an effort on it is
// an error.
func (k kind) StartEnv(in agents.Start) (map[string]string, error) {
	if err := k.CheckEffort(in.Args, in.Effort); err != nil {
		return nil, err
	}
	_, model := splitModel(in.Args)
	if model != "" && in.Effort != "" {
		model, _, _ = strings.Cut(model, "#")
		model += "#" + in.Effort
	}
	return map[string]string{ModelVar: model}, nil
}

// WithModel: --model <model> in place of the args' own (--model or -m, T-753). OpenCode has no effort arg: a type
// reference's :<effort> is its variant (StartEnv), so it is no EffortEditor.
func (kind) WithModel(args []string, model string) []string {
	return agents.SetFlag(args, []string{"--model", "-m"}, model)
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
	return agents.Caps{Hooks: agents.HooksPlugin, ModelArg: true, Resume: false, VersionVar: "OPENCODE_VERSION"}
}
