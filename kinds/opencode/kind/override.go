package kind

import (
	"encoding/json"
	"path/filepath"

	"obor/internal/agents"
)

// OverrideFile is OpenCode's override file (agents.OverrideDir): laid over the user's own config.
const OverrideFile = "opencode_override.json"

// UserDir is the user's OpenCode config dir in env (a request's snapshot): $XDG_CONFIG_HOME/opencode, else
// $HOME/.config/opencode; "" when env names neither (no user file is read).
func UserDir(env map[string]string) string {
	if d := env["XDG_CONFIG_HOME"]; d != "" {
		return filepath.Join(d, Name)
	}
	if h := env["HOME"]; h != "" {
		return filepath.Join(h, ".config", Name)
	}
	return ""
}

// Overridden is the generated config of a start from kit: obor's own two entries, on the user's own config with over
// laid over it (over nil, a lead's start or a project with no OverrideFile: obor's two entries alone). The base is
// the user's opencode.json then opencode.jsonc (userDir; one that is absent or does not parse: nothing) with over as
// a JSON Merge Patch (agents.MergePatch), so an override names only what it changes and the document holds the whole
// entry: {"mcp": {"servers": {"<name>": {"disabled": true}}}} gives the user's server whole with "disabled": true
// (OpenCode ignores a bare one). OpenCode still loads the user's own config below the file (opencode 2.2.0: the
// OPENCODE_CONFIG file ranks above it, a project's config above the file), merges objects key by key, replaces an
// MCP server whole and adds plugin lists up, so a key over deletes (null) still applies from the user's file; its own
// syntax removes an entry: "plugins": ["-<id>"]. On that base mcp.servers.obor is obor's own, and plugins is obor's
// plugin, then the base's list. read reads the user's files (nil with over nil).
func Overridden(kit string, over map[string]any, userDir string, read func(string) ([]byte, error)) []byte {
	doc := map[string]any{}
	if over != nil {
		if userDir != "" {
			for _, name := range []string{"opencode.json", "opencode.jsonc"} {
				if b, err := read(filepath.Join(userDir, name)); err == nil {
					if user, err := agents.JSONObject(plainJSON(b)); err == nil {
						doc = agents.MergePatch(doc, user)
					}
				}
			}
		}
		doc = agents.MergePatch(doc, over)
	}
	plugins := []any{pluginEntry(kit)}
	own, _ := doc["plugins"].([]any)
	plugins = append(plugins, own...)
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["obor"] = map[string]any{"type": "local", "command": []string{filepath.Join(kit, "bin", "obor"), "mcp"},
		"environment": map[string]string{agents.Pin: "1"}}
	mcp["servers"] = servers
	doc["mcp"], doc["plugins"] = mcp, plugins
	b, _ := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n')
}
