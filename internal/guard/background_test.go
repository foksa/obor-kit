package guard

import (
	"encoding/json"
	"testing"

	"obor/internal/project"
)

// bgInput is a Bash call's PreToolUse event in cwd: its command, description and whether it runs in the background.
func bgInput(cwd, cmd, desc string, background bool) []byte {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": cwd,
		"session_id": "s-1", "tool_input": map[string]any{"command": cmd, "description": desc,
			"run_in_background": background}})
	return b
}

// T-714: an allowed Bash call run in the background is recorded with its description, command, agent, pane and
// session; a foreground call and a denied background call are not.
func TestBackgroundBashCallRecorded(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	if calls := project.BackgroundCalls(f.P); len(calls) != 0 {
		t.Fatalf("precondition: no calls recorded yet, got %+v", calls)
	}
	if code, reason := runAs(f.Project, "worker w1", bgInput(f.L, "sleep 600", "wait", false), "HERDR_PANE_ID=p-9"); code != 0 {
		t.Fatalf("foreground call denied: %s", reason)
	}
	if code, _ := runAs(f.Project, "worker w1", bgInput(f.L, "pkill -f x", "kill", true), "HERDR_PANE_ID=p-9"); code != 2 {
		t.Fatalf("precondition: pkill in the background is denied, got %d", code)
	}
	if calls := project.BackgroundCalls(f.P); len(calls) != 0 {
		t.Fatalf("a foreground or denied call was recorded: %+v", calls)
	}
	if code, reason := runAs(f.Project, "worker w1", bgInput(f.L, "python convert.py --all", "convert maps", true),
		"HERDR_PANE_ID=p-9"); code != 0 {
		t.Fatalf("background call denied: %s", reason)
	}
	calls := project.BackgroundCalls(f.P)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	c := calls[0]
	if c.Command != "python convert.py --all" || c.Description != "convert maps" || c.Agent != "pj-l-01" || c.Pane != "p-9" ||
		c.Session != "s-1" || c.At == 0 {
		t.Errorf("call = %+v", c)
	}
}
