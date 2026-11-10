package claude_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/claude"
)

func kind(t *testing.T) agents.Split {
	t.Helper()
	k, ok := agents.Lookup("claude")
	if !ok || k.Name() != "claude" {
		t.Fatalf("claude not registered: %v", agents.Names())
	}
	return k.(agents.Split)
}

// Claude's start argv, fresh, resumed and strict: the settings after the args, obor's --mcp-config right before
// the strict flag (with a kit, unless a config names obor), `--` before a prompt a config flag could take, --resume
// with no prompt.
func TestStart(t *testing.T) {
	k := kind(t)
	ours := claude.MCPConfig("/kit")
	const obor = `{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`
	files := func(path string) ([]byte, error) {
		if path == "/cfg/obor.json" {
			return []byte(obor), nil
		}
		return nil, errors.New("no file")
	}
	for _, c := range []struct {
		name string
		in   agents.Start
		want []string
	}{
		{"fresh, prompt", agents.Start{Kit: "/kit", Args: []string{"--model", "opus"}, Prompt: "go"},
			[]string{"--model", "opus", "go"}},
		{"fresh, settings, prompt", agents.Start{Kit: "/kit", Args: []string{"--model", "opus"},
			Settings: map[string]any{"autoMemoryEnabled": false, "x": "<&>"}, Prompt: "go"},
			[]string{"--model", "opus", "--settings", `{"autoMemoryEnabled":false,"x":"<&>"}`, "go"}},
		{"resume: --resume last, the prompt dropped", agents.Start{Kit: "/kit", Args: []string{"--model", "opus"},
			Settings: map[string]any{"a": 1}, Resume: "sid-1", Prompt: "go"},
			[]string{"--model", "opus", "--settings", `{"a":1}`, "--resume", "sid-1"}},
		// a fresh start runs as the session obor named, and a resume names no new one.
		{"fresh, the session obor named: --session-id before the prompt", agents.Start{Kit: "/kit",
			Args: []string{"--model", "opus"}, Session: "2f1c9a56-6e2b-4d7f-9a7e-3c0b8a1d5e42", Prompt: "go"},
			[]string{"--model", "opus", "--session-id", "2f1c9a56-6e2b-4d7f-9a7e-3c0b8a1d5e42", "go"}},
		{"resume: its id, never --session-id", agents.Start{Kit: "/kit", Args: []string{"--model", "opus"},
			Session: "2f1c9a56-6e2b-4d7f-9a7e-3c0b8a1d5e42", Resume: "sid-1"},
			[]string{"--model", "opus", "--resume", "sid-1"}},
		{"strict: obor's config before the flag, -- before the prompt",
			agents.Start{Kit: "/kit", Args: []string{"--strict-mcp-config"}, Prompt: "go"},
			[]string{"--mcp-config", ours, "--strict-mcp-config", "--", "go"}},
		{"strict, resumed: obor's config, no --", agents.Start{Kit: "/kit", Args: []string{"--strict-mcp-config"}, Resume: "s"},
			[]string{"--mcp-config", ours, "--strict-mcp-config", "--resume", "s"}},
		{"strict, no kit: nothing added", agents.Start{Args: []string{"--strict-mcp-config"}, Prompt: "go"},
			[]string{"--strict-mcp-config", "go"}},
		{"strict, a file naming obor (read): nothing added",
			agents.Start{Kit: "/kit", Args: []string{"--strict-mcp-config", "--mcp-config", "/cfg/obor.json"}, Prompt: "go", Read: files},
			[]string{"--strict-mcp-config", "--mcp-config", "/cfg/obor.json", "--", "go"}},
		{"strict, a file not read: obor's added",
			agents.Start{Kit: "/kit", Args: []string{"--strict-mcp-config", "--mcp-config", "/cfg/obor.json"}},
			[]string{"--mcp-config", ours, "--strict-mcp-config", "--mcp-config", "/cfg/obor.json"}},
		{"no args, no prompt", agents.Start{Kit: "/kit"}, []string{}},
		{"a lane's start: the flag and obor's config, then the settings, -- before the prompt",
			agents.Start{Kit: "/kit", Args: []string{"--permission-mode", "auto", "--model", "opus"},
				Settings: map[string]any{"autoMemoryEnabled": false}, StrictMCP: true, Prompt: "go"},
			[]string{"--permission-mode", "auto", "--model", "opus", "--mcp-config", ours, "--strict-mcp-config",
				"--settings", `{"autoMemoryEnabled":false}`, "--", "go"}},
		{"a lane's start, the args name the flag: once", agents.Start{Kit: "/kit", Args: []string{"--strict-mcp-config"}, StrictMCP: true},
			[]string{"--mcp-config", ours, "--strict-mcp-config"}},
		{"a lane's start, the args name it with =: obor's config, the flag once",
			agents.Start{Kit: "/kit", Args: []string{"--strict-mcp-config=true"}, StrictMCP: true},
			[]string{"--mcp-config", ours, "--strict-mcp-config=true"}},
		{"a lane's start, an explicit obor entry in the args: the flag once, no second config", agents.Start{Kit: "/kit", StrictMCP: true,
			Args: []string{"--mcp-config", obor}},
			[]string{"--mcp-config", obor, "--strict-mcp-config"}},
		{"a lane's start, no kit: the flag alone", agents.Start{StrictMCP: true, Args: []string{"--model", "opus"}, Prompt: "go"},
			[]string{"--model", "opus", "--strict-mcp-config", "go"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := slices.Clone(c.in.Args)
			if got := k.Start(c.in); !slices.Equal(got, c.want) {
				t.Errorf("Start =\n %q\nwant\n %q", got, c.want)
			}
			if !slices.Equal(args, c.in.Args) {
				t.Errorf("Start changed its args: %q", c.in.Args)
			}
		})
	}
	if !strings.Contains(ours, `"command":"/kit/bin/obor"`) || !strings.Contains(ours, `"OBOR_NO_SELF_HOST":"1"`) {
		t.Errorf("MCPConfig names the kit's bin/obor with the pin: %s", ours)
	}
}

// machine is a user's machine and a project for a start that trims the user's settings: files by path, and the
// state files the start wrote, by name.
type machine struct {
	files map[string]string
	state map[string]string
}

const (
	userSettings = "/home/u/.claude/settings.json"
	userFile     = "/home/u/.claude.json"
	projSettings = "/lane/" + claude.SettingsFile
	stateFile    = "/state/settings/pj-l-01.json"
	mcpFile      = "/state/mcp/pj-l-01.json"
)

// in is a worker's start (trimmed, obor's two settings) of agent pj-l-01 in /lane of the project /main with the
// override files overrides, the user's home /home/u (or env's own variables).
func (m *machine) in(overrides, env map[string]string) agents.Start {
	if env == nil {
		env = map[string]string{"HOME": "/home/u"}
	}
	m.state = map[string]string{}
	return agents.Start{Args: []string{"--model", "opus"}, Agent: "pj-l-01", TrimUser: true, Overrides: overrides,
		Dir: "/lane", Root: "/main", Env: func(k string) string { return env[k] },
		Read: func(path string) ([]byte, error) {
			if b, ok := m.files[path]; ok {
				return []byte(b), nil
			}
			if b, ok := m.state[strings.TrimPrefix(path, "/state/")]; ok { // a file this start wrote
				return []byte(b), nil
			}
			return nil, fs.ErrNotExist
		},
		WriteState: func(name string, data []byte) (string, error) {
			m.state[name] = string(data)
			return "/state/" + name, nil
		},
		Settings: map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}}
}

// written is the state file name the start wrote, as one line of JSON with sorted keys.
func (m *machine) written(t *testing.T, name string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(m.state[name]), &v); err != nil {
		t.Fatalf("the state file %s %q (of %v): %v", name, m.state[name], m.state, err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// start is the whole start (agents.StartOf) of in with a settings override (""; none): its argv and the settings
// file it wrote, the one state file.
func (m *machine) start(t *testing.T, override string, env map[string]string) (argv []string, file string) {
	t.Helper()
	var overrides map[string]string
	if override != "" {
		overrides = map[string]string{claude.SettingsOverride: override}
	}
	l, err := agents.StartOf(kind(t), m.in(overrides, env))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.state) != 1 {
		t.Fatalf("state files %v, want %s alone", m.state, claude.StateSettings("pj-l-01"))
	}
	return l.Args, m.written(t, claude.StateSettings("pj-l-01"))
}

// hooks is a settings file's hooks member: one SessionStart group with a command hook per command.
func hooks(commands ...string) string {
	list := make([]string, len(commands))
	for i, c := range commands {
		list[i] = `{"command":"` + c + `","type":"command"}`
	}
	return `"hooks":{"SessionStart":[{"hooks":[` + strings.Join(list, ",") + `]}]}`
}

// A start that trims the user's settings (every agent but a lead) loads the project's settings and one settings
// file obor writes, never the user's source: the file is the user's settings.json with the user's plugins off (the
// project's and obor's own left alone), without what the project's settings set, with the project's
// settings_override.json laid over it as a merge patch (what it sets stays), and obor's own settings.
func TestStartSettingsFile(t *testing.T) {
	const obor = `"autoMemoryEnabled":false,`
	const remote = `,"remoteControlAtStartup":false`
	const herdr, x = "~/.config/herdr/herdr-agent-state.sh", "/bin/x.sh"
	const line = `"statusLine":{"command":"line.sh","type":"command"}`
	user := `{"env":{"A":"1","B":"2"},` + line + `,` + hooks(herdr, x) + `,"enabledPlugins":{"a@m":true,"b@m":true}}`
	for _, c := range []struct {
		name     string
		files    map[string]string
		override string
		env      map[string]string
		want     string // the settings file
	}{
		{"no override file, the project enables b: env, statusLine and both hooks kept, a off",
			map[string]string{userSettings: user, projSettings: `{"enabledPlugins":{"b@m":true}}`}, "", nil,
			`{` + obor + `"enabledPlugins":{"a@m":false},"env":{"A":"1","B":"2"},` + hooks(herdr, x) + remote + `,` + line + `}`},
		{"the override empties SessionStart: no user SessionStart hook, the Stop hook kept",
			map[string]string{userSettings: `{"hooks":{"SessionStart":[{"hooks":[{"command":"/bin/x.sh","type":"command"}]}],` +
				`"Stop":[{"hooks":[{"command":"keep.sh","type":"command"}]}]}}`},
			`{"hooks": {"SessionStart": []}}`, nil,
			`{` + obor + `"hooks":{"SessionStart":[],"Stop":[{"hooks":[{"command":"keep.sh","type":"command"}]}]}` + remote + `}`},
		{"the override sets an env variable, the project sets statusLine, the user has both: env from the override, no statusLine",
			map[string]string{userSettings: `{"env":{"A":"1","MAX_THINKING_TOKENS":"128000"},` + line + `}`,
				projSettings: `{"statusLine":{"command":"proj.sh","type":"command"}}`},
			`{"env": {"MAX_THINKING_TOKENS": "8000"}}`, nil,
			`{` + obor + `"env":{"A":"1","MAX_THINKING_TOKENS":"8000"}` + remote + `}`},
		{"the override switches one user plugin back on: the other stays off",
			map[string]string{userSettings: `{"enabledPlugins":{"improve@improve":true,"b@m":true}}`},
			`{"enabledPlugins": {"improve@improve": true}}`, nil,
			`{` + obor + `"enabledPlugins":{"b@m":false,"improve@improve":true}` + remote + `}`},
		{"a null of the override deletes the user's key, a list of it replaces the user's",
			map[string]string{userSettings: `{"env":{"A":"1","B":"2"},` + line + `,"permissions":{"allow":["Bash(ls)"]}}`},
			`{"env": {"A": null}, "statusLine": null, "permissions": {"allow": ["Bash(go)"]}}`, nil,
			`{` + obor + `"env":{"B":"2"},"permissions":{"allow":["Bash(go)"]}` + remote + `}`},
		{"the project sets one env variable: not in the file, the other variable stays",
			map[string]string{userSettings: `{"env":{"A":"1","B":"2"}}`, projSettings: `{"env":{"A":"9"}}`}, "", nil,
			`{` + obor + `"env":{"B":"2"}` + remote + `}`},
		{"the project's local settings set a key of obor's and of the override's: obor's is left to them, the override's stays",
			map[string]string{"/lane/" + claude.LocalSettingsFile: `{"autoMemoryEnabled":true,"model":"opus"}`},
			`{"model": "sonnet", "effortLevel": "low"}`, nil,
			`{"effortLevel":"low","model":"sonnet"` + remote + `}`},
		{"the project sets hooks and permission lists too: the user's lists stay (Claude adds them up), its scalar goes",
			map[string]string{userSettings: `{` + hooks(herdr) + `,"permissions":{"allow":["Bash(ls)"],"defaultMode":"auto"}}`,
				projSettings: `{` + hooks("proj.sh") + `,"permissions":{"allow":["Bash(go)"],"defaultMode":"plan"}}`}, "", nil,
			`{` + obor + hooks(herdr) + `,"permissions":{"allow":["Bash(ls)"]}` + remote + `}`},
		{"the override sets a key obor sets: obor's own stands", nil, `{"autoMemoryEnabled": true, "effortLevel": "low"}`, nil,
			`{` + obor + `"effortLevel":"low"` + remote + `}`},
		{"a user settings file that does not parse: as none",
			map[string]string{userSettings: `{"enabledPlugins":`}, "", nil, `{"autoMemoryEnabled":false` + remote + `}`},
		{"no home in the environment: nothing of the user's is read",
			map[string]string{".claude/settings.json": `{"env":{"A":"1"}}`}, "", map[string]string{},
			`{"autoMemoryEnabled":false` + remote + `}`},
		{"the user's settings under $CLAUDE_CONFIG_DIR",
			map[string]string{"/cfg/settings.json": `{"env":{"A":"1"}}`, userSettings: `{"env":{"B":"2"}}`}, "",
			map[string]string{"HOME": "/home/u", "CLAUDE_CONFIG_DIR": "/cfg"}, `{` + obor + `"env":{"A":"1"}` + remote + `}`},
		{"obor's own plugin stays on, a plugin the user has off stays off",
			map[string]string{userSettings: `{"enabledPlugins":{"obor@obor-kit":true,"x@dev":true,"off@m":false}}`}, "", nil,
			`{` + obor + `"enabledPlugins":{"obor@obor-kit":true,"off@m":false,"x@dev":false}` + remote + `}`},
		{"a number of the user's is written as it stands",
			map[string]string{userSettings: `{"big":12345678901234567890,"cleanupPeriodDays":30}`}, "", nil,
			`{` + obor + `"big":12345678901234567890,"cleanupPeriodDays":30` + remote + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := machine{files: c.files}
			argv, file := m.start(t, c.override, c.env)
			if want := []string{"--model", "opus", "--setting-sources", "project,local", "--settings", stateFile}; !slices.Equal(argv, want) {
				t.Errorf("argv %q, want %q", argv, want)
			}
			if c.name == "a number of the user's is written as it stands" { // not through a float64
				if !strings.Contains(m.state[claude.StateSettings("pj-l-01")], `"big": 12345678901234567890`) {
					t.Errorf("the file %s lacks the number as written", m.state[claude.StateSettings("pj-l-01")])
				}
				return
			}
			if file != c.want {
				t.Errorf("settings file\n %s\nwant\n %s", file, c.want)
			}
		})
	}
}

// claude_override.json naming mcpServers makes the start strict on an MCP file obor writes: the user's servers
// (~/.claude.json's own and those it holds for the project's root) with the override laid over them, the project's
// approved .mcp.json servers and obor's own. Without mcpServers in it the start is as without the file.
func TestStartMCPOverride(t *testing.T) {
	k := kind(t)
	user := `{"mcpServers":{"blender":{"command":"b"},"x":{"command":"x"}},` +
		`"projects":{"/main":{"mcpServers":{"local":{"command":"l"}}},"/other":{"mcpServers":{"far":{"command":"f"}}}}}`
	m := machine{files: map[string]string{userFile: user,
		"/lane/.mcp.json": `{"mcpServers":{"proj":{"command":"p"},"unapproved":{"command":"u"}}}`,
		projSettings:      `{"enabledMcpjsonServers":["proj"]}`}}
	in := m.in(map[string]string{claude.MCPOverride: `{"mcpServers": {"blender": null}}`}, nil)
	in.Kit, in.Prompt = "/kit", "go"
	l, err := agents.StartOf(k, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--model", "opus", "--mcp-config", mcpFile, "--strict-mcp-config", "--setting-sources", "project,local",
		"--settings", stateFile, "--", "go"}; !slices.Equal(l.Args, want) {
		t.Errorf("argv\n %q\nwant\n %q", l.Args, want)
	}
	var ours map[string]map[string]any
	if err := json.Unmarshal([]byte(claude.MCPConfig("/kit")), &ours); err != nil {
		t.Fatal(err)
	}
	obor, _ := json.Marshal(ours["mcpServers"]["obor"])
	want := `{"mcpServers":{"local":{"command":"l"},"obor":` + string(obor) + `,"proj":{"command":"p"},"x":{"command":"x"}}}`
	if got := m.written(t, claude.StateMCP("pj-l-01")); got != want {
		t.Errorf("the MCP file\n %s\nwant (blender gone, x and the project root's local server kept, the approved .mcp.json server, obor's)\n %s", got, want)
	}

	// no mcpServers in the file: the lane's start is as without it (not strict here: the project approves its servers)
	in = m.in(map[string]string{claude.MCPOverride: `{"numStartups": 3}`}, nil)
	in.Kit, in.StrictMCP = "/kit", true
	if l, err = agents.StartOf(k, in); err != nil {
		t.Fatal(err)
	}
	if want := []string{"--model", "opus", "--setting-sources", "project,local", "--settings", stateFile}; !slices.Equal(l.Args, want) {
		t.Errorf("an override without mcpServers: argv %q, want %q", l.Args, want)
	}
	if _, wrote := m.state[claude.StateMCP("pj-l-01")]; wrote {
		t.Errorf("an override without mcpServers wrote an MCP file: %v", m.state)
	}
	checker := k.(agents.OverrideChecker)
	if got, want := checker.OverrideWarnings(in), []string{"claude_override.json: numStartups: only mcpServers applies"}; !slices.Equal(got, want) {
		t.Errorf("OverrideWarnings = %q, want %q", got, want)
	}
	if got := checker.OverrideWarnings(m.in(map[string]string{claude.MCPOverride: `{"mcpServers": {}}`}, nil)); len(got) != 0 {
		t.Errorf("OverrideWarnings of mcpServers alone = %q, want none", got)
	}
}

// A lead's start (not trimmed) on the same machine and project, override files and all: the args alone, no
// --setting-sources, no --settings, no MCP file and no state file.
func TestStartLeadKeepsTheUserSettings(t *testing.T) {
	m := machine{files: map[string]string{userSettings: `{"enabledPlugins":{"a@m":true},` + hooks("/bin/x.sh") + `}`,
		userFile: `{"mcpServers":{"blender":{"command":"b"}}}`}}
	overrides := map[string]string{claude.SettingsOverride: `{"env": {"A": "1"}}`, claude.MCPOverride: `{"mcpServers": {"blender": null}}`}
	worker := m.in(overrides, nil)
	l, err := agents.StartOf(kind(t), worker)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.state) != 2 || !slices.Contains(l.Args, "--setting-sources") || !slices.Contains(l.Args, "--strict-mcp-config") {
		t.Fatalf("precondition: a worker's start with these override files writes two files: %q, %v", l.Args, m.state)
	}
	lead := m.in(overrides, nil)
	lead.Agent, lead.TrimUser, lead.Settings, lead.Kit, lead.Prompt = "pj-lead", false, nil, "/kit", "go"
	if l, err = agents.StartOf(kind(t), lead); err != nil {
		t.Fatal(err)
	}
	if want := []string{"--model", "opus", "go"}; !slices.Equal(l.Args, want) || len(m.state) != 0 {
		t.Errorf("a lead's start = %q with state files %v, want %q and none", l.Args, m.state, want)
	}
}

// The whole start of a lane's agent: the file's two flags after the effort and the strict flag with obor's
// --mcp-config, before the session and the prompt; --setting-sources as the type's args name it, when they do. A start that
// cannot write its file is refused with the file's name: no agent or no state writer, or the write failing.
func TestStartTrimmedWhole(t *testing.T) {
	k := kind(t)
	in := agents.Start{Args: []string{"--model", "opus"}, Agent: "pj-l-01", TrimUser: true, Kit: "/kit",
		StrictMCP: true, Effort: "high", Prompt: "go", Session: "s-1",
		WriteState: func(name string, _ []byte) (string, error) { return "/state/" + name, nil }}
	l, err := agents.StartOf(k, in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "opus", "--effort", "high", "--mcp-config", claude.MCPConfig("/kit"), "--strict-mcp-config",
		"--setting-sources", "project,local", "--settings", stateFile, "--session-id", "s-1", "--", "go"}
	if !slices.Equal(l.Args, want) {
		t.Errorf("StartOf =\n %q\nwant\n %q", l.Args, want)
	}

	own := in
	own.Args = []string{"--setting-sources=user,project,local"}
	own.Kit, own.StrictMCP, own.Effort, own.Session = "", false, "", ""
	if l, err = agents.StartOf(k, own); err != nil {
		t.Fatal(err)
	}
	if want := []string{"--setting-sources=user,project,local", "--settings", stateFile, "go"}; !slices.Equal(l.Args, want) {
		t.Errorf("the args name --setting-sources: %q, want %q", l.Args, want)
	}

	failing := in
	failing.WriteState = func(string, []byte) (string, error) { return "", errors.New("disk full") }
	if _, err := agents.StartOf(k, failing); err == nil || !strings.Contains(err.Error(), "settings/pj-l-01.json") ||
		!strings.Contains(err.Error(), "disk full") {
		t.Errorf("a failing write: err %v, want the start refused, naming settings/pj-l-01.json and the cause", err)
	}
	noAgent, noWriter := in, in
	noAgent.Agent, noWriter.WriteState = "", nil
	for name, in := range map[string]agents.Start{"no agent": noAgent, "no state writer": noWriter} {
		if _, err := agents.StartOf(k, in); err == nil {
			t.Errorf("%s: the start was built, want it refused", name)
		}
	}
}

// the rest of the kind: no NotReady screen, /exit with Ctrl+C after Escape and its background-task dialog, the print flag, the manifest,
// transcripts per cwd, the inbox lines, the caps.
func TestKind(t *testing.T) {
	k := kind(t)
	if why := k.NotReady("Updating to 2.0, restart codex"); why != "" {
		t.Errorf("NotReady = %q, want none (Codex's screen)", why)
	}
	if s := k.Stop(); s.Quit != "/exit" || !slices.Equal(s.AfterEscape, []string{"ctrl+c"}) || !s.SecondEnter ||
		!slices.Equal(s.ClearInput, []string{"ctrl+u"}) || s.Program != "" || s.Dialog == nil || s.Dialog.Option != "Exit and stop tasks" {
		t.Errorf("Stop = %+v", s)
	}
	// the dialog's option and its key, as Claude Code numbers them ("2. Exit…" or "2) Exit…").
	for screen, key := range map[string]string{
		"  1. Move to background\n❯ 2. Exit and stop tasks\n  3. Stay": "2",
		"3) Exit and stop tasks": "3",
		"Exit and stop tasks":    "",
	} {
		got := ""
		if m := k.Stop().Dialog.Key.FindStringSubmatch(screen); m != nil {
			got = m[1]
		}
		if got != key {
			t.Errorf("Dialog.Key on %q = %q, want %q", screen, got, key)
		}
	}
	if h := k.Headless(); !h.PrintFlag || h.Subcommands != nil || h.Anywhere != nil {
		t.Errorf("Headless = %+v", h)
	}
	m := k.Install("/kit", nil).MCP
	if m.Where != agents.Manifest || m.Path != "/kit/.claude-plugin/plugin.json" {
		t.Errorf("Install = %+v", k.Install("/kit", nil))
	}
	for _, c := range []struct {
		args               []string
		kit                string
		how                string
		registered, static bool
	}{
		{[]string{"--model", "opus"}, "/kit", "", true, true},
		{[]string{"--strict-mcp-config"}, "/kit", "--strict-mcp-config hides the plugin manifest's server; obor adds --mcp-config " + claude.MCPConfig("/kit"), true, false},
		{[]string{"--strict-mcp-config", "--mcp-config", `{"mcpServers":{"obor":{}}}`}, "/kit", "the type's args name an obor server in --mcp-config", true, false},
		{[]string{"--strict-mcp-config"}, "", "--strict-mcp-config and no obor kit to point at", false, false},
	} {
		how, reg, static := k.Install(c.kit, nil).MCP.Lane(c.args, nil)
		if how != c.how || reg != c.registered || static != c.static {
			t.Errorf("Lane(%q, kit %q) = %q, %v, %v; want %q, %v, %v", c.args, c.kit, how, reg, static, c.how, c.registered, c.static)
		}
	}

	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		env  func(string) string
		want string
	}{
		{env(map[string]string{"HOME": "/h"}), "/h/.claude/projects/*/s1.jsonl"},
		{env(map[string]string{"HOME": "/h", "CLAUDE_CONFIG_DIR": "/c"}), "/c/projects/*/s1.jsonl"},
	} {
		if got := k.Transcript(c.env, "s1"); got != c.want {
			t.Errorf("Transcript = %q, want %q", got, c.want)
		}
	}

	d, ok := k.Deliver(`hi "there"`, []byte(`{"socket":"/s.sock","token":"t1","session":"x"}`))
	want := []string{`{"type":"auth","token":"t1"}`, `{"type":"user","message":{"role":"user","content":"hi \"there\""}}`}
	if !ok || d.Typed || d.Socket != "/s.sock" || !slices.Equal(d.Lines, want) {
		t.Errorf("Deliver = %+v, %v; want the socket and %q", d, ok, want)
	}
	if d, ok := k.Deliver("hi", []byte(`{"socket":"/s.sock"}`)); !ok || len(d.Lines) != 1 {
		t.Errorf("Deliver without a token = %+v, %v; want the user line alone", d, ok)
	}
	for _, rec := range []string{"", "{}", `{"token":"t"}`, "not json"} {
		if _, ok := k.Deliver("hi", []byte(rec)); ok {
			t.Errorf("Deliver(record %q) ok; want no socket, the line kept", rec)
		}
	}
	// the record SessionStart writes is the one Deliver reads.
	rec := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s.sock", "CLAUDE_CODE_MESSAGING_TOKEN": "t1"}))
	if d, ok := k.Deliver("hi", rec); !ok || d.Socket != "/s.sock" || len(d.Lines) != 2 || d.Lines[0] != want[0] {
		t.Errorf("Deliver(InboxRecord %s) = %+v, %v; want the socket and the token's auth line", rec, d, ok)
	}
	if rec := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s.sock"})); string(rec) != `{"socket":"/s.sock"}` {
		t.Errorf("InboxRecord without a token = %s", rec)
	}
	if rec := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_TOKEN": "t1"})); rec != nil {
		t.Errorf("InboxRecord without a socket = %s, want none (messaging off)", rec)
	}
	if got, ok := k.LastTurn([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"m","codex_error_info":"usage_limit_exceeded"}}}` + "\n")); ok { // its turns are not read (Caps.Status terminal below)
		t.Errorf("LastTurn = %+v, true; want none read", got)
	}
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksNative, NamedSession: true, ProjectPlugin: true, Inbox: true,
		Resume: true, Skills: true, Transcript: true, Status: agents.StatusTerminal}) {
		t.Errorf("Caps = %+v", c)
	}
}

// StopFailure's rate_limit is a limit, its reset what its error_details name (an epoch after '|', a clock
// time's next one), none when they name no time; billing_error is no limit.
func TestReset(t *testing.T) {
	k := kind(t)
	at := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, err, details string
		want               time.Time
		limited            bool
	}{
		{"an epoch", "rate_limit", "Claude AI usage limit reached|1793383200", time.Unix(1793383200, 0), true},
		{"a clock time", "rate_limit", "You've hit your limit · resets 6pm", time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC), true},
		{"a clock time past today: tomorrow's", "rate_limit", "5-hour limit reached ∙ resets 1:30am",
			time.Date(2026, 10, 31, 1, 30, 0, 0, time.UTC), true},
		{"no time", "rate_limit", "429 Too Many Requests", time.Time{}, true},
		{"billing_error: no limit", "billing_error", "resets 6pm", time.Time{}, false},
	} {
		got, limited := k.Reset(c.err, c.details, at)
		if !got.Equal(c.want) || limited != c.limited {
			t.Errorf("%s: Reset = %v, %v; want %v, %v", c.name, got, limited, c.want, c.limited)
		}
	}
}

// Start with an effort sets --effort <effort> once, in place of the args' own (either spelling); without one the
// args are as given; a second Start over the first's argv (launch: config.Model.LaunchArgs, then the start) keeps
// one --effort.
func TestStartEffort(t *testing.T) {
	k := kind(t)
	got := k.Start(agents.Start{Args: []string{"--model", "opus", "--effort", "medium", "--permission-mode", "auto"}, Effort: "high"})
	if want := []string{"--model", "opus", "--permission-mode", "auto", "--effort", "high"}; !slices.Equal(got, want) {
		t.Errorf("Start with effort = %q, want %q", got, want)
	}
	if got := k.Start(agents.Start{Args: []string{"--effort=low", "--model", "opus"}, Effort: "max"}); !slices.Equal(got,
		[]string{"--model", "opus", "--effort", "max"}) {
		t.Errorf("--effort=low replaced: %q", got)
	}
	if got := k.Start(agents.Start{Args: []string{"--model", "opus", "--effort", "medium"}}); !slices.Equal(got,
		[]string{"--model", "opus", "--effort", "medium"}) {
		t.Errorf("no effort: args as given: %q", got)
	}
	first := k.Start(agents.Start{Args: []string{"--effort", "medium"}, Settings: map[string]any{"x": 1}, Effort: "high"})
	again := k.Start(agents.Start{Args: first, Prompt: "go", Effort: "high"})
	if n := strings.Count(strings.Join(again, " "), "--effort"); n != 1 || !slices.Contains(again, "high") {
		t.Errorf("Start over Start's argv = %q: want one --effort high", again)
	}
	if !slices.Equal(k.Efforts(), []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("Efforts = %q", k.Efforts())
	}
}

// projectFS is a reader of one start dir /lane holding rel with content (nil content: the dir holds nothing).
func projectFS(rel string, content []byte) agents.ReadFile {
	return func(path string) ([]byte, error) {
		if content != nil && path == "/lane/"+rel {
			return content, nil
		}
		return nil, fs.ErrNotExist
	}
}

// the start dir's committed .claude/settings.json outranks obor's policy flags: an MCP opt-in (either key)
// drops the strict flag (and obor's --mcp-config with it: the plugin's server serves, as for a role), a settings key
// the file sets leaves --settings (no --settings when it sets both); a file that does not parse, or one setting none
// of the keys, starts as without it.
func TestStartProjectSettings(t *testing.T) {
	k := kind(t)
	ours := claude.MCPConfig("/kit")
	both := map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}
	const bothJSON = `{"autoMemoryEnabled":false,"remoteControlAtStartup":false}`
	strict := []string{"--mcp-config", ours, "--strict-mcp-config", "--settings", bothJSON, "--", "go"}
	for _, c := range []struct {
		name, file string // file "": absent
		want       []string
	}{
		{"absent: strict, both settings", "", strict},
		{"sets no key of obor's", `{"model":"opus","permissions":{"allow":["Bash(ls)"]}}`, strict},
		{"does not parse: as absent", `{"enabledMcpjsonServers":["x"],`, strict},
		{"enabledMcpjsonServers: not strict, no --mcp-config", `{"enabledMcpjsonServers":["mine"]}`,
			[]string{"--settings", bothJSON, "go"}},
		{"enableAllProjectMcpServers: not strict", `{"enableAllProjectMcpServers":true}`,
			[]string{"--settings", bothJSON, "go"}},
		{"enableAllProjectMcpServers false: strict", `{"enableAllProjectMcpServers":false}`, strict},
		{"empty enabledMcpjsonServers: strict", `{"enabledMcpjsonServers":[]}`, strict},
		{"sets autoMemoryEnabled: only the other key", `{"autoMemoryEnabled":true}`,
			[]string{"--mcp-config", ours, "--strict-mcp-config", "--settings", `{"remoteControlAtStartup":false}`, "--", "go"}},
		{"sets remoteControlAtStartup: only the other key", `{"remoteControlAtStartup":true}`,
			[]string{"--mcp-config", ours, "--strict-mcp-config", "--settings", `{"autoMemoryEnabled":false}`, "--", "go"}},
		{"sets both: no --settings", `{"remoteControlAtStartup":true,"autoMemoryEnabled":true}`,
			[]string{"--mcp-config", ours, "--strict-mcp-config", "--", "go"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var content []byte
			if c.file != "" {
				content = []byte(c.file)
			}
			got := k.Start(agents.Start{Kit: "/kit", Settings: both, StrictMCP: true, Prompt: "go", Dir: "/lane",
				Read: projectFS(claude.SettingsFile, content)})
			if !slices.Equal(got, c.want) {
				t.Errorf("Start =\n %q\nwant\n %q", got, c.want)
			}
		})
	}
	// No start dir: the file is never read, the flags as before.
	got := k.Start(agents.Start{Kit: "/kit", Settings: both, StrictMCP: true, Prompt: "go",
		Read: projectFS(claude.SettingsFile, []byte(`{"enableAllProjectMcpServers":true}`))})
	if !slices.Equal(got, strict) {
		t.Errorf("no dir: %q", got)
	}
}

// config validate's notes on the settings file: disableAllHooks true, a file that does not parse; none for a
// clean or absent one.
func TestProjectWarnings(t *testing.T) {
	pc, ok := kind(t).(agents.ProjectChecker)
	if !ok {
		t.Fatal("claude reads no project file")
	}
	for _, c := range []struct{ file, want string }{
		{"", ""},
		{`{"model":"opus","disableAllHooks":false}`, ""},
		{`{"disableAllHooks":true}`, ".claude/settings.json: disableAllHooks is true"},
		{`{"model":`, ".claude/settings.json: does not parse"},
	} {
		var content []byte
		if c.file != "" {
			content = []byte(c.file)
		}
		got := pc.ProjectWarnings("/lane", projectFS(claude.SettingsFile, content))
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%s: warnings %q, want none", c.file, got)
		case c.want != "" && (len(got) != 1 || !strings.HasPrefix(got[0], c.want)):
			t.Errorf("%s: warnings %q, want one starting %q", c.file, got, c.want)
		}
	}
}
