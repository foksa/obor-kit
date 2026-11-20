package launch_test

import (
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/testkit"
)

// a fresh start's first prompt of many lines reaches the terminal's start as one line (each run of newlines and \r
// one space), for every kind, the text otherwise unchanged: the start's argv is typed as one command line at the
// pane's shell (herdr refuses an argument with a newline), so a typed job's multi-line text would leave the pane at a
// shell. A prompt with no line break is passed as it is.
func TestStartPromptIsOneLine(t *testing.T) {
	t.Parallel()
	const prompt = "You are a worker.\n\nRead AGENTS.md.\r\n\r\nGo.\nMore."
	const one = "You are a worker. Read AGENTS.md. Go. More."
	const plain = "  a b\tc  `d` \"e\" $f & g;  " // no line break: unchanged
	const models = `{"oc":{"kind":"opencode","id":""}}`
	settings := " " + settings
	for _, c := range []struct {
		name, typ, kind string
		head            string // the start args before the prompt
		prompt, want    string // the first prompt given, and what reaches the start
	}{
		{"claude", "opus", "claude", "-- --permission-mode auto --model opus" + settings + " --session-id <new> ", prompt, one},
		// a Codex reviewer writes the main checkout's reviews dir (<reviews>) from its role worktree.
		{"codex", "luna", "codex", "-- -s workspace-write -a never --no-daemon -m gpt-6-luna --add-dir <reviews> " + codexUpdate + " " + codexMCP + " ", prompt, one},
		{"opencode", "oc", "opencode", "-- --auto --standalone --prompt ", prompt, one},
		{"no line break: unchanged", "opus", "claude", "-- --permission-mode auto --model opus" + settings + " --session-id <new> ", plain, plain},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t, testkit.Set("agents.models", models), testkit.Set("agents.roles", `{"reviewer":"`+c.typ+`"}`))
			x.shell("w1:p7", "pj-rev", "")
			out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Prompt: c.prompt})
			if !ok {
				t.Fatalf("the first prompt failed the start: %s", out)
			}
			eq(t, last(out), "RESTARTED pj-rev (pane w1:p7, session new)", "result")
			args := strings.ReplaceAll(strings.TrimPrefix(c.head, "-- "), "<reviews>", filepath.Join(x.p.Root, "docs", "reviews"))
			eq(t, x.named(x.startCall()), "Start argv=["+args+c.want+"] kind="+c.kind+" label=pj-rev pane=w1:p7", "agent start: the prompt")
			contains(t, x.startCall(), c.want, "the prompt's text")
			eq(t, x.f.At("w1:p7").Name, "pj-rev", "the terminal ran the agent")
		})
	}
}

// a start of a kind obor has no bridge for is refused before the terminal's agent start (dispatch and obor
// config validate refuse such a model; no degraded start with its args alone).
func TestStartWithoutBridgeRefused(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("agents.models", `{"cur":{"kind":"cursor","id":"x"}}`),
		testkit.Set("agents.roles", `{"reviewer":"cur"}`))
	x.shell("w1:p7", "pj-rev", "")
	out, ok := x.restart(launch.Request{Agent: "pj-rev", Fresh: true, Prompt: "go"})
	if ok {
		t.Fatalf("a start of kind cursor ran: %s", out)
	}
	contains(t, out, "pane w1:p7 (from label)", "precondition: the restart found the pane")
	eq(t, failLine(out), "FAIL pj-rev: kind cursor has no bridge (obor config validate)", "result")
	eq(t, x.startCall(), "", "no agent start")
}
