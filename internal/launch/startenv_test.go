package launch_test

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// T-812: every start path types its env line through the one builder (agents.StartVars): run in a real sh after a
// shell that exports a stale value of every registry var (another role, another lane, another kind's model and config,
// the retired OBOR_AGENT), the line leaves the new start's values and none of the stale ones. Paths: a restart, a
// dispatch's type (OpenCode: its model and config), a start in another pane (a move), and setup's reused pane
// (SetEnv). A restore runs obor restart (TestRestoreWorkerWithoutSessionFresh): the restart path.
func TestEveryStartPathTypesTheRegistryEnv(t *testing.T) {
	t.Parallel()
	const stale = "export OBOR_ROLE=lead OBOR_LANE=L-09 OBOR_AGENT=old OBOR_OPENCODE_MODEL=old/m OPENCODE_CONFIG=/old.json"
	const show = `echo "${OBOR_ROLE-UNSET} ${OBOR_LANE-UNSET} ${OBOR_AGENT-UNSET} ${OBOR_OPENCODE_MODEL-UNSET} ${OPENCODE_CONFIG-UNSET}"`
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
			x := setup(t, testkit.Set("models", `{"oc":{"kind":"opencode","model":"go/ds"}}`))
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
			want := strings.Replace(c.want, "CONFIG", x.p.Root+"/.obor/state/opencode.json", 1)
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("after a stale shell, the env line %q leaves %q, want %q", line, got, want)
			}
		})
	}
}
