//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/agents/plugin"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/kinds/opencode/kind"
)

// a lane worker of an OpenCode model, started the way a dispatch starts it
// (launch.Launcher.Build), runs the real obor-kind-opencode binary built here, once, through obor's plugin package
// (a recording wrapper keeps each request): the start request carries the complete start (kit, the model's args, the
// effort, the prompt, the lane dir, strict), and the start gets the argv, the env and the generated config the compiled
// kind gave it (--auto --standalone, the model gone, --prompt; OBOR_OPENCODE_MODEL model#effort, OPENCODE_CONFIG the
// file in the state dir holding the kit's mcp server and plugin).
func TestLaneStartRunsTheBinary(t *testing.T) {
	delete(plugin.Local, kind.Name) // testkit answers OpenCode in process: this test runs the binary
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", real, ".").CombinedOutput(); err != nil {
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
	if k, ok := agents.Lookup(kind.Name); !ok || !agents.IsPlugin(kind.Name) || k.Caps().Hooks != agents.HooksPlugin {
		t.Fatalf("precondition: opencode is the cached plugin kind (%v, %v)", ok, agents.IsPlugin(kind.Name))
	}
	if err := os.Remove(rec); err != nil {
		t.Fatalf("precondition: setup's describe was recorded: %v", err)
	}

	m, _ := cfg.Model("ds")
	l := &launch.Launcher{Cfg: cfg, Kit: "/kit"}
	built, err := l.Build(port.Build{Agent: "pj-l-01", Kind: m.Kind, Args: m.Args, Role: "worker", Effort: "high",
		Dir: lane, Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--auto", "--standalone", "--prompt", "go"}; !slices.Equal(built.Args, want) {
		t.Errorf("argv %q, want %q", built.Args, want)
	}
	conf := filepath.Join(cfg.StateDir(), kind.ConfigFile)
	if len(built.Vars) != 2 || built.Vars[kind.ModelVar] != "go/ds#high" || built.Vars[kind.ConfigVar] != conf {
		t.Errorf("env %v, want %s=go/ds#high and %s=%s", built.Vars, kind.ModelVar, kind.ConfigVar, conf)
	}
	if got, err := os.ReadFile(conf); err != nil || !bytes.Equal(got, kind.Config("/kit")) {
		t.Errorf("the generated config %q (%v), want the kit's:\n%s", got, err, kind.Config("/kit"))
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
		Settings  map[string]any `json:"settings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(calls[0], "start ")), &req); err != nil {
		t.Fatal(err)
	}
	if req.Version != 1 || req.Call != "start" || req.Kit != "/kit" || !slices.Equal(req.Args, m.Args) || req.Effort != "high" ||
		req.Prompt != "go" || req.Dir != lane || !req.StrictMCP || req.Settings == nil {
		t.Errorf("start request %+v: want the complete start (kit, the model's args %q, effort, prompt, the lane, strict, settings)", req, m.Args)
	}
}
