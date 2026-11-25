// needs.go: the needs-you lines: the wait that raises one, and the rule that lines the lead about each in force.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"obor/internal/app/describe"
	"obor/internal/app/event"
	"obor/internal/app/outbox"
)

// msgWait is how long a line may wait for its delivery before it needs the owner.
const msgWait = 24 * time.Hour

// A needs-you line to the lead is posted again while the needs-you stays in force and unanswered: 30 min
// after the line's delivery, then every needsAgainEvery after each repeat's delivery (no repeat while a line
// of that needs-you waits undelivered). Constants, not config; the deadline is folded from the delivery's record.
const (
	needsAgain      = 30 * time.Minute
	needsAgainEvery = 60 * time.Minute
)

// msgNeedsYou keeps p's needs-you for lines waiting longer than msgWait: one for the oldest such line (a newer oldest
// replaces it), ended (resumed) once none waits.
func (a *App) msgNeedsYou(p *proj, now time.Time) {
	a.mu.Lock()
	reason := ""
	for _, q := range p.out.Queued() {
		if q.Msg().Fresh && q.Refused() != "" { // a fresh brief's stop or start failed: at once, naming the agent
			reason = fmt.Sprintf("fresh brief to %s from %s not started: %s (the next delivery tries again; obor brief %s "+
				"--fresh briefs it anew)", q.Msg().To, q.Msg().From, q.Refused(), q.Msg().To)
			break
		}
		if now.Sub(q.At()) >= msgWait {
			reason = fmt.Sprintf("line to %s from %s waiting over %s for its delivery: %s", q.Msg().To, q.Msg().From,
				describe.Age(msgWait), q.Msg().Line)
			if why := q.Why(); why != "" { // why the last pass held it (two panes claiming its agent among
				// them), or its last refusal
				reason += " (undelivered: " + why + ")"
			}
			break
		}
	}
	a.mu.Unlock()
	a.settleNeeds(p, event.MsgTask, reason)
}

// ruleNeeds is the needs-you line's rule in the rule pass (rules.go).
const ruleNeeds = outbox.RuleNeeds

// needsSpan: a needs-you line's did key is its needs-you's seq times needsSpan plus the line's number (0 the first,
// then repeat n), decimal so a log reads both (needs-you 412, repeat 3: 412000003).
const needsSpan = 1_000_000

// needsKey is the did key of line n of needs-you fact seq.
func needsKey(seq int64, n int) int64 { return seq*needsSpan + int64(n) }

// needsLined is how many lines of needs-you fact seq are recorded (under mu).
func (p *proj) needsLined(seq int64) int {
	n := 0
	for {
		if _, did := p.folded.did[event.Of{Kind: ruleNeeds, Seq: needsKey(seq, n)}]; !did {
			return n
		}
		n++
	}
}

// needsRule lines p's lead about each needs-you fact in force, naming the commands that answer it (the lead is
// the one who acts), and toasts the owner as the first line's best-effort after (a repeat does not toast again),
// and lines it again while it stays in force:
// 30 min after the first line's delivery, then 60 after each repeat's (the delivery's result, folded; never
// while its last line waits undelivered), the line ending in " (again, <its age>)". The needs-you of the project's
// own lines gets none (a lead line waiting would only queue another one), nor a closed task's watchdog fact (one
// older than its end's clear). Key: needsKey(the needs-you's seq, the line's number); the oldest needs-you due
// goes first, the next one in the next pass (the line's record wakes it).
func needsRule(_ context.Context, a *App, p *proj, _ *Loop, now time.Time) (int64, action, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.out.Lead() == "" {
		return 0, action{}, false
	}
	fs := p.needsYou()
	sort.Slice(fs, func(i, j int) bool { return fs[i].Seq < fs[j].Seq })
	for _, r := range fs {
		if r.Task == event.MsgTask || p.closed(r.Task) && !r.Fact().Merge {
			continue
		}
		n := p.needsLined(r.Seq)
		if n > 0 {
			last := event.Of{Kind: ruleNeeds, Seq: needsKey(r.Seq, n-1)}
			if p.out.Waits(last) {
				continue
			}
			base, ok := p.out.Delivered(last)
			if !ok { // no delivery recorded: the needs-you fact's time
				base = event.RecordTime(r)
			}
			wait := needsAgain
			if n > 1 {
				wait = needsAgainEvery
			}
			if now.Sub(base) < wait {
				continue
			}
		}
		act := action{line: p.needsLine(r, n, now)}
		if n == 0 { // the owner's toast rides the key's first line; a repeat does not toast again
			title, body := needsToast(p, r)
			act.after = func(ctx context.Context) { a.toast(ctx, title, body) }
		}
		return needsKey(r.Seq, n), act, true
	}
	return 0, action{}, false
}

// needsToast is the owner's herdr toast for needs-you fact r: the task and lane, or the project fact's reason alone.
func needsToast(p *proj, r event.Record) (title, body string) {
	d := r.Fact()
	body = describe.NeedsText(d)
	if !projectKey(r.Task) {
		body = fmt.Sprintf("%s in %s: %s", r.Task, d.Lane, body)
	}
	return "obor: " + p.name + " needs you", body
}

// needsYou is p's needs-you facts in force, the one source obor status, the needs rule and the view read: the
// watchdogs' and the project's (folded.active, the nightly's among them) and the merge loop's (a held block,
// mtask.needs).
func (p *proj) needsYou() []event.Record {
	var out []event.Record
	for _, fs := range p.folded.active {
		for _, r := range fs {
			if r.Step == event.StepNeedsYou {
				out = append(out, r)
			}
		}
	}
	for _, mt := range p.folded.loop {
		if mt.needs.held() {
			out = append(out, mt.needs.fact)
		}
	}
	return out
}

// needsLine is line n of needs-you fact r at now (under mu): "[obor] needs-you T-xx (<lane>): <reason> → <the
// commands that answer it, in its task's state now>", a blocked pane's question with where a person answers it
// (NeedsText), a repeat adding " (again, <its age>)".
func (p *proj) needsLine(r event.Record, n int, now time.Time) string {
	d := r.Fact()
	line := "[obor] needs-you " + r.Task
	if d.Lane != "" {
		line += " (" + d.Lane + ")"
	}
	line += ": " + describe.NeedsText(d)
	if ans := p.answerPath(r); ans != "" {
		line += " → " + ans
	}
	if n > 0 {
		line += " (again, " + describe.Age(now.Sub(event.RecordTime(r))) + ")"
	}
	return line
}

// oldLineOf is the did key of a needs-you's or a watch's line that carries no Of (its own field; under mu): needs,
// the needs-you's seq and the lines of it folded so far; watch, the watch's seq. It stays: logs keep lead lines
// recorded before lines carried Of, and the rules read their key at every replay.
func (p *proj) oldLineOf(data json.RawMessage) event.Of {
	var old struct {
		Needs int64 `json:"needs"`
		Watch int64 `json:"watch"`
	}
	_ = json.Unmarshal(data, &old)
	switch {
	case old.Needs != 0:
		return event.Of{Kind: ruleNeeds, Seq: needsKey(old.Needs, p.needsLined(old.Needs))}
	case old.Watch != 0:
		return event.Of{Kind: ruleWatch, Seq: old.Watch}
	}
	return event.Of{}
}
