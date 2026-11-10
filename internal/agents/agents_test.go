package agents_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	_ "obor/internal/testkit" // OpenCode, a plugin kind answering in process
)

// the registry is the one list of kinds (sorted), Default is one of them, an unknown kind is no kind (Cursor's
// rows went), and a second registration of a name panics.
func TestRegistry(t *testing.T) {
	if got := agents.Names(); !slices.Equal(got, []string{"claude", "codex", "opencode"}) {
		t.Errorf("Names = %q", got)
	}
	if k, ok := agents.Lookup(agents.Default); !ok || k.Name() != agents.Default {
		t.Errorf("Default %s is no registered kind", agents.Default)
	}
	for _, name := range []string{"cursor", "gemini", ""} {
		if _, ok := agents.Lookup(name); ok {
			t.Errorf("Lookup(%q) ok", name)
		}
	}
	k, _ := agents.Lookup("codex")
	defer func() {
		if recover() == nil {
			t.Error("a second codex registered")
		}
	}()
	agents.Register(k)
}

// A kind's programs default to its name; ByProgram finds a kind by an
// executable's base name and no kind by another word.
func TestByProgram(t *testing.T) {
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		if got := agents.Programs(k); !slices.Equal(got, []string{name}) {
			t.Errorf("Programs(%s) = %q, want its name", name, got)
		}
		if b, ok := agents.ByProgram(name); !ok || b.Name() != name {
			t.Errorf("ByProgram(%q) = %v, %v", name, b, ok)
		}
	}
	for _, w := range []string{"git", "/usr/bin/claude", ""} {
		if _, ok := agents.ByProgram(w); ok {
			t.Errorf("ByProgram(%q) ok", w)
		}
	}
}

// Resumes names the kinds that honour a session id: Claude and Codex yes, OpenCode no (its session is
// recorded but never resumed), an unknown kind no.
func TestResumes(t *testing.T) {
	for _, c := range []struct {
		kind string
		want bool
	}{{"claude", true}, {"codex", true}, {"opencode", false}, {"", false}, {"cursor", false}} {
		if got := agents.Resumes(c.kind); got != c.want {
			t.Errorf("Resumes(%q) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// ResetIn reads an epoch after '|', else a clock time ("resets 6pm", "resets at 6:30 PM", "try again at
// 11:01 PM.") as its next occurrence after at in at's location (12 am is midnight, 12 pm noon); else none.
func TestResetIn(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := time.Date(2026, 10, 30, 14, 0, 0, 0, loc)
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	for _, c := range []struct {
		text string
		want time.Time
	}{
		{"usage limit reached|1793383200", time.Unix(1793383200, 0)},
		{"resets 6pm", day(30, 18, 0)},
		{"resets at 6:30 PM", day(30, 18, 30)},
		{"or try again at 11:01 PM.", day(30, 23, 1)},
		{"try again at 4:01 AM.", day(31, 4, 1)},
		{"resets 12am", day(31, 0, 0)},
		{"resets 12:15pm", day(31, 12, 15)},
		{"resets 2pm", day(30, 14, 0)},
		{"429 Too Many Requests", time.Time{}},
		{"resets 13pm", time.Time{}},
	} {
		if got := agents.ResetIn(c.text, at); !got.Equal(c.want) {
			t.Errorf("ResetIn(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestSkills(t *testing.T) {
	for _, c := range []struct {
		kind string
		want bool
	}{{"claude", true}, {"codex", false}, {"opencode", false}, {"", false}} {
		if got := agents.Skills(c.kind); got != c.want {
			t.Errorf("Skills(%q) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// noEffort is a kind that takes no effort (no registered kind is one).
type noEffort struct{ agents.Kind }

func (noEffort) Name() string      { return "plain" }
func (noEffort) Efforts() []string { return nil }

// an effort is checked per kind: each kind's own list, a value outside it refused naming the list, a kind with
// no effort and an unknown kind refused, "" always fine.
func TestCheckEffort(t *testing.T) {
	for _, c := range []struct{ kind, effort, refusal string }{
		{"claude", "xhigh", ""}, {"claude", "max", ""}, {"claude", "minimal", "not one of claude's (low, medium, high, xhigh, max)"},
		{"codex", "max", ""}, {"codex", "ultra", ""}, {"codex", "minimal", "not one of codex's (low, medium, high, xhigh, max, ultra)"},
		{"opencode", "max", ""}, {"cursor", "high", "has no bridge"}, {"cursor", "", ""}, {"claude", "", ""},
	} {
		err := agents.CheckEffort(c.kind, c.effort)
		if c.refusal == "" && err != nil || c.refusal != "" && (err == nil || !strings.Contains(err.Error(), c.refusal)) {
			t.Errorf("CheckEffort(%s, %q) = %v, want %q", c.kind, c.effort, err, c.refusal)
		}
	}
	if err := agents.CheckKindEffort(noEffort{}, "high"); err == nil || !strings.Contains(err.Error(), "kind plain takes no effort") {
		t.Errorf("a kind with no effort: %v", err)
	}
}

// ModelArgs puts a model in args as each kind takes it: in place of the args' own (their spelling kept,
// a later one dropped), appended when they have none, dropped for ""; every other arg stays where it is.
func TestModelArgs(t *testing.T) {
	for _, c := range []struct {
		kind, val  string
		args, want []string
	}{
		{"claude", "fable", []string{"--model", "opus", "--effort", "medium", "--permission-mode", "auto"},
			[]string{"--model", "fable", "--effort", "medium", "--permission-mode", "auto"}},
		{"claude", "fable", []string{"--permission-mode", "auto", "--model=opus"}, []string{"--permission-mode", "auto", "--model=fable"}},
		{"claude", "fable", []string{"--permission-mode", "auto"}, []string{"--permission-mode", "auto", "--model", "fable"}},
		{"claude", "", []string{"--model", "opus", "--permission-mode", "auto"}, []string{"--permission-mode", "auto"}},
		{"codex", "gpt-7", []string{"-m", "gpt-6", "-c", "model_reasoning_effort=high", "-s", "workspace-write"},
			[]string{"-m", "gpt-7", "-c", "model_reasoning_effort=high", "-s", "workspace-write"}},
		{"codex", "gpt-7", []string{"-s", "workspace-write", "--model=gpt-6"}, []string{"-s", "workspace-write", "--model=gpt-7"}},
		{"codex", "gpt-7", []string{"-a", "never"}, []string{"-a", "never", "-m", "gpt-7"}},
	} {
		got, err := agents.ModelArgs(c.kind, c.args, c.val)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s model %q of %q: %q, %v; want %q", c.kind, c.val, c.args, got, err, c.want)
		}
	}
	if _, err := agents.ModelArgs("cursor", nil, "m"); err == nil {
		t.Error("a kind with no bridge names no model: want an error")
	}
	if _, err := agents.ModelArgs("opencode", nil, "m"); err == nil { // config appends --model <id>
		t.Error("a plugin kind names no model in its args: want an error")
	}
}

// the registry of the env vars obor sets for an agent is one list: the identity vars, the retired OBOR_AGENT and
// every kind's own (Kind.Vars), and a kind's start sets none outside it.
func TestVarsRegistry(t *testing.T) {
	vars := agents.Vars()
	for _, v := range []string{"OBOR_ROLE", "OBOR_LANE", "OBOR_AGENT", "OBOR_OPENCODE_MODEL", "OPENCODE_CONFIG", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"} {
		if !slices.Contains(vars, v) {
			t.Errorf("Vars %q lacks %s", vars, v)
		}
	}
	if !slices.IsSorted(vars) || len(slices.Compact(slices.Clone(vars))) != len(vars) {
		t.Errorf("Vars %q: want sorted, each once", vars)
	}
	write := func(name string, _ []byte) (string, error) { return "/state/" + name, nil }
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		for _, v := range k.Vars() {
			if !slices.Contains(vars, v) {
				t.Errorf("%s's var %s is not in the registry", name, v)
			}
		}
		l, err := agents.StartOf(k, agents.Start{Kit: "/kit", Args: []string{"--model", "p/m"}, Effort: "high", WriteState: write})
		if err != nil {
			t.Fatalf("%s: start: %v", name, err)
		}
		env := l.Env
		if name == "opencode" && len(env) == 0 {
			t.Fatal("precondition: opencode's start sets vars")
		}
		for v := range env {
			if !slices.Contains(k.Vars(), v) {
				t.Errorf("%s's start sets %s, not one of its Vars %q", name, v, k.Vars())
			}
		}
	}
}

// StartVars, the one builder of a start's env: the role, the lane, the pane and the kind's vars set (a
// "" value: none), every other registry var unset, whatever kind set it before; keep leaves one as the pane has it.
func TestStartVars(t *testing.T) {
	names := func(set []agents.EnvVar) string {
		var out []string
		for _, v := range set {
			out = append(out, v.Name+"="+v.Value)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name, role, lane, pane string
		kind                   map[string]string
		keep                   []string
		set, unset             string
	}{
		{"a claude worker: its pane set, another kind's vars unset", "worker", "L-01", "w1:3", nil, nil,
			"OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:3", "OBOR_AGENT OBOR_OPENCODE_MODEL OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"an opencode role agent: its vars set, the lane unset", "reviewer", "", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": "p/m#high"}, nil,
			"OBOR_ROLE=reviewer OBOR_PANE=w1:4 OBOR_OPENCODE_MODEL=p/m#high OPENCODE_CONFIG=/s/oc.json", "OBOR_AGENT OBOR_LANE OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"an opencode start without a model: the model unset", "reviewer", "", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": ""}, nil,
			"OBOR_ROLE=reviewer OBOR_PANE=w1:4 OPENCODE_CONFIG=/s/oc.json", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"a trimmed opencode worker: the skills switch set", "worker", "L-01", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": "p/m", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "1"}, nil,
			"OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:4 OBOR_OPENCODE_MODEL=p/m OPENCODE_CONFIG=/s/oc.json OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1",
			"OBOR_AGENT"},
		{"an opencode lead: the skills switch empty in its env, so unset", "lead", "", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": "p/m", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": ""}, nil,
			"OBOR_ROLE=lead OBOR_PANE=w1:4 OBOR_OPENCODE_MODEL=p/m OPENCODE_CONFIG=/s/oc.json",
			"OBOR_AGENT OBOR_LANE OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"a pane being made: OBOR_PANE unset (not the maker's), a kind's OBOR_PANE ignored", "worker", "L-01", "",
			map[string]string{"OBOR_PANE": "w9:9"}, nil,
			"OBOR_ROLE=worker OBOR_LANE=L-01", "OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_PANE OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"no role: unset", "", "", "", nil, nil, "", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_PANE OBOR_ROLE OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"no role, OBOR_ROLE kept", "", "", "", nil, []string{"OBOR_ROLE"}, "", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_PANE OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
	} {
		set, unset := agents.StartVars(c.role, c.lane, c.pane, c.kind, c.keep...)
		if got := names(set); got != c.set {
			t.Errorf("%s: set %q, want %q", c.name, got, c.set)
		}
		if got := strings.Join(unset, " "); got != c.unset {
			t.Errorf("%s: unset %q, want %q", c.name, got, c.unset)
		}
	}
}
