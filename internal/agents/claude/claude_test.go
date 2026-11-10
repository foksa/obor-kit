package claude_test

import (
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

// userFS is a user's machine and a project for a trimmed start: files by path, the skills dirs by their parent dir.
type userFS struct {
	files map[string]string
	dirs  map[string][]string
}

func (u userFS) read(path string) ([]byte, error) {
	if b, ok := u.files[path]; ok {
		return []byte(b), nil
	}
	return nil, fs.ErrNotExist
}

func (u userFS) list(dir string) ([]string, error) {
	if d, ok := u.dirs[dir]; ok {
		return d, nil
	}
	return nil, fs.ErrNotExist
}

// start is a trimmed start in /lane with obor's two settings, the user's home /home/u (or env's own variables).
func (u userFS) start(env map[string]string) agents.Start {
	if env == nil {
		env = map[string]string{"HOME": "/home/u"}
	}
	return agents.Start{Args: []string{"--model", "opus"}, TrimUser: true, Dir: "/lane", Read: u.read, Dirs: u.list,
		Env:      func(k string) string { return env[k] },
		Settings: map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false}}
}

// A trimmed start switches off the user's own plugins and skills in its one --settings flag, beside obor's two
// settings, and names no --setting-sources (the user's env, hooks and every other setting apply): enabledPlugins
// false per plugin the user's settings enable, skillOverrides "off" per dir of the user's skills dir. The project
// wins per entry (its settings.json or settings.local.json naming a plugin or a skill keeps that one, the others
// still go), obor's own plugin is never switched off, and a plugin the user already has off is not named. With no
// user settings file and no skills dir neither key is there. A lead's start (untrimmed) carries none of it.
func TestStartTrimUser(t *testing.T) {
	k := kind(t)
	const obor = `"autoMemoryEnabled":false,` // obor's settings sort around the two maps
	const remote = `,"remoteControlAtStartup":false}`
	const userSettings = "/home/u/.claude/settings.json"
	for _, c := range []struct {
		name string
		fs   userFS
		env  map[string]string
		want string // the --settings value
	}{
		{"user enables a and b, the project enables b: a alone goes",
			userFS{files: map[string]string{
				userSettings:                   `{"enabledPlugins":{"a@m":true,"b@m":true},"env":{"X":"1"},"hooks":{"SessionStart":[]}}`,
				"/lane/" + claude.SettingsFile: `{"enabledPlugins":{"b@m":true}}`}}, nil,
			`{` + obor + `"enabledPlugins":{"a@m":false}` + remote},
		{"the project's local settings name a plugin (off there): left to them",
			userFS{files: map[string]string{
				userSettings:                        `{"enabledPlugins":{"a@m":true,"b@m":true}}`,
				"/lane/" + claude.LocalSettingsFile: `{"enabledPlugins":{"a@m":false}}`}}, nil,
			`{` + obor + `"enabledPlugins":{"b@m":false}` + remote},
		{"obor's own plugin and a plugin the user has off: neither named",
			userFS{files: map[string]string{
				userSettings: `{"enabledPlugins":{"obor@obor-kit":true,"off@m":false,"a@m":true}}`}}, nil,
			`{` + obor + `"enabledPlugins":{"a@m":false}` + remote},
		{"skills x and y, the project's skillOverrides name y: x alone goes",
			userFS{dirs: map[string][]string{"/home/u/.claude/skills": {"y", "x", ".cache"}},
				files: map[string]string{"/lane/" + claude.SettingsFile: `{"skillOverrides":{"y":"on"}}`}}, nil,
			`{` + obor + `"remoteControlAtStartup":false,"skillOverrides":{"x":"off"}}`},
		{"plugins and skills under $CLAUDE_CONFIG_DIR",
			userFS{dirs: map[string][]string{"/cfg/skills": {"x"}},
				files: map[string]string{"/cfg/settings.json": `{"enabledPlugins":{"a@m":true}}`}},
			map[string]string{"HOME": "/home/u", "CLAUDE_CONFIG_DIR": "/cfg"},
			`{` + obor + `"enabledPlugins":{"a@m":false},"remoteControlAtStartup":false,"skillOverrides":{"x":"off"}}`},
		{"no user settings file, no skills dir: neither key", userFS{}, nil, `{"autoMemoryEnabled":false` + remote},
		{"a user settings file that does not parse, an empty skills dir: neither key",
			userFS{files: map[string]string{userSettings: `{"enabledPlugins":`}, dirs: map[string][]string{"/home/u/.claude/skills": nil}},
			nil, `{"autoMemoryEnabled":false` + remote},
		{"no home in the environment: nothing of the user's is read",
			userFS{files: map[string]string{".claude/settings.json": `{"enabledPlugins":{"a@m":true}}`}},
			map[string]string{}, `{"autoMemoryEnabled":false` + remote},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := k.Start(c.fs.start(c.env))
			if want := []string{"--model", "opus", "--settings", c.want}; !slices.Equal(got, want) {
				t.Errorf("Start =\n %q\nwant\n %q", got, want)
			}
		})
	}

	u := userFS{files: map[string]string{userSettings: `{"enabledPlugins":{"a@m":true}}`},
		dirs: map[string][]string{"/home/u/.claude/skills": {"x"}}}
	// The project sets one of obor's settings whole and names nothing of the maps: that key goes, the maps stay.
	u.files["/lane/"+claude.SettingsFile] = `{"autoMemoryEnabled":true}`
	if got, want := k.Start(u.start(nil)), []string{"--model", "opus", "--settings",
		`{"enabledPlugins":{"a@m":false},"remoteControlAtStartup":false,"skillOverrides":{"x":"off"}}`}; !slices.Equal(got, want) {
		t.Errorf("the project sets autoMemoryEnabled:\n %q\nwant\n %q", got, want)
	}
	delete(u.files, "/lane/"+claude.SettingsFile)
	// A lead's start: the same machine, untrimmed and without obor's settings.
	lead := u.start(nil)
	lead.TrimUser, lead.Settings = false, nil
	if got := k.Start(lead); !slices.Equal(got, []string{"--model", "opus"}) {
		t.Errorf("a lead's start = %q, want the args alone", got)
	}
	// The whole start (agents.StartOf, its two passes): one --settings flag with obor's settings and the two maps.
	whole := u.start(nil)
	whole.Kit, whole.StrictMCP, whole.Prompt = "/kit", true, "go"
	l, err := agents.StartOf(k, whole)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "opus", "--mcp-config", claude.MCPConfig("/kit"), "--strict-mcp-config", "--settings",
		`{"autoMemoryEnabled":false,"enabledPlugins":{"a@m":false},"remoteControlAtStartup":false,"skillOverrides":{"x":"off"}}`,
		"--", "go"}
	if !slices.Equal(l.Args, want) {
		t.Errorf("StartOf =\n %q\nwant\n %q", l.Args, want)
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
