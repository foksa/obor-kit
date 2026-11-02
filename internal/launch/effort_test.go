package launch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/testkit"
)

// T-695: the effort a start runs at: the request's (a dispatch's type reference), else the role's type reference
// (roles.<role>.type `opus:max`), else the type's args. The kind sets it once, in place of the args' own; OpenCode's
// reaches its config through the env line (OPENCODE_CONFIG_CONTENT), which unsets it when the start names none.
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
			[]string{"--effort high"}, []string{"medium"}, "", "OPENCODE_CONFIG_CONTENT"},
		{"the role's effort", "opus:max", "claude", "", []string{"--effort max"}, []string{"medium"}, "", ""},
		{"the request's effort before the role's", "opus:max", "claude", "low", []string{"--effort low"}, []string{"max", "medium"}, "", ""},
		{"no effort: the type's args", "opus", "claude", "", []string{"--effort medium"}, nil, "", ""},
		{"codex: the role's effort", "luna:xhigh", "codex", "", []string{"-c model_reasoning_effort=xhigh"},
			[]string{"model_reasoning_effort=high"}, "", ""},
		{"opencode: the effort reaches its config as the model's variant", "oc:high", "opencode", "",
			[]string{"--auto"}, []string{"--model", "go/ds", "high"}, `export OPENCODE_CONFIG_CONTENT='{"model":"go/ds#high"}'`, ""},
		{"opencode, no model or effort: the variable unset", "bare", "opencode", "", []string{"--auto"}, nil,
			"unset OPENCODE_CONFIG_CONTENT", "export OPENCODE"},
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

// resumingOpencode is OpenCode as if it resumed a session (Caps.Resume), to reach a resume of a kind with its own
// state dir (T-696); OpenCode itself starts fresh every time (T-581).
type resumingOpencode struct{ agents.Kind }

func (resumingOpencode) Name() string { return "ocresume" }
func (k resumingOpencode) Caps() agents.Caps {
	c := k.Kind.Caps()
	c.Resume = true
	return c
}

func init() {
	k, _ := agents.Lookup("opencode")
	agents.Register(resumingOpencode{k})
}

// T-696: an OpenCode start's env line points XDG_STATE_HOME at the agent's own dir
// (${OBOR_STATE_HOME}/opencode-state/<project>/<agent>): a fresh start empties it first (a variant an earlier start
// remembered there is gone), a resume keeps what is there; the user's own ~/.local/state/opencode is not touched.
func TestOpencodeStateDir(t *testing.T) {
	t.Parallel()
	const types = `{"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]},"ocr":{"kind":"ocresume","args":["--auto","--model","go/ds"]}}`
	for _, c := range []struct {
		name, role string
		req        launch.Request
		kept       bool
	}{
		{"a fresh start empties the dir", "oc:high", launch.Request{Agent: "pj-rev", Fresh: true}, false},
		{"a resume keeps it", "ocr:high", launch.Request{Agent: "pj-rev", Sid: "ses_1"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("lane_types", types), testkit.Set("roles", `{"reviewer":{"type":"`+c.role+`"}}`))
			x.agent("w1:p7", "pj-rev", "opencode", "idle")
			cfg, err := config.Load(x.p.Root)
			if err != nil {
				t.Fatal(err)
			}
			env := x.env()
			dir := filepath.Join(project.StateHome(env), "opencode-state", cfg.ProjectName(), "pj-rev")
			user := filepath.Join(env.Get("HOME"), ".local", "state", "opencode", "model.json")
			for _, f := range []string{filepath.Join(dir, "model.json"), user} {
				if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f, []byte(`{"variant":{"go/ds":"default"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out, ok := x.restart(c.req)
			if !ok {
				t.Fatal(out)
			}
			if c.kept != strings.Contains(out, "resuming ses_1") {
				t.Fatalf("resume %v, but the output says %q", c.kept, out)
			}
			line := strings.Join(x.calls("pane.send_input"), "\n")
			if want := "export XDG_STATE_HOME=" + dir; !strings.Contains(line, want) {
				t.Errorf("env line %q lacks %q", line, want)
			}
			if want := `export OPENCODE_CONFIG_CONTENT='{"model":"go/ds#high"}'`; !strings.Contains(line, want) {
				t.Errorf("env line %q lacks %q", line, want)
			}
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				t.Fatalf("the state dir %s is not there after the start: %v", dir, err)
			}
			_, err = os.Stat(filepath.Join(dir, "model.json"))
			if kept := err == nil; kept != c.kept {
				t.Errorf("the dir's model.json kept %v, want %v", kept, c.kept)
			}
			if _, err := os.Stat(user); err != nil {
				t.Errorf("the user's own state: %v", err)
			}
		})
	}
}
