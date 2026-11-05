package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"time"
)

// Idle (T-803, T-815, T-866): serve says when the pipeline empties, and the lead answers. Two rules (rules.go) carry
// it, keyed by the merged or dropped record that emptied the pipeline (idleNow's end): once nothing has been in flight
// for idleSettle, the lead's line ("[obor] idle: nothing in flight (last: T-xx merged <sha7>) — dispatch next, or obor
// idle \"<why nothing>\""); idlePingAfter after that line, the ping (the line again) and the owner's herdr
// notification. Each goes once per key, neither once the lead acknowledged the key: obor idle "<why>" records fact
// idle.ack{key, why}. Work in the settle window (a verify the merge loop requests seconds after the last merge, a review
// request made just then, T-859) restarts it; a new emptying is a new key, with its own line.

// idlePingAfter is how long after its line an unacknowledged idle state waits before the ping.
const idlePingAfter = 30 * time.Minute

// idleSettle is how long the pipeline must stay empty before the idle line goes out (T-859).
const idleSettle = time.Minute

// The idle rules' names: their lines' Of kinds.
const (
	ruleIdle     = "idle"
	ruleIdlePing = "idle.ping"
)

// idleAck is fact idle.ack's data (obor idle "<why>"): the merged or dropped record's seq whose emptying it answers,
// the why, who said it, and the call with its reply.
type idleAck struct {
	Key   int64  `json:"key,omitempty"`
	Why   string `json:"why,omitempty"`
	By    string `json:"by,omitempty"`
	Call  string `json:"call,omitempty"`
	Reply string `json:"reply,omitempty"`
}

// idle is idleNow's answer: what ended the pipeline last ("T-xx merged <sha7>", "T-xx dropped: <why>"), that fact's
// seq and time, and whether nothing is in flight — the pipeline is empty, no needs-you is in force, no review request
// is pending, no verify waits for its brief and no role worktree agent (the reviewer, the deep dive) holds one.
type idle struct {
	last string
	end  int64
	at   time.Time
	ok   bool
}

// foldIdle folds r, the lead's acknowledgement of an idle state.
func (p *proj) foldIdle(r event.Record) {
	if r.Task != event.IdleTask || r.Kind != event.KindFact {
		return
	}
	switch r.Step {
	case event.StepIdle: // compat until v0.28: an idle epoch fact (before T-866) folds to nothing
	case event.StepIdleAck: // one from before T-866 names an epoch, no key: it acknowledges nothing
		var d idleAck
		_ = json.Unmarshal(r.Data, &d)
		if d.Call != "" {
			p.folded.calls[verdictStep(wire.MethodIdle, d.Call)] = d.Reply
		}
		if d.Key != 0 {
			p.folded.idleAck = d
		}
	}
}

// idleNow is the pipeline's state now (under mu): idle with the last end's line and fact seq when nothing is in
// flight, no needs-you is in force, no review request or verify is pending and no reviewer or deep dive holds a brief.
// A task merged but still on its after-merge steps is in flight; a rejected one is no cause of idle (its needs-you ends
// it).
func (p *proj) idleNow() idle {
	var last string
	var end int64
	var at time.Time
	for _, task := range p.loopTasks() {
		mt := p.folded.loop[task]
		switch mt.state {
		case stMerged:
			if mt.after < len(afterSteps) && !mt.needs.held() {
				return idle{}
			}
			if mt.mergedSeq > end {
				end, last, at = mt.mergedSeq, idleMerged(mt), mt.endedAt
			}
		case stDropped:
			if mt.droppedSeq > end {
				end, last, at = mt.droppedSeq, task+" dropped: "+oneLine(mt.dropped), mt.endedAt
			}
		case stRejected:
			// ended without a merge: the owner's (its needs-you or its drop), never an idle cause
			return idle{}
		default:
			return idle{}
		}
	}
	// A review request is work until it ends (skills/lead/commands.md: a review request ends idle, T-818), and a pass
	// starting one is in flight from its claim to the reviewer's brief (its fire fact ends the request first).
	if p.folded.review != nil || p.rv.what != "" {
		return idle{}
	}
	// A verify requested (or admitted: the fold still has it requested, T-856) is in flight until its brief, and so is
	// a failed one its fix round requests again (T-858): its tasks are not verified until it is done.
	if p.folded.rv.waiting() {
		return idle{}
	}
	// A rule's needs-you (rules.go) is serve's failure, not work: it would end the idle state its rule acts on.
	if end == 0 || slices.ContainsFunc(p.needsYou(), func(r event.Record) bool { return r.Task != event.RuleTask }) {
		return idle{}
	}
	for _, w := range p.wtAgentsNow() { // an open brief to the reviewer or the deep dive is work in flight
		if p.out.briefs[w.agent] != nil {
			return idle{}
		}
	}
	return idle{last: last, end: end, at: at, ok: true}
}

// idleOpen is the idle state the lead has not acknowledged (under mu): idleNow's answer, ok when nothing is in flight,
// the project has a lead and no acknowledgement names its key.
func (p *proj) idleOpen() (idle, bool) {
	n := p.idleNow()
	return n, n.ok && p.out.lead() != "" && p.folded.idleAck.Key != n.end
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
// answers the state: IDLE …, or NOT IDLE. Nothing idle: NOT IDLE, nothing recorded.
func (a *App) idleCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New("idle: a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodIdle, c)
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
	why := oneLine(c.Note)
	a.mu.Lock()
	n := p.idleNow()
	shown := p.idleText(n)
	a.mu.Unlock()
	switch {
	case why == "":
		return shown, nil
	case !n.ok:
		return "NOT IDLE", nil
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

// idleText is the idle state n as obor idle shows it (under mu): NOT IDLE, the acknowledgement's why, or the
// unacknowledged state with when it began.
func (p *proj) idleText(n idle) string {
	switch {
	case !n.ok:
		return "NOT IDLE"
	case p.folded.idleAck.Key == n.end:
		return "IDLE: " + p.folded.idleAck.Why
	}
	return "IDLE, not acknowledged since " + n.at.Local().Format("Jan 2 15:04") + " (last: " + n.last + ")"
}
