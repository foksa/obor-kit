package kind

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"obor/internal/agents"
)

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

// Overridden is the generated config of a start from kit with over applied (nil, a lead's start: obor's own two
// entries alone). OpenCode ranks the OPENCODE_CONFIG file above the user's own config (opencode 2.2.0, probed: a
// scalar, an MCP server and a plugin removal in the file each won over the user's opencode.json; a project's config
// still ranks above the file), merges objects key by key, replaces an MCP server whole and adds plugin lists up. So:
//   - over's settings are the document's base, keys of opencode.json;
//   - each MCP server of the user's opencode.json[c] (userDir) whose name matches off.mcp is repeated whole with
//     "disabled": true (a bare {"disabled": true} is ignored);
//   - mcp.servers.obor is obor's own, whatever settings name there;
//   - plugins are obor's plugin, then settings' own list, then "-<id>" for each plugin of the user's plugins dir
//     whose id matches off.plugins.
//
// off.hooks names nothing here: OpenCode's hooks are plugins (off.plugins); off.skills neither: SkillsVar is the
// skills switch. read and list are the user's files (nil with over nil).
func Overridden(kit string, over *agents.Override, userDir string, read func(string) ([]byte, error),
	list func(string) ([]os.DirEntry, error)) []byte {
	doc := map[string]any{}
	plugins := []any{"file://" + filepath.Join(kit, Name)}
	servers := map[string]any{}
	if over != nil {
		doc = agents.Merge(nil, over.Settings)
		own, _ := doc["plugins"].([]any)
		plugins = append(plugins, own...)
		if mcp, ok := doc["mcp"].(map[string]any); ok {
			if s, ok := mcp["servers"].(map[string]any); ok {
				servers = s
			}
		}
		if userDir != "" {
			for name, def := range userServers(userDir, read) {
				if agents.Matches(over.Off.MCP, name) {
					def["disabled"] = true
					servers[name] = def
				}
			}
			for _, id := range userPlugins(userDir, read, list) {
				if agents.Matches(over.Off.Plugins, id) {
					plugins = append(plugins, "-"+id)
				}
			}
		}
	}
	servers["obor"] = map[string]any{"type": "local", "command": []string{filepath.Join(kit, "bin", "obor"), "mcp"},
		"environment": map[string]string{agents.Pin: "1"}}
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	mcp["servers"] = servers
	doc["mcp"], doc["plugins"] = mcp, plugins
	b, _ := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n')
}

// userServers are the MCP servers the user's own config defines (mcp.servers of opencode.json and opencode.jsonc in
// dir), by name; a file that is absent or does not parse defines none.
func userServers(dir string, read func(string) ([]byte, error)) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		b, err := read(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var doc struct {
			MCP struct {
				Servers map[string]map[string]any `json:"servers"`
			} `json:"mcp"`
		}
		if json.Unmarshal(plainJSON(b), &doc) != nil {
			continue
		}
		for server, def := range doc.MCP.Servers {
			if def != nil {
				out[server] = def
			}
		}
	}
	return out
}

// pluginID finds the id a plugin module declares: `id: "<id>"` in its source.
var pluginID = regexp.MustCompile(`\bid\s*:\s*["']([^"'\s]+)["']`)

// userPlugins are the ids of the plugins OpenCode discovers in the user's plugins dir (dir/plugins): each .js or
// .ts file there and each package dir (its package.json's main, else index.js). A plugin's id is the one its module
// declares in its default export, not its file name, so the source is searched for it (after `export default`
// where the file has one); a file that declares none is not listed.
func userPlugins(dir string, read func(string) ([]byte, error), list func(string) ([]os.DirEntry, error)) []string {
	root := filepath.Join(dir, "plugins")
	entries, _ := list(root)
	var ids []string
	for _, e := range entries {
		file := filepath.Join(root, e.Name())
		switch {
		case e.IsDir():
			var pkg struct {
				Main string `json:"main"`
			}
			if b, err := read(filepath.Join(file, "package.json")); err == nil {
				_ = json.Unmarshal(b, &pkg)
			}
			if pkg.Main == "" {
				pkg.Main = "index.js"
			}
			file = filepath.Join(file, pkg.Main)
		case !strings.HasSuffix(file, ".js") && !strings.HasSuffix(file, ".ts"):
			continue
		}
		b, err := read(file)
		if err != nil {
			continue
		}
		src := string(b)
		if i := strings.LastIndex(src, "export default"); i >= 0 {
			src = src[i:]
		}
		if m := pluginID.FindStringSubmatch(src); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}
