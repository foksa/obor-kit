package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/port"
)

// trustDialogScreen is a pane at Codex's trust dialog.
const trustDialogScreen = "> You are in /x\n  Do you trust the files in this folder?\n  Trust this folder? 1. Yes  2. No\n"

// trustFacts is p's trust needs-you facts in force.
func trustFacts(p *proj) []event.FactData {
	var out []event.FactData
	for _, f := range p.folded.active[event.TrustTask] {
		if f.Step == event.StepNeedsYou {
			out = append(out, f.Fact())
		}
	}
	return out
}

// screens is a fake terminal's screen per pane: a pane it has no screen for is not found.
type screens struct {
	mu   sync.Mutex
	text map[string]string
}

func (s *screens) set(pane, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text[pane] = text
}

func (s *screens) gone(pane string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.text, pane)
}

func (s *screens) Screen(_ context.Context, pane string, _ int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, ok := s.text[pane]
	if !ok {
		return "", notFoundErr{}
	}
	return text, nil
}

// clearTrust runs the trust rule once and its action when it holds: whether it ended a needs-you.
func clearTrust(t *testing.T, a *App, p *proj) bool {
	t.Helper()
	_, act, ok := trustRule(context.Background(), a, p, nil, a.opt.Now())
	if !ok {
		return false
	}
	if err := act.call(context.Background()); err != nil {
		t.Fatalf("the trust rule's action: %v", err)
	}
	return true
}

// A review start held by a trust dialog is a started review: review.started and the reviewer's brief are recorded,
// the open review is RV-n, no review needs-you, and the trust needs-you names the reviewer's pane (Started's), its
// reason the launch's hint. The
// needs rule lines it while the dialog shows; once the pane no longer shows it, the trust rule ends it (a replay
// keeps it ended), and nothing is lined again.
func TestReviewStartHeldByTrustDialog(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	sc := &screens{text: map[string]string{"w:rev": trustDialogScreen}}
	r.a.opt.Screen = sc.Screen
	r.agents.trust, r.agents.resume = "w:rev", true // a fresh start: held, not failed
	r.mustCall("r1", nil, "", false)
	r.pass()
	if len(r.agents.launches) != 1 {
		t.Fatalf("precondition: launches %+v, want the reviewer's one", r.agents.launches)
	}
	if n := r.facts(event.StepReviewStarted); n != 1 {
		t.Errorf("review started facts %d, want 1", n)
	}
	if r.p.out.Brief("pj-rev") == nil {
		t.Error("no brief recorded for pj-rev")
	}
	if ro := r.a.reviewOpen(r.p, loadCfg(t, r.p.root)); ro == nil || ro.ID != r.next {
		t.Errorf("open review %+v, want %s", ro, r.next)
	}
	if n := r.needs(); len(n) != 0 {
		t.Errorf("review needs-you %q, want none: the review started", n)
	}
	fs := trustFacts(r.p)
	if len(fs) != 1 || fs[0].Pane != "w:rev" || fs[0].Agent != "pj-rev" ||
		fs[0].Reason != agents.TrustHint(agents.Default, "pj-rev", "w:rev") {
		t.Fatalf("trust needs-you %+v, want one naming pj-rev's pane w:rev, its reason the launch's hint", fs)
	}
	if _, act, ok := needsRule(context.Background(), r.a, r.p, nil, r.now); !ok ||
		!strings.HasPrefix(act.line, "[obor] needs-you trust: pj-rev waits at a 'Trust this folder?' dialog in pane w:rev") {
		t.Errorf("the lead's line %q (ok %v), want the trust needs-you", act.line, ok)
	}

	if clearTrust(t, r.a, r.p) || len(trustFacts(r.p)) != 1 {
		t.Fatalf("the dialog still shows: trust needs-you %+v, want it kept", trustFacts(r.p))
	}
	sc.set("w:rev", "› Reading the brief\n  working (3s)")
	if !clearTrust(t, r.a, r.p) {
		t.Fatal("the trust rule did not hold once the pane stopped showing the dialog")
	}
	if fs := trustFacts(r.p); len(fs) != 0 {
		t.Errorf("trust needs-you %+v after the dialog was answered, want none", fs)
	}
	if _, act, ok := needsRule(context.Background(), r.a, r.p, nil, r.now.Add(needsAgain)); ok {
		t.Errorf("a needs-you line after the dialog was answered: %q", act.line)
	}
	r.reload()
	if fs := trustFacts(r.p); len(fs) != 0 || r.a.reviewOpen(r.p, loadCfg(t, r.p.root)) == nil {
		t.Errorf("after a replay: trust needs-you %+v, open review %v; want none, %s open", fs, r.a.reviewOpen(r.p, loadCfg(t, r.p.root)), r.next)
	}
}

// A review start that fails for another reason, a startup dialog that is no trust dialog among them, stays a failed
// start: the review's needs-you, no review.started, no trust needs-you.
func TestReviewStartNotReadyIsAFailedStart(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.agents.launchErr = &port.StartError{Err: errors.New("agent_not_ready"), NotReady: true,
		Hint: "pj-rev waits at a startup dialog in pane w:rev: answer it there"}
	r.mustCall("r1", nil, "", false)
	r.pass()
	if len(r.agents.launches) != 1 {
		t.Fatalf("precondition: launches %+v, want the reviewer's one", r.agents.launches)
	}
	if n := r.facts(event.StepReviewStarted); n != 0 {
		t.Errorf("review started facts %d, want none", n)
	}
	if n := r.needs(); len(n) != 1 || n[0] != r.next+": pj-rev waits at a startup dialog in pane w:rev: answer it there" {
		t.Errorf("review needs-you %q, want the failed start's", n)
	}
	if fs := trustFacts(r.p); len(fs) != 0 {
		t.Errorf("trust needs-you %+v, want none", fs)
	}
}

// A worker start (agent.start) held by a trust dialog is a start: its result recorded (the worker started, its work
// job delivered by the start's first prompt), then the trust needs-you naming the worker's pane; the needs-you ends
// once the pane is no longer there to show the dialog.
func TestWorkerStartHeldByTrustDialog(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: r.a.obs["w1:p2"].pane.Cwd, Agent: "pj-l-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	sc := &screens{text: map[string]string{"w1:p2": trustDialogScreen}}
	r.a.opt.Screen = sc.Screen
	f := &startEffects{concFakes: r.fakes["pj"], trust: "w1:p2", resume: true} // a fresh start: held, not failed
	fx := withAppCfg(r.a, &Loop{Agents: f, Prompter: f, Kit: r.kit})
	mt := *p.folded.loop["T-2"]
	if mt.started {
		t.Fatal("precondition: T-2's worker started already")
	}
	start := event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepStartWork}
	work := event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepWork}
	if !r.a.startWorker(context.Background(), p, fx, mt, start, startData{Agent: mt.agent, Pane: "w1:p2"}, work) {
		t.Fatal("the start held by the dialog failed")
	}
	if !p.log.Done(start) || !p.log.Done(work) || !p.folded.loop[mt.task].started {
		t.Errorf("start done %v, work job done %v, started %v; want the start and its work job recorded",
			p.log.Done(start), p.log.Done(work), p.folded.loop[mt.task].started)
	}
	if len(f.typed) != 0 {
		t.Errorf("typed %q into the pane at the dialog, want nothing", f.typed)
	}
	fs := trustFacts(p)
	if len(fs) != 1 || fs[0].Pane != "w1:p2" || fs[0].Agent != "pj-l-02" || !strings.Contains(fs[0].Reason, "in pane w1:p2") {
		t.Fatalf("trust needs-you %+v, want one naming pj-l-02's pane w1:p2", fs)
	}
	if clearTrust(t, r.a, p) {
		t.Fatal("the trust rule held while the dialog shows")
	}
	sc.gone("w1:p2")
	if !clearTrust(t, r.a, p) || len(trustFacts(p)) != 0 {
		t.Errorf("trust needs-you %+v once the pane is gone, want none", trustFacts(p))
	}
}

// The worker start rule's start held by a trust dialog is a start (the rule's action succeeds), then the trust
// needs-you of the worker's pane.
func TestWorkerStartRuleHeldByTrustDialog(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Agent = ""
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	p := h.proj()
	h.a.mu.Lock()
	p.dispatching = true
	p.folded.loop["T-1"] = &mtask{task: "T-1", lane: "L-01", agent: "pj-l-01", attempt: 1, dispSeq: 5, state: stWorking,
		work: "work", started: true}
	h.a.mu.Unlock()
	spy := &agentsSpy{trust: "w1:p1"}
	_, act, ok := workerStartRule(context.Background(), h.a, p, withAppCfg(h.a, &Loop{Agents: spy}), h.now)
	if !ok {
		t.Fatal("precondition: the rule does not hold")
	}
	if err := act.call(context.Background()); err != nil {
		t.Fatalf("the start held by the dialog failed the rule: %v", err)
	}
	if fs := trustFacts(p); len(fs) != 1 || fs[0].Pane != "w1:p1" || fs[0].Agent != "pj-l-01" {
		t.Errorf("trust needs-you %+v, want pj-l-01's of pane w1:p1", fs)
	}
}

// The trust needs-you is one per pane: a second start held in the same pane records none, and the dialog answered
// in one pane ends only that pane's.
func TestTrustNeedsPerPane(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	sc := &screens{text: map[string]string{"w:a": trustDialogScreen, "w:b": trustDialogScreen}}
	r.a.opt.Screen = sc.Screen
	r.a.trustNeeds(r.p, "pj-a", &trustHold{pane: "w:a"})
	r.a.trustNeeds(r.p, "pj-a", &trustHold{pane: "w:a"})
	r.a.trustNeeds(r.p, "pj-b", &trustHold{pane: "w:b"})
	if fs := trustFacts(r.p); len(fs) != 2 {
		t.Fatalf("trust needs-you %+v, want one per pane", fs)
	}
	sc.set("w:b", "idle")
	if !clearTrust(t, r.a, r.p) {
		t.Fatal("the trust rule did not hold for w:b")
	}
	if fs := trustFacts(r.p); len(fs) != 1 || fs[0].Pane != "w:a" {
		t.Errorf("trust needs-you %+v, want w:a's kept", fs)
	}
}

// The worker start rule's start that resumes the worker's session at a trust dialog is a failed start (launch's: the
// wake is not typed while the dialog is open): the rule's action fails, no trust needs-you, no start fact or job
// delivery recorded, and the rule holds again on a later pass (its retry).
func TestWorkerStartRuleResumeAtTrustDialogFails(t *testing.T) {
	t.Parallel()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.pane("w1:p1").Agent = ""
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	p := h.proj()
	h.a.mu.Lock()
	p.dispatching = true
	p.folded.loop["T-1"] = &mtask{task: "T-1", lane: "L-01", agent: "pj-l-01", attempt: 1, dispSeq: 5, state: stWorking,
		work: "work", started: true}
	h.a.mu.Unlock()
	before := p.log.LastSeq()
	spy := &agentsSpy{trust: "w1:p1", resume: true}
	_, act, ok := workerStartRule(context.Background(), h.a, p, withAppCfg(h.a, &Loop{Agents: spy}), h.now)
	if !ok {
		t.Fatal("precondition: the rule does not hold")
	}
	err := act.call(context.Background())
	if len(spy.launches) != 1 || spy.launches[0].Fresh {
		t.Fatalf("precondition: launches %+v, want one that is no --fresh start (a resume)", spy.launches)
	}
	if err == nil || !strings.Contains(err.Error(), "start failed in w1:p1") {
		t.Fatalf("the rule's action: %v, want the failed start", err)
	}
	if fs := trustFacts(p); len(fs) != 0 {
		t.Errorf("trust needs-you %+v, want none: the start failed", fs)
	}
	if n := p.log.LastSeq(); n != before {
		t.Errorf("records %d → %d, want none (no start or delivery recorded)", before, n)
	}
	if _, _, again := workerStartRule(context.Background(), h.a, p, withAppCfg(h.a, &Loop{Agents: spy}), h.now); !again {
		t.Error("the rule does not hold again after the failed start: no retry")
	}
}

// The integrator's later start of a job (start.review.2: its first start done, the integrator gone, no --fresh) that
// resumes its session at a trust dialog is a failed start: the job's step fails and counts it (fact failed of
// start.review.2), the start is not done, the job not delivered (no result), no trust needs-you. Its first start
// (fresh) at the dialog is a start that carried the wake, then the trust needs-you.
func TestIntegratorJobStartAtTrustDialog(t *testing.T) {
	t.Parallel()
	gate := func(r *concRig) {
		r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
		r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-1",
			SHA: "c", Target: "b", Patch: "p"}})
	}
	review := event.Key{Task: "T-1", Attempt: 1, Step: event.StepReview}
	t.Run("a later start, resumed: the failed start", func(t *testing.T) {
		t.Parallel()
		r := newConcRig(t)
		p := r.proj("pj")
		gate(r)
		r.add("pj", "T-1", "start.review", event.KindIntent, startData{Agent: "pj-int"})
		r.add("pj", "T-1", "start.review", event.KindResult, startOut{Line: "STARTED", Agent: "pj-int", Pane: "w1:p9"})
		delete(r.a.obs, "w1:p9")
		f := &startEffects{concFakes: r.fakes["pj"], trust: "w1:p9", resume: true}
		mt := *p.folded.loop["T-1"]
		if r.a.job(context.Background(), p, withAppCfg(r.a, &Loop{Agents: f, Prompter: f}), mt, event.StepReview) {
			t.Error("the job step succeeded, want the failed start")
		}
		if len(f.starts) != 1 || f.starts[0].Fresh {
			t.Fatalf("precondition: starts %+v, want one that is no --fresh start (a resume)", f.starts)
		}
		later := event.Key{Task: "T-1", Attempt: 1, Step: "start.review.2"}
		same(t, p.log.Done(later), false, "start.review.2 done")
		same(t, p.log.Done(review), false, "the review job delivered")
		same(t, deliveredOf(r, "T-1/1/review"), 0, "delivered facts of the job")
		r.a.mu.Lock()
		fails := p.folded.loop["T-1"].fails["start.review.2"].n
		r.a.mu.Unlock()
		same(t, fails, 1, "counted failures of start.review.2")
		if fs := trustFacts(p); len(fs) != 0 {
			t.Errorf("trust needs-you %+v, want none: the start failed", fs)
		}
		same(t, len(f.typed), 0, "wakes typed")
	})
	t.Run("the first start, fresh: started, the trust needs-you", func(t *testing.T) {
		t.Parallel()
		r := newConcRig(t)
		p := r.proj("pj")
		gate(r)
		delete(r.a.obs, "w1:p9")
		f := &startEffects{concFakes: r.fakes["pj"], trust: "w1:p9", resume: true}
		mt := *p.folded.loop["T-1"]
		if !r.a.job(context.Background(), p, withAppCfg(r.a, &Loop{Agents: f, Prompter: f}), mt, event.StepReview) {
			t.Fatal("the job step failed, want the start held by the dialog to carry the wake")
		}
		if len(f.starts) != 1 || !f.starts[0].Fresh {
			t.Fatalf("precondition: starts %+v, want one fresh", f.starts)
		}
		same(t, p.log.Done(event.Key{Task: "T-1", Attempt: 1, Step: "start.review"}), true, "start.review done")
		same(t, resultOf(p, review), `"sent (start prompt)"`, "the review job's result")
		fs := trustFacts(p)
		if len(fs) != 1 || fs[0].Pane != "w1:p9" || fs[0].Agent != "pj-int" ||
			fs[0].Reason != agents.TrustHint(agents.Default, "pj-int", "w1:p9") {
			t.Errorf("trust needs-you %+v, want pj-int's of pane w1:p9, its reason the launch's hint", fs)
		}
	})
}

// The trust guidance is one text, per kind: Claude's (whose default option is "No, exit") says to select the trust
// option and names no Enter; another kind's names no key either (no other default is established); the needs-you's
// reason is the launch's hint verbatim.
func TestTrustGuidance(t *testing.T) {
	t.Parallel()
	claude := agents.TrustHint("claude", "pj-rev", "w:rev")
	for _, h := range []string{claude, agents.TrustHint("codex", "pj-rev", "w:rev")} {
		if strings.Contains(h, "Enter") || !strings.Contains(h, "select its trust option") ||
			!strings.Contains(h, "pj-rev waits at a 'Trust this folder?' dialog in pane w:rev") {
			t.Errorf("hint %q, want the agent, the pane and 'select its trust option', no Enter", h)
		}
	}
	if !strings.Contains(claude, `not the default "No, exit"`) {
		t.Errorf("Claude's hint %q, want it to warn of its default", claude)
	}
	r := newReviewRig(t)
	r.a.trustNeeds(r.p, "pj-rev", &trustHold{pane: "w:rev", hint: claude})
	if fs := trustFacts(r.p); len(fs) != 1 || fs[0].Reason != claude {
		t.Errorf("trust needs-you %+v, want its reason the launch's hint %q", fs, claude)
	}
}
