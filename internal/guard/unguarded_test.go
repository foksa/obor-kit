package guard

import (
	"testing"
	"time"

	"obor/internal/project"
)

// A call a kind's transport cannot hand the guard (tool_name "unguarded") is denied with the transport's reason for
// every session but a role-less one in the main checkout (the owner's own agent), which has no opinion; a reason the
// transport left empty is denied naming the tool. In a stopped project the guard never loads: no opinion for anyone.
func TestUnguardedDeniedButTheOwners(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	const why = "obor cannot guard a workflow script; run commands with run_command"
	check(t, f.Project, []tc{
		unguardedCall("worker", L, why, why),
		unguardedCall("-", L, why, why), // a lane cwd makes a worker
		unguardedCall("reviewer", P, why, why),
		unguardedCall("lead", P, why, why),
		unguardedCall("integrator", P, why, why),
		unguardedCall("-", P, why, ""), // the owner's own session
		unguardedCall("worker", L, "", "tool run_workflow is one obor cannot guard, and its transport gives no reason"),
	})
	if code, reason := runAs(f.Project, "worker", unguardedCall("worker", L, why, "").input(nil)); code != 2 || reason != why {
		t.Errorf("a worker's unguarded call: %d %q, want a deny with exactly the transport's reason", code, reason)
	}

	if err := project.WriteStopMarker(f.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
		t.Fatal(err)
	}
	check(t, f.Project, []tc{unguardedCall("worker", L, why, ""), unguardedCall("lead", P, why, "")})
}

// A write naming no file and a Bash call with no command are denied for every session, the owner's own too, naming
// the missing argument; the same call with its argument is allowed (the precondition: the deny is the empty one's).
func TestEmptyArgumentsDenied(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	check(t, f.Project, []tc{
		edit("-", P, "Write", P+"/notes.md", ""),
		edit("lead", P, "Edit", P+"/notes.md", ""),
		bash("-", P, "ls", ""),
		bash("lead", P, "ls", ""),
	})
	for _, who := range []string{"-", "lead", "worker", "reviewer"} {
		cwd := P
		if who == "worker" {
			cwd = L
		}
		check(t, f.Project, []tc{
			edit(who, cwd, "Write", "", "a Write call naming no file (tool_input.file_path is empty)"),
			edit(who, cwd, "Edit", "", "an Edit call naming no file (tool_input.file_path is empty)"),
			edit(who, cwd, "MultiEdit", "", "(tool_input.file_path is empty)"),
			edit(who, cwd, "NotebookEdit", "", "a NotebookEdit call naming no file (tool_input.notebook_path is empty)"),
			bash(who, cwd, "", "a Bash call with no command (tool_input.command is empty)"),
			bash(who, cwd, "  \n", "a Bash call with no command (tool_input.command is empty)"),
		})
	}
}
