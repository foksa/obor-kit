package agents_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	"obor/internal/testkit" // OpenCode, agy and pi, plugin kinds answering in process
)

// kinds are a project's kinds as its config holds them: the compiled ones and the kit's plugin kinds (OpenCode, agy,
// pi).
var kinds = testkit.Kinds()

// the registry holds the compiled kinds alone (the zero Kinds); a Kinds adds its plugin kinds to them, one list
// (sorted), never in place of a compiled one of the same name; Default is one of them, an unknown kind is no kind
// (Cursor's rows went), and a second registration of a name panics.
func TestRegistry(t *testing.T) {
	if got := (agents.Kinds{}).Names(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Errorf("the compiled kinds = %q", got)
	}
	if got := kinds.Names(); !slices.Equal(got, []string{"agy", "claude", "codex", "opencode", "pi"}) {
		t.Errorf("Names = %q", got)
	}
	oc, ok := kinds.Lookup("opencode")
	if !ok || !kinds.IsPlugin("opencode") || kinds.IsPlugin("claude") || (agents.Kinds{}).IsPlugin("opencode") {
		t.Fatalf("opencode: %v, a plugin kind %v; claude a plugin kind %v", ok, kinds.IsPlugin("opencode"), kinds.IsPlugin("claude"))
	}
	compiled, _ := agents.Kinds{}.Lookup("claude")
	shadow := agents.WithPlugins([]agents.Kind{namedKind{oc, "claude"}, oc})
	if k, _ := shadow.Lookup("claude"); k != compiled || shadow.IsPlugin("claude") || !shadow.IsPlugin("opencode") {
		t.Errorf("a plugin kind named claude: Lookup %v (want the compiled kind), a plugin kind %v", k, shadow.IsPlugin("claude"))
	}
	if k, ok := kinds.Lookup(agents.Default); !ok || k.Name() != agents.Default {
		t.Errorf("Default %s is no registered kind", agents.Default)
	}
	for _, name := range []string{"cursor", "gemini", ""} {
		if _, ok := kinds.Lookup(name); ok {
			t.Errorf("Lookup(%q) ok", name)
		}
	}
	k, _ := kinds.Lookup("codex")
	defer func() {
		if recover() == nil {
			t.Error("a second codex registered")
		}
	}()
	agents.Register(k)
}

// namedKind is k under another name.
type namedKind struct {
	agents.Kind
	name string
}

func (k namedKind) Name() string { return k.name }

// A kind's programs default to its name; ByProgram finds a kind by an
// executable's base name and no kind by another word.
func TestByProgram(t *testing.T) {
	for _, name := range kinds.Names() {
		k, _ := kinds.Lookup(name)
		if got := agents.Programs(k); !slices.Equal(got, []string{name}) {
			t.Errorf("Programs(%s) = %q, want its name", name, got)
		}
		if b, ok := kinds.ByProgram(name); !ok || b.Name() != name {
			t.Errorf("ByProgram(%q) = %v, %v", name, b, ok)
		}
	}
	for _, w := range []string{"git", "/usr/bin/claude", ""} {
		if _, ok := kinds.ByProgram(w); ok {
			t.Errorf("ByProgram(%q) ok", w)
		}
	}
}

// ByApp finds a kind by the app name its agents report in an OSC 7501 status (P1 of docs/research/hc-probes.md:
// Claude Code says claude-code, Codex and OpenCode their kind's name, agy antigravity), and no kind by another name,
// Claude's version (its process title) or "".
func TestByApp(t *testing.T) {
	for app, want := range map[string]string{"claude-code": "claude", "codex": "codex", "opencode": "opencode",
		"antigravity": "agy", "claude": "", "2.1.329": "", "vim": "", "": ""} {
		if got := agents.ByApp(kinds, app); got != want {
			t.Errorf("ByApp(%q) = %q, want %q", app, got, want)
		}
	}
}

// Resumes names the kinds that honour a session id: Claude, Codex and OpenCode yes, an unknown kind no.
func TestResumes(t *testing.T) {
	for _, c := range []struct {
		kind string
		want bool
	}{{"claude", true}, {"codex", true}, {"opencode", true}, {"", false}, {"cursor", false}} {
		if got := kinds.Resumes(c.kind); got != c.want {
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
		if got := kinds.Skills(c.kind); got != c.want {
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
		err := kinds.CheckEffort(c.kind, c.effort)
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
		got, err := kinds.ModelArgs(c.kind, c.args, c.val)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s model %q of %q: %q, %v; want %q", c.kind, c.val, c.args, got, err, c.want)
		}
	}
	if _, err := kinds.ModelArgs("cursor", nil, "m"); err == nil {
		t.Error("a kind with no bridge names no model: want an error")
	}
	if _, err := kinds.ModelArgs("opencode", nil, "m"); err == nil { // config appends --model <id>
		t.Error("a plugin kind names no model in its args: want an error")
	}
}

// the registry of the env vars obor sets for an agent is one list: the identity vars, the retired OBOR_AGENT and
// every kind's own (Kind.Vars), and a kind's start sets no value outside it (an empty name outside it is that start's
// own clear, no registry var).
func TestVarsRegistry(t *testing.T) {
	vars := kinds.Vars()
	for _, v := range []string{"OBOR_ROLE", "OBOR_LANE", "OBOR_AGENT", "OBOR_OPENCODE_MODEL", "OBOR_PI_RESUME", "OPENCODE_CONFIG", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"} {
		if !slices.Contains(vars, v) {
			t.Errorf("Vars %q lacks %s", vars, v)
		}
	}
	if !slices.IsSorted(vars) || len(slices.Compact(slices.Clone(vars))) != len(vars) {
		t.Errorf("Vars %q: want sorted, each once", vars)
	}
	write := func(name string, _ []byte) (string, error) { return "/state/" + name, nil }
	for _, name := range kinds.Names() {
		k, _ := kinds.Lookup(name)
		for _, v := range k.Vars() {
			if !slices.Contains(vars, v) {
				t.Errorf("%s's var %s is not in the registry", name, v)
			}
		}
		model := "p/m"
		if name == "agy" { // agy's start checks its model against its list (testkit.AgyModels in a test)
			model = "gemini-4-neon"
		}
		l, err := agents.StartOf(k, agents.Start{Kit: "/kit", Args: []string{"--model", model}, Effort: "high", WriteState: write})
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

// StartVars, the one builder of a start's env: the role, the lane, the pane and the kind's vars set (a non-empty
// value; a "" value of a var outside the registry is this start's own clear), every other registry var unset, whatever
// kind set it before; keep leaves one as the pane has it.
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
			"OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:3", "OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PI_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"an opencode role agent: its vars set, the lane unset", "reviewer", "", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": "p/m#high"}, nil,
			"OBOR_ROLE=reviewer OBOR_PANE=w1:4 OBOR_OPENCODE_MODEL=p/m#high OPENCODE_CONFIG=/s/oc.json", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_RESUME OBOR_PI_RESUME OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"an opencode start without a model: the model unset", "reviewer", "", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": ""}, nil,
			"OBOR_ROLE=reviewer OBOR_PANE=w1:4 OPENCODE_CONFIG=/s/oc.json", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PI_RESUME OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"a trimmed opencode worker: the skills switch set", "worker", "L-01", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": "p/m", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "1"}, nil,
			"OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:4 OBOR_OPENCODE_MODEL=p/m OPENCODE_CONFIG=/s/oc.json OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1",
			"OBOR_AGENT OBOR_OPENCODE_RESUME OBOR_PI_RESUME"},
		{"an opencode lead: the skills switch empty in its env, so unset", "lead", "", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OBOR_OPENCODE_MODEL": "p/m", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": ""}, nil,
			"OBOR_ROLE=lead OBOR_PANE=w1:4 OBOR_OPENCODE_MODEL=p/m OPENCODE_CONFIG=/s/oc.json",
			"OBOR_AGENT OBOR_LANE OBOR_OPENCODE_RESUME OBOR_PI_RESUME OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"a lane's opencode start clears the shell's inline config alone", "worker", "L-01", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OPENCODE_CONFIG_CONTENT": ""}, nil,
			"OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:4 OPENCODE_CONFIG=/s/oc.json",
			"OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PI_RESUME OPENCODE_CONFIG_CONTENT OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"a kept inline config stays as the pane has it", "worker", "L-01", "w1:4",
			map[string]string{"OPENCODE_CONFIG": "/s/oc.json", "OPENCODE_CONFIG_CONTENT": ""}, []string{"OPENCODE_CONFIG_CONTENT"},
			"OBOR_ROLE=worker OBOR_LANE=L-01 OBOR_PANE=w1:4 OPENCODE_CONFIG=/s/oc.json",
			"OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PI_RESUME OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"a pane being made: OBOR_PANE unset (not the maker's), a kind's OBOR_PANE ignored", "worker", "L-01", "",
			map[string]string{"OBOR_PANE": "w9:9"}, nil,
			"OBOR_ROLE=worker OBOR_LANE=L-01", "OBOR_AGENT OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PANE OBOR_PI_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"no role: unset", "", "", "", nil, nil, "", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PANE OBOR_PI_RESUME OBOR_ROLE OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
		{"no role, OBOR_ROLE kept", "", "", "", nil, []string{"OBOR_ROLE"}, "", "OBOR_AGENT OBOR_LANE OBOR_OPENCODE_MODEL OBOR_OPENCODE_RESUME OBOR_PANE OBOR_PI_RESUME OPENCODE_CONFIG OPENCODE_DISABLE_CLAUDE_CODE_SKILLS"},
	} {
		set, unset := kinds.StartVars(c.role, c.lane, c.pane, c.kind, c.keep...)
		if got := names(set); got != c.set {
			t.Errorf("%s: set %q, want %q", c.name, got, c.set)
		}
		if got := strings.Join(unset, " "); got != c.unset {
			t.Errorf("%s: unset %q, want %q", c.name, got, c.unset)
		}
	}
}

// MergePatch lays a patch over a target as RFC 7396 does: objects merged key by key, a null deleting the key, any
// other value (a list too) replacing it, in a new map that shares no object with either. ParseOverride reads an
// override file by its extension, TOML or JSON, and refuses what is no table or object.
func TestMergePatchAndParseOverride(t *testing.T) {
	a := map[string]any{"env": map[string]any{"A": "1", "B": "2"}, "model": "opus", "list": []any{"x"}, "gone": 1, "keep": true}
	b := map[string]any{"env": map[string]any{"B": "3", "C": "4", "A": nil}, "model": map[string]any{"id": "m", "none": nil},
		"list": []any{"y"}, "new": map[string]any{"k": true}, "gone": nil, "absent": nil}
	got := agents.MergePatch(a, b)
	want := map[string]any{"env": map[string]any{"B": "3", "C": "4"}, "model": map[string]any{"id": "m"},
		"list": []any{"y"}, "new": map[string]any{"k": true}, "keep": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergePatch = %v, want %v", got, want)
	}
	got["env"].(map[string]any)["B"] = "changed"
	delete(got["new"].(map[string]any), "k")
	if a["env"].(map[string]any)["B"] != "2" || b["env"].(map[string]any)["B"] != "3" || b["new"].(map[string]any)["k"] != true {
		t.Errorf("a change of the merged map reached its inputs: a %v, b %v", a, b)
	}
	only := agents.MergePatch(a, nil)
	delete(only["env"].(map[string]any), "A")
	if a["env"].(map[string]any)["A"] != "1" {
		t.Errorf("MergePatch(a, nil) shares a's object: %v", a)
	}

	doc, err := agents.ParseOverride("config_override.toml", "model_reasoning_effort = \"low\"\n[features]\nweb_search = true\n")
	if want := map[string]any{"model_reasoning_effort": "low", "features": map[string]any{"web_search": true}}; err != nil || !reflect.DeepEqual(doc, want) {
		t.Errorf("ParseOverride(toml) = %v, %v, want %v", doc, err, want)
	}
	doc, err = agents.ParseOverride("settings_override.json", `{"big": 12345678901234567890, "off": null}`)
	if want := map[string]any{"big": json.Number("12345678901234567890"), "off": nil}; err != nil || !reflect.DeepEqual(doc, want) {
		t.Errorf("ParseOverride(json) = %v, %v, want %v (numbers as written)", doc, err, want)
	}
	for name, bad := range map[string]string{"a.toml": "x = ", "a.json": `{"x": `, "list.json": `[1]`, "null.json": `null`, "two.json": `{} {}`} {
		if doc, err := agents.ParseOverride(name, bad); err == nil {
			t.Errorf("ParseOverride(%s, %q) = %v, want an error", name, bad, doc)
		}
	}
	in := agents.Start{Overrides: map[string]string{"ok.json": `{"a": 1}`, "bad.json": `{`}}
	if in.Override("ok.json") == nil || in.Override("bad.json") != nil || in.Override("none.json") != nil {
		t.Errorf("Start.Override: ok %v, bad %v, none %v, want a tree, nil, nil", in.Override("ok.json"), in.Override("bad.json"), in.Override("none.json"))
	}
}
