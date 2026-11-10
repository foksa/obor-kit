package kind_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/kinds/opencode/kind"
)

// opencode is the kind as obor's plugin package runs it (agents.Kind from the protocol's replies), answered in process
// by Handle: the same requests and replies as the binary's, without the exec. stateDir: where a start's files go.
func opencode(t *testing.T, stateDir string) agents.Kind {
	t.Helper()
	plugin.Local[kind.Name] = kind.Handle
	t.Cleanup(func() { delete(plugin.Local, kind.Name) })
	k := agents.Plugins.Kind(kind.Name, "obor-kind-opencode", stateDir)
	if k.Name() != kind.Name || k.Caps().Hooks != agents.HooksPlugin {
		t.Fatalf("precondition: the kind describes itself: %s %+v", k.Name(), k.Caps())
	}
	return k
}

// OpenCode's start argv (--prompt, no resume, no registration on the command line), /exit with the popup's
// second Enter and the clear keys, `opencode run`, registered per start (nothing user-global), no transcript, typed
// delivery, the plugin hook transport.
func TestKind(t *testing.T) {
	k := opencode(t, t.TempDir())
	if got := agents.OverrideFiles(k); !slices.Equal(got, []string{"opencode_override.json"}) {
		t.Errorf("OverrideFiles = %q, want the manifest's override_files, opencode_override.json", got)
	}
	write := func(name string, _ []byte) (string, error) { return "/state/" + name, nil }
	for _, c := range []struct {
		name string
		in   agents.Start
		want []string
	}{
		{"fresh, prompt", agents.Start{Kit: "/kit", Args: []string{"--auto"}, Prompt: "go"}, []string{"--auto", "--prompt", "go"}},
		{"fresh, no prompt", agents.Start{Kit: "/kit", Args: []string{"--auto"}}, []string{"--auto"}},
		{"a resume is no resume: nothing added", agents.Start{Kit: "/kit", Args: []string{"--auto"}, Resume: "s", Prompt: "go"},
			[]string{"--auto"}},
		{"the model is no flag of the TUI: dropped (the env carries it)", agents.Start{Args: []string{"--auto", "-m", "p/x",
			"--model=p/y"}, Effort: "high"}, []string{"--auto"}},
	} {
		c.in.WriteState = write
		if l, err := agents.StartOf(k, c.in); err != nil || !slices.Equal(l.Args, c.want) {
			t.Errorf("%s: start = %q, %v; want %q", c.name, l.Args, err, c.want)
		}
	}
	if k.NotReady("Updated codex") != "" {
		t.Error("NotReady sees Codex's screen")
	}
	if s := k.Stop(); s.Quit != "/exit" || !s.SecondEnter || !slices.Equal(s.ClearInput, []string{"esc", "ctrl+u"}) || s.AfterEscape != nil ||
		s.Dialog != nil {
		t.Errorf("Stop = %+v", s)
	}
	if h := k.Headless(); h.PrintFlag || !slices.Equal(h.Subcommands, []string{"run"}) || !slices.Equal(h.Anywhere, []string{"run"}) ||
		!slices.Contains(h.ValueOpts, "--prompt") {
		t.Errorf("Headless = %+v", h)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	// nothing user-global; an old global entry and link are the user's to remove by hand.
	in, err := agents.InstallOf(k, "/kit", env(map[string]string{"HOME": "/h"}))
	if m := in.MCP; err != nil || m.Where != agents.PerStart || m.Path != "" || m.Entry != nil {
		t.Errorf("Install = %+v, %v: want per start, nothing user-global", in, err)
	}
	if how, reg, static := in.MCP.Lane([]string{"-m", "x"}, nil); !reg || static || !strings.Contains(how, "OPENCODE_CONFIG") ||
		!strings.Contains(how, "/kit/bin/obor mcp") || !strings.Contains(how, "plugin /kit/opencode") {
		t.Errorf("Lane = %q, %v, %v: want registered per start by the generated config", how, reg, static)
	}
	if got := k.Transcript(env(map[string]string{"HOME": "/h"}), "s1"); got != "" {
		t.Errorf("Transcript = %q, want none", got)
	}
	if d, ok := k.Deliver("hi", nil); !ok || !d.Typed {
		t.Errorf("Deliver = %+v, %v; want typed", d, ok)
	}
	if got, ok := k.LastTurn([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"m","codex_error_info":"usage_limit_exceeded"}}}` + "\n")); ok { // its turns are not read (Caps.Status terminal below)
		t.Errorf("LastTurn = %+v, true; want none read", got)
	}
	if got, ok := k.Session([]byte(`{"type":"session_meta","payload":{"id":"x"}}` + "\n")); ok {
		t.Errorf("Session = %+v, true; want none read", got)
	}
	// parse the plugin's StopFailure details; the app tests exercise worker and role routing.
	at := time.Unix(1793361600, 0)
	for _, c := range []struct {
		name, err, details string
		want               time.Time
		limited            bool
	}{
		{"rate_limit with plugin error_details", "rate_limit", "You exceeded your current quota; resets at 6pm (provider.rate-limit)|1793365200", time.Unix(1793365200, 0), true},
		{"rate_limit naming a clock time: its next occurrence", "rate_limit", "resets at 6pm", time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC), true},
		{"rate_limit naming no time: limited, unknown reset", "rate_limit", "slow down", time.Time{}, true},
		{"billing_error", "billing_error", "budget spent (provider.budget-limit)", time.Time{}, false},
	} {
		at := at
		if c.name == "rate_limit naming a clock time: its next occurrence" {
			at = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
		}
		got, limited := k.Reset(c.err, c.details, at)
		if !got.Equal(c.want) || limited != c.limited {
			t.Errorf("%s: Reset = %v, %v; want %v, %v", c.name, got, limited, c.want, c.limited)
		}
	}
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksPlugin, ModelArg: true, VersionVar: "OPENCODE_VERSION", Status: agents.StatusTerminal}) {
		t.Errorf("Caps = %+v", c)
	}
	if r := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s"})); r != nil {
		t.Errorf("InboxRecord = %s, want none", r)
	}
	if !slices.Equal(k.Vars(), []string{kind.ModelVar, kind.ConfigVar, kind.SkillsVar}) {
		t.Errorf("Vars = %q", k.Vars())
	}
}

// the type's model and the effort reach obor's plugin as OBOR_OPENCODE_MODEL
// <provider>/<model>#<effort>, the model being the type's --model/-m only: obor never guesses OpenCode's model, so a
// type without one unsets the variable (OpenCode's own choice stands, even when the user's files name a model) and an
// effort on it is refused, by the start and by the args check before it (agents.ArgsChecker). Without a kit the
// model is the only variable with a value: no config, no file, and the skills switch unset.
func TestStartEnvModelReachesThePlugin(t *testing.T) {
	k := opencode(t, t.TempDir())
	// The user's files name models; none may be read (Read is a trap).
	read := func(p string) ([]byte, error) {
		t.Errorf("the start read %s: obor never reads OpenCode's config or state", p)
		return []byte(`{"model":"cfg/m","recent":[{"providerID":"go","modelID":"ds"}]}`), nil
	}
	env := func(k string) string { return map[string]string{"HOME": "/h"}[k] }
	start := func(args []string, effort string) (string, bool, error) {
		l, err := agents.StartOf(k, agents.Start{Args: args, Effort: effort, Read: read, Env: env,
			WriteState: func(name string, _ []byte) (string, error) {
				t.Errorf("a start without a kit wrote %s", name)
				return "", nil
			}})
		v, set := l.Env[kind.ModelVar]
		if off, unset := l.Env[kind.SkillsVar]; err == nil && (!set || len(l.Env) != 2 || !unset || off != "") {
			t.Errorf("start(%q, %q) env = %v: want %s, %s unset and nothing else", args, effort, l.Env, kind.ModelVar, kind.SkillsVar)
		}
		return v, set, err
	}
	if kind.ModelVar != "OBOR_OPENCODE_MODEL" {
		t.Errorf("ModelVar = %q: opencode/obor.js reads OBOR_OPENCODE_MODEL", kind.ModelVar)
	}
	if v, _, err := start([]string{"--auto", "--model", "p/m"}, "high"); err != nil || v != "p/m#high" {
		t.Errorf("args' model with effort: %q, %v", v, err)
	}
	if v, _, err := start([]string{"-m=p/m#low"}, "max"); err != nil || v != "p/m#max" {
		t.Errorf("the effort replaces the model's own variant: %q, %v", v, err)
	}
	if v, _, err := start([]string{"--model=p/m#low"}, ""); err != nil || v != "p/m#low" {
		t.Errorf("the model's own variant, no effort: %q, %v", v, err)
	}
	if v, _, err := start([]string{"--model", "p/m"}, ""); err != nil || v != "p/m" {
		t.Errorf("args' model, no effort: %q, %v", v, err)
	}
	if v, set, err := start([]string{"--auto"}, ""); err != nil || !set || v != "" {
		t.Errorf("no model, no effort: %q (set %v), %v; want the variable unset", v, set, err)
	}
	_, _, err := start([]string{"--auto"}, "high")
	if err == nil || !strings.Contains(err.Error(), "needs a model id") {
		t.Errorf("effort without --model: %v; want a refusal naming the missing model id", err)
	}
	if cerr := agents.CheckEffortOf(k, []string{"--auto"}, "high"); cerr == nil || !strings.Contains(cerr.Error(), "needs a model id") {
		t.Errorf("the args check without --model: %v; want the same refusal", cerr)
	}
	if cerr := agents.CheckEffortOf(k, []string{"--model", "p/m"}, "high"); cerr != nil {
		t.Errorf("the args check with --model: %v", cerr)
	}
	if cerr := agents.CheckEffortOf(k, []string{"--auto"}, ""); cerr != nil {
		t.Errorf("the args check, no effort: %v", cerr)
	}
	if cerr := agents.CheckEffortOf(k, []string{"--model", "p/m"}, "turbo"); cerr == nil || !strings.Contains(cerr.Error(), `effort "turbo" is not one of opencode's`) {
		t.Errorf("an effort not among its efforts: %v; want a refusal", cerr)
	}
}

// a trimmed start (every agent but a lead) sets OpenCode's own switch for the Claude skill dirs; an untrimmed one
// has it in its env as "", which the start unsets (the pane's shell may hold an earlier, trimmed start's), and the
// manifest's vars name it so every other kind's start unsets it too. The argv is the same either way.
func TestStartEnvTrimsTheUserSkills(t *testing.T) {
	k := opencode(t, t.TempDir())
	if kind.SkillsVar != "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS" {
		t.Errorf("SkillsVar = %q: OpenCode reads OPENCODE_DISABLE_CLAUDE_CODE_SKILLS", kind.SkillsVar)
	}
	if !slices.Contains(k.Vars(), kind.SkillsVar) {
		t.Errorf("Vars %q lack %s: another kind's start would not unset it", k.Vars(), kind.SkillsVar)
	}
	start := func(trim bool) agents.Launch {
		l, err := agents.StartOf(k, agents.Start{Args: []string{"--auto", "--model", "p/m"}, Prompt: "go", TrimUser: trim})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	trimmed, whole := start(true), start(false)
	if v := trimmed.Env[kind.SkillsVar]; v != "1" {
		t.Errorf("a trimmed start: %s = %q in %v, want 1", kind.SkillsVar, v, trimmed.Env)
	}
	if v, set := whole.Env[kind.SkillsVar]; !set || v != "" {
		t.Errorf("an untrimmed start: %s = %q (in the env: %v), want it there and empty (unset)", kind.SkillsVar, v, set)
	}
	if !slices.Equal(trimmed.Args, whole.Args) || !slices.Equal(whole.Args, []string{"--auto", "--prompt", "go"}) {
		t.Errorf("argv %q (trimmed) and %q: want the same, no switch on the command line", trimmed.Args, whole.Args)
	}
}

// a start from a kit writes one generated config into the state dir, named by OPENCODE_CONFIG (a path, so the
// env line stays short): mcp.servers.obor runs the launching kit's `bin/obor mcp`, pinned, and plugins names the kit's
// plugin package dir; the model variable still rides along. A failed write is the start's error.
func TestStartEnvNamesTheGeneratedConfig(t *testing.T) {
	state := t.TempDir()
	k := opencode(t, state)
	written := map[string][]byte{}
	write := func(name string, data []byte) (string, error) {
		written[name] = data
		return "/p/.obor/state/" + name, nil
	}
	l, err := agents.StartOf(k, agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}, Effort: "high", WriteState: write})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[kind.ConfigFile] == nil {
		t.Fatalf("written %v: want the one generated config", written)
	}
	vars := l.Env
	if kind.ConfigVar != "OPENCODE_CONFIG" || vars[kind.ConfigVar] != "/p/.obor/state/opencode.json" || vars[kind.ModelVar] != "p/m#high" || len(vars) != 3 {
		t.Errorf("start env = %v: want OPENCODE_CONFIG=<the file> and the model", vars)
	}
	if !bytes.Equal(written[kind.ConfigFile], kind.Config("/kit")) {
		t.Errorf("written %s, want Config(/kit)", written[kind.ConfigFile])
	}
	var got struct {
		MCP struct {
			Servers map[string]struct {
				Type        string
				Command     []string
				Environment map[string]string
			}
		}
		Plugins []string
	}
	if err := json.Unmarshal(written[kind.ConfigFile], &got); err != nil {
		t.Fatalf("%v: %s", err, written[kind.ConfigFile])
	}
	o := got.MCP.Servers["obor"]
	if len(got.MCP.Servers) != 1 || o.Type != "local" || !slices.Equal(o.Command, []string{"/kit/bin/obor", "mcp"}) ||
		len(o.Environment) != 1 || o.Environment[agents.Pin] != "1" {
		t.Errorf("mcp.servers = %+v: want obor, the launching kit's bin/obor mcp, pinned", got.MCP.Servers)
	}
	if !slices.Equal(got.Plugins, []string{"file:///kit/opencode"}) {
		t.Errorf("plugins = %q: want the kit's plugin package dir", got.Plugins)
	}
	if _, err := agents.StartOf(k, agents.Start{Kit: "/kit", WriteState: func(string, []byte) (string, error) { return "", errors.New("disk full") }}); err == nil ||
		!strings.Contains(err.Error(), "disk full") {
		t.Errorf("a failed write: %v; want the start's error", err)
	}
	// no writer: the kind's state dir
	if l, err := agents.StartOf(k, agents.Start{Kit: "/kit"}); err != nil || l.Env[kind.ConfigVar] != filepath.Join(state, kind.ConfigFile) {
		t.Errorf("no writer: env %v, %v; want the config in the state dir", l.Env, err)
	}
}

// config validate notes a project file (any of the four OpenCode reads) defining mcp.servers.obor, JSONC
// comments and trailing commas read, and one that does not parse; none for a clean project. Through the protocol
// (project-warnings) the kind reads the files of the dir itself.
func TestProjectWarnings(t *testing.T) {
	pc, ok := opencode(t, t.TempDir()).(agents.ProjectChecker)
	if !ok {
		t.Fatal("opencode reads no project file")
	}
	const jsonc = "{\n // mine\n \"mcp\": {\"servers\": {\"obor\": {\"type\": \"local\", \"command\": [\"x // y\"],},}, /* z */ },\n}\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"none", nil, nil},
		{"clean", map[string]string{"opencode.json": `{"model":"m","mcp":{"servers":{"mine":{}}}}`}, nil},
		{"obor in opencode.json", map[string]string{"opencode.json": `{"mcp":{"servers":{"obor":{}}}}`},
			[]string{"opencode.json: mcp.servers.obor"}},
		{"obor in .opencode/opencode.jsonc, comments and trailing commas", map[string]string{".opencode/opencode.jsonc": jsonc},
			[]string{".opencode/opencode.jsonc: mcp.servers.obor"}},
		{"does not parse", map[string]string{"opencode.jsonc": `{"mcp": `}, []string{"opencode.jsonc: does not parse"}},
	} {
		dir := t.TempDir()
		for rel, text := range c.files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := pc.ProjectWarnings(dir, nil)
		if len(got) != len(c.want) {
			t.Errorf("%s: warnings %q, want %q", c.name, got, c.want)
			continue
		}
		for i := range got {
			if !strings.HasPrefix(got[i], c.want[i]) {
				t.Errorf("%s: warning %q, want %q…", c.name, got[i], c.want[i])
			}
		}
	}
	// a file there that cannot be read is a file that does not parse
	read := func(string) ([]byte, error) { return nil, fs.ErrPermission }
	if got := kind.ProjectWarnings("/p", read); len(got) != 4 || !strings.Contains(got[0], "does not parse") {
		t.Errorf("unreadable files: %q", got)
	}
}

// The binary's own edges: a request with another version, a call it does not answer and a request that is no JSON
// exit 2 with the reason on stderr; a call with no argument prints its usage.
func TestMainRefuses(t *testing.T) {
	for _, c := range []struct {
		name, req, why string
		args           []string
	}{
		{"another version", `{"version":2}`, "request version 2", []string{"describe"}},
		{"an unknown call", `{"version":1}`, `no call "mcp-lane"`, []string{"mcp-lane"}},
		{"no JSON", `hello`, "malformed request", []string{"start"}},
		{"no call", `{"version":1}`, "usage:", nil},
	} {
		var out, errs bytes.Buffer
		if rc := kind.Main(c.args, strings.NewReader(c.req), &out, &errs); rc != 2 || out.Len() != 0 || !strings.Contains(errs.String(), c.why) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want 2, nothing, %q", c.name, rc, out.String(), errs.String(), c.why)
		}
	}
	var out, errs bytes.Buffer
	if rc := kind.Main([]string{"describe"}, strings.NewReader(`{"version":1,"call":"describe"}`), &out, &errs); rc != 0 ||
		!strings.Contains(out.String(), `"name":"opencode"`) || errs.Len() != 0 {
		t.Errorf("describe: exit %d, stdout %q, stderr %q", rc, out.String(), errs.String())
	}
}

// userConfig is a user's OpenCode config dir under a fresh XDG_CONFIG_HOME: opencode.json (with comments and
// trailing commas) holding a setting and two MCP servers. started is the generated config of a start from kit /kit,
// trimmed or not, with the override file override ("": the project has none), read back.
func userConfig(t *testing.T) (started func(trim bool, override string) (map[string]any, []byte)) {
	t.Helper()
	xdg := t.TempDir()
	file := filepath.Join(xdg, "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"default_agent": "plan", // mine
		"mcp": {"servers": {
			"herd": {"type": "local", "command": ["/h/bin/herd", "mcp"], "environment": {"HERD_NO_SELF_HOST": "1"}},
			"docs": {"type": "remote", "url": "https://docs.example/mcp"},},},}`), 0o644); err != nil {
		t.Fatal(err)
	}
	k := opencode(t, t.TempDir())
	env := func(v string) string { return map[string]string{"XDG_CONFIG_HOME": xdg, "HOME": "/nowhere"}[v] }
	return func(trim bool, override string) (map[string]any, []byte) {
		t.Helper()
		var b []byte
		write := func(name string, data []byte) (string, error) { b = data; return "/state/" + name, nil }
		in := agents.Start{Kit: "/kit", TrimUser: trim, Env: env, WriteState: write}
		if override != "" {
			in.Overrides = map[string]string{kind.OverrideFile: override}
		}
		if _, err := agents.StartOf(k, in); err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("the generated config: %v: %s", err, b)
		}
		return doc, b
	}
}

// servers of a generated config.
func serversOf(doc map[string]any) map[string]any {
	mcp, _ := doc["mcp"].(map[string]any)
	s, _ := mcp["servers"].(map[string]any)
	return s
}

// the override file is laid over the user's own config as a merge patch, and the result is the generated config's
// base: OpenCode's own plugin removal is in its plugins after obor's plugin, a key it sets wins over the user's, a
// server it only marks disabled is the user's whole definition with disabled true (OpenCode ignores a bare one),
// the user's other server and keys stay, and obor's server is not the file's to replace.
func TestOverrideFileIsMergedOverTheUserConfig(t *testing.T) {
	started := userConfig(t)
	doc, _ := started(true, `{"plugins": ["-herdr.opencode"]}`)
	if want := []any{"file:///kit/opencode", "-herdr.opencode"}; !reflect.DeepEqual(doc["plugins"], want) {
		t.Errorf("plugins = %v, want %v", doc["plugins"], want)
	}
	if s := serversOf(doc); doc["default_agent"] != "plan" || len(s) != 3 || s["herd"] == nil || s["docs"] == nil {
		t.Errorf("the override names plugins alone: default_agent %v, servers %v, want the user's own beside obor's", doc["default_agent"], s)
	}

	doc, _ = started(true, `{"default_agent": "build", "mcp": {"timeout": 5, "servers": {
		"herd": {"disabled": true}, "docs": null, "mine": {"type": "local", "command": ["m"]},
		"obor": {"type": "local", "command": ["not-obor"]}}}}`)
	if doc["default_agent"] != "build" {
		t.Errorf("default_agent = %v, want the override's build", doc["default_agent"])
	}
	mcp, _ := doc["mcp"].(map[string]any)
	s := serversOf(doc)
	herd := map[string]any{"type": "local", "command": []any{"/h/bin/herd", "mcp"},
		"environment": map[string]any{"HERD_NO_SELF_HOST": "1"}, "disabled": true}
	if !reflect.DeepEqual(s["herd"], herd) {
		t.Errorf("mcp.servers.herd = %v, want the user's definition with disabled: %v", s["herd"], herd)
	}
	mine, _ := s["mine"].(map[string]any)
	obor, _ := s["obor"].(map[string]any)
	if _, docs := s["docs"]; docs || mcp["timeout"] != 5.0 || len(s) != 3 || mine["type"] != "local" ||
		!reflect.DeepEqual(obor["command"], []any{"/kit/bin/obor", "mcp"}) {
		t.Errorf("mcp = %v: want the override's timeout and server, no docs (null), and obor's own server", mcp)
	}
	if want := []any{"file:///kit/opencode"}; !reflect.DeepEqual(doc["plugins"], want) {
		t.Errorf("plugins = %v, want %v", doc["plugins"], want)
	}
}

// a lead's start (not trimmed) writes obor's own config unchanged, whatever the user's config and the project's
// override file hold, and so does a trimmed start of a project with no override file; the same override on a trimmed
// start does change the file.
func TestLeadStartConfigIsUnchanged(t *testing.T) {
	started := userConfig(t)
	const override = `{"plugins": ["-herdr.opencode"]}`
	if _, b := started(true, override); bytes.Equal(b, kind.Config("/kit")) {
		t.Fatal("precondition: the override file changes a trimmed start's config")
	}
	if _, b := started(false, override); !bytes.Equal(b, kind.Config("/kit")) {
		t.Errorf("a lead's config:\n%s\nwant obor's own:\n%s", b, kind.Config("/kit"))
	}
	if _, b := started(true, ""); !bytes.Equal(b, kind.Config("/kit")) {
		t.Errorf("a trimmed start's config with no override file:\n%s\nwant obor's own:\n%s", b, kind.Config("/kit"))
	}
}
