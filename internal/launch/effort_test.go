package launch_test

import (
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// T-695, T-722, T-765: the model and effort a start runs at: the request's (a dispatch's model ref), else the role's
// model ref (roles.<role> `opus:max`), else the lane row's (a pooled lane's type), else none (the kind's default); the
// request's type and effort come before the row's. The kind sets the effort once, in place of any kinds args' own;
// OpenCode's reaches obor's plugin through the env line (OBOR_OPENCODE_MODEL, T-703), which unsets it when the start
// names none.
func TestStartEffort(t *testing.T) {
	t.Parallel()
	const models = `{"oc":{"kind":"opencode","id":"go/ds"},"bare":{"kind":"opencode","id":""}}`
	for _, c := range []struct {
		name, role, effort string   // the role's model ref (a role agent), the request's effort
		row, typ           string   // the lane row's type, the request's type (a lane worker)
		has, lacks         []string // in agent.start
		env, noEnv         string   // in, not in the env line
	}{
		{"a request's effort replaces the kinds args' own, once", "opus", "high",
			"", "", []string{"--effort high"}, []string{"medium"}, "unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL ", "export OBOR_OPENCODE_MODEL"},
		{"the role's effort", "opus:max", "", "", "", []string{"--effort max"}, []string{"medium"}, "", ""},
		{"the request's effort before the role's", "opus:max", "low", "", "", []string{"--effort low"}, []string{"max", "medium"}, "", ""},
		{"no effort: the kinds args' own", "opus", "", "", "", []string{"--effort medium"}, nil, "", ""},
		{"codex: the role's effort", "luna:xhigh", "", "", "", []string{"-c model_reasoning_effort=xhigh"},
			[]string{"model_reasoning_effort=high"}, "unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL ", ""},
		{"opencode: the model and effort reach the plugin as model#variant", "oc:high", "",
			"", "", []string{"--auto"}, []string{"--model", "go/ds", "high"}, "export OBOR_ROLE=reviewer OBOR_PANE=w1:p7 OBOR_OPENCODE_MODEL='go/ds#high'", "OPENCODE_CONFIG_CONTENT"},
		{"opencode, no model or effort: the variable unset", "bare", "",
			"", "", []string{"--auto"}, nil, "unset OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL ", "export OBOR_OPENCODE_MODEL"},
		// T-722, T-765: a pooled lane's worker runs the model its lane row's type names, and the launch's type before it.
		{"no dispatch model: the default kind, no model", "", "",
			"", "", []string{"kind=claude"}, []string{"gpt-6-luna", "--model"}, "", ""},
		{"the launch's type and effort", "", "xhigh", "", "luna",
			[]string{"kind=codex", "-m gpt-6-luna", "model_reasoning_effort=xhigh"}, []string{"--model opus", "kind=claude"}, "", ""},
		{"the launch's type before the row's", "", "", "opus:low", "luna",
			[]string{"kind=codex", "-m gpt-6-luna"}, []string{"--model opus", "low"}, "", ""},
		{"the lane row's type and effort", "", "", "luna:high", "",
			[]string{"kind=codex", "-m gpt-6-luna", "model_reasoning_effort=high"}, []string{"--model opus", "kind=claude"}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.models", models), testkit.Set("agents.roles", `{"reviewer":"`+c.role+`"}`),
				testkit.Set("agents.kinds", `{"claude":{"args":["--effort","medium"]},"codex":{"args":["-c","model_reasoning_effort=high"]},`+
					`"opencode":{"plugin":"obor-kind-opencode"}}`))
			// a role's model ref: the reviewer's start; a lane row's: lane L-01's worker.
			agent, pane := "pj-rev", "w1:p7"
			if c.role == "" {
				agent, pane = "pj-l-01", "w1:p3"
			}
			if c.row != "" {
				x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"`+c.row+`"}`)
			}
			if row, ok := x.p.Row("L-01"); c.row != "" && (!ok || row.Type != c.row) {
				t.Fatalf("precondition: lane row %+v (%v), want type %s", row, ok, c.row)
			}
			x.shell(pane, agent, "")
			out, ok := x.restart(launch.Request{Agent: agent, Fresh: true, Effort: c.effort, Type: c.typ})
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
			if !strings.Contains(line, "export OBOR_ROLE=") {
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
	const models = `{"bare":{"kind":"opencode","id":""}}`
	for _, c := range []struct{ role, effort, want string }{
		{"opus", "turbo", `effort "turbo" is not one of claude's`},
		{"bare", "high", "needs a model"},
	} {
		// the fixture's HOME is a temp dir: no opencode config or state names a model
		x := setup(t, testkit.Set("agents.models", models), testkit.Set("agents.roles", `{"reviewer":"`+c.role+`"}`))
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
