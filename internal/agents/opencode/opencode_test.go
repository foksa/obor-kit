package opencode_test

import (
	"slices"
	"testing"
	"time"

	"obor/internal/agents"
	_ "obor/internal/agents/opencode"
)

// T-495: OpenCode's start argv (--prompt, no resume, no registration on the command line), /exit with the popup's
// second Enter and the clear keys, `opencode run`, its user config entry and plugin link, no transcript, typed
// delivery, the plugin hook transport.
func TestKind(t *testing.T) {
	k, ok := agents.Lookup("opencode")
	if !ok || k.Name() != "opencode" {
		t.Fatalf("opencode not registered: %v", agents.Names())
	}
	for _, c := range []struct {
		name string
		in   agents.Start
		want []string
	}{
		{"fresh, prompt", agents.Start{Kit: "/kit", Args: []string{"-m", "x"}, Prompt: "go"}, []string{"-m", "x", "--prompt", "go"}},
		{"fresh, no prompt", agents.Start{Kit: "/kit", Args: []string{"-m", "x"}}, []string{"-m", "x"}},
		{"a resume is no resume: nothing added", agents.Start{Kit: "/kit", Args: []string{"-m", "x"}, Resume: "s", Prompt: "go"},
			[]string{"-m", "x"}},
	} {
		if got := k.Start(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: Start = %q, want %q", c.name, got, c.want)
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
	in := k.Install("/kit", env(map[string]string{"HOME": "/h"}))
	m := in.MCP
	if m.Where != agents.Config || m.Path != "/h/.config/opencode/opencode.json" || m.AltPath != "/h/.config/opencode/opencode.jsonc" {
		t.Errorf("MCP = %+v", m)
	}
	if cmd, _ := m.Entry["command"].([]string); m.Entry["type"] != "local" || !slices.Equal(cmd, []string{"/kit/bin/obor", "mcp"}) ||
		m.Entry["environment"].(map[string]string)["OBOR_NO_SELF_HOST"] != "1" || m.Unpinned["environment"] != nil {
		t.Errorf("Entry = %v, Unpinned = %v", m.Entry, m.Unpinned)
	}
	if _, reg, static := m.Lane([]string{"-m", "x"}, nil); !reg || !static {
		t.Error("Lane: the user config serves every start")
	}
	if in.Plugin == nil || *in.Plugin != (agents.Link{From: "/kit/opencode/obor.js", To: "/h/.config/opencode/plugins/obor.js"}) {
		t.Errorf("Plugin = %+v", in.Plugin)
	}
	if p := k.Install("/kit", env(map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"})).Plugin; p.To != "/x/opencode/plugins/obor.js" {
		t.Errorf("Plugin under XDG_CONFIG_HOME = %+v", p)
	}
	if got := k.Transcript(env(map[string]string{"HOME": "/h"}), "", "s1"); got != "" {
		t.Errorf("Transcript = %q, want none", got)
	}
	if d, ok := k.Deliver("hi", nil); !ok || !d.Typed {
		t.Errorf("Deliver = %+v, %v; want typed", d, ok)
	}
	if got, ok := k.LastTurn([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"m","codex_error_info":"usage_limit_exceeded"}}}` + "\n")); ok { // T-563: its turns are not read (Caps.Turns false below)
		t.Errorf("LastTurn = %+v, true; want none read", got)
	}
	// T-584: parse the plugin's StopFailure details; the app tests exercise worker and role routing.
	at := time.Unix(1793361600, 0)
	for _, c := range []struct {
		name, err, details string
		want               time.Time
		limited            bool
	}{
		{"rate_limit with plugin error_details", "rate_limit", "You exceeded your current quota; resets at 6pm (provider.rate-limit)|1793365200", time.Unix(1793365200, 0), true},
		{"billing_error", "billing_error", "budget spent (provider.budget-limit)", time.Time{}, false},
	} {
		got, limited := k.Reset(c.err, c.details, at)
		if !got.Equal(c.want) || limited != c.limited {
			t.Errorf("%s: Reset = %v, %v; want %v, %v", c.name, got, limited, c.want, c.limited)
		}
	}
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksPlugin, VersionVar: "OPENCODE_VERSION"}) {
		t.Errorf("Caps = %+v", c)
	}
	if r := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s"})); r != nil {
		t.Errorf("InboxRecord = %s, want none", r)
	}
}
