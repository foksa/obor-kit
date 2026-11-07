package app

import (
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"regexp"
	"strings"
	"testing"
)

// answerSeed is the harness with T-1 dispatched in lane L-01 (the lane adapters faked), its loop state set to state
// and its attempt's job to job, then the needs-you of cause recorded (a watchdog's when watch: no merge-loop fact). It
// returns the fact in force.
func answerSeed(t *testing.T, cause, state, job string, watch bool) (*harness, *proj, event.Record) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	f := newConcFakes("")
	fx := &Loop{Lane: f, Specs: f, Agents: f}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	mt := p.folded.loop["T-1"]
	if watch {
		h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Lane: "L-01", Reason: cause})
		return h, p, p.folded.active["T-1"][0]
	}
	mt.state, mt.job = state, job
	if state == stWaiting { // a waiting task holds no lane yet
		mt.lane = ""
	}
	switch cause {
	case causeGate, causePublish, causeSendbacks, causeResolveRejected: // its phase's work failed: a due block first
		mt.block(block{cause: cause, why: cause + ": probe"})
	}
	if !h.a.needsYou(p, mt, cause, cause+": probe") || !mt.needs.held() {
		t.Fatalf("precondition: %s's needs-you not in force", cause)
	}
	return h, p, mt.needs.fact
}

// runAnswer runs cmd, one command of an answerPath, as the lead does: obor resume through its own parser (ResumeArgs)
// with a one-word text for its placeholder (RV-54 #4), obor drop as its CLI calls serve (by task id, T-775).
func runAnswer(h *harness, fact event.Record, cmd string) (string, error) {
	c := wire.Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "c1"}
	args := strings.Fields(regexp.MustCompile(`"<[a-z]+>"`).ReplaceAllString(cmd, "yes"))
	switch {
	case len(args) > 2 && args[1] == "resume":
		rc, ok := wire.ResumeArgs(args[2:])
		if !ok {
			return "", &usageError{cmd}
		}
		c.Task, c.To, c.Note = rc.Task, rc.To, rc.Note
		return h.a.resume(c)
	case len(args) == 4 && args[1] == "drop" && args[2] == fact.Task:
		c.Task, c.Note = args[2], "not needed"
		return h.a.drop(c)
	}
	return "", &usageError{cmd}
}

type usageError struct{ cmd string }

func (e *usageError) Error() string { return "not an obor command: " + e.cmd }

// RV-54 #2 (T-515), T-775: each cause's suggested commands are the ones its verbs take in the state the needs-you
// leaves (resumeFits, mtask.may): the one obor resume form (its text where it needs one; the owner's choice of --regate
// or --sendback where there is no default: red or sendbacks past K, a resolve rejected, a fix job not sent), no drop
// for a merged task (after-merge failures), only drop for a reject; every command names the task, never the lane.
// Each command, run on a fresh seed through obor resume's parser, is accepted.
func TestAnswerPathEveryCause(t *testing.T) {
	t.Parallel()
	const drop, resume = ` | obor drop T-1 "<why>"`, "obor resume T-1"
	const choice = resume + " --regate | " + resume + ` --sendback "<why>"`
	for _, row := range []struct {
		cause, state, job string
		watch             bool
		want              string
	}{
		{event.CauseHeld, stAwaiting, event.StepReview, false, `obor resume T-1 "<decision>"` + drop},
		{causeRed, stRed, "", false, choice + drop},
		{causeUndelivered, stAwaiting, event.StepReview, false, resume + drop},
		{causeStalled, stAwaiting, event.StepReview, false, resume + drop},
		{causeGate, stSubmitted, "", false, resume + drop},
		{causePublish, stAccepted, "", false, resume + drop},
		{causeSendbacks, stAwaiting, event.StepReview, false, choice + drop},
		{causeResolveRejected, stResolving, event.StepResolve, false, choice + drop},
		{causeFix, stWorking, "", false, choice + drop},
		{causeLane, stDispatched, "", false, resume + drop},
		{causeStart, stDispatched, "", false, resume + drop},
		{causeNoPane, stDispatched, "", false, resume + drop},
		{causeAfter, stWaiting, "", false, resume + drop}, // no lane: the drop just cancels it
		{"submit" + failedSuffix, stSubmitted, "", false, resume + drop},
		{"review" + failedSuffix, stAwaiting, event.StepReview, false, resume + drop},
		{event.StepAfterMerge + failedSuffix, stMerged, "", false, resume},
		{event.StepStop + failedSuffix, stMerged, "", false, resume},
		{causeRejected, stRejected, "", false, `obor drop T-1 "<why>"`},
		{"stalled", "", "", true, `its pane, or obor resume T-1 "<text>"` + drop}, // T-746
		{"blocked", "", "", true, `its pane, or obor drop T-1 "<why>"`},
		{"pane exited", "", "", true, `its pane, or obor drop T-1 "<why>"`},
	} {
		t.Run(row.cause, func(t *testing.T) {
			t.Parallel()
			h, p, fact := answerSeed(t, row.cause, row.state, row.job, row.watch)
			h.a.mu.Lock()
			path := p.answerPath(fact)
			h.a.mu.Unlock()
			if path != row.want {
				t.Fatalf("suggested %q, want %q", path, row.want)
			}
			if got := h.a.Status().Project.Answers[fact.Seq]; got != path { // obor status shows serve's
				t.Errorf("status answers %q, want %q", got, path)
			}
			for _, cmd := range strings.Split(strings.TrimPrefix(path, "its pane, or "), " | ") {
				if cmd == "its pane" {
					continue
				}
				h, _, fact := answerSeed(t, row.cause, row.state, row.job, row.watch)
				// a drop answers DROPPED, or DROPPING for a task holding a lane (T-852)
				if reply, err := runAnswer(h, fact, cmd); err != nil {
					t.Errorf("%s: refused: %v", cmd, err)
				} else if want := map[bool]string{true: "SENDBACK", false: map[string]string{"resume": "RESUMED",
					"drop": "DROPP"}[strings.Fields(cmd)[1]]}[strings.Contains(cmd, "--sendback")]; !strings.Contains(reply, want) {
					t.Errorf("%s: %q", cmd, reply)
				}
			}
		})
	}
	for _, task := range []string{event.LoopTask, event.NightlyTask, event.MsgTask} { // a project's fact: fix what it names
		if path := answerPath(nil, event.Record{Task: task, Step: event.StepNeedsYou, Kind: event.KindFact}, mtask{}); path != "" {
			t.Errorf("%s: suggests %q, want none", task, path)
		}
	}
}

// RV-54 #4, T-775: obor resume's arguments: --regate and --sendback are its flags (one, anywhere after the task); any
// other nonblank argument is the text, a single word too, the old keywords included, one starting with - or -- too
// (RV-99 #5); after -- every argument is the text; nothing else parses.
func TestResumeArgs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args     []string
		to, note string
		ok       bool
	}{
		{[]string{"T-1"}, "", "", true},
		{[]string{"T-1", "yes"}, "", "yes", true},
		{[]string{"T-1", "no"}, "", "no", true},
		{[]string{"T-1", "approved"}, "", "approved", true},
		{[]string{"T-1", "retry"}, "", "retry", true}, // the old keywords are a text now (T-775)
		{[]string{"T-1", "regate"}, "", "regate", true},
		{[]string{"T-1", "yes please"}, "", "yes please", true},
		{[]string{"T-1", "--regate"}, wire.ResumeRegate, "", true},
		{[]string{"T-1", "--sendback", "fix x"}, wire.ResumeSendback, "fix x", true},
		{[]string{"T-1", "fix x", "--sendback"}, wire.ResumeSendback, "fix x", true},
		{[]string{"T-1", "--regate", "--sendback"}, "", "", false},
		{[]string{"T-1", "--reprompt", "yes"}, "", "", false}, // --reprompt is a text: two texts
		{[]string{"T-1", "--regate", "yes", "more"}, "", "", false},
		{[]string{"T-1", "yes", "more"}, "", "", false},
		{[]string{"T-1", " "}, "", "", false},
		{[]string{"T-1", "--regate", ""}, "", "", false},
		{[]string{"--regate", "T-1"}, "", "", false},
		// RV-99 #5: only --regate and --sendback are flags; any other argument is the text, -- ends the flags
		{[]string{"T-1", "- use A\n- use B"}, "", "- use A\n- use B", true},
		{[]string{"T-1", "--force is safe"}, "", "--force is safe", true},
		{[]string{"T-1", "--force is safe", "--sendback"}, wire.ResumeSendback, "--force is safe", true},
		{[]string{"T-1", "-x"}, "", "-x", true},
		{[]string{"T-1", "--", "--regate"}, "", "--regate", true},
		{[]string{"T-1", "--regate", "--", "--sendback"}, wire.ResumeRegate, "--sendback", true},
		{[]string{"T-1", "--", "--"}, "", "--", true},
		{[]string{"T-1", "--", "--regate", "yes"}, "", "", false},
		{[]string{"T-1", "--regate", "--regate"}, "", "", false},
		{[]string{""}, "", "", false},
		{nil, "", "", false},
	} {
		got, ok := wire.ResumeArgs(c.args)
		if ok != c.ok || ok && (got.Task != c.args[0] || got.To != c.to || got.Note != c.note) {
			t.Errorf("ResumeArgs(%q) = %+v, %v; want to %q, note %q, %v", c.args, got, ok, c.to, c.note, c.ok)
		}
	}
}
