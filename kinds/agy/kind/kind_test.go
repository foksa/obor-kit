package kind_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/kinds/agy/kind"
)

// agy is the kind as obor's plugin package runs it (agents.Kind from the protocol's replies), answered in process by
// Handle: the same requests and replies as the binary's, without the exec. stateDir: where a start's files go.
func agy(t *testing.T, stateDir string) agents.Kind {
	t.Helper()
	plugin.Local[kind.Name] = kind.Handle
	t.Cleanup(func() { delete(plugin.Local, kind.Name) })
	k := agents.Plugins.Kind(kind.Name, "obor-kind-agy", stateDir)
	if k.Name() != kind.Name || !k.Caps().Resume {
		t.Fatalf("precondition: the kind describes itself: %s %+v", k.Name(), k.Caps())
	}
	return k
}

// The manifest: no hooks, a resume, no named session and no session read, the model as an id, the terminal's status,
// the efforts agy takes, /exit with one Enter and Ctrl+U, -p/--print, --prompt and `agy acp` headless, the
// conversation's transcript.jsonl under HOME, typed delivery, the override file, and the not-ready pattern that fails a
// start on agy's wrong-model notice (each notice of probes P5) and on no ready screen.
func TestManifest(t *testing.T) {
	k := agy(t, t.TempDir())
	if c := k.Caps(); c != (agents.Caps{Resume: true, ModelArg: true, Transcript: true, Status: agents.StatusTerminal}) {
		t.Errorf("Caps = %+v", c)
	}
	if got := k.Efforts(); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("Efforts = %q", got)
	}
	if s := k.Stop(); s.Quit != "/exit" || s.SecondEnter || !slices.Equal(s.ClearInput, []string{"ctrl+u"}) || s.Dialog != nil {
		t.Errorf("Stop = %+v", s)
	}
	h := k.Headless()
	if !h.PrintFlag || !slices.Equal(h.Flags, []string{"--prompt"}) || !slices.Equal(h.Subcommands, []string{"acp"}) ||
		!slices.Contains(h.ValueOpts, "--conversation") || !slices.Contains(h.ValueOpts, "-i") ||
		!slices.Contains(h.ValueOpts, "--settings") || slices.Contains(h.ValueOpts, "--prompt") {
		t.Errorf("Headless = %+v", h)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := k.Transcript(env(map[string]string{"HOME": "/h"}), "c1"); got != "/h/.gemini/antigravity-cli/brain/c1/.system_generated/logs/transcript.jsonl" {
		t.Errorf("Transcript = %q", got)
	}
	if d, ok := k.Deliver("hi", nil); !ok || !d.Typed {
		t.Errorf("Deliver = %+v, %v; want typed", d, ok)
	}
	if got := agents.OverrideFiles(k); !slices.Equal(got, []string{"settings_override.json"}) {
		t.Errorf("OverrideFiles = %q", got)
	}
	if len(k.Vars()) != 0 {
		t.Errorf("Vars = %q, want none", k.Vars())
	}
	for _, screen := range []string{
		`--model gemini-4-neon-high conflicts with --effort=low. Using "Gemini 4 Neon (High)" instead.`,
		`gemini-3.8-flash has no "xhigh" effort (available: low, medium, high). Using "Gemini 4 Neon (High)" instead.`,
		"model gemini-9-bogus is not recognized as a known model or custom model in settings. Using \"Gemini 4 Neon\n(High)\"",
		"model gemini-9-bogus is not\nrecognized as a known model",
	} {
		if why := k.NotReady(screen); !strings.Contains(why, "runs another model than the start asked for") {
			t.Errorf("NotReady(%q) = %q, want the wrong-model reason", screen, why)
		}
	}
	if why := k.NotReady("Introducing Gemini 4 Neon … esc to dismiss\n> \n? for shortcuts\nauto · Gemini 3.8 Flash · medium"); why != "" {
		t.Errorf("NotReady on a ready start screen = %q", why)
	}
}

// home is a HOME whose saved agy settings are user (nil: no file).
func home(t *testing.T, user string) map[string]string {
	t.Helper()
	h := t.TempDir()
	if user != "" {
		p := filepath.Join(h, kind.UserSettingsFile)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(user), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{"HOME": h}
}

// started is one start through the plugin bridge: its argv with the settings file's path as {settings}, and the
// settings file it wrote.
func started(t *testing.T, k agents.Kind, in agents.Start) ([]string, map[string]any, error) {
	t.Helper()
	files := map[string][]byte{}
	in.WriteState = func(name string, b []byte) (string, error) { files[name] = b; return "/state/" + name, nil }
	l, err := agents.StartOf(k, in)
	if err != nil {
		return nil, nil, err
	}
	if len(files) != 1 || len(l.Env) != 0 {
		t.Fatalf("start wrote %d files, env %v; want the settings file and no env", len(files), l.Env)
	}
	var settings map[string]any
	for name, b := range files {
		if err := json.Unmarshal(b, &settings); err != nil {
			t.Fatalf("settings file %s: %v", name, err)
		}
		for i, a := range l.Args {
			if a == "/state/"+name {
				l.Args[i] = "{settings}"
			}
		}
	}
	return l.Args, settings, nil
}

// The start's argv per ref: the settings file first, the type's args, then the model (with an effort: the slug's base
// and --effort; without: the slug alone, or the args' own --effort with the base), a write dir as --add-dir, then -i
// with a first prompt or --conversation on a resume (its prompt typed later, not in the argv). An effort with no model
// is refused, by the start and by the args check.
func TestStartArgs(t *testing.T) {
	k := agy(t, t.TempDir())
	env := home(t, "")
	for _, c := range []struct {
		name string
		in   agents.Start
		want []string
	}{
		{"a ref's effort: the slug's base and --effort", agents.Start{Args: []string{"--model", "gemini-3.8-flash-low"},
			Effort: "high", Prompt: "go"},
			[]string{"--settings", "{settings}", "--model", "gemini-3.8-flash", "--effort", "high", "-i", "go"}},
		{"no effort: the slug alone", agents.Start{Args: []string{"--sandbox", "--model", "gemini-4-neon-high"}, Prompt: "go"},
			[]string{"--settings", "{settings}", "--sandbox", "--model", "gemini-4-neon-high", "-i", "go"}},
		{"a slug with no effort word is its own base", agents.Start{Args: []string{"--model=gemini-4-neon"}, Effort: "low"},
			[]string{"--settings", "{settings}", "--model", "gemini-4-neon", "--effort", "low"}},
		{"the args' own --effort, no ref effort", agents.Start{Args: []string{"--effort", "medium", "--model", "gemini-3.8-flash-low"}},
			[]string{"--settings", "{settings}", "--model", "gemini-3.8-flash", "--effort", "medium"}},
		{"a resume: --conversation, no -i", agents.Start{Args: []string{"--model", "gemini-4-neon-high"}, Resume: "c1", Prompt: "go"},
			[]string{"--settings", "{settings}", "--model", "gemini-4-neon-high", "--conversation", "c1"}},
		{"a write dir", agents.Start{WriteDirs: []string{"/main/docs/reviews"}},
			[]string{"--settings", "{settings}", "--add-dir", "/main/docs/reviews"}},
	} {
		c.in.Env = func(v string) string { return env[v] }
		if got, _, err := started(t, k, c.in); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: start = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, _, err := started(t, k, agents.Start{Effort: "high"}); err == nil || !strings.Contains(err.Error(), "needs a model id") {
		t.Errorf("effort without a model: %v, want a refusal naming the model id", err)
	}
	if err := agents.CheckEffortOf(k, []string{"--sandbox"}, "high"); err == nil || !strings.Contains(err.Error(), "needs a model id") {
		t.Errorf("the args check without a model: %v, want the same refusal", err)
	}
	if err := agents.CheckEffortOf(k, []string{"--model", "gemini-4-neon-high"}, "high"); err != nil {
		t.Errorf("the args check with a model: %v", err)
	}
}

// A lead's start (untrimmed) and another agent's (trimmed) write their settings to different files, per start dir:
// the lead's has no agentMode (its own mode) and allows obor's commands beside its MCP tools, and the project's
// override file reaches only the trimmed start, as OpenCode's does.
func TestStartSettingsLeadAndWorker(t *testing.T) {
	k := agy(t, t.TempDir())
	env := home(t, `{"colorScheme":"dark","permissions":{"allow":["command(git status)"],"deny":["command(rm)"]}}`)
	over := map[string]string{kind.OverrideFile: `{"permissions":{"allow":["command(go test)"]}}`}
	start := func(trim bool) map[string]any {
		_, s, err := started(t, k, agents.Start{TrimUser: trim, Overrides: over, Dir: "/p/wt/L-01", Root: "/p",
			Env: func(v string) string { return env[v] }})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	lead := start(false)
	if want := (map[string]any{"permissions": map[string]any{"allow": []any{"command(git status)", kind.MCPRule, kind.LeadRule},
		"deny": []any{"command(rm)"}}}); !reflect.DeepEqual(lead, want) {
		t.Errorf("a lead's settings = %v, want %v", lead, want)
	}
	worker := start(true)
	if want := (map[string]any{"agentMode": "auto", "permissions": map[string]any{"allow": []any{"command(go test)", kind.MCPRule},
		"deny": []any{"command(rm)"}}}); !reflect.DeepEqual(worker, want) {
		t.Errorf("a worker's settings = %v, want %v", worker, want)
	}
	if kind.SettingsFile(true, "/p") == kind.SettingsFile(false, "/p") ||
		kind.SettingsFile(false, "/p/wt/L-01") == kind.SettingsFile(false, "/p/wt/L-02") ||
		kind.SettingsFile(false, "/p/wt/L-01") != kind.SettingsFile(false, "/p/wt/L-01") {
		t.Error("SettingsFile: want one name per kind of start and start dir")
	}
	if _, _, err := started(t, k, agents.Start{TrimUser: true, Overrides: map[string]string{kind.OverrideFile: `{`}}); err == nil ||
		!strings.Contains(err.Error(), "settings_override.json: does not parse") {
		t.Errorf("an override that does not parse: %v, want the start refused naming the file", err)
	}
}

// Each key the settings file names is the user's saved value with the override laid over it (a JSON Merge Patch) and
// obor's entries last; the user's other keys are left out (agy keeps its saved values), and so is a key the override
// deletes.
func TestSettingsMerge(t *testing.T) {
	user := map[string]any{"colorScheme": "dark", "model": "gemini-4-neon-high",
		"permissions": map[string]any{"allow": []any{"command(ls)"}, "ask": []any{"read_url(*)"}}}
	over := map[string]any{"model": nil, "toolPermission": "request-review", "agentMode": "plan",
		"permissions": map[string]any{"allow": []any{"read_url(proxy.golang.org)", "mcp(obor/*)"}, "ask": nil}}
	got := kind.Settings(user, over, false, "", "")
	want := map[string]any{"toolPermission": "request-review", "agentMode": "auto",
		"permissions": map[string]any{"allow": []any{"read_url(proxy.golang.org)", "mcp(obor/*)"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Settings = %v, want %v", got, want)
	}
	if user["model"] != "gemini-4-neon-high" || len(user["permissions"].(map[string]any)["allow"].([]any)) != 1 {
		t.Errorf("Settings changed the user's settings: %v", user)
	}
	if got := kind.Settings(nil, nil, false, "", ""); !reflect.DeepEqual(got, map[string]any{"agentMode": "auto",
		"permissions": map[string]any{"allow": []any{kind.MCPRule}}}) {
		t.Errorf("Settings with no user file and no override = %v", got)
	}
}

// Trust (D7): the start dir joins trustedWorkspaces when the project's root is in the user's saved list, the saved
// list kept; with the root not saved (the owner has not trusted the project) the key is not written, so agy asks; the
// override's own trusted folders are not the owner's trust of the project.
func TestSettingsTrust(t *testing.T) {
	saved := map[string]any{"trustedWorkspaces": []any{"/home/me", "/p/"}}
	got := kind.Settings(saved, nil, false, "/p/wt/L-01", "/p")
	if want := []any{"/home/me", "/p/", "/p/wt/L-01"}; !reflect.DeepEqual(got["trustedWorkspaces"], want) {
		t.Errorf("root saved: trustedWorkspaces = %v, want %v", got["trustedWorkspaces"], want)
	}
	if got := kind.Settings(saved, nil, true, "/p", "/p"); !reflect.DeepEqual(got["trustedWorkspaces"], []any{"/home/me", "/p/", "/p"}) {
		t.Errorf("a lead in the root: trustedWorkspaces = %v, want the saved list and the root", got["trustedWorkspaces"])
	}
	for name, c := range map[string]struct {
		user, over map[string]any
	}{
		"root not saved":              {map[string]any{"trustedWorkspaces": []any{"/home/me"}}, nil},
		"no saved list":               {nil, nil},
		"root only in the override":   {nil, map[string]any{"trustedWorkspaces": []any{"/p"}}},
		"a parent of the root, saved": {map[string]any{"trustedWorkspaces": []any{"/"}}, nil},
	} {
		got := kind.Settings(c.user, c.over, false, "/p/wt/L-01", "/p")
		if tw, _ := got["trustedWorkspaces"].([]any); slices.Contains(tw, any("/p/wt/L-01")) {
			t.Errorf("%s: trustedWorkspaces = %v, want the start dir not trusted", name, tw)
		}
	}
}

// The context of a transcript.jsonl tail is its last PLANNER_RESPONSE's input_tokens + cache_read_tokens at its
// created_at (the fixture's lines as agy 1.5.14 writes them); a tail with none, or a line whose time does not parse,
// reads none.
func TestContext(t *testing.T) {
	k := agy(t, t.TempDir())
	fixture := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-22T12:09:36Z","content":"<USER_REQUEST>hi</USER_REQUEST>"}
{"step_index":11,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T12:09:47Z","model":"gemini-3.8-flash-low","input_tokens":2291,"cache_read_tokens":10233,"output_tokens":37,"tool_calls":[{"name":"call_mcp_tool","args":{}}]}
{"step_index":12,"source":"SYSTEM","type":"TOOL_RESULT","status":"DONE","created_at":"2026-11-22T12:09:48Z"}
{"step_index":13,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T12:09:49Z","model":"gemini-3.8-flash-low","input_tokens":2396,"cache_read_tokens":10492,"output_tokens":1,"content":"DONE"}
{"step_index":14,"source":"MODEL","type":"PLANNER_RESPONSE","status":"RUNNING","created_at":"2026-11-22T12:09:50Z"}
`
	c, ok := k.Context([]byte(fixture))
	if !ok || c.Tokens != 2396+10492 || !c.At.Equal(time.Date(2026, 11, 22, 12, 9, 49, 0, time.UTC)) || c.TTL != "" {
		t.Errorf("Context = %+v, %v; want 12888 tokens at 12:09:49", c, ok)
	}
	for name, tail := range map[string]string{
		"no planner line": `{"type":"USER_INPUT","created_at":"2026-11-22T12:09:36Z"}` + "\n",
		"a bad time":      `{"type":"PLANNER_RESPONSE","created_at":"yesterday","input_tokens":5}` + "\n",
		"claude's line":   `{"type":"assistant","timestamp":"2026-10-13T10:00:00.000Z","message":{"usage":{"input_tokens":5}}}` + "\n",
	} {
		if c, ok := k.Context([]byte(tail)); ok {
			t.Errorf("%s: Context = %+v, want none", name, c)
		}
	}
	if _, ok := k.LastTurn([]byte(fixture)); ok {
		t.Error("LastTurn read a turn: status terminal reads none")
	}
	if _, ok := k.Session([]byte(fixture)); ok {
		t.Error("Session read one: the transcript names none")
	}
}

// Install registers obor's server through the project's committed .agents/mcp_config.json (where project): the file
// under the request's root, the mcpServers.obor entry naming obor on PATH, nothing user-global; the lane's start is
// served by that file (static).
func TestInstall(t *testing.T) {
	k := agy(t, t.TempDir())
	in, err := agents.InstallOf(k, "/kit", "/p", nil)
	m := in.MCP
	if err != nil || m.Where != agents.Project || m.Path != "/p/.agents/mcp_config.json" ||
		!slices.Equal(m.Key, []string{"mcpServers", "obor"}) ||
		!reflect.DeepEqual(m.Entry, map[string]any{"command": "obor", "args": []any{"mcp"}}) {
		t.Errorf("Install = %+v, %v", m, err)
	}
	if how, reg, static := m.Lane(nil, nil); !reg || !static || !strings.Contains(how, ".agents/mcp_config.json") {
		t.Errorf("Lane = %q, %v, %v; want served by the project file", how, reg, static)
	}
}

// Reset: an error naming a limit is limited, with the time the message names (none: unknown); any other is not.
func TestReset(t *testing.T) {
	k := agy(t, t.TempDir())
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	if got, limited := k.Reset("ERROR", "usage limit reached, resets at 6pm", at); !limited ||
		!got.Equal(time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("a limit with a time: %v, %v", got, limited)
	}
	if got, limited := k.Reset("RESOURCE_EXHAUSTED", "", at); !limited || !got.IsZero() {
		t.Errorf("a limit with no time: %v, %v", got, limited)
	}
	if _, limited := k.Reset("ERROR", "internal server error", at); limited {
		t.Error("a server error read as a limit")
	}
}
