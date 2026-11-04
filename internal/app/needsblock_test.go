package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// T-827 (DD-consolidation S2): a needs-you is a block on the task's phase. These golden folds were captured with the
// code before the change (the three encodings: fail, needs-you with left, merged/rejected with needs): each case is a
// log in the event log's format, replayed, and what obor status, the needs-you list, the task verbs, the next step
// and obor resume make of it (each action folded in turn) must stay the same. OBOR_UPDATE_GOLDEN=1 rewrites the file.

const blockTask = "T-1"

// blockLog builds a case's records: each step appends records of blockTask in the event log's format.
type blockLog struct{ recs []event.Record }

func (b *blockLog) add(attempt int, step, kind string, data any) *blockLog {
	r := event.Record{Task: blockTask, Attempt: attempt, Step: step, Kind: kind}
	if data != nil {
		r.Data, _ = json.Marshal(data)
	}
	b.recs = append(b.recs, r)
	return b
}

func (b *blockLog) fact(attempt int, step string, data any) *blockLog {
	return b.add(attempt, step, event.KindFact, data)
}

// result appends step's intent and its result.
func (b *blockLog) result(attempt int, step string, in, out any) *blockLog {
	return b.add(attempt, step, event.KindIntent, in).add(attempt, step, event.KindResult, out)
}

func (b *blockLog) needs(attempt int, cause, reason string) *blockLog {
	return b.fact(attempt, event.StepNeedsYou, event.FactData{Lane: "L-01", Machine: Machine, Reason: reason, Merge: true,
		Cause: cause})
}

func (b *blockLog) failed(attempt int, step, why string, n int) *blockLog {
	for range n {
		b.fact(attempt, event.StepFailed, event.FactData{Lane: "L-01", Machine: Machine, Reason: step + ": " + why,
			Merge: true, Step: step})
	}
	return b
}

func (b *blockLog) copy() *blockLog { return &blockLog{recs: slices.Clone(b.recs)} }

// dispatched: attempt 1 of blockTask dispatched to L-01, prepared and started, its work job delivered (working).
func dispatched() *blockLog {
	b := &blockLog{}
	b.fact(1, event.StepDispatched, dispatchData{Lane: "L-01", Agent: "pj-w1", Reply: "DISPATCHED"})
	b.result(1, event.StepPrepare, nil, prepareOut{Base: "base", Head: "base"})
	b.result(1, event.StepStartWork, nil, startOut{Agent: "pj-w1"})
	b.add(1, event.StepWork, event.KindIntent, nil)
	b.fact(1, event.StepDelivered, event.FactData{Job: JobID(event.Key{Task: blockTask, Attempt: 1, Step: event.StepWork})})
	return b
}

// submitted: attempt n submitted by its worker and pinned; its gate's intent written.
func (b *blockLog) submitted(n int) *blockLog {
	b.fact(n, event.StepSubmitted, submitData{Lane: "L-01", Agent: "pj-w1", Report: "Summary: x"})
	b.result(n, event.StepSubmit, nil, submitOut{SHA: fmt.Sprintf("sha%d", n)})
	return b
}

func (b *blockLog) gate(n int, outcome string) *blockLog {
	out := gateOut{Outcome: outcome, Line: "gate " + outcome}
	if outcome == outGreen {
		out.Candidate = port.Candidate{SHA: "cand", Patch: "patch"}
	}
	return b.result(n, event.StepGate, gateIn{Kind: port.InputLane, Lane: "L-01"}, out)
}

// redPastK: two red gates sent back, the third red (sendbacks K).
func redPastK() *blockLog {
	b := dispatched().submitted(1).gate(1, outRed).fact(1, event.StepSendback, noteData{Note: "gate red"})
	b.submitted(2).gate(2, outRed).fact(2, event.StepSendback, noteData{Note: "gate red"})
	return b.submitted(3).gate(3, outRed)
}

// review: attempt n green and its review job out.
func (b *blockLog) review(n int) *blockLog {
	return b.gate(n, outGreen).add(n, event.StepReview, event.KindIntent, JobData{Patch: "patch"})
}

func (b *blockLog) verdict(n int, method, job string, v verdictResult) *blockLog {
	v.Job = JobID(event.Key{Task: blockTask, Attempt: n, Step: job})
	return b.result(n, verdictStep(method, "c"+method), nil, v)
}

func (b *blockLog) accepted(n int) *blockLog {
	return b.review(n).verdict(n, wire.MethodAccept, event.StepReview, verdictResult{Patch: "patch", Reply: "ACCEPTED"})
}

// blockCase is one case: its log; whether a merge outside the app is checked first (drive: Scratch.Landed).
type blockCase struct {
	name string
	log  *blockLog
}

func blockCases() []blockCase {
	k := redPastK()
	sendbacksK := dispatched().submitted(1).gate(1, outRed).fact(1, event.StepSendback, noteData{Note: "gate red"}).
		submitted(2).gate(2, outRed).fact(2, event.StepSendback, noteData{Note: "gate red"}).submitted(3).review(3).
		verdict(3, wire.MethodSendback, event.StepReview, verdictResult{Note: "again"})
	resolve := dispatched().submitted(1).gate(1, outConflict).add(1, event.StepResolve, event.KindIntent, JobData{Patch: "patch"}).
		verdict(1, wire.MethodReject, event.StepResolve, verdictResult{Note: "cannot"})
	gateFail := dispatched().submitted(1).gate(1, outFail)
	submitRefused := dispatched().fact(1, event.StepSubmitted, submitData{Lane: "L-01", Agent: "pj-w1", Report: "Summary: x"}).
		result(1, event.StepSubmit, nil, submitOut{Refused: "no commit"})
	gateFailed := dispatched().submitted(1).add(1, event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01"}).
		failed(1, event.StepGate, "boom", tries)
	publish := dispatched().submitted(1).accepted(1).result(1, event.StepPublish, nil, gateOut{Outcome: outFail, Line: "push refused"})
	resolvedFail := dispatched().submitted(1).gate(1, outConflict).add(1, event.StepResolve, event.KindIntent, JobData{Patch: "patch"}).
		verdict(1, wire.MethodResolved, event.StepResolve, verdictResult{SHA: "res"}).
		result(1, nth(event.StepGate, 2), gateIn{Kind: port.InputResolved, Lane: "L-01"}, gateOut{Outcome: outFail, Line: "boom"})
	prepRefused := &blockLog{}
	prepRefused.fact(1, event.StepDispatched, dispatchData{Lane: "L-01", Agent: "pj-w1", Reply: "DISPATCHED"}).
		result(1, event.StepPrepare, nil, prepareOut{Refused: "dirty lane"})
	waiting := &blockLog{}
	waiting.fact(1, event.StepWaiting, dispatchData{After: []string{"T-0"}, Reply: "WAITING"})
	fixUndelivered := dispatched().submitted(1).gate(1, outRed).fact(1, event.StepSendback, noteData{Note: "gate red"}).
		add(2, event.StepFix, event.KindIntent, nil)
	merged := dispatched().submitted(1).accepted(1).result(1, event.StepPublish, nil, gateOut{Outcome: outMerged, Line: "merged"}).
		fact(1, event.StepMerged, event.MergedData{Head: "cand"}).add(1, event.StepAfterMerge, event.KindIntent, nil).
		failed(1, event.StepAfterMerge, "boom", tries)
	rejected := dispatched().submitted(1).review(1).verdict(1, wire.MethodReject, event.StepReview, verdictResult{Note: "no"}).
		result(1, event.StepUnpin, nil, "unpinned")
	held := dispatched().submitted(1).review(1).fact(1, event.StepNeedsYou, event.FactData{Lane: "L-01", Machine: Machine,
		Reason: "held: which way", Merge: true, Cause: event.CauseHeld, Job: JobID(event.Key{Task: blockTask, Attempt: 1, Step: event.StepReview})})
	stalled := dispatched().submitted(1).review(1).needs(1, causeStalled, "job stalled: integrator gone")
	asks := dispatched().fact(0, event.StepNeedsYou, event.FactData{Lane: "L-01", Machine: Machine, Reason: reasonAsks + " which way"})
	asks.recs[len(asks.recs)-1].Attempt = 0
	return []blockCase{
		{"red gate past K, due", k.copy()},
		{"red gate past K, held", k.copy().needs(3, causeRed, "gate red after 2 sendbacks: gate red")},
		{"sendbacks past K, due", sendbacksK.copy()},
		{"sendbacks past K, held", sendbacksK.copy().needs(3, causeSendbacks, "sent back 2 times, the integrator would again: again")},
		{"resolve rejected, due", resolve.copy()},
		{"resolve rejected, held", resolve.copy().needs(1, causeResolveRejected, "resolve rejected: cannot")},
		{"gate failure, due", gateFail.copy()},
		{"gate failure, held", gateFail.copy().needs(1, causeGate, "gate: gate fail")},
		{"resolved gate failure, held", resolvedFail.copy().needs(1, causeGate, "gate: boom")},
		{"submit refused, due", submitRefused.copy()},
		{"submit refused, held", submitRefused.copy().needs(1, causeGate, "submit: no commit")},
		{"gate step failed, held", gateFailed.copy().needs(1, event.StepGate+failedSuffix, "gate failed: boom")},
		{"publish failure, due", publish.copy()},
		{"publish failure, held", publish.copy().needs(1, causePublish, "publish: push refused")},
		{"worker question while working", asks.copy()},
		{"dispatch refused, due", prepRefused.copy()},
		{"dispatch refused, held", prepRefused.copy().needs(1, causeLane, "lane: dirty lane")},
		{"waiting needs-you", waiting.copy().needs(1, causeAfter, "after: T-0 dropped")},
		{"undelivered fix job", fixUndelivered.copy().needs(2, causeUndelivered, "job not delivered: T-1/2/fix prompted twice")},
		{"after-merge step failed", merged.copy().needs(1, event.StepAfterMerge+failedSuffix, "after_merge failed: boom")},
		{"rejected", rejected.copy().needs(1, causeRejected, "rejected by the integrator: no")},
		{"held review verdict", held.copy()},
		{"held review, accepted", held.copy().verdict(1, wire.MethodAccept, event.StepReview, verdictResult{Patch: "patch"})},
		{"held review, lead's sendback", held.copy().result(1, verdictStep(wire.MethodSendback, "lead"), nil,
			verdictResult{Job: JobID(event.Key{Task: blockTask, Attempt: 1, Step: event.StepReview}), Note: "redo", Lead: true})},
		{"stalled review job, held", stalled.copy()},
		{"stalled resolve job, held", dispatched().submitted(1).gate(1, outConflict).add(1, event.StepResolve, event.KindIntent,
			JobData{Patch: "patch"}).needs(1, causeStalled, "job stalled: integrator gone")},
		{"resolved gate step failed, held", dispatched().submitted(1).gate(1, outConflict).add(1, event.StepResolve, event.KindIntent,
			JobData{Patch: "patch"}).verdict(1, wire.MethodResolved, event.StepResolve, verdictResult{SHA: "res"}).
			add(1, nth(event.StepGate, 2), event.KindIntent, gateIn{Kind: port.InputResolved, Lane: "L-01"}).
			failed(1, nth(event.StepGate, 2), "boom", tries).needs(1, event.StepGate+failedSuffix, "gate.2 failed: boom")},
		{"held fix job, drive take-over", fixUndelivered.copy().needs(2, causeUndelivered, "job not delivered").
			fact(2, event.StepDrive, dispatchData{Lane: "L-01", Agent: "pj-w1-drive", Drive: true, Reply: "DRIVE"})},
		{"held, merged outside", gateFail.copy().needs(1, causeGate, "gate: gate fail").fact(1, event.StepMerged, event.MergedData{Outside: true})},
		{"held, dropped", gateFail.copy().needs(1, causeGate, "gate: gate fail").fact(1, event.StepDropped, event.FactData{Reason: "no"})},
		{"due, dropped", gateFail.copy().fact(1, event.StepDropped, event.FactData{Reason: "no"})},
	}
}

// blockProj replays recs as a log serve opens: written to a new log, then read back and folded.
func blockProj(t *testing.T, recs []event.Record) (*App, *proj) {
	t.Helper()
	root := logRoot(t)
	appendRecs(t, root, recs)
	return blockProjAt(t, root)
}

// appendRecs appends recs to root's log.
func appendRecs(t *testing.T, root string, recs []event.Record) {
	t.Helper()
	l, _ := openLog(t, root)
	for _, r := range recs {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
}

// blockProjAt is serve's project of root's log, replayed.
func blockProjAt(t *testing.T, root string) (*App, *proj) {
	t.Helper()
	l, recs := openLog(t, root)
	a := &App{opt: Options{Now: func() time.Time { return logNow }, Out: io.Discard}}
	p := &proj{root: root, name: "pj", log: l, tasks: map[string]*watch{}, retry: map[event.Key]retry{},
		integrator: "pj-integrator", out: newOutbox("pj-lead", ""), merging: true, timers: map[string]*jobTimer{},
		replies: map[string]string{}, nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		unwritten: map[event.Key]json.RawMessage{}, exec: map[event.Key]time.Time{}, scratch: make(chan struct{}, 1),
		held: map[string]string{}, ended: map[string]bool{}, roleFail: map[string]time.Time{}, reg: newRegistry(),
		folded: newFolded(), holds: map[string]mergeHold{}}
	for _, r := range recs {
		p.foldRecord(r)
	}
	a.p = p
	return a, p
}

// blockSummary is what the code makes of blockTask now: obor status' row and facts, the needs-you list, the verbs it
// takes, its next step, the integrator job open, the scratch holder, dispatch and task_done.
func blockSummary(a *App, p *proj) string {
	var b strings.Builder
	st := a.Status()
	for _, ts := range st.Project.Tasks {
		if ts.Task == blockTask {
			fmt.Fprintf(&b, "  status: state %s, phase %s, stage %q, attempt %d, job %q\n", ts.State, ts.Phase, ts.Stage,
				ts.Attempt, ts.Job)
		}
	}
	var facts []string
	for _, f := range st.Project.Facts {
		facts = append(facts, f.Step+"{"+f.Fact().Reason+"}")
	}
	slices.Sort(facts)
	fmt.Fprintf(&b, "  facts: %s\n", strings.Join(facts, "; "))
	var needs []string
	for _, f := range p.needsYou() {
		needs = append(needs, f.Fact().Reason)
	}
	slices.Sort(needs)
	fmt.Fprintf(&b, "  needs-you: %s\n", strings.Join(needs, "; "))
	mt := p.folded.loop[blockTask]
	if mt == nil {
		b.WriteString("  not in the loop\n")
		return b.String()
	}
	var vs []string
	for _, v := range allVerbs {
		if mt.may(v) {
			vs = append(vs, verbName[v])
		}
	}
	fmt.Fprintf(&b, "  verbs: %s\n", strings.Join(vs, " "))
	next := "waits"
	if a.pick(p, mt) != nil {
		next = mt.stepName()
	}
	holder, why := p.scratchHolder("")
	fmt.Fprintf(&b, "  next: %s; job open %v; scratch %q %q; dispatchable %v; awaits task_done %v; in loop %v; sendbacks %d\n",
		next, mt.openJob(), holder, why, mt.dispatchable(), mt.awaitsTaskDone(), mt.inLoop(), mt.sendbacks)
	fmt.Fprintf(&b, "  resume cmds: %s\n", needsCmds(p, blockTask, *mt))
	return b.String()
}

// needsCmds is the obor resume and drop forms the needs-you line names for mt ("": none).
func needsCmds(p *proj, task string, mt mtask) string {
	var cmds []string
	if c := resumeCmds(p, task, mt); c != "" {
		cmds = append(cmds, c)
	}
	if mt.may(vDrop) {
		cmds = append(cmds, "obor drop "+task)
	}
	return strings.Join(cmds, " | ")
}

// blockResume is obor resume's decision for each flag, and what each action's fold makes of the task.
func blockResume(t *testing.T, c blockCase) string {
	var b strings.Builder
	a, p := blockProj(t, c.log.recs)
	mt := p.folded.loop[blockTask]
	if mt == nil {
		return ""
	}
	acts := map[string]bool{}
	for _, flag := range []string{"", wire.ResumeRegate, wire.ResumeSendback} {
		act, err := p.resumeAct(*mt, flag)
		if err != nil {
			fmt.Fprintf(&b, "  resume --%s: refused: %v\n", flag, err)
			continue
		}
		fmt.Fprintf(&b, "  resume --%s: %s (text needed %v)\n", flag, act, p.resumeNeedsText(*mt, act))
		acts[act] = true
	}
	_ = a
	for _, act := range []string{wire.ResumeRegate, wire.ResumeReprompt, wire.ResumeRetry, actSendback} {
		if !acts[act] {
			continue
		}
		l := c.log.copy()
		if act == actSendback {
			job := ""
			if mt.openJob() {
				job = JobID(mt.jobKey())
			}
			l.result(mt.attempt, verdictStep(wire.MethodSendback, "lead"), nil, verdictResult{Job: job, Note: "redo", Lead: true})
		} else {
			l.fact(mt.attempt, event.StepUnblocked, event.FactData{Lane: mt.lane, Machine: Machine, Reason: act, Merge: true,
				Cause: mt.needs.fact.Fact().Cause, Note: "decided"})
		}
		a2, p2 := blockProj(t, l.recs)
		m2 := p2.folded.loop[blockTask]
		fmt.Fprintf(&b, "  after %s:\n%s", act, indent(blockSummary(a2, p2)))
		fmt.Fprintf(&b, "    input %q, decision %q, work %q, fixed %v, fails %d\n", m2.input, m2.decision, m2.work, m2.fixed, len(m2.fails))
	}
	return b.String()
}

func indent(s string) string {
	return strings.ReplaceAll(strings.TrimSuffix("  "+s, "  "), "\n  ", "\n    ")
}

// blockGolden is every case's summary, its resume decisions and their folds.
func blockGolden(t *testing.T) string {
	var b strings.Builder
	for _, c := range blockCases() {
		a, p := blockProj(t, c.log.recs)
		fmt.Fprintf(&b, "== %s\n%s%s", c.name, blockSummary(a, p), blockResume(t, c))
	}
	return b.String()
}

func TestNeedsYouBlockGoldenFolds(t *testing.T) {
	got := blockGolden(t)
	path := filepath.Join("testdata", "needsblock.golden")
	if os.Getenv("OBOR_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Errorf("line %d:\n got  %s\n want %s", i+1, g, w)
			}
		}
	}
}

// landedScratch: the target holds merge evidence of every task (a merge outside the app).
type landedScratch struct{ port.Scratch }

func (landedScratch) Landed(context.Context, string) (bool, error) { return true, nil }

// TestNeedsYouBlockMergedOutside: a task blocked (due or held) is checked for a merge outside the app first: merged,
// its block cleared.
func TestNeedsYouBlockMergedOutside(t *testing.T) {
	gateFail := dispatched().submitted(1).gate(1, outFail)
	for _, l := range []*blockLog{gateFail.copy(), gateFail.copy().needs(1, causeGate, "gate: gate fail")} {
		a, p := blockProj(t, l.recs)
		if len(p.needsYou()) > 1 {
			t.Fatalf("precondition: %d needs-you", len(p.needsYou()))
		}
		a.drive(context.Background(), p, &Loop{Scratch: landedScratch{}}, blockTask)
		mt := p.folded.loop[blockTask]
		if mt.state != stMerged || len(p.needsYou()) != 0 {
			t.Errorf("after a merge outside: %s, %d needs-you; want merged, none", mt.state, len(p.needsYou()))
		}
	}
}

// TestNeedsYouBlockDueRecordedOnce: a due block (a gate failure not recorded yet) is recorded by the next pass, once:
// a serve restart before it records it then; one after it records nothing more.
func TestNeedsYouBlockDueRecordedOnce(t *testing.T) {
	root := logRoot(t)
	appendRecs(t, root, dispatched().submitted(1).gate(1, outFail).recs)
	count := func(p *proj) int {
		n := 0
		for _, f := range p.needsYou() {
			if f.Task == blockTask {
				n++
			}
		}
		return n
	}
	for pass := range 3 { // each pass a restart: the log replayed into a new project
		a, p := blockProjAt(t, root)
		mt := p.folded.loop[blockTask]
		if pass == 0 && count(p) != 0 {
			t.Fatal("precondition: the due block is recorded already")
		}
		s := a.next(p, mt)
		if pass == 0 && s == nil {
			t.Fatal("precondition: the due block has no step")
		}
		if s != nil {
			s(context.Background(), &Loop{Scratch: notLanded{}})
		}
		if n := count(p); n != 1 {
			t.Fatalf("pass %d: %d needs-you in force, want 1", pass, n)
		}
		p.log.Close()
	}
	_, recs := openLog(t, root)
	n := 0
	for _, r := range recs {
		if r.Step == event.StepNeedsYou {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d needs-you recorded over three restarts, want 1", n)
	}
}

// notLanded: no merge outside the app.
type notLanded struct{ port.Scratch }

func (notLanded) Landed(context.Context, string) (bool, error) { return false, nil }
