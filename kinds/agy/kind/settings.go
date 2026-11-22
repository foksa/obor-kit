package kind

import (
	"maps"
	"path/filepath"
	"slices"

	"obor/internal/agents"
)

// OverrideFile is agy's override file (agents.OverrideDir: .obor/agy/), in agy's own settings format: laid over the
// user's saved settings for every agent but a lead.
const OverrideFile = "settings_override.json"

// UserSettingsFile is the user's saved agy settings, relative to HOME.
const UserSettingsFile = ".gemini/antigravity-cli/settings.json"

// MCPRule is the permission rule that lets obor's MCP tools run with no prompt (the workspace server is named obor).
const MCPRule = "mcp(obor/*)"

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
// Merge Patch: over nil for a lead) and obor's entries last:
//   - every key over names (one over deletes, null, is left out: the saved value applies);
//   - agentMode "auto" for every agent, a lead too (auto mode runs commands in agy's sandbox and has its reviewer
//     approve, with no prompt, the re-run of one the sandbox refused: a socket, the network, a write outside the
//     workspace; docs/research/agy-probes.md P11), obor's hooks (hook.go) guarding every call;
//   - permissions, its allow list gaining MCPRule;
//   - trustedWorkspaces gaining dir, only when root is in the user's saved trustedWorkspaces (the owner trusted the
//     project in its main checkout; its lanes follow): else the key is left to the saved value, and agy asks.
func Settings(user, over map[string]any, dir, root string) map[string]any {
	merged := agents.MergePatch(user, over)
	out := map[string]any{}
	for k := range over {
		if v, ok := merged[k]; ok {
			out[k] = v
		}
	}
	out["agentMode"] = "auto"
	perms, _ := merged["permissions"].(map[string]any)
	perms = maps.Clone(perms)
	if perms == nil {
		perms = map[string]any{}
	}
	perms["allow"] = with(perms["allow"], MCPRule)
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
