// Package opencode is the OpenCode bridge (agents.Kind): --prompt, no resume, /exit with its popup's second Enter,
// `opencode run` denied, obor's MCP server in the user config and obor's plugin (<kit>/opencode/obor.js, its hook
// transport) linked into it, prompts typed.
package opencode

import (
	"path/filepath"
	"time"

	"obor/internal/agents"
)

func init() { agents.Register(kind{}) }

type kind struct{}

const name = "opencode"

func (kind) Name() string { return name }

// Start: the type's args, then --prompt <prompt>. OpenCode takes no settings and has no resume.
func (kind) Start(in agents.Start) []string {
	out := append([]string(nil), in.Args...)
	if in.Prompt != "" && in.Resume == "" {
		out = append(out, "--prompt", in.Prompt)
	}
	return out
}

func (kind) NotReady(string) string { return "" }

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
