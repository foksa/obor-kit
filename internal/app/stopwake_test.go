package app_test

import (
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
)

// continueLine is the continue wake T-1's stopped worker gets (T-568): its work job's tag, then the text.
const continueLine = "[obor job T-1/1/work] Your usage limit has reset: continue where your turn stopped (call the obor job_next tool for your job)."

// stillStopped is the alarm's addition to the stop's reason.
const stillStopped = "; still stopped 30m after the reset wake"

// limitStop is the fetch rig with T-1's worker working, then its turn ended on the API error errText with the details
// details gives at the rig's clock (the stopped call s1), then idle: the stop in force, its reason returned.
func limitStop(t *testing.T, errText string, details func(r *loopRig) string) (*loopRig, string) {
	return limitStopWith(t, errText, details, func(*loopRig) {})
}

// limitStopWith is limitStop with prep run on the rig before it starts.
func limitStopWith(t *testing.T, errText string, details func(r *loopRig) string, prep func(r *loopRig)) (*loopRig, string) {
	r := fetchRig(t)
	prep(r)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.workerIs("working")
	d := details(r)
	if _, err := r.line(wire.MethodStopped, "s1", wire.Call{Agent: "pj-o1", Role: "worker", Lane: "O-01",
		Error: errText, Details: d}); err != nil {
		t.Fatal(err)
	}
	reason := "pj-o1 stopped: " + errText + " (" + d + ")"
	if r.stopIn(reason) != 1 {
		t.Fatalf("precondition: the stop in force: %+v", r.app.Status().Project.Facts)
	}
	r.workerIs("idle")
	return r, reason
}

// stopIn is how many needs-you{reason} of T-1 are in force.
func (r *loopRig) stopIn(reason string) int {
	n := 0
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == "T-1" && f.Step == event.StepNeedsYou && f.Fact().Reason == reason {
			n++
		}
	}
	return n
}

// wakes is how many continue wakes pj-o1 got.
func (r *loopRig) wakes() int { return r.typedTo("pj-o1", continueLine) }

// alarms is how many notifications the owner got of T-1's alarm.
func (r *loopRig) alarms() int {
	n := 0
	for _, req := range r.srv.Requests() {
		var p struct{ Body string }
		_ = json.Unmarshal(req.Params, &p)
		if req.Method == "notification.show" && strings.HasPrefix(p.Body, "T-1 ") && strings.HasSuffix(p.Body, stillStopped) {
			n++
		}
	}
	return n
}

// resetIn is a Claude usage limit's details naming the reset d after the rig's clock.
func resetIn(d time.Duration) func(r *loopRig) string {
	return func(r *loopRig) string {
		return "Claude AI usage limit reached|" + strconv.FormatInt(r.clock.Now().Add(d).Unix(), 10)
	}
}

// T-568: a stop on a usage limit whose reset T is known (the hook's details) wakes the worker once at T+2m, never
// before, nor while its pane is blocked; a replay after T wakes it no second time; still stopped 30 min after the
// wake → the alarm: the needs-you's reason says so and the owner is notified, once.
func TestStopWakeAtReset(t *testing.T) {
	t.Parallel()
	r, reason := limitStop(t, "rate_limit", resetIn(time.Hour))
	r.later(time.Hour + time.Minute) // T+1m
	if n := r.wakes(); n != 0 {
		t.Fatalf("woken %d times before T+2m: %q", n, r.promptsTo("pj-o1"))
	}
	r.workerIs("blocked")
	r.later(2 * time.Minute) // T+3m, blocked
	if n := r.wakes(); n != 0 {
		t.Fatalf("woken %d times into a blocked pane", n)
	}
	r.workerIs("idle")
	r.waitFor("the wake", func() bool { return r.wakes() == 1 })
	r.later(time.Minute)
	r.stop() // a replay after T
	r.start()
	r.push()
	r.passes()
	if n := r.wakes(); n != 1 {
		t.Fatalf("woken %d times after a replay, want once", n)
	}
	if r.stopIn(reason+stillStopped) != 0 || r.alarms() != 0 {
		t.Fatalf("the alarm before 30 min after the wake: %+v", r.app.Status().Project.Facts)
	}
	r.later(30 * time.Minute)
	r.waitFor("the alarm", func() bool { return r.stopIn(reason+stillStopped) == 1 && r.alarms() == 1 })
	r.later(30 * time.Minute)
	r.later(30 * time.Minute)
	if n, m, w := r.count("0 fact needs-you{"+reason+stillStopped+"}"), r.alarms(), r.wakes(); n != 1 || m != 1 || w != 1 {
		t.Errorf("later: %d alarm facts, %d notifications, %d wakes; want 1, 1, 1", n, m, w)
	}
}

// T-568: serve down over the reset: a restart before T wakes the worker at T+2m; one after T+2m wakes it once at
// its start.
func TestStopWakeAcrossRestarts(t *testing.T) {
	t.Parallel()
	t.Run("restart before T", func(t *testing.T) {
		t.Parallel()
		r, _ := limitStop(t, "rate_limit", resetIn(time.Hour))
		r.stop()
		r.clock.Add(30 * time.Minute)
		r.start()
		r.workerIs("idle")
		r.later(time.Minute)
		if n := r.wakes(); n != 0 {
			t.Fatalf("woken %d times at T-29m", n)
		}
		r.later(31 * time.Minute) // T+2m
		r.waitFor("the wake at T+2m", func() bool { return r.wakes() == 1 })
	})
	t.Run("restart after T", func(t *testing.T) {
		t.Parallel()
		r, _ := limitStop(t, "rate_limit", resetIn(time.Hour))
		r.stop()
		r.clock.Add(3 * time.Hour)
		r.start()
		r.workerIs("idle")
		r.waitFor("the missed wake", func() bool { return r.wakes() == 1 })
		r.later(time.Minute)
		if n := r.wakes(); n != 1 {
			t.Errorf("woken %d times, want once", n)
		}
	})
}

// T-568: the woken worker's next turn ends its stop as before (its hook's turn request): no alarm, no more wakes.
func TestStopWakeNewTurnNoAlarm(t *testing.T) {
	t.Parallel()
	r, reason := limitStop(t, "rate_limit", resetIn(time.Hour))
	r.later(time.Hour + 2*time.Minute)
	r.waitFor("the wake", func() bool { return r.wakes() == 1 })
	if reply, err := r.turnCall(); err != nil || reply != "TURN pj-o1 (the stop of T-1 ended)" {
		t.Fatalf("turn: %q, %v", reply, err)
	}
	r.later(31 * time.Minute)
	r.later(31 * time.Minute)
	if r.stopIn(reason) != 0 || r.stopIn(reason+stillStopped) != 0 || r.alarms() != 0 || r.wakes() != 1 {
		t.Errorf("after the next turn: %+v, %d alarms, %d wakes; want no stop, no alarm, one wake",
			r.app.Status().Project.Facts, r.alarms(), r.wakes())
	}
}

// T-568: a stop that is no usage limit (billing_error) gets no wake: its needs-you stays as it is.
func TestStopWakeNotALimit(t *testing.T) {
	t.Parallel()
	r, reason := limitStop(t, "billing_error", func(*loopRig) string { return "credit balance too low" })
	for range 4 {
		r.later(31 * time.Minute)
	}
	if n := r.wakes(); n != 0 || r.stopIn(reason) != 1 || r.alarms() != 0 {
		t.Errorf("billing_error: %d wakes, %d stops, %d alarms; want 0, 1, 0", n, r.stopIn(reason), r.alarms())
	}
}

// T-568: a usage limit whose reset is unknown (429, no time) wakes the worker every 30 min from the stop, each once.
func TestStopWakeUnknownReset(t *testing.T) {
	t.Parallel()
	r, _ := limitStop(t, "rate_limit", func(*loopRig) string { return "429 Too Many Requests" })
	r.later(29 * time.Minute)
	if n := r.wakes(); n != 0 {
		t.Fatalf("woken %d times before 30 min", n)
	}
	for want := 1; want <= 3; want++ {
		r.later(time.Minute)
		r.waitFor("wake "+strconv.Itoa(want), func() bool { return r.wakes() == want })
		r.later(29 * time.Minute)
		if n := r.wakes(); n != want {
			t.Fatalf("woken %d times within the period, want %d", n, want)
		}
	}
}

// T-568: a Codex stop's reset is its rollout's full rate-limit window (resets_at): the stop fact carries it, and the
// worker is woken (typed) at that time + 2m, not before.
func TestStopWakeCodexRateLimits(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC) // the rig's clock starts at 12:00
	full := `{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":100.0,` +
		`"window_minutes":300,"resets_at":` + strconv.FormatInt(reset.Unix(), 10) + `},"secondary":null}}}` + "\n"
	r := codexStoppedWith(t, full+cdxLimited)
	var got int64
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepStopped {
			var d struct {
				NeedsYou event.FactData `json:"needs_you"`
			}
			_ = json.Unmarshal(rec.Data, &d)
			got = d.NeedsYou.Reset
		}
	}
	if got != reset.Unix() {
		t.Fatalf("the stop's reset %d, want %d", got, reset.Unix())
	}
	r.later(reset.Add(time.Minute).Sub(r.clock.Now())) // T+1m
	if n := r.wakes(); n != 0 {
		t.Fatalf("woken %d times before T+2m", n)
	}
	r.later(time.Minute)
	r.waitFor("the wake", func() bool { return r.wakes() == 1 })
}

// roleContinue is the continue line a stopped role agent with its brief open gets (T-569).
const roleContinue = "[obor] Your usage limit has reset: continue your brief where your turn stopped."

// finishedLines is how many finished lines for pj-rev the lead got.
func finishedLines(r *loopRig) int {
	n := 0
	for _, p := range r.inbox.posted() {
		if strings.HasPrefix(p, "pj-lead: [obor] pj-rev finished (briefed ") {
			n++
		}
	}
	return n
}

// notified is how many owner notifications ended with suffix.
func notified(r *loopRig, suffix string) int {
	n := 0
	for _, req := range r.srv.Requests() {
		var p struct{ Body string }
		_ = json.Unmarshal(req.Params, &p)
		if req.Method == "notification.show" && strings.HasSuffix(p.Body, suffix) {
			n++
		}
	}
	return n
}

// T-569: a Codex reviewer (no StopFailure hook) briefed, its rollout's turn ended on the usage limit (its full
// rate-limit window resetting at T): read when it goes idle, one fact stopped with the lead's line, its brief open (no
// finished line); woken (typed) once at T+2m, not before; a replay wakes it no second time; still stopped 30 min after
// the wake → the lead's line says so and the owner is notified, once; its next turn in the rollout ends the stop
// (fact turned) and the finished line follows when it is idle.
func TestRoleWakeCodexReviewer(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	reset := r.clock.Now().Add(time.Hour)
	rollout := filepath.Join(t.TempDir(), "rollout-2026-10-24T12-00-00-r1.jsonl")
	appendTo := func(s string) {
		f, err := os.OpenFile(rollout, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	appendTo(cdxStarted)
	r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Kind, p.Transcript = "codex", rollout })
	r.start()
	if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "review T-1"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the brief typed", func() bool { return r.typedTo("pj-rev", "[obor pj-lead] review T-1") == 1 })
	r.agentIs("w1:p6", "pj-rev", "working")
	r.passes()
	appendTo(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":100.0,` +
		`"window_minutes":300,"resets_at":` + strconv.FormatInt(reset.Unix(), 10) + `},"secondary":null}}}` + "\n" + cdxLimited)
	r.agentIs("w1:p6", "pj-rev", "idle")
	stopped := "[obor] pj-rev stopped: usage_limit_exceeded (" + cdxLimitMsg + ")"
	r.waitFor("the stopped line posted", func() bool { return r.inbox.count("pj-lead", stopped) == 1 })
	r.passes()
	if n := r.taskStep(event.MsgTask, "fact stopped"); n != 1 || finishedLines(r) != 0 {
		t.Fatalf("after the stop: %d stopped facts, %d finished lines; want 1, 0: %q", n, finishedLines(r), r.steps())
	}
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepStopped {
			var d struct {
				Brief, Reset int64
				Ends, Turn   string
			}
			_ = json.Unmarshal(rec.Data, &d)
			if d.Brief == 0 || d.Ends != "" || d.Reset != reset.Unix() || d.Turn != "t1" {
				t.Fatalf("the stop's data %s: want its brief open, the reset %d, turn t1", rec.Data, reset.Unix())
			}
		}
	}

	r.later(time.Hour + time.Minute) // T+1m
	if n := r.typedTo("pj-rev", roleContinue); n != 0 {
		t.Fatalf("woken %d times before T+2m", n)
	}
	r.later(time.Minute)
	r.waitFor("the wake", func() bool { return r.typedTo("pj-rev", roleContinue) == 1 })
	r.stop() // a replay after the wake
	r.start()
	r.push()
	r.passes()
	if n, m := r.typedTo("pj-rev", roleContinue), r.taskStep(event.MsgTask, "fact stopped"); n != 1 || m != 1 {
		t.Fatalf("after a replay: %d wakes, %d stopped facts; want 1, 1", n, m)
	}
	alarm := stopped + stillStopped
	if r.inbox.count("pj-lead", alarm) != 0 || notified(r, stillStopped) != 0 {
		t.Fatalf("the alarm before 30 min after the wake")
	}
	r.later(30 * time.Minute)
	r.waitFor("the alarm", func() bool { return r.inbox.count("pj-lead", alarm) == 1 && notified(r, stillStopped) == 1 })
	r.later(30 * time.Minute)
	r.stop()
	r.start()
	r.push()
	r.passes()
	if n, m, w := r.inbox.count("pj-lead", alarm), notified(r, stillStopped), r.typedTo("pj-rev", roleContinue); n != 1 || m != 1 || w != 1 {
		t.Fatalf("later: %d alarm lines, %d notifications, %d wakes; want 1, 1, 1", n, m, w)
	}

	appendTo(cdxStarted2)
	r.agentIs("w1:p6", "pj-rev", "working")
	r.waitFor("fact turned", func() bool { return r.taskStep(event.MsgTask, "fact turned") == 1 })
	if finishedLines(r) != 0 {
		t.Fatal("a finished line while the next turn runs")
	}
	appendTo(cdxDone2)
	r.agentIs("w1:p6", "pj-rev", "idle")
	r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
	r.passes()
	if n, m := r.taskStep(event.MsgTask, "fact stopped"), finishedLines(r); n != 1 || m != 1 {
		t.Errorf("after the next turn: %d stopped facts, %d finished lines; want 1, 1", n, m)
	}
}

// claudeRoleStop is the line rig with pj-rev (Claude) briefed when brief, worked and idle, then its StopFailure hook's
// request: errText with the details details gives at the rig's clock.
func claudeRoleStop(t *testing.T, brief bool, errText string, details func(r *loopRig) string) *loopRig {
	r := lineRig(t, "idle", "idle")
	r.start()
	if brief {
		if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "verify finding 3"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] verify finding 3") == 1 })
		r.agentIs("w1:p6", "pj-rev", "working")
		r.passes()
	}
	d := details(r)
	reply, err := r.line(wire.MethodStopped, "s1", wire.Call{Agent: "pj-rev", Role: "reviewer", Error: errText, Details: d})
	if err != nil || reply != "STOPPED pj-rev (the lead is told)" {
		t.Fatalf("stopped: %q, %v", reply, err)
	}
	r.agentIs("w1:p6", "pj-rev", "idle")
	stopped := "[obor] pj-rev stopped: " + errText + " (" + d + ")"
	r.waitFor("the stopped line posted", func() bool { return r.inbox.count("pj-lead", stopped) == 1 })
	return r
}

// T-569: a Claude role agent briefed, stopped on rate_limit with its reset T in the details: its brief stays open (no
// finished line), it is woken (posted) once at T+2m, not before; its next turn's hook ends the stop and its finished
// line follows the work. With no brief open: the stopped line only, never a wake.
func TestRoleWakeClaude(t *testing.T) {
	t.Parallel()
	t.Run("brief open", func(t *testing.T) {
		t.Parallel()
		r := claudeRoleStop(t, true, "rate_limit", resetIn(time.Hour))
		r.later(time.Hour + time.Minute)
		if n := r.inbox.count("pj-rev", roleContinue); n != 0 || finishedLines(r) != 0 {
			t.Fatalf("before T+2m: %d wakes, %d finished lines; want 0, 0", n, finishedLines(r))
		}
		r.later(time.Minute)
		r.waitFor("the wake", func() bool { return r.inbox.count("pj-rev", roleContinue) == 1 })
		reply, err := r.line(wire.MethodTurn, wire.NewCallID(), wire.Call{Agent: "pj-rev", Role: "reviewer"})
		if err != nil || reply != "TURN pj-rev (the stop of its brief ended)" {
			t.Fatalf("turn: %q, %v", reply, err)
		}
		r.passes()
		if finishedLines(r) != 0 {
			t.Fatal("a finished line before the next turn's work")
		}
		r.agentIs("w1:p6", "pj-rev", "working")
		r.agentIs("w1:p6", "pj-rev", "idle")
		r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
		r.later(31 * time.Minute)
		if n := r.inbox.count("pj-rev", roleContinue); n != 1 || notified(r, stillStopped) != 0 {
			t.Errorf("after the next turn: %d wakes, %d alarms; want 1, 0", n, notified(r, stillStopped))
		}
	})
	t.Run("no brief", func(t *testing.T) {
		t.Parallel()
		r := claudeRoleStop(t, false, "rate_limit", resetIn(time.Hour))
		for range 4 {
			r.later(31 * time.Minute)
		}
		if n := r.inbox.count("pj-rev", roleContinue); n != 0 {
			t.Errorf("woken %d times with no brief open", n)
		}
		if reply, err := r.line(wire.MethodTurn, wire.NewCallID(), wire.Call{Agent: "pj-rev", Role: "reviewer"}); err != nil ||
			reply != "TURN pj-rev (no stop)" {
			t.Errorf("turn: %q, %v", reply, err)
		}
	})
}

// T-569: a briefed role agent's stop that is no usage limit (billing_error) ends its brief as before: no wake, no
// finished line, no alarm.
func TestRoleWakeNotALimit(t *testing.T) {
	t.Parallel()
	r := claudeRoleStop(t, true, "billing_error", func(*loopRig) string { return "credit balance too low" })
	for range 4 {
		r.later(31 * time.Minute)
	}
	if n := r.inbox.count("pj-rev", roleContinue); n != 0 || finishedLines(r) != 0 || notified(r, stillStopped) != 0 {
		t.Errorf("billing_error: %d wakes, %d finished lines, %d alarms; want 0, 0, 0", n, finishedLines(r),
			notified(r, stillStopped))
	}
}

// RV-60 #2: a stopped worker's continue wake and its alarm do not wait for the main checkout: with it off the target
// branch, or its check failing, they happen as on the target branch. A Claude worker (the wake posted to its inbox)
// and a Codex worker (typed), each with the checkout held by a branch or by a failing check.
func TestStopWakeBehindCheckout(t *testing.T) {
	t.Parallel()
	type stopRig struct {
		*loopRig
		wakes  func() int
		reason string
	}
	// stopped is a rig with the worker stopped on a usage limit resetting in an hour (reset) or none known, the
	// main checkout failing with checkErr from then on.
	stopped := map[string]func(t *testing.T, reset bool, checkErr error) stopRig{
		"claude": func(t *testing.T, reset bool, checkErr error) stopRig {
			details := func(*loopRig) string { return "429 Too Many Requests" }
			if reset {
				details = resetIn(time.Hour)
			}
			r, reason := limitStopWith(t, "rate_limit", details, func(r *loopRig) { r.inbox.record("pj-o1") })
			r.setCheckout(t, checkErr)
			return stopRig{r, func() int { return r.inbox.count("pj-o1", continueLine) }, reason}
		},
		"codex": func(t *testing.T, reset bool, checkErr error) stopRig {
			rollout := cdxLimited
			if reset {
				at := time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC) // the rig's clock starts at 12:00
				rollout = `{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":100.0,` +
					`"window_minutes":300,"resets_at":` + strconv.FormatInt(at.Unix(), 10) + `},"secondary":null}}}` + "\n" + cdxLimited
			}
			c := codexStoppedWith(t, rollout)
			c.setCheckout(t, checkErr)
			return stopRig{c.loopRig, c.wakes, c.reason}
		},
	}
	checks := map[string]error{
		"off the target branch": errors.New("main checkout is on side, not main"),
		"a failing check":       errors.New("git: signal: killed"),
	}
	for kind, mk := range stopped {
		for name, checkErr := range checks {
			t.Run(kind+"/"+name, func(t *testing.T) {
				t.Parallel()
				t.Run("known reset", func(t *testing.T) {
					t.Parallel()
					r := mk(t, true, checkErr)
					r.later(time.Hour + time.Minute)
					if n := r.wakes(); n != 0 {
						t.Fatalf("woken %d times before reset+2m", n)
					}
					r.later(time.Minute)
					r.waitFor("the wake", func() bool { return r.wakes() == 1 })
					r.later(30 * time.Minute)
					r.waitFor("the alarm", func() bool { return r.stopIn(r.reason+stillStopped) == 1 && r.alarms() == 1 })
					r.mustStillHold(checkErr)
				})
				if kind == "codex" { // its fixture limit names a time of day: no unknown reset
					return
				}
				t.Run("unknown reset", func(t *testing.T) {
					t.Parallel()
					r := mk(t, false, checkErr)
					r.later(29 * time.Minute)
					if n := r.wakes(); n != 0 {
						t.Fatalf("woken %d times before 30 min", n)
					}
					for want := 1; want <= 2; want++ {
						r.later(time.Minute)
						r.waitFor("a periodic wake", func() bool { return r.wakes() == want })
						r.later(29 * time.Minute)
					}
					r.mustStillHold(checkErr)
				})
				t.Run("missed deadline", func(t *testing.T) {
					t.Parallel()
					r := mk(t, false, checkErr)
					r.stop()
					r.clock.Add(3 * time.Hour)
					r.start()
					r.push()
					r.waitFor("the missed wake", func() bool { return r.wakes() == 1 })
					r.later(time.Minute)
					if n := r.wakes(); n != 1 {
						t.Errorf("woken %d times, want once", n)
					}
					r.mustStillHold(checkErr)
				})
			})
		}
	}
}

// setCheckout makes the main checkout's check fail with err.
func (r *loopRig) setCheckout(t *testing.T, err error) {
	t.Helper()
	r.mu.Lock()
	r.checkoutErr = err
	r.mu.Unlock()
	r.waitStep("0 fact needs-you{loop: " + err.Error() + "}")
}

// mustStillHold fails unless the merge loop is still paused by err (so the wakes above went round it).
func (r *loopRig) mustStillHold(err error) {
	r.t.Helper()
	if !r.hasPrefix("0 fact needs-you{loop: " + err.Error()) {
		r.t.Fatalf("precondition: the loop's hold %q gone: %q", err, r.steps())
	}
}
