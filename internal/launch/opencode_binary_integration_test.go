//go:build integration

package launch_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/kinds/opencode/kind"
)

// a lane worker of an OpenCode model, started the way a dispatch starts it
// (launch.Launcher.Build, the test helper of export_test.go), runs the real obor-kind-opencode binary built here, once, through obor's plugin package
// (a recording wrapper keeps each request): the start request carries the complete start (kit, the model's args, the
// effort, the prompt, the lane dir, strict), and the start gets the argv, the env and the generated config the compiled
// kind gave it (--auto --standalone, the model gone, --prompt; OBOR_OPENCODE_MODEL model#effort, OPENCODE_CONFIG the
// lane's file in the state dir holding obor's server and plugin and the user's own server disabled).
func TestLaneStartRunsTheBinary(t *testing.T) {
	// the config names the binary by its path, not the kit's name: obor runs it, not the in-process kind
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", real, "obor/kinds/opencode").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	rec := filepath.Join(dir, "requests")
	wrapper := filepath.Join(dir, "obor-kind-opencode")
	script := "#!/bin/sh\nreq=$(cat)\nprintf '%s %s\\n' \"$1\" \"$req\" >> '" + rec + "'\nprintf '%s' \"$req\" | exec '" + real + "' \"$1\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	root, lane := filepath.Join(dir, "proj"), filepath.Join(dir, "proj-wt", "L-01")
	for _, d := range []string{filepath.Join(root, ".obor"), lane} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	doc := map[string]any{"version": 2,
		"project": map[string]any{"name": "proj", "agent_prefix": "pj", "task_id_pattern": "T-[0-9]+"},
		"lanes":   map[string]any{"dir": "../proj-wt", "count": 1, "task_branch": "task/{id}"},
		"merge":   map[string]any{"target_branch": "main"}, "commands": map[string]any{},
		"agents": map[string]any{"kinds": map[string]any{"opencode": map[string]any{"plugin": wrapper}},
			"models": map[string]any{"ds": map[string]any{"kind": "opencode", "id": "go/ds"}}}}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Cache(kind.Name, cfg.KindCommand(kind.Name), cfg.StateDir()); err != nil { // obor setup agents
		t.Fatal(err)
	}
	if cfg, err = config.Load(root); err != nil {
		t.Fatal(err)
	}
	if k, ok := cfg.Kind(kind.Name); !ok || !cfg.Kinds().IsPlugin(kind.Name) || k.Caps().Hooks != agents.HooksPlugin {
		t.Fatalf("precondition: opencode is the cached plugin kind (%v, %v)", ok, cfg.Kinds().IsPlugin(kind.Name))
	}
	if err := os.Remove(rec); err != nil {
		t.Fatalf("precondition: setup's describe was recorded: %v", err)
	}

	// the start reads the user's own OpenCode config (a lane's trim disables its MCP servers): a scratch one, so the
	// machine's own servers are no row of this test
	home := filepath.Join(dir, "home")
	userConf := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(userConf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userConf, "opencode.json"),
		[]byte(`{"mcp": {"servers": {"mine": {"type": "local", "command": ["x"]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(key string) (string, bool) {
		v, ok := map[string]string{"HOME": home}[key]
		return v, ok
	}

	m, _ := cfg.Model("ds")
	l := &launch.Launcher{Cfg: cfg, Kit: "/kit", Env: env}
	built, err := l.Build(launch.Build{Agent: "pj-l-01", Kind: m.Kind, Args: m.Args, Role: "worker", Effort: "high",
		Dir: lane, Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--auto", "--standalone", "--prompt", "go"}; !slices.Equal(built.Args, want) {
		t.Errorf("argv %q, want %q", built.Args, want)
	}
	conf := filepath.Join(cfg.StateDir(), kind.LaneConfigFile(lane))
	if r, set := built.Vars[kind.ResumeVar]; len(built.Vars) != 4 || built.Vars[kind.ModelVar] != "go/ds#high" ||
		built.Vars[kind.ConfigVar] != conf || built.Vars[kind.SkillsVar] != "1" || !set || r != "" {
		t.Errorf("env %v, want %s=go/ds#high, %s=%s, a worker's %s=1 and %s unset (a fresh start)", built.Vars, kind.ModelVar,
			kind.ConfigVar, conf, kind.SkillsVar, kind.ResumeVar)
	}
	var got struct {
		MCP struct {
			Servers map[string]map[string]any `json:"servers"`
		} `json:"mcp"`
	}
	raw, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if mine := got.MCP.Servers["mine"]; mine["disabled"] != true || mine["command"] == nil {
		t.Errorf("the generated config's mine = %v, want the user's server whole with disabled: true", mine)
	}
	if o := got.MCP.Servers["obor"]; o["type"] != "local" || len(got.MCP.Servers) != 2 {
		t.Errorf("mcp.servers = %v, want obor's own and the user's disabled", got.MCP.Servers)
	}

	lines, _ := os.ReadFile(rec)
	calls := strings.Split(strings.TrimSpace(string(lines)), "\n")
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "start ") {
		t.Fatalf("requests %q, want the one start", calls)
	}
	var req struct {
		Version   int            `json:"version"`
		Call      string         `json:"call"`
		Kit       string         `json:"kit"`
		Args      []string       `json:"args"`
		Effort    string         `json:"effort"`
		Prompt    string         `json:"prompt"`
		Dir       string         `json:"dir"`
		StrictMCP bool           `json:"strict_mcp"`
		TrimUser  bool           `json:"trim_user"`
		Settings  map[string]any `json:"settings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(calls[0], "start ")), &req); err != nil {
		t.Fatal(err)
	}
	if req.Version != 1 || req.Call != "start" || req.Kit != "/kit" || !slices.Equal(req.Args, m.Args) || req.Effort != "high" ||
		req.Prompt != "go" || req.Dir != lane || !req.StrictMCP || !req.TrimUser || req.Settings == nil {
		t.Errorf("start request %+v: want the complete start (kit, the model's args %q, effort, prompt, the lane, strict, trimmed, settings)", req, m.Args)
	}
}
