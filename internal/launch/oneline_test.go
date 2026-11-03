package launch_test

import (
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// T-444: a fresh start's first prompt of many lines reaches herdr as one line (each run of newlines and \r one space),
// for every kind, the text otherwise unchanged: herdr refuses an agent.start argument with a newline
// ("agent arguments cannot be encoded safely for the target shell"; the fake refuses it too), so a typed job's
// multi-line text would leave the pane at a shell.
func TestStartPromptIsOneLine(t *testing.T) {
	t.Parallel()
	const prompt = "You are a worker.\n\nRead AGENTS.md.\r\n\r\nGo.\nMore."
	const one = "You are a worker. Read AGENTS.md. Go. More."
	const models = `{"oc":{"kind":"opencode","model":""}}`
	settings := " --settings " + config.Compact(map[string]any{"remoteControlAtStartup": false, "autoMemoryEnabled": false})
	for _, c := range []struct {
		name, typ, kind string
		head            string // the start args before the prompt
	}{
		{"claude", "opus", "claude", "-- --permission-mode auto --model opus" + settings + " "},
		{"codex", "luna", "codex", "-- -s workspace-write -a never -m gpt-6-luna " + codexUpdate + " " + codexMCP + " "},
		{"opencode", "oc", "opencode", "-- --auto --standalone --prompt "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("models", models), testkit.Set("roles", `{"reviewer":"`+c.typ+`"}`))
			x.shell("w1:p7", "pj-rev", "")
			out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Prompt: prompt})
			if !ok {
				t.Fatalf("a multi-line first prompt failed the start: %s", out)
			}
			eq(t, last(out), "RESTARTED pj-rev (pane w1:p7, session new)", "result")
			args := strings.TrimPrefix(c.head, "-- ")
			eq(t, x.startCall(), "agent.start args=["+args+one+"] kind="+c.kind+" name=pj-rev pane_id=w1:p7", "agent start: the prompt one line")
			contains(t, x.startCall(), one, "the prompt's text, its line breaks one space each")
			eq(t, x.f.Pane("w1:p7").Name, "pj-rev", "herdr ran the agent")
		})
	}
}

// DD-v0.13 D1: a start of a kind obor has no bridge for is refused before herdr's agent start (dispatch and obor
// config validate refuse such a model; no degraded start with its args alone).
func TestStartWithoutBridgeRefused(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("models", `{"cur":{"kind":"cursor","model":"x"}}`),
		testkit.Set("roles", `{"reviewer":"cur"}`))
	x.shell("w1:p7", "pj-rev", "")
	out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Prompt: "go"})
	if ok {
		t.Fatalf("a start of kind cursor ran: %s", out)
	}
	contains(t, out, "pane w1:p7 (from label)", "precondition: the restart found the pane")
	eq(t, failLine(out), "FAIL pj-rev: kind cursor has no bridge (obor config validate)", "result")
	eq(t, x.startCall(), "", "no herdr agent start")
}

// T-444: a prompt with no line break is passed as it is (the change is only to line breaks).
func TestStartPromptWithoutLineBreaksUnchanged(t *testing.T) {
	t.Parallel()
	x := setup(t)
	x.shell("w1:p3", "pj-l-01", "")
	const prompt = "  a b\tc  `d` \"e\" $f & g;  "
	if out, ok := x.restart(launch.Request{Agent: "pj-l-01", Fresh: true, Prompt: prompt}); !ok {
		t.Fatalf("start failed: %s", out)
	}
	if got := x.startCall(); !strings.Contains(got, " "+prompt+"]") {
		t.Errorf("agent start %q does not end with the prompt as it is %q", got, prompt)
	}
}
