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

// config is one start's generated config as the inputs it reads: the kit, the project's override file (over), the
// user's config dir (userDir), the start dir (dir: the project's own opencode.json(c) live there), lane (a worker's or
// the drive agent's start: the user's own MCP servers stay out) and read (the kind's I/O: the user's and the project's
// files).
type config struct {
	kit     string
	over    map[string]any
	userDir string
	dir     string
	lane    bool
	read    agents.ReadFile
}

// Config is the generated config of a start from kit: mcp.servers.obor runs the kit's `bin/obor mcp`, pinned to the
// kit (agents.Pin), and plugins names the kit's plugin package dir (<kit>/opencode: opencode loads a package dir from an
// env config, not a plain .js file).
func Config(kit string) []byte { return config{kit: kit}.bytes() }

// bytes is the generated config: obor's own two entries on the user's own config with over laid over it, when over names
// the project's override file. The base is the user's opencode.json then opencode.jsonc (userDir; one that is absent or
// does not parse: nothing) with over as a JSON Merge Patch (agents.MergePatch), so an override names only what it changes
// and the document holds the whole entry: {"mcp": {"servers": {"<name>": {"disabled": true}}}} gives the user's server
// whole with "disabled": true (OpenCode ignores a bare one). OpenCode still loads the user's own config below the file
// (opencode 2.2.0: the OPENCODE_CONFIG file ranks above it, a project's config above the file), merges objects key by
// key, replaces an MCP server whole and adds plugin lists up, so a key over deletes (null) still applies from the user's
// file; its own syntax removes an entry: "plugins": ["-<id>"]. A lane's start also reads the user's config and writes
// each server the project's own config (dir) does not define and over does not declare as the user's whole definition
// with "disabled": true (disabled), so a lane's agent reaches obor's server and the project's own, not the user's. On that
// base mcp.servers.obor is obor's own, and plugins is obor's plugin, then the base's list.
func (c config) bytes() []byte {
	doc := map[string]any{}
	user := map[string]any{}
	if c.over != nil || c.lane {
		user = c.userConfig()
	}
	if c.over != nil {
		doc = agents.MergePatch(user, c.over)
	}
	plugins := []any{pluginEntry(c.kit)}
	own, _ := doc["plugins"].([]any)
	plugins = append(plugins, own...)
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	list, _ := mcp["servers"].(map[string]any)
	if list == nil {
		list = map[string]any{}
	}
	if c.lane {
		for name, entry := range c.disabled(user) {
			list[name] = entry
		}
	}
	list["obor"] = map[string]any{"type": "local", "command": []string{filepath.Join(c.kit, "bin", "obor"), "mcp"},
		"environment": map[string]string{agents.Pin: "1"}}
	mcp["servers"] = list
	doc["mcp"], doc["plugins"] = mcp, plugins
	b, _ := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n')
}

// userConfig is the user's own config: userDir's opencode.json then opencode.jsonc (the later file's keys win); nothing
// when the start names no config dir, or neither file parses.
func (c config) userConfig() map[string]any {
	doc := map[string]any{}
	if c.userDir == "" || c.read == nil {
		return doc
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if b, err := c.read(filepath.Join(c.userDir, name)); err == nil {
			if user, err := agents.JSONObject(plainJSON(b)); err == nil {
				doc = agents.MergePatch(doc, user)
			}
		}
	}
	return doc
}

// disabled is the entry a lane's start writes for each of the user's own MCP servers: the user's whole definition with
// "disabled": true (OpenCode ignores a bare {"disabled": true}; docs/research/user-settings-overrides.md, o2 and o3).
// obor's own server stays obor's; a name the project's own opencode.json(c) in the start dir define loads from the
// project's layer, above the generated config, so disabling it here would be no word of obor's; and one the override
// file declares (a null deletes the key, and OpenCode still loads the user's file) is the override's word. A user
// entry that is no object stays the user's.
func (c config) disabled(user map[string]any) map[string]any {
	out := map[string]any{}
	project, named := c.projectServers(), servers(c.over)
	for name, def := range servers(user) {
		entry, _ := def.(map[string]any)
		_, projectOwn := project[name]
		overOwn := named[name] != nil
		if name == "obor" || projectOwn || overOwn || entry == nil {
			continue
		}
		out[name] = agents.MergePatch(entry, map[string]any{"disabled": true})
	}
	return out
}

// projectServers is the names the project's own config files (ProjectFiles, in the start dir) define under
// mcp.servers: OpenCode lays them above the generated config, whatever it holds.
func (c config) projectServers() map[string]bool {
	out := map[string]bool{}
	if c.dir == "" || c.read == nil {
		return out
	}
	for _, rel := range ProjectFiles {
		b, err := c.read(filepath.Join(c.dir, rel))
		if err != nil {
			continue
		}
		doc, err := agents.JSONObject(plainJSON(b))
		if err != nil {
			continue
		}
		for name := range servers(doc) {
			out[name] = true
		}
	}
	return out
}

// servers are doc's mcp.servers; nil when it names none.
func servers(doc map[string]any) map[string]any {
	mcp, _ := doc["mcp"].(map[string]any)
	s, _ := mcp["servers"].(map[string]any)
	return s
}
