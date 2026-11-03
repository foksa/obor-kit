package guard

import (
	"testing"
	"time"
)

// T-581: Turn and StopFailure name the lane's agent when OBOR_AGENT is unset (an OpenCode plugin's shared process
// carries no env identity), for every kind; OBOR_AGENT still wins (the Claude and Codex controls); a cwd that is no
// lane names no agent (nothing is sent).
func TestTurnAndStopFailureResolveTheLaneAgent(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"claude", "codex", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, o1 := sessionFx(t, true)
			f.runs(map[string]string{"claude": "opus", "codex": "luna", "opencode": "deepseek"}[kind])
			turnEvent := []byte(`{"hook_event_name": "UserPromptSubmit", "cwd": "` + o1 + `"}`)
			failureEvent := []byte(`{"hook_event_name": "StopFailure", "cwd": "` + o1 + `", "error": "rate_limit", "error_details": "429"}`)

			turn, ok := Turn(turnEvent, f.Env())
			if !ok || turn.Agent != "pj-l-01" || turn.Role != "worker" || turn.Lane != "L-01" || turn.Root != f.Root {
				t.Errorf("Turn from the lane: %+v, %v; want the lane's agent", turn, ok)
			}
			stop, ok, err := StopFailure(failureEvent, f.Env(), time.Now())
			if err != nil || !ok || stop.Agent != "pj-l-01" || stop.Role != "worker" || stop.Lane != "L-01" ||
				stop.Error != "rate_limit" || stop.Details != "429" {
				t.Errorf("StopFailure from the lane: %+v, %v, %v; want the lane's agent", stop, ok, err)
			}

			// The environment still names the session: unchanged for Claude and Codex.
			env := f.Env("OBOR_AGENT=pj-other")
			if turn, _ := Turn(turnEvent, env); turn.Agent != "pj-other" {
				t.Errorf("Turn with OBOR_AGENT: agent %q, want pj-other", turn.Agent)
			}
			if stop, _, _ := StopFailure(failureEvent, env, time.Now()); stop.Agent != "pj-other" {
				t.Errorf("StopFailure with OBOR_AGENT: agent %q, want pj-other", stop.Agent)
			}

			// The main checkout is no lane: no agent to name.
			outside := []byte(`{"hook_event_name": "UserPromptSubmit", "cwd": "` + f.Root + `"}`)
			if turn, ok := Turn(outside, f.Env()); ok || turn.Agent != "" {
				t.Errorf("Turn outside a lane: %+v, %v; want no agent", turn, ok)
			}
			outsideFailure := []byte(`{"hook_event_name": "StopFailure", "cwd": "` + f.Root + `", "error": "rate_limit"}`)
			if stop, _, _ := StopFailure(outsideFailure, f.Env(), time.Now()); stop.Agent != "" {
				t.Errorf("StopFailure outside a lane: agent %q, want none", stop.Agent)
			}
		})
	}
}
