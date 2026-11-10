package launch_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// every start path types its env line through the one builder (agents.StartVars): run in a real sh after a
// shell that exports a stale value of every registry var (another role, another lane, another kind's model and config,
// the retired OBOR_AGENT, another pane's OBOR_PANE), the line leaves the new start's values and none of the stale ones:
// OBOR_PANE is the pane the start runs in. A user's own
// XDG_STATE_HOME and OPENCODE_CONFIG_CONTENT are none of obor's (obor keeps no per-agent state dirs) and stay as
// they were. Paths: a restart, a dispatch's type (OpenCode: its model and config), a start in another pane (a move),
// and setup's reused pane (SetEnv). A restore runs obor restart (TestRestoreWorkerWithoutSessionFresh): the restart path.
func TestEveryStartPathTypesTheRegistryEnv(t *testing.T) {
	t.Parallel()
	const stale = "export OBOR_ROLE=lead OBOR_LANE=L-09 OBOR_AGENT=old OBOR_OPENCODE_MODEL=old/m OPENCODE_CONFIG=/old.json" +
		" OBOR_PANE=w9:old XDG_STATE_HOME=/mine OPENCODE_CONFIG_CONTENT=c"
	const show = `echo "${OBOR_ROLE-UNSET} ${OBOR_LANE-UNSET} ${OBOR_AGENT-UNSET} ${OBOR_OPENCODE_MODEL-UNSET} ${OPENCODE_CONFIG-UNSET}"` +
		` "${OBOR_PANE-UNSET}" "${XDG_STATE_HOME-UNSET} ${OPENCODE_CONFIG_CONTENT-UNSET}"`
	const user = " /mine c" // the user's own, untouched by every row
	for _, c := range []struct {
		name string
		pane string
		run  func(x *fx) (string, bool)
		want string // show's line; CONFIG: the generated config's path
	}{
		{"restart: a lane's worker", "w1:p3", func(x *fx) (string, bool) {
			return x.restart(launch.Request{Agent: "pj-l-01", Fresh: true})
		}, "worker L-01 UNSET UNSET UNSET"},
		{"dispatch: the lane's worker on an OpenCode type", "w1:p3", func(x *fx) (string, bool) {
			return x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Type: "oc", Effort: "high"})
		}, "worker L-01 UNSET go/ds#high CONFIG"},
		{"move: a role agent started in another pane", "w1:p4", func(x *fx) (string, bool) {
			return x.restart(launch.Request{Agent: "pj-int", Pane: "w1:p4", Fresh: true})
		}, "integrator UNSET UNSET UNSET UNSET"},
		{"setup: a reused pane (SetEnv)", "w1:p5", func(x *fx) (string, bool) {
			err := x.launcher(io.Discard).SetEnv(context.Background(), "w1:p5",
				launch.AgentEnv{Agent: "pj-rev", Role: "reviewer", Cwd: x.p.Root})
			return "", err == nil
		}, "reviewer UNSET UNSET UNSET UNSET"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.models", `{"oc":{"kind":"opencode","id":"go/ds"}}`))
			x.shell("w1:p3", "pj-l-01", "")
			x.shell("w1:p4", "", "")
			x.shell("w1:p5", "", "")
			if out, ok := c.run(x); !ok {
				t.Fatal(out)
			}
			runs := x.calls("pane.send_input pane_id=" + c.pane)
			if len(runs) == 0 {
				t.Fatalf("no env line typed at %s", c.pane)
			}
			_, text, _ := strings.Cut(runs[len(runs)-1], " text=")
			m := envMarksRE.FindStringIndex(text)
			if m == nil {
				t.Fatalf("no marks at the end of %q", text)
			}
			_, line, ok := strings.Cut(text[:m[0]], " && unset ") // not the cd: the fixture's worktree is no directory
			if !ok || strings.Contains(line, "&& unset") {
				t.Fatalf("the env line %q: want one unset after the cd", text)
			}
			out, err := exec.Command("sh", "-c", stale+"\nunset "+line+"\n"+show).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			want := strings.Replace(c.want, "CONFIG", x.p.Root+"/.obor/state/opencode.json", 1) + " " + c.pane + user
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("after a stale shell, the env line %q leaves %q, want %q", line, got, want)
			}
		})
	}
}

// an OpenCode start, a lane's worker and a role agent alike, names its generated config in the env line
// (OPENCODE_CONFIG=<project>/.obor/state/opencode.json, a path: the line stays short): the file that line names holds
// obor's MCP server (the launching kit's `bin/obor mcp`) and plugin (the kit's package dir). Another kind's start
// names none.
func TestOpencodeStartNamesItsGeneratedConfig(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("agents.models", `{"oc":{"kind":"opencode","id":"go/ds"}}`),
		testkit.Set("agents.roles", `{"reviewer":"oc","integrator":"opus"}`))
	file := filepath.Join(x.p.Root, ".obor", "state", "opencode.json")
	if _, err := os.Stat(file); err == nil {
		t.Fatal("precondition: no generated config yet")
	}
	x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"oc"}`)
	x.shell("w1:p3", "pj-l-01", "")
	x.agent("w1:p7", "pj-rev", "opencode", "idle")
	x.agent("w1:p8", "pj-int", "claude", "idle")
	// lineOf runs req's restart and reads the env line it typed: the line, and the config path it names ("" when none).
	lineOf := func(req launch.Request) (string, string) {
		before := len(x.calls("pane.send_input"))
		if out, ok := x.restart(req); !ok {
			t.Fatal(out)
		}
		for _, c := range x.calls("pane.send_input")[before:] {
			_, line, _ := strings.Cut(c, " text=")
			if !strings.Contains(line, "export ") {
				continue
			}
			_, rest, ok := strings.Cut(line, "OPENCODE_CONFIG=")
			if !ok {
				return line, ""
			}
			config, _, _ := strings.Cut(rest, " ")
			return line, strings.Trim(config, "'")
		}
		t.Fatalf("%s: no env line typed", req.Agent)
		return "", ""
	}
	for _, req := range []launch.Request{{Agent: "pj-l-01", Pane: "w1:p3", Fresh: true}, {Agent: "pj-rev", Fresh: true}} {
		if line, config := lineOf(req); config != file {
			t.Errorf("%s: the env line %q names config %q, want %s", req.Agent, line, config, file)
		}
	}
	if line, config := lineOf(launch.Request{Agent: "pj-int", Fresh: true}); config != "" {
		t.Errorf("a claude start names OPENCODE_CONFIG: %q", line)
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
