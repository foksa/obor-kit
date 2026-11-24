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
//   - permissions, its allow list gaining MCPRule and CommandRule, and on a trimmed start TemplatesRule(kit) (kit "":
//     none);
//   - trustedWorkspaces gaining dir when dir is a worktree obor made for the agent (a lane's or a role's: dir is not
//     root): agy trusts by exact path and loads the workspace's MCP config and hooks only in a folder trusted when it
//     starts, so a lane at agy's trust dialog would run without obor's server even once it is answered. A start in
//     root leaves the key to the saved value: trusting the main checkout is the owner's answer to agy's dialog.
func Settings(user, over map[string]any, lead bool, dir, root, kit string) map[string]any {
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
	rules := []string{MCPRule, CommandRule}
	if !lead && kit != "" {
		rules = append(rules, TemplatesRule(kit))
	}
	perms["allow"] = with(perms["allow"], rules...)
	out["permissions"] = perms
	if dir != "" && root != "" && filepath.Clean(dir) != filepath.Clean(root) {
		out["trustedWorkspaces"] = with(merged["trustedWorkspaces"], filepath.Clean(dir))
	}
	return out
}

// TemplatesRule is the permission rule that lets an agent read the kit's templates dir (its role texts, which the
// session start names for a read) with no prompt: obor's PreToolUse hook answers a read "ask", which hands it to
// agy's own check, and that asks for a file outside the workspace; a read_file rule naming a dir covers the files
// under it (docs/research/agy-probes.md P15).
func TemplatesRule(kit string) string {
	return "read_file(" + filepath.Join(kit, "templates") + ")"
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
