package opencode_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/opencode"
)

// T-495: OpenCode's start argv (--prompt, no resume, no registration on the command line), /exit with the popup's
// second Enter and the clear keys, `opencode run`, registered per start (the old user config entry and plugin link
// retired, T-809), no transcript, typed
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
		{"fresh, prompt", agents.Start{Kit: "/kit", Args: []string{"--auto"}, Prompt: "go"}, []string{"--auto", "--prompt", "go"}},
		{"fresh, no prompt", agents.Start{Kit: "/kit", Args: []string{"--auto"}}, []string{"--auto"}},
		{"a resume is no resume: nothing added", agents.Start{Kit: "/kit", Args: []string{"--auto"}, Resume: "s", Prompt: "go"},
			[]string{"--auto"}},
		{"the model is no flag of the TUI: dropped (StartEnv carries it)", agents.Start{Args: []string{"--auto", "-m", "p/x",
			"--model=p/y"}, Effort: "high"}, []string{"--auto"}},
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
	// T-809: nothing user-global; the old global entry and link retired.
	in := k.Install("/kit", env(map[string]string{"HOME": "/h"}))
	if m := in.MCP; m.Where != agents.PerStart || m.Path != "" || m.Entry != nil || in.Plugin != nil {
		t.Errorf("Install = %+v: want per start, nothing user-global", in)
	}
	if how, reg, static := in.MCP.Lane([]string{"-m", "x"}, nil); !reg || static || !strings.Contains(how, "OPENCODE_CONFIG") ||
		!strings.Contains(how, "/kit/bin/obor mcp") || !strings.Contains(how, "plugin /kit/opencode") {
		t.Errorf("Lane = %q, %v, %v: want registered per start by the generated config", how, reg, static)
	}
	r := in.Retired
	if r == nil || r.MCP.Where != agents.Config || r.MCP.Path != "/h/.config/opencode/opencode.json" ||
		r.MCP.AltPath != "/h/.config/opencode/opencode.jsonc" || !slices.Equal(r.MCP.Key, []string{"mcp", "servers", "obor"}) ||
		r.Link == nil || *r.Link != (agents.Link{From: "/kit/opencode/obor.js", To: "/h/.config/opencode/plugins/obor.js"}) {
		t.Fatalf("Retired = %+v", r)
	}
	if l := k.Install("/kit", env(map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"})).Retired.Link; l.To != "/x/opencode/plugins/obor.js" {
		t.Errorf("Retired link under XDG_CONFIG_HOME = %+v", l)
	}
	for _, c := range []struct {
		entry string
		ours  bool
	}{
		{`{"type":"local","command":["/h/.obor/kit/bin/obor","mcp"],"environment":{"OBOR_NO_SELF_HOST":"1"}}`, true},
		{`{"type":"local","command":["/other/kit/bin/obor","mcp"]}`, true},
		{`{"type":"local","command":["/usr/bin/my-obor","mcp"]}`, false},
		{`{"type":"local","command":["/k/bin/obor","serve"]}`, false},
		{`{"type":"local","command":["/k/bin/obor","mcp","x"]}`, false},
		{`{"type":"remote","url":"http://x"}`, false},
		{`"obor"`, false},
	} {
		var v any
		if err := json.Unmarshal([]byte(c.entry), &v); err != nil {
			t.Fatal(err)
		}
		if got := r.Ours(v); got != c.ours {
			t.Errorf("Ours(%s) = %v, want %v", c.entry, got, c.ours)
		}
	}
	if got := k.Transcript(env(map[string]string{"HOME": "/h"}), "s1"); got != "" {
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
	if c := k.Caps(); c != (agents.Caps{Hooks: agents.HooksPlugin, ModelArg: true, VersionVar: "OPENCODE_VERSION"}) {
		t.Errorf("Caps = %+v", c)
	}
	if r := k.InboxRecord(env(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s"})); r != nil {
		t.Errorf("InboxRecord = %s, want none", r)
	}
}

// T-695, T-697, T-703: the type's model and the effort reach obor's plugin as OBOR_OPENCODE_MODEL
// <provider>/<model>#<effort>, the model being the type's --model/-m only: obor never guesses OpenCode's model, so a
// type without one unsets the variable (OpenCode's own choice stands, even when the user's files name a model) and an
// effort on it is refused. Without a kit and a state writer the variable is the only one set: no config content, no
// state dir.
func TestStartEnvModelReachesThePlugin(t *testing.T) {
	k, _ := agents.Lookup("opencode")
	// The user's files name models; none may be read (Read is a trap).
	read := func(p string) ([]byte, error) {
		t.Errorf("StartEnv read %s: obor never reads OpenCode's config or state", p)
		return []byte(`{"model":"cfg/m","recent":[{"providerID":"go","modelID":"ds"}]}`), nil
	}
	env := func(k string) string { return map[string]string{"HOME": "/h"}[k] }
	start := func(args []string, effort string) (string, bool, error) {
		m, err := k.StartEnv(agents.Start{Args: args, Effort: effort, Read: read, Env: env})
		v, set := m[opencode.ModelVar]
		if err == nil && (!set || len(m) != 1) {
			t.Errorf("StartEnv(%q, %q) = %v: want %s and nothing else", args, effort, m, opencode.ModelVar)
		}
		return v, set, err
	}
	if opencode.ModelVar != "OBOR_OPENCODE_MODEL" {
		t.Errorf("ModelVar = %q: opencode/obor.js reads OBOR_OPENCODE_MODEL", opencode.ModelVar)
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
	if cerr := agents.CheckEffortArgs("opencode", []string{"--auto"}, "high"); cerr == nil || !strings.Contains(cerr.Error(), "needs a model id") {
		t.Errorf("CheckEffortArgs without --model: %v; want the same refusal", cerr)
	}
	if cerr := agents.CheckEffortArgs("opencode", []string{"--model", "p/m"}, "high"); cerr != nil {
		t.Errorf("CheckEffortArgs with --model: %v", cerr)
	}
	if cerr := agents.CheckEffortArgs("opencode", []string{"--auto"}, ""); cerr != nil {
		t.Errorf("CheckEffortArgs, no effort: %v", cerr)
	}
	if err := agents.CheckEffort("opencode", "high"); err != nil {
		t.Errorf("CheckEffort(opencode, high): %v", err)
	}
	if err := agents.CheckEffort("opencode", "turbo"); err == nil {
		t.Error("CheckEffort(opencode, turbo): want a refusal")
	}
}

// T-809: a start from a kit with a state writer writes one generated config, named by OPENCODE_CONFIG (a path, so the
// env line stays short): mcp.servers.obor runs the launching kit's `bin/obor mcp`, pinned, and plugins names the kit's
// plugin package dir; the model variable still rides along. A failed write is the start's error.
func TestStartEnvNamesTheGeneratedConfig(t *testing.T) {
	k, _ := agents.Lookup("opencode")
	written := map[string][]byte{}
	write := func(name string, data []byte) (string, error) {
		written[name] = data
		return "/p/.obor/state/" + name, nil
	}
	vars, err := k.StartEnv(agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}, Effort: "high", WriteState: write})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[opencode.ConfigFile] == nil {
		t.Fatalf("written %v: want the one generated config", written)
	}
	if opencode.ConfigVar != "OPENCODE_CONFIG" || vars[opencode.ConfigVar] != "/p/.obor/state/opencode.json" || vars[opencode.ModelVar] != "p/m#high" || len(vars) != 2 {
		t.Errorf("StartEnv = %v: want OPENCODE_CONFIG=<the file> and the model", vars)
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
	if err := json.Unmarshal(written[opencode.ConfigFile], &got); err != nil {
		t.Fatalf("%v: %s", err, written[opencode.ConfigFile])
	}
	o := got.MCP.Servers["obor"]
	if len(got.MCP.Servers) != 1 || o.Type != "local" || !slices.Equal(o.Command, []string{"/kit/bin/obor", "mcp"}) ||
		len(o.Environment) != 1 || o.Environment[agents.Pin] != "1" {
		t.Errorf("mcp.servers = %+v: want obor, the launching kit's bin/obor mcp, pinned", got.MCP.Servers)
	}
	if !slices.Equal(got.Plugins, []string{"file:///kit/opencode"}) {
		t.Errorf("plugins = %q: want the kit's plugin package dir", got.Plugins)
	}
	if _, err := k.StartEnv(agents.Start{Kit: "/kit", WriteState: func(string, []byte) (string, error) { return "", errors.New("disk full") }}); err == nil ||
		!strings.Contains(err.Error(), "disk full") {
		t.Errorf("a failed write: %v; want the start's error", err)
	}
}
