package guard

import (
	"encoding/json"
	"testing"
	"time"

	"obor/internal/project"
)

// in a stopped project (obor stop) every hook stands down: the PreToolUse guard allows what it denies in a
// running project, the Stop hook lets a worker without its task_done stop, the wrong-tab guard and SessionStart add
// nothing, and Turn/StopFailure tell serve nothing. Each is checked denying/acting first (the precondition).
func TestHooksStandDownInAStoppedProject(t *testing.T) {
	t.Parallel()
	f := newHfx(t)
	o1 := f.PlainLane("L-01", "task/T-05")
	f.Config()
	f.state(ago(100))
	f.Dispatched("L-01")
	f.State("L-01", `{"lane":"L-01","task":"T-05","branch":"task/T-05","base":"main","agent":"pj-l1"}`)

	commit := tc{"worker pj-l1", o1, "Bash", "git commit -m x", ""}.input(nil)
	turn, _ := json.Marshal(map[string]any{"cwd": o1})
	failure, _ := json.Marshal(map[string]any{"cwd": o1, "error": "rate_limit"})
	acts := func() (deny bool, stop, prompt, session string, turned, failed bool) {
		code, _ := Run(commit, f.Env("OBOR_ROLE=worker", "OBOR_AGENT=pj-l1"))
		r := f.stop(t, "-", o1, "")
		_, turned = Turn(turn, f.Env("OBOR_AGENT=pj-l1"))
		_, failed, _ = StopFailure(failure, f.Env("OBOR_AGENT=pj-l1"), time.Now())
		return code == 2, r.out, f.prompt(t, o1, "hello", ""), f.session(t, o1, "s1"),
			turned, failed
	}
	deny, stop, prompt, session, turned, failed := acts()
	if !deny || stop == "" || prompt == "" || session == "" || !turned || !failed {
		t.Fatalf("precondition (running project): deny %v, stop %q, prompt %q, session %q, turn %v, stop-failure %v",
			deny, stop, prompt, session, turned, failed)
	}

	if err := project.WriteStopMarker(f.Root, project.StopMarker{At: time.Now(), Why: "test"}); err != nil {
		t.Fatal(err)
	}
	deny, stop, prompt, session, turned, failed = acts()
	if deny || stop != "" || prompt != "" || session != "" || turned || failed {
		t.Errorf("stopped project: deny %v, stop %q, prompt %q, session %q, turn %v, stop-failure %v; want all to stand down",
			deny, stop, prompt, session, turned, failed)
	}
}
