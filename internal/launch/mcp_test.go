package launch_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/agents/claude"
	"obor/internal/agents/codex"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// strictMCP is the --mcp-config pair obor adds: this kit's server, as the plugin manifest declares it with the kit's
// path spelled out and the pin.
const strictMCP = `--mcp-config {"mcpServers":{"obor":{"args":["mcp"],"command":"/kit/bin/obor","env":{"OBOR_NO_SELF_HOST":"1"}}}}`

// jsonStr is s as a JSON string (config.Model.Args holds the words as the config writes them).
func jsonStr(s string) string { return config.Compact(s) }

// MCPArgs is the pair a start adds, from the kind, the kit and the args alone: the --mcp-config value may be inline JSON
// or a file, in either spelling, and only an obor server in one of them counts as explicit.
func TestMCPArgs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(file, []byte(`{"mcpServers":{"obor":{"command":"`+filepath.Join(dir, "bin", "obor")+`"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const obor = `{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`
	want := []string{"--mcp-config", claude.MCPConfig("/kit")}
	for _, c := range []struct {
		name, kind, kit string
		args            []string
		want            []string
	}{
		{"strict Claude", "claude", "/kit", []string{"--model", "opus", "--strict-mcp-config"}, want},
		{"no strict flag", "claude", "/kit", []string{"--model", "opus"}, nil},
		{"strict, an inline obor entry", "claude", "/kit", []string{"--strict-mcp-config", "--mcp-config", obor}, nil},
		{"strict, an inline obor entry after =", "claude", "/kit", []string{"--strict-mcp-config", "--mcp-config=" + obor}, nil},
		{"strict, a file naming obor", "claude", "/kit", []string{"--strict-mcp-config", "--mcp-config", file}, nil},
		{"strict, a file naming no obor server", "claude", "/kit",
			[]string{"--strict-mcp-config", "--mcp-config", filepath.Join(dir, "nope.json")}, want},
		{"strict codex: its own registration", "codex", "/kit", []string{"--strict-mcp-config"}, nil},
		{"strict, no kit to point at", "claude", "", []string{"--strict-mcp-config"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := launch.MCPArgs(c.kind, c.kit, c.args)
			if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("MCPArgs(%s, %q, %q) = %q, want %q", c.kind, c.kit, c.args, got, c.want)
			}
		})
	}
	if !strings.Contains(claude.MCPConfig("/kit"), `"command":"/kit/bin/obor"`) {
		t.Errorf("MCPConfig names the kit's bin/obor: %s", claude.MCPConfig("/kit"))
	}
}

// TestRV47ExplicitVariadicConfig (RV-47 #3): --mcp-config takes several values, so an explicit obor entry in any of them
// (a later inline value, a later file value, after the = spelling, in a repeated flag) is the type's own obor server:
// obor adds none. A strict start whose configs name no obor server still gets its pair.
func TestRV47ExplicitVariadicConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oborFile := filepath.Join(dir, "obor.json")
	otherFile := filepath.Join(dir, "other.json")
	if err := os.WriteFile(oborFile, []byte(`{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherFile, []byte(`{"mcpServers":{"other":{"command":"/bin/other"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const obor = `{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`
	const other = `{"mcpServers":{"other":{"command":"/bin/other"}}}`
	const strict = "--strict-mcp-config"
	want := []string{"--mcp-config", claude.MCPConfig("/kit")}
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"obor in a later inline value", []string{"--mcp-config", other, obor, strict}, nil},
		{"obor in a later file value", []string{"--mcp-config", otherFile, oborFile, strict}, nil},
		{"obor in a later value after =", []string{"--mcp-config=" + other, obor, strict}, nil},
		{"obor in a repeated flag", []string{"--mcp-config", other, "--mcp-config", obor, strict}, nil},
		{"obor in a repeated flag, after =", []string{"--mcp-config=" + other, "--mcp-config=" + obor, strict}, nil},
		{"no obor in any value", []string{"--mcp-config", other, otherFile, strict}, want},
		{"no obor in a repeated flag", []string{"--mcp-config", other, "--mcp-config", otherFile, strict}, want},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := launch.MCPArgs("claude", "/kit", c.args)
			if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("MCPArgs(%q) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

// RV-47 #3 at the start: the strict start's argv keeps the type's own configs (obor's later value) and adds no second obor
// entry; the prompt stays the first prompt after the flag.
func TestRV47StartVariadicConfig(t *testing.T) {
	t.Parallel()
	const oborEntry = `{"mcpServers":{"obor":{"command":"/other/bin/obor","args":["mcp"]}}}`
	const otherEntry = `{"mcpServers":{"other":{"command":"/bin/other"}}}`
	const wake = "[obor job T-1/1/work] call the obor job_next tool"
	kindArgs := `["--strict-mcp-config","--mcp-config",` + jsonStr(otherEntry) + `,` + jsonStr(oborEntry) + `]`
	x := setup(t, testkit.Set("agents.kinds.claude.args", kindArgs), testkit.Set("agents.roles", `{"reviewer":"opus"}`))
	x.shell("w1:p7", "pj-rev", "")
	out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Prompt: wake})
	if !ok {
		t.Fatalf("start failed:\n%s", out)
	}
	want := "--permission-mode auto --model opus --strict-mcp-config --mcp-config " + otherEntry + " " + oborEntry + " " + settings + " --session-id <new> -- " + wake
	eq(t, x.named(x.startCall()), "agent.start args=["+want+"] kind=claude name=pj-rev pane_id=w1:p7", "agent start")
	if strings.Contains(x.startCall(), claude.MCPConfig("/kit")) {
		t.Errorf("obor added a second obor entry: %q", x.startCall())
	}
}

// TestStartArgsTable: the start argv for every shape of type args (T-451; RV-47 #3, RV-47-verify N1, RV-47-verify-2 N1).
// Each row is the args as the lane type and obor's settings leave them, the kind, the prompt, and the argv; the prompt is
// positional, never a value of --mcp-config, and `--` appears at most once.
func TestStartArgsTable(t *testing.T) {
	t.Parallel()
	const obor = `{"mcpServers":{"obor":{"command":"/x/bin/obor"}}}`
	const other = `{"mcpServers":{"other":{"command":"/bin/other"}}}`
	const prompt = "review the rollout"
	dir := t.TempDir()
	oborFile, otherFile := filepath.Join(dir, "obor.json"), filepath.Join(dir, "other.json")
	for f, body := range map[string]string{oborFile: obor, otherFile: other} {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ours := claude.MCPConfig("/kit")
	const strict, cfg = "--strict-mcp-config", "--mcp-config"
	// codexReg is a Codex start's argv before its prompt (T-375: the MCP registration is on every start, prompted or not).
	codexReg := []string{"-a", "never",
		"-c", `mcp_servers.obor.command="/kit/bin/obor"`,
		"-c", `mcp_servers.obor.args=["mcp"]`,
		"-c", "mcp_servers.obor.env_vars=" + codex.EnvVarsTOML(),
		"-c", `mcp_servers.obor.env={OBOR_NO_SELF_HOST="1"}`,
		"-c", `mcp_servers.obor.default_tools_approval_mode="approve"`,
		"-c", "check_for_update_on_startup=false"}
	for _, c := range []struct {
		name, kind string
		args       []string
		prompt     string
		want       []string
	}{
		{"non-strict Claude, prompt", "claude", []string{"--model", "opus"}, prompt,
			[]string{"--model", "opus", prompt}},
		{"non-strict Claude with a config, prompt", "claude", []string{cfg, other}, prompt,
			[]string{cfg, other, "--", prompt}},
		{"strict, no obor config, prompt", "claude", []string{strict}, prompt,
			[]string{cfg, ours, strict, "--", prompt}},
		{"strict, obor config first value", "claude", []string{strict, cfg, obor}, prompt,
			[]string{strict, cfg, obor, "--", prompt}},
		{"strict, obor in a later inline value", "claude", []string{cfg, other, obor, strict}, prompt,
			[]string{cfg, other, obor, strict, "--", prompt}},
		{"strict, obor in a later file value", "claude", []string{cfg, otherFile, oborFile, strict}, prompt,
			[]string{cfg, otherFile, oborFile, strict, "--", prompt}},
		{"strict, obor after the = spelling", "claude", []string{cfg + "=" + other, obor, strict}, prompt,
			[]string{cfg + "=" + other, obor, strict, "--", prompt}},
		{"strict, obor in a repeated flag", "claude", []string{cfg, other, cfg, obor, strict}, prompt,
			[]string{cfg, other, cfg, obor, strict, "--", prompt}},
		{"strict, trailing unrelated config, prompt", "claude", []string{strict, cfg, other}, prompt,
			[]string{cfg, ours, strict, cfg, other, "--", prompt}},
		{"resume: no -- and nothing positional added", "claude", []string{strict, cfg, obor}, "",
			[]string{strict, cfg, obor}},
		{"resume, obor added: no --", "claude", []string{strict}, "",
			[]string{cfg, ours, strict}},
		{"lead, default settings absent (a config trailing): prompt positional", "claude", []string{strict, cfg, other}, prompt,
			[]string{cfg, ours, strict, cfg, other, "--", prompt}},
		{"worker with obor's --settings after the configs: prompt positional", "claude",
			[]string{strict, cfg, obor, "--settings", `{"autoMemoryEnabled":false,"remoteControlAtStartup":false}`}, prompt,
			[]string{strict, cfg, obor, "--settings", `{"autoMemoryEnabled":false,"remoteControlAtStartup":false}`, "--", prompt}},
		{"a multi-line prompt is one line", "claude", []string{"--model", "opus"}, "a\nb",
			[]string{"--model", "opus", "a b"}},
		{"Codex: its own registration, prompt last", "codex", []string{"-a", "never"}, prompt, slices.Concat(codexReg, []string{prompt})},
		{"Codex, no prompt: still registered", "codex", []string{"-a", "never"}, "", codexReg},
		{"OpenCode: --prompt, no registration", "opencode", []string{"--auto"}, prompt,
			[]string{"--auto", "--prompt", prompt}},
		{"OpenCode resume", "opencode", []string{"--auto"}, "", []string{"--auto"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := append([]string(nil), c.args...)
			got := launch.StartArgs(c.kind, "/kit", c.args, c.prompt)
			if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
				t.Errorf("startArgs(%s, %q, %q) =\n %q\nwant\n %q", c.kind, c.args, c.prompt, got, c.want)
			}
			if strings.Join(c.args, "\x00") != strings.Join(in, "\x00") {
				t.Errorf("startArgs changed its args: %q", c.args)
			}
			dashes := 0
			for _, a := range got {
				if a == "--" {
					dashes++
				}
			}
			if dashes > 1 {
				t.Errorf("%d `--` in %q", dashes, got)
			}
			if c.kind == "claude" && c.prompt != "" && got[len(got)-1] != strings.ReplaceAll(c.prompt, "\n", " ") {
				t.Errorf("the prompt is not last: %q", got)
			}
		})
	}
}

// T-1033: a lane worker's start reads the project's committed CLI config in its lane worktree (its cwd), in both
// passes of the argv (LaunchArgs, then the kind's start with the kit): Claude with an MCP opt-in starts without the
// strict flag and obor's --mcp-config, --settings keeping the key the file does not set; Codex with a top-level
// sandbox_mode and update check starts without -s workspace-write and obor's update-check option.
func TestStartReadsTheLaneProjectConfig(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, typ, rel, file string
		has, lacks           []string
	}{
		{"claude", "", ".claude/settings.json", `{"enabledMcpjsonServers":["mine"],"autoMemoryEnabled":true}`,
			[]string{`--settings {"remoteControlAtStartup":false}`, "kind=claude"},
			[]string{"--strict-mcp-config", "--mcp-config", "autoMemoryEnabled"}},
		{"codex", "luna", ".codex/config.toml", "sandbox_mode = \"read-only\"\ncheck_for_update_on_startup = true\n",
			[]string{"kind=codex", "-a never"}, []string{"workspace-write", "check_for_update_on_startup"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			start := func(file string) string {
				x := setup(t)
				if file != "" {
					path := filepath.Join(x.lane, c.rel)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				x.shell("w1:p3", "pj-l-01", "")
				if out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Type: c.typ}); !ok {
					t.Fatal(out)
				}
				return x.startCall()
			}
			before := start("") // precondition: without the file, the start carries obor's flags
			for _, l := range c.lacks {
				if !strings.Contains(before, l) {
					t.Fatalf("precondition: no project file, agent.start %q lacks %q", before, l)
				}
			}
			got := start(c.file)
			for _, h := range c.has {
				if !strings.Contains(got, h) {
					t.Errorf("agent.start %q lacks %q", got, h)
				}
			}
			for _, l := range c.lacks {
				if strings.Contains(got, l) {
					t.Errorf("agent.start %q has %q", got, l)
				}
			}
		})
	}
}
