package kind

import (
	"maps"
	"path/filepath"
	"slices"

	"obor/internal/agents"
)

// OverrideFile is agy's override file (agents.OverrideDir: .obor/agy/), in agy's own settings format: laid over the
// user's saved settings for every start (whole on a trimmed start; without modeKeys and agentMode on a lead's start).
const OverrideFile = "settings_override.json"

// modeKeys are the agy settings keys that set a permission mode, omitted from the project's override on a lead's start
// so a lead keeps plain auto mode while taking the override's permissions and other keys.
var modeKeys = []string{"toolPermission"}

// UserSettingsFile is the user's saved agy settings, relative to HOME.
const UserSettingsFile = ".gemini/antigravity-cli/settings.json"

// MCPRule is the permission rule that lets obor's MCP tools run with no prompt (the workspace server is named obor).
const MCPRule = "mcp(obor/*)"

// CommandRule is the permission rule that lets obor's own commands run with no reviewer prompt on an unsandboxed re-run.
const CommandRule = "command(obor)"

// UserSettings are the user's saved settings (UserSettingsFile under env's HOME); none: no HOME, no file, or one that
// does not parse.
func UserSettings(env map[string]string, read func(string) ([]byte, error)) map[string]any {
	home := env["HOME"]
	if home == "" {
		return map[string]any{}
	}
	b, err := read(filepath.Join(home, UserSettingsFile))
	if err != nil {
		return map[string]any{}
	}
	m, err := agents.JSONObject(b)
	if err != nil {
		return map[string]any{}
	}
	return m
}

// Settings is a start's --settings document. agy lays it over the user's saved settings by top-level key, a key it
// names replacing the saved value whole, so each key it names is the user's saved value with over laid over it (a JSON
// Merge Patch) and obor's entries last:
//   - every key over names (on a lead's start, without modeKeys: the lead takes the override's permissions and other
//     keys, never its permission or agent mode; a key over deletes, null, is left out so the saved value applies);
//   - agentMode "auto" for every agent, a lead too (auto mode runs commands in agy's sandbox and has its reviewer
//     approve, with no prompt, the re-run of one the sandbox refused: a socket, the network, a write outside the
//     workspace; docs/research/agy-probes.md P11), obor's hooks (hook.go) guarding every call; a trimmed start's
//     override names the mode it wants instead, and a lead's never applies (so a lead is always auto);
//   - permissions, its allow list gaining MCPRule and CommandRule;
//   - trustedWorkspaces gaining dir, only when root is in the user's saved trustedWorkspaces (the owner trusted the
//     project in its main checkout; its lanes follow): else the key is left to the saved value, and agy asks.
func Settings(user, over map[string]any, lead bool, dir, root string) map[string]any {
	if lead && len(over) > 0 {
		over = maps.Clone(over)
		for _, k := range modeKeys {
			delete(over, k)
		}
	}
	merged := agents.MergePatch(user, over)
	out := map[string]any{}
	for k := range over {
		if v, ok := merged[k]; ok {
			out[k] = v
		}
	}
	out["agentMode"] = "auto"
	if mode := over["agentMode"]; !lead && mode != nil {
		out["agentMode"] = mode
	}
	perms, _ := merged["permissions"].(map[string]any)
	perms = maps.Clone(perms)
	if perms == nil {
		perms = map[string]any{}
	}
	perms["allow"] = with(perms["allow"], MCPRule, CommandRule)
	out["permissions"] = perms
	if dir != "" && root != "" && slices.Contains(paths(user["trustedWorkspaces"]), filepath.Clean(root)) {
		out["trustedWorkspaces"] = with(merged["trustedWorkspaces"], filepath.Clean(dir))
	}
	return out
}

// with is the list v (a JSON array; anything else: none) with each of add appended that it lacks.
func with(v any, add ...string) []any {
	list, _ := v.([]any)
	list = slices.Clone(list)
	for _, a := range add {
		if !slices.Contains(list, any(a)) {
			list = append(list, a)
		}
	}
	return list
}

// paths are the string members of the JSON array v, each a cleaned path.
func paths(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, filepath.Clean(s))
		}
	}
	return out
}
