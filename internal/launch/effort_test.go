package launch_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
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
			dir := filepath.Join(project.StateHome(env), "opencode-state", strings.ToLower(cfg.ProjectName())+"-"+hex8(cfg.ProjectName()), "pj-rev-"+hex8("pj-rev"))
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

// T-698: XDG_STATE_HOME is the OpenCode agent's alone, and obor's dirs are told by their prefix, not by saved
// provenance. The env lines of starts typed at one pane's shell (run in a real sh) after what the pane held before:
// one row per case.
func TestOpencodeStateHomeIsTheOpencodeAgentsAlone(t *testing.T) {
	t.Parallel()
	const types = `{"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]},"cl":{"kind":"claude","args":[]},"cx":{"kind":"codex","args":[]}}`
	x := setup(t, testkit.Set("lane_types", types),
		testkit.Set("roles", `{"reviewer":{"type":"oc"},"integrator":{"type":"cl"},"triage":{"type":"cx"},"advisor":{"type":"oc"}}`))
	x.agent("w1:p7", "pj-rev", "opencode", "idle")
	x.agent("w1:p8", "pj-int", "claude", "idle")
	x.agent("w1:p9", "pj-triage", "codex", "idle")
	x.agent("w1:p6", "pj-advisor", "opencode", "idle")
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
	oc, other, cl, cx := lineOf("pj-rev"), lineOf("pj-advisor"), lineOf("pj-int"), lineOf("pj-triage")
	if !strings.Contains(oc, "export XDG_STATE_HOME=/") || strings.Contains(cl, "export XDG_STATE_HOME=/") || strings.Contains(cx, "export XDG_STATE_HOME=/") {
		t.Fatalf("precondition: only the OpenCode line exports it:\n%s\n%s\n%s", oc, cl, cx)
	}
	run := func(script string) string {
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s\n%s", err, script, out)
		}
		return strings.TrimSpace(string(out))
	}
	const show = "echo ${XDG_STATE_HOME-UNSET}"
	for _, c := range []struct {
		name, script string
		want         string // "" : only that it is an obor dir (opencode-state)
	}{
		{"opencode start sets its dir", "unset XDG_STATE_HOME\n" + oc, ""},
		{"claude after opencode in the pane: unset", oc + "\n" + cl, "UNSET"},
		{"codex after opencode in the pane: unset", oc + "\n" + cx, "UNSET"},
		{"claude with the user's own XDG_STATE_HOME: kept", "export XDG_STATE_HOME=/mine\n" + cl, "/mine"},
		{"claude with none: still none", "unset XDG_STATE_HOME\n" + cl, "UNSET"},
		{"a user's value that only looks like the root: kept", "export XDG_STATE_HOME=/mine/opencode-state/x\n" + cl, "/mine/opencode-state/x"},
		{"opencode after opencode (other agent): replaced", oc + "\n" + other, ""},
	} {
		got := run(c.script + "\n" + show)
		switch {
		case c.want == "" && !strings.Contains(got, "opencode-state"):
			t.Errorf("%s: XDG_STATE_HOME = %q, want an opencode-state dir", c.name, got)
		case c.want != "" && got != c.want:
			t.Errorf("%s: XDG_STATE_HOME = %q, want %q", c.name, got, c.want)
		}
	}
	// replaced means the second agent's own dir, not the first's
	first, second := run(oc+"\n"+show), run(oc+"\n"+other+"\n"+show)
	if first == second || !strings.HasSuffix(second, "pj-advisor-"+hex8("pj-advisor")) {
		t.Errorf("opencode after opencode: %q then %q, want the second agent's own dir", first, second)
	}
}

func hex8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}
