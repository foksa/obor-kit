package app

import (
	"context"
	"strings"
	"testing"
)

// answerSeed is the harness with T-1 dispatched in lane O-01 (the lane adapters faked), its loop state set to state
// and its attempt's job to job, then the needs-you of cause recorded (a watchdog's when watch: no merge-loop fact). It
// returns the fact in force.
func answerSeed(t *testing.T, cause, state, job string, watch bool) (*harness, *proj, Record) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	f := newConcScratch("")
	fx := &Loop{Lane: f, Specs: f, Agents: f}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", StepDispatched, KindFact, dispatchData{Lane: "O-01", Dir: h.lane("O-01"), Agent: "pj-o-01",
		Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", StepPrepare, KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	mt := p.folded.loop["T-1"]
	if watch {
		h.a.record(p, Record{Task: "T-1", Step: StepNeedsYou, Kind: KindFact}, FactData{Lane: "O-01", Reason: cause})
		return h, p, p.folded.active["T-1"][0]
	}
	mt.state, mt.job = state, job
	if state == stWaiting { // a waiting task holds no lane yet
		mt.lane = ""
	}
	if !h.a.needsYou(p, mt, cause, cause+": probe") || mt.needs.Seq == 0 {
		t.Fatalf("precondition: %s's needs-you not in force", cause)
	}
	return h, p, mt.needs
}

// runAnswer runs cmd, one command of an answerPath, as the lead does: obor resume through its own parser (ResumeArgs) with
// a one-word decision for the placeholder (RV-54 #4), obor drop and obor sendback as their CLIs call serve.
func runAnswer(h *harness, fact Record, cmd string) (string, error) {
	c := Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "c1"}
	args := strings.Fields(strings.NewReplacer(`["<decision>"]`, "yes", `"<why>"`, "").Replace(cmd))
	switch {
	case len(args) > 2 && args[1] == "resume":
		rc, ok := ResumeArgs(args[2:])
		if !ok {
			return "", &usageError{cmd}
		}
		c.Task, c.To, c.Note = rc.Task, rc.To, rc.Note
		return h.a.resume(c)
	case len(args) == 3 && args[1] == "drop":
		c.Note = "not needed"
		if args[2] == fact.Fact().Lane {
			c.Lane = args[2]
		} else {
			c.Task = args[2]
		}
		return h.a.drop(c)
	case len(args) == 3 && args[1] == "sendback": // obor sendback <LANE>: the CLI asks serve for the lane's task
		c.Task, c.Note = fact.Task, "please fix"
		return h.a.verdict(context.Background(), MethodSendback, c)
	}
	return "", &usageError{cmd}
}

type usageError struct{ cmd string }

func (e *usageError) Error() string { return "not an obor command: " + e.cmd }

// RV-54 #2 (T-515): each cause's suggested commands are the ones its verdicts take in the state the needs-you leaves
// (resumeTo, unsendable, droppable): explicit modes where there is no default (sendbacks, resolve rejected, fix job
// not sent), no drop for a merged task (after-merge failures), none but drop for a reject; a merge-loop needs-you of a
// task with a lane takes the lead's sendback (T-619). Each command, run on a fresh seed through obor resume's parser,
// is accepted.
func TestAnswerPathEveryCause(t *testing.T) {
	const drop, resume = ` | obor drop O-01 "<why>"`, "obor resume T-1"
	const sb = `obor sendback O-01 "<why>" | ` // a merge-loop needs-you's advice starts with it (T-619)
	for _, row := range []struct {
		cause, state, job string
		watch             bool
		want              string
	}{
		{causeHeld, stAwaiting, StepReview, false, sb + `obor resume T-1 ["<decision>"]` + drop},
		{causeOpen, stGreen, "", false, sb + `obor resume T-1 ["<decision>"]` + drop},
		{causeRed, stRed, "", false, sb + resume + drop},
		{causeUndelivered, stAwaiting, StepReview, false, sb + resume + drop},
		{causeStalled, stAwaiting, StepReview, false, sb + resume + drop},
		{causeGate, stFail, "", false, sb + resume + drop},
		{causePublish, stFail, "", false, sb + resume + drop},
		{causeSendbacks, stFail, StepReview, false, sb + resume + " regate | " + resume + " reprompt" + drop},
		{causeResolveRejected, stFail, StepResolve, false, sb + resume + " regate | " + resume + " reprompt" + drop},
		{causeFix, stWorking, "", false, sb + resume + " regate" + drop},
		{causeLane, stDispatched, "", false, sb + resume + drop},
		{causeStart, stDispatched, "", false, sb + resume + drop},
		{causeNoPane, stDispatched, "", false, sb + resume + drop},
		{causeAfter, stWaiting, "", false, resume + ` | obor drop T-1 "<why>"`}, // no lane: no obor sendback
		{"submit" + failedSuffix, stSubmitted, "", false, sb + resume + drop},
		{"review" + failedSuffix, stAwaiting, StepReview, false, sb + resume + drop},
		{StepAfterMerge + failedSuffix, stMerged, "", false, resume},
		{StepStop + failedSuffix, stMerged, "", false, resume},
		{causeRejected, stRejected, "", false, `obor drop O-01 "<why>"`},
		{"stalled", "", "", true, `its pane, or obor drop O-01 "<why>"`},
		{"blocked", "", "", true, `its pane, or obor drop O-01 "<why>"`},
		{"pane exited", "", "", true, `its pane, or obor drop O-01 "<why>"`},
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
				if reply, err := runAnswer(h, fact, cmd); err != nil {
					t.Errorf("%s: refused: %v", cmd, err)
				} else if !strings.Contains(reply, map[string]string{"resume": "RESUMED", "drop": "DROPPED",
					"sendback": "SENDBACK"}[strings.Fields(cmd)[1]]) {
					t.Errorf("%s: %q", cmd, reply)
				}
			}
		})
	}
	for _, task := range []string{LoopTask, NightlyTask, MsgTask} { // a project's fact: fix what it names
		if path := answerPath(Record{Task: task, Step: StepNeedsYou, Kind: KindFact}, mtask{}); path != "" {
			t.Errorf("%s: suggests %q, want none", task, path)
		}
	}
}

// RV-54 #4: obor resume's arguments: regate and reprompt are the reserved modes; any other nonblank last argument
// is the decision, a single word too; nothing else parses.
func TestResumeArgs(t *testing.T) {
	for _, c := range []struct {
		args     []string
		to, note string
		ok       bool
	}{
		{[]string{"T-1"}, "", "", true},
		{[]string{"T-1", "yes"}, "", "yes", true},
		{[]string{"T-1", "no"}, "", "no", true},
		{[]string{"T-1", "approved"}, "", "approved", true},
		{[]string{"T-1", "retry"}, "", "retry", true}, // not a reserved mode: a decision, the cause's default
		{[]string{"T-1", "yes please"}, "", "yes please", true},
		{[]string{"T-1", "regate"}, ResumeRegate, "", true},
		{[]string{"T-1", "reprompt", "yes"}, ResumeReprompt, "yes", true},
		{[]string{"T-1", "regate", "yes", "more"}, "", "", false},
		{[]string{"T-1", "yes", "more"}, "", "", false},
		{[]string{"T-1", " "}, "", "", false},
		{[]string{"T-1", "regate", ""}, "", "", false},
		{[]string{""}, "", "", false},
		{nil, "", "", false},
	} {
		got, ok := ResumeArgs(c.args)
		if ok != c.ok || ok && (got.Task != c.args[0] || got.To != c.to || got.Note != c.note) {
			t.Errorf("ResumeArgs(%q) = %+v, %v; want to %q, note %q, %v", c.args, got, ok, c.to, c.note, c.ok)
		}
	}
}
