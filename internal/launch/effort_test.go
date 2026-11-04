package launch_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// T-695: the effort a start runs at: the request's (a dispatch's model ref), else the role's model ref (roles.<role>
// `opus:max`), else none (the kind's default). The kind sets it once, in place of any kinds args' own; OpenCode's
// reaches obor's plugin through the env line (OBOR_OPENCODE_MODEL, T-703), which unsets it when the start names none.
func TestStartEffort(t *testing.T) {
	t.Parallel()
	const models = `{"oc":{"kind":"opencode","model":"go/ds"},"bare":{"kind":"opencode","model":""}}`
	for _, c := range []struct {
		name, role, kind, effort string
		has, lacks               []string // in agent.start's args
		env, noEnv               string   // in, not in the env line
	}{
		{"a request's effort replaces the kinds args' own, once", "opus", "claude", "high",
			[]string{"--effort high"}, []string{"medium"}, "unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL ", "export OBOR_OPENCODE_MODEL"},
		{"the role's effort", "opus:max", "claude", "", []string{"--effort max"}, []string{"medium"}, "", ""},
		{"the request's effort before the role's", "opus:max", "claude", "low", []string{"--effort low"}, []string{"max", "medium"}, "", ""},
		{"no effort: the kinds args' own", "opus", "claude", "", []string{"--effort medium"}, nil, "", ""},
		{"codex: the role's effort", "luna:xhigh", "codex", "", []string{"-c model_reasoning_effort=xhigh"},
			[]string{"model_reasoning_effort=high"}, "unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL ", ""},
		{"opencode: the model and effort reach the plugin as model#variant", "oc:high", "opencode", "",
			[]string{"--auto"}, []string{"--model", "go/ds", "high"}, "export OBOR_ROLE=reviewer OBOR_OPENCODE_MODEL='go/ds#high'", "OPENCODE_CONFIG_CONTENT"},
		{"opencode, no model or effort: the variable unset", "bare", "opencode", "", []string{"--auto"}, nil,
			"unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL ", "export OBOR_OPENCODE_MODEL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("models", models), testkit.Set("roles", `{"reviewer":"`+c.role+`"}`),
				testkit.Set("kinds", `{"claude":{"args":["--effort","medium"]},"codex":{"args":["-c","model_reasoning_effort=high"]}}`))
			x.agent("w1:p7", "pj-rev", c.kind, "idle")
			out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Effort: c.effort})
			if !ok {
				t.Fatal(out)
			}
			start := x.startCall()
			if !strings.HasPrefix(start, "agent.start args=[") {
				t.Fatalf("no agent.start: %q", start)
			}
			for _, h := range c.has {
				if n := strings.Count(start, h); n != 1 {
					t.Errorf("agent.start %q has %q %d times, want once", start, h, n)
				}
			}
			for _, l := range c.lacks {
				if strings.Contains(start, l) {
					t.Errorf("agent.start %q has %q", start, l)
				}
			}
			line := strings.Join(x.calls("pane.send_input"), "\n")
			if !strings.Contains(line, "export OBOR_ROLE=reviewer") {
				t.Fatalf("no env line typed: %q", line)
			}
			if c.env != "" && !strings.Contains(line, c.env) {
				t.Errorf("env line %q lacks %q", line, c.env)
			}
			if c.noEnv != "" && strings.Contains(line, c.noEnv) {
				t.Errorf("env line %q has %q", line, c.noEnv)
			}
		})
	}
}

// T-695: an effort the role's kind does not take, or one OpenCode cannot carry (no model anywhere), fails the start
// before anything is stopped or typed.
func TestStartEffortRefused(t *testing.T) {
	t.Parallel()
	const models = `{"bare":{"kind":"opencode","model":""}}`
	for _, c := range []struct{ role, effort, want string }{
		{"opus", "turbo", `effort "turbo" is not one of claude's`},
		{"bare", "high", "needs a model"},
	} {
		// the fixture's HOME is a temp dir: no opencode config or state names a model
		x := setup(t, testkit.Set("models", models), testkit.Set("roles", `{"reviewer":"`+c.role+`"}`))
		x.agent("w1:p7", "pj-rev", "claude", "idle")
		out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Effort: c.effort})
		if ok || !strings.Contains(out, c.want) {
			t.Errorf("%s at %s: %v %q; want a FAIL naming %q", c.role, c.effort, ok, out, c.want)
		}
		if n := len(x.calls("pane.send_input")) + len(x.calls("agent.start")); n != 0 {
			t.Errorf("%s at %s: %d calls typed or started, want none", c.role, c.effort, n)
		}
	}
}

// T-785: the effort is a dispatch's (port.Launch.Effort), not a scheduled CLI restart's: the argv carries neither it
// nor a first prompt, and ParseRequest reads back exactly what Args writes for the CLI's own fields.
func TestRequestArgsCarryTheEffort(t *testing.T) {
	r := launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: "go", Effort: "high"}
	args := launch.Args(r)
	if got := strings.Join(args, " "); got != "pj-l-01 --fresh" {
		t.Errorf("Args(%+v) = %q", r, got)
	}
	back, err := launch.ParseRequest(args)
	if err != nil || back != (launch.Request{Agent: "pj-l-01", Fresh: true}) {
		t.Errorf("ParseRequest(%q) = %+v, %v", args, back, err)
	}
}

// T-703: the OpenCode agent's model reaches its plugin through the pane's shell, nothing else: the env lines of starts
// typed at one pane's shell (run in a real sh) after what the pane held before, one row per case. No start sets or
// unsets XDG_STATE_HOME (the T-696 per-agent state dir is gone) or OPENCODE_CONFIG_CONTENT, so a user's own stay.
func TestOpencodeModelIsTheOpencodeStartsAlone(t *testing.T) {
	t.Parallel()
	const models = `{"oc":{"kind":"opencode","model":"go/ds"},"bare":{"kind":"opencode","model":""}}`
	x := setup(t, testkit.Set("models", models),
		testkit.Set("roles", `{"reviewer":"oc:high","advisor":"bare","integrator":"opus","deep_dive":"luna"}`))
	x.agent("w1:p9", "pj-deep-dive", "codex", "idle")
	x.agent("w1:p7", "pj-rev", "opencode", "idle")
	x.agent("w1:p6", "pj-advisor", "opencode", "idle")
	x.agent("w1:p8", "pj-int", "claude", "idle")
	lineOf := func(agent string) string {
		if out, ok := x.restart(launch.Request{Agent: agent, Fresh: true}); !ok {
			t.Fatal(out)
		}
		role := map[string]string{"pj-rev": "reviewer", "pj-advisor": "advisor", "pj-int": "integrator", "pj-deep-dive": "deep_dive"}[agent]
		runs := x.calls("pane.send_input OBOR_ROLE=" + role + " ")
		if len(runs) != 1 {
			t.Fatalf("%s: one env line typed, got %q", agent, runs)
		}
		_, text, _ := strings.Cut(runs[0], " text=")
		m := envMarksRE.FindStringIndex(text)
		if m == nil {
			t.Fatalf("%s: no marks at the end of %q", agent, text)
		}
		_, line, _ := strings.Cut(text[:m[0]], " && unset ") // not the cd: the fixture's worktree is no directory
		return "unset " + line
	}
	oc, bare, cl, cx := lineOf("pj-rev"), lineOf("pj-advisor"), lineOf("pj-int"), lineOf("pj-deep-dive")
	for _, l := range []string{oc, bare, cl, cx} {
		for _, v := range []string{"XDG_STATE_HOME", "OPENCODE_CONFIG_CONTENT"} {
			if strings.Contains(l, v) {
				t.Errorf("the env line %q names %s", l, v)
			}
		}
	}
	run := func(script string) string {
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s\n%s", err, script, out)
		}
		return strings.TrimSpace(string(out))
	}
	const show = "echo ${OBOR_OPENCODE_MODEL-UNSET} ${XDG_STATE_HOME-UNSET} ${OPENCODE_CONFIG_CONTENT-UNSET}"
	for _, c := range []struct{ name, script, want string }{
		{"opencode with a model: set", "unset OBOR_OPENCODE_MODEL XDG_STATE_HOME OPENCODE_CONFIG_CONTENT\n" + oc, "go/ds#high UNSET UNSET"},
		{"opencode without one after it: unset", oc + "\n" + bare, "UNSET UNSET UNSET"},
		{"the user's own XDG_STATE_HOME and config content: kept", "export XDG_STATE_HOME=/mine OPENCODE_CONFIG_CONTENT=c\n" + oc,
			"go/ds#high /mine c"},
		// T-707 (RV-83 #4): another kind's start after an OpenCode one in the pane does not inherit its model.
		{"opencode then claude: unset", oc + "\n" + cl, "UNSET UNSET UNSET"},
		{"opencode then codex: unset", oc + "\n" + cx, "UNSET UNSET UNSET"},
	} {
		if got := run(c.script + "\n" + show); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// T-809: an OpenCode start, a lane's worker and a role agent alike, names its generated config in the env line
// (OPENCODE_CONFIG=<project>/.obor/state/opencode.json, a path: the line stays short): obor's MCP server is the
// launching kit's `bin/obor mcp` and its plugin the kit's package dir. Another kind's start names none.
func TestOpencodeStartNamesItsGeneratedConfig(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("models", `{"oc":{"kind":"opencode","model":"go/ds"}}`),
		testkit.Set("roles", `{"reviewer":"oc","integrator":"opus"}`))
	file := filepath.Join(x.p.Root, ".obor", "state", "opencode.json")
	if _, err := os.Stat(file); err == nil {
		t.Fatal("precondition: no generated config yet")
	}
	x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"oc"}`)
	x.shell("w1:p3", "pj-l-01", "")
	x.agent("w1:p7", "pj-rev", "opencode", "idle")
	x.agent("w1:p8", "pj-int", "claude", "idle")
	lineOf := func(req launch.Request) string {
		before := len(x.calls("pane.send_input"))
		if out, ok := x.restart(req); !ok {
			t.Fatal(out)
		}
		for _, c := range x.calls("pane.send_input")[before:] {
			if strings.Contains(c, " && export ") || strings.Contains(c, "export ") {
				return c
			}
		}
		t.Fatalf("%s: no env line typed", req.Agent)
		return ""
	}
	want := "OPENCODE_CONFIG=" + file
	for _, req := range []launch.Request{{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true}, {Agent: "pj-rev", Fresh: true}} {
		if line := lineOf(req); !strings.Contains(line, want) {
			t.Errorf("%s: the env line %q lacks %s", req.Agent, line, want)
		}
	}
	if line := lineOf(launch.Request{Agent: "pj-int", Fresh: true}); strings.Contains(line, "OPENCODE_CONFIG=") {
		t.Errorf("a claude start sets OPENCODE_CONFIG: %q", line)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MCP struct {
			Servers map[string]struct{ Command []string }
		}
		Plugins []string
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if c := got.MCP.Servers["obor"].Command; strings.Join(c, " ") != "/kit/bin/obor mcp" {
		t.Errorf("mcp.servers.obor.command = %q: want the launching kit's", c)
	}
	if strings.Join(got.Plugins, " ") != "file:///kit/opencode" {
		t.Errorf("plugins = %q: want the kit's plugin dir", got.Plugins)
	}
}
