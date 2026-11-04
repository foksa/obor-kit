package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strconv"
	"strings"
	"time"
)

// Idle (T-803, T-815): serve says once when the pipeline empties, and the lead answers.
//
// The state is the store's (docs/state-model.md "Lines to the lead"): fact idle{Last, End} on the task key idle marks
// the pipeline empty since then (End is the merged or dropped fact it follows), and fact idle.ack{Epoch, Why} is the
// lead's acknowledgement of that epoch. Idle is an epoch: it ends when anything makes idleNow false — a dispatch, a
// waiting dispatch, a needs-you, a review request (T-818), a hold agent's open brief (the reviewer's or the deep
// dive's, T-815) — and a new one begins, one line and unacknowledged, when the pipeline is empty again, whatever ended
// the last one. An acknowledgement of an ended epoch is refused. A replay derives it all; a restart with an
// unacknowledged idle state pings again at most once.
//
// On the transition serve posts one line to the lead ("[obor] idle: nothing in flight (last: T-xx merged <sha7>) —
// dispatch next, or obor idle \"<why nothing>\""), after a watch line firing for the same merge (notify records the
// watch lines first). Unacknowledged after idlePingAfter, one more line and a herdr notification reach the owner;
// after that only obor status and obor web show the state.

// idlePingAfter is how long an idle state may wait unacknowledged before the ping.
const idlePingAfter = 30 * time.Minute

// idleNotify is the prefix of the notification's intent key, "<idleNotify><the idle fact's seq>".
const idleNotify = "notify."

// idleData is an idle fact's data (StepIdle: Last and End; StepIdleAck: Epoch, Why, and the call's reply fields).
type idleData struct {
	Last  string `json:"last,omitempty"`  // "T-xx merged <sha7>", "T-xx dropped: <why>"
	End   int64  `json:"end,omitempty"`   // the merged or dropped fact's seq this idle state follows
	Epoch int64  `json:"epoch,omitempty"` // an ack's: the idle fact's seq it answers
	Why   string `json:"why,omitempty"`   // the acknowledgement's why nothing is in flight
	By    string `json:"by,omitempty"`
	Call  string `json:"call,omitempty"`
	Reply string `json:"reply,omitempty"`
}

// idleState is one idle epoch: its fact's seq and time, what ended the pipeline, whether a pass recorded the first
// line and the ping's (folded from their message facts), the lead's acknowledgement, and whether anything ended the
// epoch since (ended: no longer the state in force).
type idleState struct {
	seq    int64
	at     time.Time
	last   string
	end    int64
	ended  bool
	lined  bool
	pinged bool
	ack    bool
	why    string
}

// idle is idleNow's answer: what ended the pipeline last ("T-xx merged <sha7>", "T-xx dropped: <why>"), that fact's
// seq, and whether nothing is in flight — the pipeline is empty, no needs-you is in force, no review request is
// pending and no hold agent holds a brief.
type idle struct {
	last string
	end  int64
	ok   bool
}

// foldIdle folds r, a record of the project's idle state (T-803, T-815).
func (p *proj) foldIdle(r event.Record) {
	switch {
	case r.Task == event.IdleTask && r.Kind == event.KindFact && r.Step == event.StepIdle:
		var d idleData
		_ = json.Unmarshal(r.Data, &d)
		p.folded.idle = &idleState{seq: r.Seq, at: event.RecordTime(r), last: d.Last, end: d.End}
	case r.Task == event.IdleTask && r.Kind == event.KindFact && r.Step == event.StepIdleAck:
		var d idleData
		_ = json.Unmarshal(r.Data, &d)
		if d.Call != "" {
			p.folded.calls[verdictStep(wire.MethodIdle, d.Call)] = d.Reply
		}
		// An acknowledgement names the epoch it read: one of an ended epoch, or of a later one, acknowledges nothing.
		if st := p.folded.idle; st != nil && !st.ended && (d.Epoch == 0 || d.Epoch == st.seq) {
			st.ack, st.why = true, d.Why
		}
	}
	// Idle is an epoch (T-815): anything that makes the pipeline not idle, or a later end, ends the recorded epoch, so
	// the next idle transition begins a new one — a new line, unacknowledged — rather than reviving the last one.
	if st := p.folded.idle; st != nil && !st.ended {
		if n := p.idleNow(); !n.ok || n.end != st.end {
			st.ended = true
		}
	}
}

// idleNow is the pipeline's state now (under mu): idle with the last end's line and fact seq when nothing is in
// flight, no needs-you is in force, no review request is pending and no reviewer or deep dive holds a brief. A task
// merged but still on its after-merge steps is in flight; a rejected one is no cause of idle (its needs-you ends it).
func (p *proj) idleNow() idle {
	var last string
	var end int64
	for _, task := range p.loopTasks() {
		mt := p.folded.loop[task]
		switch mt.state {
		case stMerged:
			if mt.after < len(afterSteps) && !mt.needs.held() {
				return idle{}
			}
			if mt.mergedSeq > end {
				end, last = mt.mergedSeq, idleMerged(mt)
			}
		case stDropped:
			if mt.droppedSeq > end {
				end, last = mt.droppedSeq, task+" dropped: "+oneLine(mt.dropped)
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
	if end == 0 || len(p.needsYou()) > 0 {
		return idle{}
	}
	for _, h := range p.holdAgentsNow() {
		if p.out.briefs[h.agent] != nil {
			return idle{}
		}
	}
	return idle{last: last, end: end, ok: true}
}

// idleEpoch is the idle epoch in force now (under mu), idleNow's n given: the recorded state while the pipeline is
// still empty after the same end; nil when it has work again, a later end began, or the fold saw the epoch end.
func (p *proj) idleEpoch(n idle) *idleState {
	st := p.folded.idle
	if st == nil || st.ended || !n.ok || n.end != st.end {
		return nil
	}
	return st
}

// idleLines announces the idle state once and pings an unacknowledged one after idlePingAfter: the lead's line (and
// later the ping's, with the owner's notification). A pass records the watch lines before this one, so a watch line
// firing for the same merge comes first. Nothing is recorded when the log refuses it: the next pass tries again.
func (a *App) idleLines(ctx context.Context) {
	a.mu.Lock()
	p := a.p
	if p == nil || !p.merging || p.out.lead() == "" {
		a.mu.Unlock()
		return
	}
	now := a.opt.Now()
	cur := p.idleNow()
	st := p.idleEpoch(cur)
	newState, first, ping := false, false, false
	switch {
	case !cur.ok:
	case st == nil: // no epoch recorded, or the last one ended: a new epoch begins, with its own line
		newState, first = true, true
	default:
		first = !st.lined
		ping = !st.ack && st.lined && !st.pinged && now.Sub(st.at) >= idlePingAfter
	}
	if cur.ok {
		for _, w := range p.folded.watches { // a watch's line is being recorded: it goes first, the idle line next pass
			if w.lining {
				a.mu.Unlock()
				return
			}
		}
	}
	a.mu.Unlock()

	if newState {
		if !a.record(p, event.Record{Task: event.IdleTask, Step: event.StepIdle, Kind: event.KindFact},
			idleData{Last: cur.last, End: cur.end}) {
			return
		}
	}
	if first {
		a.mu.Lock()
		var m event.Message
		if st := p.folded.idle; st != nil {
			m = event.Message{To: p.out.lead(), From: "obor", Line: idleLine(st.last), Idle: st.seq}
		}
		a.mu.Unlock()
		if m.Line == "" {
			return
		}
		if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
			return
		}
		a.wake()
	}
	if ping {
		a.mu.Lock()
		var m event.Message
		if st := p.folded.idle; st != nil {
			m = event.Message{To: p.out.lead(), From: "obor", Line: idleLine(st.last) + " (again, " + Age(now.Sub(st.at)) + ")",
				Idle: st.seq, IdlePing: true}
		}
		a.mu.Unlock()
		if m.Line == "" {
			return
		}
		if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
			return
		}
		a.wake()
	}
	// The ping's notification reaches the owner once per idle epoch, also after a restart: its intent is folded by
	// the replay, and an attempt that failed is made again on the next pass.
	a.mu.Lock()
	st = p.idleEpoch(p.idleNow())
	a.mu.Unlock()
	if st != nil && st.pinged && !st.ack && a.idleShown(ctx, p, st) {
		a.wake()
	}
}

// idleShown shows the owner the idle ping's herdr notification once (the intent under the idle fact's seq); a failed
// one is tried again at the next pass.
func (a *App) idleShown(ctx context.Context, p *proj, st *idleState) bool {
	key := event.Key{Task: event.IdleTask, Step: idleNotify + strconv.FormatInt(st.seq, 10)}
	a.mu.Lock()
	done := p.log.Done(key)
	a.mu.Unlock()
	if done {
		return false
	}
	_, err := a.do(ctx, p, event.Intent{Key: key, Class: event.ClassFact}, func(ctx context.Context) (any, error) {
		if err := a.panes.Notify(ctx, "obor: "+p.name+" is idle", strings.TrimPrefix(idleLine(st.last), "[obor] ")); err != nil {
			return nil, err
		}
		return "shown", nil
	})
	if err != nil {
		a.logf("%s idle: the owner not notified: %v", p.name, err)
	}
	return err == nil
}

// endIdleNotify settles an open notification intent of an idle state its lead acknowledged: nothing is sent any more.
func (a *App) endIdleNotify(p *proj, seq int64) {
	key := event.Key{Task: event.IdleTask, Step: idleNotify + strconv.FormatInt(seq, 10)}
	a.mu.Lock()
	open := p.log.Open(key)
	a.mu.Unlock()
	if !open {
		return
	}
	_, _ = a.do(a.life, p, event.Intent{Key: key, Class: event.ClassFact},
		func(context.Context) (any, error) { return "not sent: the lead acknowledged idle", nil })
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
// force (fact idle.ack, recorded with the time); a call id is required and a repeated one gets the stored reply. With
// no text it answers the state: IDLE …, or NOT IDLE. Nothing in force: NOT IDLE, nothing recorded.
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
	st := p.idleEpoch(p.idleNow())
	shown := idleText(st)
	seq := int64(0)
	if st != nil {
		seq = st.seq
	}
	a.mu.Unlock()
	if why == "" {
		return shown, nil
	}
	if st == nil {
		return "NOT IDLE", nil
	}
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	d := idleData{Epoch: seq, Why: why, By: by, Call: c.CallID, Reply: "ACKED idle: " + why}
	if !a.record(p, event.Record{Task: event.IdleTask, Step: event.StepIdleAck, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("idle: the event log of %s refused it", p.name)
	}
	a.endIdleNotify(p, seq)
	return d.Reply, nil
}

// idleText is the idle state as obor idle shows it (under mu): NOT IDLE, the acknowledgement's why, or the
// unacknowledged state with when it began.
func idleText(st *idleState) string {
	switch {
	case st == nil:
		return "NOT IDLE"
	case st.ack:
		return "IDLE: " + st.why
	}
	return "IDLE, not acknowledged since " + st.at.Local().Format("Jan 2 15:04") + " (last: " + st.last + ")"
}
