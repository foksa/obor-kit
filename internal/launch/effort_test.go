package launch_test

import (
	"os/exec"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// T-695: the effort a start runs at: the request's (a dispatch's type reference), else the role's type reference
// (roles.<role>.type `opus:max`), else the type's args. The kind sets it once, in place of the args' own; OpenCode's
// reaches obor's plugin through the env line (OBOR_OPENCODE_MODEL, T-703), which unsets it when the start names none.
func TestStartEffort(t *testing.T) {
	t.Parallel()
	const types = `{"opus":{"kind":"claude","args":["--model","opus","--effort","medium"]},` +
		`"luna":{"kind":"codex","args":["-m","gpt-6-luna","-c","model_reasoning_effort=high"]},` +
		`"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]},"bare":{"kind":"opencode","args":["--auto"]}}`
	for _, c := range []struct {
		name, role, kind, effort string
		has, lacks               []string // in agent.start's args
		env, noEnv               string   // in, not in the env line
	}{
		{"a request's effort replaces the type's, once", "opus", "claude", "high",
			[]string{"--effort high"}, []string{"medium"}, "", "OBOR_OPENCODE_MODEL"},
		{"the role's effort", "opus:max", "claude", "", []string{"--effort max"}, []string{"medium"}, "", ""},
		{"the request's effort before the role's", "opus:max", "claude", "low", []string{"--effort low"}, []string{"max", "medium"}, "", ""},
		{"no effort: the type's args", "opus", "claude", "", []string{"--effort medium"}, nil, "", ""},
		{"codex: the role's effort", "luna:xhigh", "codex", "", []string{"-c model_reasoning_effort=xhigh"},
			[]string{"model_reasoning_effort=high"}, "", ""},
		{"opencode: the model and effort reach the plugin as model#variant", "oc:high", "opencode", "",
			[]string{"--auto"}, []string{"--model", "go/ds", "high"}, "export OBOR_OPENCODE_MODEL='go/ds#high'", "OPENCODE_CONFIG_CONTENT"},
		{"opencode, no model or effort: the variable unset", "bare", "opencode", "", []string{"--auto"}, nil,
			"unset OBOR_OPENCODE_MODEL", "export OBOR_OPENCODE_MODEL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("lane_types", types), testkit.Set("roles", `{"reviewer":{"type":"`+c.role+`"}}`))
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
			if !strings.Contains(line, "export OBOR_AGENT=pj-rev") {
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
	const types = `{"opus":{"kind":"claude","args":["--model","opus"]},"bare":{"kind":"opencode","args":["--auto"]}}`
	for _, c := range []struct{ role, effort, want string }{
		{"opus", "turbo", `effort "turbo" is not one of claude's`},
		{"bare", "high", "needs a model"},
	} {
		// the fixture's HOME is a temp dir: no opencode config or state names a model
		x := setup(t, testkit.Set("lane_types", types), testkit.Set("roles", `{"reviewer":{"type":"`+c.role+`"}}`))
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

// T-695: the scheduled restart's argv carries the effort (Args and ParseRequest are one mapping).
func TestRequestArgsCarryTheEffort(t *testing.T) {
	r := launch.Request{Agent: "pj-o-01", Fresh: true, Prompt: "go", Effort: "high"}
	back, err := launch.ParseRequest(launch.Args(r))
	if err != nil || back != r {
		t.Errorf("ParseRequest(Args(%+v)) = %+v, %v", r, back, err)
	}
}

// T-703: the OpenCode agent's model reaches its plugin through the pane's shell, nothing else: the env lines of starts
// typed at one pane's shell (run in a real sh) after what the pane held before, one row per case. No start sets or
// unsets XDG_STATE_HOME (the T-696 per-agent state dir is gone) or OPENCODE_CONFIG_CONTENT, so a user's own stay.
func TestOpencodeModelIsTheOpencodeStartsAlone(t *testing.T) {
	t.Parallel()
	const types = `{"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]},"bare":{"kind":"opencode","args":["--auto"]},"cl":{"kind":"claude","args":[]}}`
	x := setup(t, testkit.Set("lane_types", types),
		testkit.Set("roles", `{"reviewer":{"type":"oc:high"},"advisor":{"type":"bare"},"integrator":{"type":"cl"}}`))
	x.agent("w1:p7", "pj-rev", "opencode", "idle")
	x.agent("w1:p6", "pj-advisor", "opencode", "idle")
	x.agent("w1:p8", "pj-int", "claude", "idle")
	lineOf := func(agent string) string {
		if out, ok := x.restart(launch.Request{Agent: agent, Fresh: true}); !ok {
			t.Fatal(out)
		}
		runs := x.calls("pane.send_input OBOR_AGENT=" + agent)
		if len(runs) != 1 {
			t.Fatalf("%s: one env line typed, got %q", agent, runs)
		}
		_, text, _ := strings.Cut(runs[0], " text=")
		m := envMarksRE.FindStringIndex(text)
		if m == nil {
			t.Fatalf("%s: no marks at the end of %q", agent, text)
		}
		_, line, _ := strings.Cut(text[:m[0]], " && export ") // not the cd: the fixture's worktree is no directory
		return "export " + line
	}
	oc, bare, cl := lineOf("pj-rev"), lineOf("pj-advisor"), lineOf("pj-int")
	for _, l := range []string{oc, bare, cl} {
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
	} {
		if got := run(c.script + "\n" + show); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
