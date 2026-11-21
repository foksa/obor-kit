package launch_test

import (
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// an OpenCode start carries --auto --standalone once, whatever the terminal: the kind's start adds them (obor's
// model table holds no OpenCode flag), before kinds.opencode.args, with the model id left to the environment and
// --prompt last. A kinds.opencode.args that repeats a fixed flag does not double it.
func TestOpenCodeStartCarriesItsFixedFlags(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"herdr", "hc"} {
		for _, extra := range []string{`["--log-level","DEBUG"]`, `["--standalone","--log-level","DEBUG"]`} {
			t.Run(terminal+" "+extra, func(t *testing.T) {
				t.Parallel()
				x := setup(t, testkit.Set("project.terminal", terminal),
					testkit.AddModels(`{"oc":{"kind":"opencode","id":"go/ds"}}`),
					testkit.Set("agents.kinds", `{"opencode":{"args":`+extra+`}}`),
					testkit.Set("agents.roles", `{"reviewer":"oc"}`))
				x.shell("w1:p7", "pj-rev", "")
				out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Prompt: "go"})
				if !ok {
					t.Fatalf("the start failed: %s", out)
				}
				call := x.startCall()
				const want = "Start argv=[--auto --standalone --log-level DEBUG --prompt go] kind=opencode"
				if !strings.Contains(x.named(call), want) {
					t.Errorf("start call %q, want it to hold %q", x.named(call), want)
				}
				for _, flag := range []string{"--auto", "--standalone"} {
					n := 0
					for _, w := range strings.Fields(call) { // argv=[--auto … : the first word opens the list
						if strings.TrimPrefix(w, "argv=[") == flag {
							n++
						}
					}
					if n != 1 {
						t.Errorf("start call %q holds %s %d times, want once", call, flag, n)
					}
				}
				if strings.Contains(call, "--model") {
					t.Errorf("start call %q holds --model: the model id rides in the environment", call)
				}
			})
		}
	}
}
