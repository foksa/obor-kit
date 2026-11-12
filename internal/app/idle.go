package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"time"
)

// Idle: serve says when the pipeline empties, and the lead answers. Two rules (rules.go)
// carry it, keyed by the newest record that ended work the pipeline waited on (idleNow's end: a merge, a drop, a
// finished brief, a verify's end, the last after-merge step, a review request's end): once nothing has been in flight
// for idleSettle, the lead's line ("[obor] idle: nothing in flight (last: T-xx merged <sha7>) — dispatch next, or obor
// idle \"<why nothing>\""); idlePingAfter after that line, the ping (the line again) and the owner's herdr
// notification. Each goes once per key, neither once the lead acknowledged the key: obor idle "<why>" records fact
// idle.ack{key, why}. Work in the settle window (a verify the merge loop requests seconds after the last merge, a review
// request made just then) restarts it; a new end is a new key, with its own line (a long verify's or
// brief's end after the merge is one, so an acknowledgement of the merge before it does not cover it).

// idlePingAfter is how long after its line an unacknowledged idle state waits before the ping.
const idlePingAfter = 30 * time.Minute

// idleSettle is how long the pipeline must stay empty before the idle line goes out.
const idleSettle = time.Minute

// The idle rules' names: their lines' Of kinds.
const (
	ruleIdle     = "idle"
	ruleIdlePing = "idle.ping"
)

// idleAck is fact idle.ack's data (obor idle "<why>"): the end record's seq whose emptying it answers,
// the why, who said it, and the call with its reply.
type idleAck struct {
	Key   int64  `json:"key,omitempty"`
	Why   string `json:"why,omitempty"`
	By    string `json:"by,omitempty"`
	Call  string `json:"call,omitempty"`
	Reply string `json:"reply,omitempty"`
}

// idleEnd is one record that ended work the pipeline waited on: its seq (the idle key) and its time (the age's
// start).
type idleEnd struct {
	seq int64
	at  time.Time
}

// idle is idleNow's answer: the last merge or drop as the line names it ("T-xx merged <sha7>", "T-xx dropped: <why>"),
// the newest end record's seq (the idle key) and time, and whether nothing is in flight — the pipeline is empty, no
// needs-you is in force, no review request is pending, no verify waits for its brief and no role worktree agent (the
// reviewer, the deep dive) holds one. why is the first condition that said no, as the refusal names it ("T-12 open
// (L-02)", "RV-4 verify pending"; "" while idle).
type idle struct {
	last string
	end  int64
	at   time.Time
	ok   bool
	why  string
}

// foldIdle folds r, the lead's acknowledgement of an idle state.
func (p *proj) foldIdle(r event.Record) {
	if r.Task != event.IdleTask || r.Kind != event.KindFact || r.Step != event.StepIdleAck {
		return
	}
	var d idleAck
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodIdle, d.Call)] = d.Reply
	}
	if d.Key != 0 {
		p.folded.idleAck = d
	}
}

// idleNow is the pipeline's state now (under mu): idle with the last merge or drop's line, the newest end record's seq
// (the idle key) and time when nothing is in flight, no needs-you is in force, no review request or verify is pending
// and no reviewer or deep dive holds a brief. A task merged but still on its after-merge steps is in flight; a rejected
// one is no cause of idle (its needs-you ends it). The end is the newest of the records that ended what it waited on:
// a merge, a drop, the last after-merge step, a role worktree agent's finished or stopped brief, a
// review request's end (every end: a failed --after end, a --rm or a firing). Not idle: why names the first condition
// that said no.
func (p *proj) idleNow() idle {
	var last string
	var merge idleEnd // the newest merge or drop: what the line names
	for _, task := range p.loopTasks() {
		mt := p.folded.loop[task]
		switch mt.state {
		case stMerged:
			if mt.after < len(mt.afterList()) && !mt.needs.held() {
				return idle{why: task + " after-merge steps pending"}
			}
			if e := (idleEnd{mt.mergedSeq, mt.endedAt}); e.seq > merge.seq {
				merge, last = e, idleMerged(mt)
			}
		case stDropped:
			if e := (idleEnd{mt.droppedSeq, mt.endedAt}); e.seq > merge.seq {
				merge, last = e, task+" dropped: "+oneLine(mt.dropped)
			}
		case stRejected:
			// ended without a merge: the owner's (its needs-you or its drop), never an idle cause
			return idle{why: task + " rejected"}
		default:
			return idle{why: openTaskText(mt)}
		}
	}
	// A review request is work until it ends (skills/lead/commands.md: a review request ends idle), and a pass
	// starting one is in flight from its claim to the reviewer's brief (its fire fact ends the request first).
	if why := p.reviewIdleText(); why != "" {
		return idle{why: why}
	}
	// A verify owed (the verify rule's condition) is in flight until its brief: its tasks are not verified yet.
	if p.verifyDue {
		if id := p.folded.rv.Last().ID; id != "" {
			return idle{why: id + " verify pending"}
		}
		return idle{why: "verify pending"}
	}
	if merge.seq == 0 {
		return idle{why: "no merge or drop yet"}
	}
	// A rule's needs-you (rules.go) is serve's failure, not work: it would end the idle state its rule acts on.
	for _, r := range p.needsYou() {
		if r.Task == event.RuleTask {
			continue
		}
		why := r.Task + " needs you"
		if lane := r.Fact().Lane; lane != "" {
			why += " (" + lane + ")"
		}
		return idle{why: why}
	}
	for _, w := range p.wtAgentsNow() { // an open brief to the reviewer or the deep dive is work in flight
		if p.out.Brief(w.agent) != nil {
			return idle{why: w.agent + " holds a brief"}
		}
	}
	end, _ := p.idleEnd()
	return idle{last: last, end: end.seq, at: end.at, ok: true}
}

// openTaskText is mt, a task in flight (not merged, dropped or rejected), as the idle refusal names it: its id with
// its lane.
func openTaskText(mt *mtask) string {
	if mt.lane == "" {
		return mt.task + " open"
	}
	return mt.task + " open (" + mt.lane + ")"
}

// reviewIdleText is the review request in flight as the idle refusal names it ("" when none): the review it starts, or
// the one a pass is starting now (under mu).
func (p *proj) reviewIdleText() string {
	if p.folded.review == nil && p.rv.what == "" {
		return ""
	}
	id := p.folded.rv.Next().ID
	if p.folded.review == nil { // the request fired: the review it started is open now
		if open := p.folded.rv.Last().Open(); open != "" {
			id = open
		}
	}
	return id + " review request pending"
}

// idleEnd is the newest record that ended what the pipeline waited on (under mu): idleNow's key once nothing is in
// flight, whether or not it is now; and the task whose record it is ("": a review request's or a brief's end, or
// none). The prune keeps that task whole: its pruned replay would move the key back to its merged fact.
func (p *proj) idleEnd() (idleEnd, string) {
	var end idleEnd
	by := ""
	newer := func(e idleEnd, task string) {
		if e.seq > end.seq {
			end, by = e, task
		}
	}
	for task, mt := range p.folded.loop {
		switch mt.state {
		case stMerged:
			newer(idleEnd{mt.mergedSeq, mt.endedAt}, task)
			newer(idleEnd{mt.afterSeq, mt.afterAt}, task)
		case stDropped:
			newer(idleEnd{mt.droppedSeq, mt.endedAt}, task)
		}
	}
	newer(p.folded.reviewIdleEnd, "")
	for _, w := range p.wtAgentsNow() {
		newer(p.folded.briefEnds[w.agent], "")
	}
	return end, by
}

// idleOpen is the idle state the lead has not acknowledged (under mu): idleNow's answer, ok when nothing is in flight,
// the project has a lead and no acknowledgement names its key.
func (p *proj) idleOpen() (idle, bool) {
	n := p.idleNow()
	return n, n.ok && p.out.Lead() != "" && p.folded.idleAck.Key != n.end
}

// idleRule is the idle line's rule: the line once per key while the state is open.
func idleRule(_ context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool) {
	a.mu.Lock()
	n, ok := p.idleOpen()
	a.mu.Unlock()
	return n.end, action{line: idleLine(n.last)}, ok
}

// idlePingRule is the ping's rule: idlePingAfter after the key's line, still open, the owner's notification and the
// line again.
func idlePingRule(_ context.Context, a *App, p *proj, _ *Loop, now time.Time) (int64, action, bool) {
	a.mu.Lock()
	n, ok := p.idleOpen()
	lined, did := p.folded.did[event.Of{Kind: ruleIdle, Seq: n.end}]
	a.mu.Unlock()
	if !ok || !did || now.Sub(lined) < idlePingAfter {
		return 0, action{}, false
	}
	title, body := "obor: "+p.name+" is idle", strings.TrimPrefix(idleLine(n.last), "[obor] ")
	return n.end, action{line: idleLine(n.last) + " (again, " + Age(now.Sub(lined)) + ")",
		call: func(ctx context.Context) error { return a.panes.Notify(ctx, title, body) }}, true
}

// idleLine is the lead's line for the idle state that followed last.
func idleLine(last string) string {
	return `[obor] idle: nothing in flight (last: ` + last + `) — dispatch next, or obor idle "<why nothing>"`
}

// idleMerged is mt's end as the idle line names it: "T-xx merged <sha7>", or a merge outside the app.
func idleMerged(mt *mtask) string {
	if mt.head == "" {
		return mt.task + " merged (outside obor serve)"
	}
	return mt.task + " merged " + mt.head[:min(7, len(mt.head))]
}

// oneLine is s with its lines joined by spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// idleCall answers the socket's idle request (the lead or the owner): with a text, it acknowledges the idle state in
// force (fact idle.ack{key, why}); a call id is required and a repeated one gets the stored reply. With no text it
// answers the state: IDLE …, or NOT IDLE: <why>, the first condition that keeps it busy. Nothing idle: the refusal,
// nothing recorded.
func (a *App) idleCall(c wire.IdleReq) (string, error) {
	if c.CallID == "" {
		return "", errors.New("idle: a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodIdle, c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodIdle, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("idle: the lead or the owner answers it, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	why := oneLine(c.Why)
	a.mu.Lock()
	n := p.idleNow()
	shown := p.idleText(n)
	a.mu.Unlock()
	switch {
	case why == "":
		return shown, nil
	case !n.ok:
		return "NOT IDLE: " + n.why, nil
	}
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	d := idleAck{Key: n.end, Why: why, By: by, Call: c.CallID, Reply: "ACKED idle: " + why}
	if !a.record(p, event.Record{Task: event.IdleTask, Step: event.StepIdleAck, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("idle: the event log of %s refused it", p.name)
	}
	return d.Reply, nil
}

// idleText is the idle state n as obor idle shows it (under mu): NOT IDLE and why, the acknowledgement's why, or the
// unacknowledged state with when it began.
func (p *proj) idleText(n idle) string {
	switch {
	case !n.ok:
		return "NOT IDLE: " + n.why
	case p.folded.idleAck.Key == n.end:
		return "IDLE: " + p.folded.idleAck.Why
	}
	return "IDLE, not acknowledged since " + n.at.Local().Format("Jan 2 15:04") + " (last: " + n.last + ")"
}
