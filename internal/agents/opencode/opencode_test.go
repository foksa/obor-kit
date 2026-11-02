package opencode_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/agents/opencode"
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

// T-695: an effort reaches OpenCode's config as the model's variant (OPENCODE_CONFIG_CONTENT's
// "model":"<provider>/<model>#<effort>"): the model from the type's args, else the user config's, else the TUI's last
// one; none and no effort unsets the variable; an effort with no model anywhere is an error.
func TestStartEnvEffortReachesTheConfig(t *testing.T) {
	k, _ := agents.Lookup("opencode")
	files := map[string]string{}
	read := func(p string) ([]byte, error) {
		if b, ok := files[p]; ok {
			return []byte(b), nil
		}
		return nil, os.ErrNotExist
	}
	env := func(k string) string { return map[string]string{"HOME": "/h"}[k] }
	start := func(args []string, effort string) (string, bool, error) {
		m, err := k.StartEnv(agents.Start{Args: args, Effort: effort, Read: read, Env: env})
		v, set := m[opencode.ConfigVar]
		if err == nil && (!set || len(m) != 1) {
			t.Errorf("StartEnv(%q, %q) = %v: want %s and nothing else", args, effort, m, opencode.ConfigVar)
		}
		return v, set, err
	}
	if v, _, err := start([]string{"--auto", "--model", "p/m"}, "high"); err != nil || v != `{"model":"p/m#high"}` {
		t.Errorf("args' model with effort: %q, %v", v, err)
	}
	if v, _, err := start([]string{"-m=p/m#low"}, "max"); err != nil || v != `{"model":"p/m#max"}` {
		t.Errorf("the effort replaces the model's own variant: %q, %v", v, err)
	}
	if v, _, err := start([]string{"--model", "p/m"}, ""); err != nil || v != `{"model":"p/m"}` {
		t.Errorf("args' model, no effort: %q, %v", v, err)
	}
	if v, set, err := start([]string{"--auto"}, ""); err != nil || !set || v != "" {
		t.Errorf("no model, no effort: %q (set %v), %v; want the variable unset", v, set, err)
	}
	if _, _, err := start([]string{"--auto"}, "high"); err == nil || !strings.Contains(err.Error(), "needs a model") {
		t.Errorf("effort with no model anywhere: %v; want an error", err)
	}
	files["/h/.local/state/opencode/model.json"] = `{"recent":[{"providerID":"go","modelID":"ds"},{"providerID":"o","modelID":"x"}]}`
	if v, _, err := start([]string{"--auto"}, "high"); err != nil || v != `{"model":"go/ds#high"}` {
		t.Errorf("the TUI's last model: %q, %v", v, err)
	}
	files["/h/.config/opencode/opencode.json"] = `{"model":"cfg/m"}`
	if v, _, err := start([]string{"--auto"}, "low"); err != nil || v != `{"model":"cfg/m#low"}` {
		t.Errorf("the user config's model before the TUI's: %q, %v", v, err)
	}
	if err := agents.CheckEffort("opencode", "high"); err != nil {
		t.Errorf("CheckEffort(opencode, high): %v", err)
	}
	if err := agents.CheckEffort("opencode", "turbo"); err == nil {
		t.Error("CheckEffort(opencode, turbo): want a refusal")
	}
}

// T-696: every OpenCode start gets its own state dir, <Home>/opencode-state/<project>/<agent>, as XDG_STATE_HOME, so a
// variant remembered in the user's state (~/.local/state/opencode/model.json) cannot beat the effort; the model the
// TUI used last is then named (its own state remembers none). A resume gets the same dir; no Home, project or agent,
// or one that is no plain name: no dir, the variable left alone.
func TestStateDirPerProjectAndAgent(t *testing.T) {
	k, _ := agents.Lookup("opencode")
	in := agents.Start{Home: "/h/.obor", Project: "pj", Agent: "pj-d-01"}
	if got := k.StateDir(in); got != "/h/.obor/opencode-state/pj/pj-d-01" {
		t.Errorf("StateDir = %q", got)
	}
	other := in
	other.Agent = "pj-d-02"
	if k.StateDir(other) == k.StateDir(in) {
		t.Error("two agents share a state dir")
	}
	other = in
	other.Project = "qq"
	if got := k.StateDir(other); got != "/h/.obor/opencode-state/qq/pj-d-01" {
		t.Errorf("StateDir of another project = %q", got)
	}
	resume := in
	resume.Resume = "ses_1"
	if k.StateDir(resume) != k.StateDir(in) {
		t.Error("a resume's state dir differs from the fresh start's")
	}
	for _, bad := range []agents.Start{{Project: "pj", Agent: "a"}, {Home: "/h", Agent: "a"}, {Home: "/h", Project: "pj"},
		{Home: "/h", Project: "..", Agent: "a"}, {Home: "/h", Project: "pj", Agent: "x/y"}} {
		if got := k.StateDir(bad); got != "" {
			t.Errorf("StateDir(%+v) = %q, want none", bad, got)
		}
		if m, err := k.StartEnv(bad); err != nil {
			t.Errorf("StartEnv(%+v): %v", bad, err)
		} else if _, set := m[opencode.StateVar]; set {
			t.Errorf("StartEnv(%+v) = %v: sets %s with no state dir", bad, m, opencode.StateVar)
		}
	}

	read := func(p string) ([]byte, error) {
		if p == "/u/.local/state/opencode/model.json" {
			return []byte(`{"recent":[{"providerID":"go","modelID":"ds"}],"variant":{"go/ds":"default"}}`), nil
		}
		return nil, os.ErrNotExist
	}
	env := func(k string) string { return map[string]string{"HOME": "/u"}[k] }
	in.Read, in.Env = read, env
	in.Args, in.Effort = []string{"--auto", "--model", "p/m"}, "high"
	m, err := k.StartEnv(in)
	if err != nil || m[opencode.StateVar] != "/h/.obor/opencode-state/pj/pj-d-01" || m[opencode.ConfigVar] != `{"model":"p/m#high"}` || len(m) != 2 {
		t.Errorf("StartEnv with a state dir = %v, %v; want %s the agent's dir and the model's variant", m, err, opencode.StateVar)
	}
	in.Args, in.Effort = []string{"--auto"}, ""
	if m, err = k.StartEnv(in); err != nil || m[opencode.ConfigVar] != `{"model":"go/ds"}` {
		t.Errorf("no model, no effort, own state dir: %v, %v; want the TUI's last model named, no variant", m, err)
	}
	in.Read = nil
	if m, err = k.StartEnv(in); err != nil || m[opencode.ConfigVar] != "" || m[opencode.StateVar] == "" {
		t.Errorf("no model anywhere, no effort: %v, %v; want %s unset and the state dir", m, err, opencode.ConfigVar)
	} else if _, set := m[opencode.ConfigVar]; !set {
		t.Errorf("no model anywhere, no effort: %v; want %s unset", m, opencode.ConfigVar)
	}
}
