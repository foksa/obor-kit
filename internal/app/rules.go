// rules.go: the rule pass (T-866; docs/design.md "Change rules" 1, the rule tier). Serve recomputes each rule's
// condition from the current state on every merge-loop pass and acts: a line, one idempotent call, or a best-effort
// after the line (T-949). A rule is done when its action's effect shows in state; only a line records did{key} (its
// message fact's Of). A rule has no intent, crash class, replay case, claim or fold of its own; its written failure
// mode: after a crash an action may run twice, or one pass late.
package app

import (
	"context"
	"errors"
	"obor/internal/app/event"
	"strconv"
	"time"
)

// rule is a name and its condition: when, at now, is the record the rule is keyed by and its action (ok false: the
// condition does not hold). when runs not under mu: it takes the lock for what it reads. The action runs once the
// condition has held with the same key for settle (counted from the first pass that saw it, and again from an action
// that succeeded; a restart counts again), and never for a key whose line is recorded.
type rule struct {
	name   string
	settle time.Duration
	when   func(ctx context.Context, a *App, p *proj, fx *Loop, now time.Time) (key int64, act action, ok bool)
}

// action is a rule's effect: call (nil: none), then the lead's line, recorded with Of{the rule's name, its key} ("":
// none), then after, a best-effort effect whose failure is logged and dropped (the owner's toast, T-949). A failed
// call records no line, so the next pass runs both again; after runs only once the line is recorded, so it never
// delays or fails it.
type action struct {
	call  func(ctx context.Context) error
	line  string
	after func(ctx context.Context)
}

// ruleLive is a rule's state in this process: the key its condition holds with and since when, and its failed
// actions in a row.
type ruleLive struct {
	key   int64
	since time.Time
	fails int
}

// ruleFails is how many failed actions in a row make a rule's needs-you.
const ruleFails = 3

// rules are serve's rules, in the order a pass runs them.
var rules = []rule{
	{name: ruleNeeds, when: needsRule},
	{name: ruleStopAlarm, when: stopAlarmRule},
	{name: ruleWatch, when: watchRule},
	{name: ruleReviewOnMerge, when: reviewOnMergeRule}, // before idle: the request it records is work in flight
	{name: ruleVerify, when: verifyRule},               // before idle: the brief it records is work in flight
	{name: ruleIdle, settle: idleSettle, when: idleRule},
	{name: ruleIdlePing, when: idlePingRule}, // its line went after the idle line's settle
	{name: ruleIntegratorStop, when: integratorStopRule},
	{name: ruleReviewerStop, when: reviewerStopRule},
	{name: ruleWorkerStart, settle: workerStartSettle, when: workerStartRule},
	{name: ruleHandoff, when: handoffRule}, // a keep lane's agent past handoff_tokens between tasks (T-1031)
}

// rulePass runs each of rs on the project once (the merge loop's goroutine, after the pass's lines, on the pass's
// adapters fx): a rule whose condition has held for its settle acts; a failed action counts, and ruleFails in a row put
// one needs-you naming the first such rule in force (task key rule), ended once no rule fails that often.
func (a *App) rulePass(ctx context.Context, rs []rule, fx *Loop) {
	a.mu.Lock()
	p := a.p
	run := p != nil && p.merging
	a.mu.Unlock()
	if !run {
		return
	}
	failing, now := "", a.opt.Now()
	for _, r := range rs {
		key, act, ok := r.when(ctx, a, p, fx, now)
		a.mu.Lock()
		live, seen := p.rules[r.name]
		if !seen || live.key != key {
			live = ruleLive{key: key, since: now}
		}
		of := event.Of{Kind: r.name, Seq: key}
		_, did := p.folded.did[of]
		due := ok && !did && now.Sub(live.since) >= r.settle
		lead := p.out.lead()
		a.mu.Unlock()
		fails := 0
		if due {
			if err := a.act(ctx, p, lead, of, act); err != nil {
				fails = live.fails + 1
				a.logf("%s rule %s (key %d): %v", p.name, r.name, key, err)
			} else {
				live.since = now // its settle again before the next: the snapshot may not show the effect yet (T-874)
			}
		}
		live.fails = fails
		a.mu.Lock()
		if ok {
			p.rules[r.name] = live
		} else {
			delete(p.rules, r.name)
		}
		a.mu.Unlock()
		if fails >= ruleFails && failing == "" {
			failing = r.name
		}
	}
	reason := ""
	if failing != "" {
		reason = "rule " + failing + " failed " + strconv.Itoa(ruleFails) + " passes in a row (serve's log names the errors)"
	}
	a.settleNeeds(p, event.RuleTask, reason)
}

// act runs a rule's action: its call, then its line to lead with the back-reference of, then its best-effort after.
// Only the call and the line can fail it.
func (a *App) act(ctx context.Context, p *proj, lead string, of event.Of, act action) error {
	if act.call != nil {
		if err := act.call(ctx); err != nil {
			return err
		}
	}
	if act.line != "" {
		if err := a.lineTo(p, lead, of, act.line); err != nil {
			return err
		}
	}
	if act.after != nil {
		act.after(ctx)
	}
	return nil
}

// lineLead is a rule's line to p's lead, recorded with the back-reference of (a rule's did key).
func (a *App) lineLead(p *proj, of event.Of, line string) error {
	a.mu.Lock()
	lead := p.out.lead()
	a.mu.Unlock()
	return a.lineTo(p, lead, of, line)
}

// lineTo records line to lead with the back-reference of and wakes the loop, which delivers it.
func (a *App) lineTo(p *proj, lead string, of event.Of, line string) error {
	if lead == "" {
		return errors.New("no lead to line")
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
		event.Message{To: lead, From: "obor", Line: line, Of: of}) {
		return errors.New("the event log refused the line")
	}
	a.wake() // the next pass delivers it
	return nil
}
