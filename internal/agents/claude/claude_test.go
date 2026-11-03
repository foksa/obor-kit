package claude_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/claude"
)

func kind(t *testing.T) agents.Kind {
	t.Helper()
	k, ok := agents.Lookup("claude")
	if !ok || k.Name() != "claude" {
		t.Fatalf("claude not registered: %v", agents.Names())
	}
	return k
}

// T-495: Claude's start argv, fresh, resumed and strict: the settings after the args, obor's --mcp-config right before
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

// T-495: the rest of the kind: no NotReady screen, /exit with Ctrl+C after Escape and its background-task dialog, the print flag, the manifest,
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
	// DD-v0.13 B1: the dialog's option and its key, as Claude Code numbers them ("2. Exit…" or "2) Exit…").
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
	if m.Where != agents.Manifest || m.Path != "/kit/.claude-plugin/plugin.json" || k.Install("/kit", nil).Plugin != nil {
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
	home := env(map[string]string{"HOME": "/h"})
	for _, c := range []struct {
		env       func(string) string
		cwd, want string
	}{
		{home, "", "/h/.claude/projects/*/s1.jsonl"},
		{home, "/work/my.repo", "/h/.claude/projects/-work-my-repo/s1.jsonl"},
		{env(map[string]string{"HOME": "/h", "CLAUDE_CONFIG_DIR": "/c"}), "", "/c/projects/*/s1.jsonl"},
	} {
		if got := k.Transcript(c.env, c.cwd, "s1"); got != c.want {
			t.Errorf("Transcript(cwd %q) = %q, want %q", c.cwd, got, c.want)
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
	// DD-v0.13 B1: the record SessionStart writes is the one Deliver reads.
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
	if got, ok := k.LastTurn([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"m","codex_error_info":"usage_limit_exceeded"}}}` + "\n")); ok { // T-563: its turns are not read (Caps.Turns false below)
		t.Errorf("LastTurn = %+v, true; want none read", got)
	}
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksNative, ProjectPlugin: true, Inbox: true, Resume: true, Skills: true, Transcript: true}) {
		t.Errorf("Caps = %+v", c)
	}
}

// T-568: StopFailure's rate_limit is a limit, its reset what its error_details name (an epoch after '|', a clock
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

// T-695: Start with an effort sets --effort <effort> once, in place of the args' own (either spelling); without one the
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
