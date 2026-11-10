// Package outbox is one project's lines to its agents (app's proj.out), its own package: the lines
// not delivered yet, the open briefs by role agent, each needs-you line's delivery time, and the fold of the records
// that change them (Fold). Its maps are unexported, so only this package names them: package app reads them through
// the queries (Lead, Brief, Briefs, Line, Queued, Waits, Delivered, Post, BriefStatus, MessageStatus) and delivers the
// lines (app's messages.go: deliver, route, deliverLine, deliverLead, recordBrief). Fold returns what a record means
// for the project's other state (Ended); app's foldMsg applies it in its fold order.
//
// No lock: an Outbox is part of package app's per-project state and is used under App.mu like the rest of it.
// It imports only the leaves event and wire, never package app.
package outbox

import (
	"encoding/json"
	"iter"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// StepDeliver is a line's delivery step: prompt intent/result (P) deliver.<seq>, the line of fact <seq> delivered
// (posted or typed).
const StepDeliver = "deliver."

// ViaInbox is fact failed's Via for a line whose refused delivery was an inbox post: the next attempt types it.
const ViaInbox = "inbox"

// RetiredResult is the result that closes a settled line's open delivery unsent (Closed).
const RetiredResult = "retired: its question was settled while its delivery was open"

// RuleNeeds is the needs-you line's rule in app's rule pass (rules.go): its lines' delivery times are kept
// (Delivered).
const RuleNeeds = "needs"

// Outbox is one project's lines to its agents: leadAgent, the lead's agent (config.AgentName), and leadKind, its kind
// as configured (its role's type, else agents.Default): its lines' inbox; msgs, the lines not delivered yet by their
// fact's seq; delivered, when each needs-you line was delivered, by its did key (the repeat's clock, folded from
// the delivery's result); briefs, the open briefs by role agent; posts, a delivery intent's members by its key's seq,
// its result not folded yet. A line's delivery claim (app's deliverLine: its state Sending) and a brief's
// supersede of the agent's undelivered brief lines (app's recordBrief: the choice and the append in one App.mu
// section) exclude each other.
type Outbox struct {
	leadAgent string
	leadKind  string
	msgs      map[int64]*Line
	delivered map[event.Of]time.Time
	briefs    map[string]*Brief
	posts     map[int64][]int64
}

// New is an empty outbox of the lead agent lead, its configured kind leadKind.
func New(lead, leadKind string) Outbox {
	return Outbox{leadAgent: lead, leadKind: leadKind, msgs: map[int64]*Line{}, delivered: map[event.Of]time.Time{},
		briefs: map[string]*Brief{}, posts: map[int64][]int64{}}
}

// Lead is the project's lead agent ("": none configured).
func (o *Outbox) Lead() string { return o.leadAgent }

// LeadKind is the lead's configured kind: a lead line's post when its pane is not identified.
func (o *Outbox) LeadKind() string { return o.leadKind }

// Brief is agent's open brief (nil: none).
func (o *Outbox) Brief(agent string) *Brief { return o.briefs[agent] }

// Briefs is the open briefs by role agent, in no order.
func (o *Outbox) Briefs() iter.Seq2[string, *Brief] { return maps.All(o.briefs) }

// Line is the undelivered line of fact seq (nil: none).
func (o *Outbox) Line(seq int64) *Line { return o.msgs[seq] }

// Post is the lines the open delivery intent of seq's key carries (none: no such intent, or its result is folded).
func (o *Outbox) Post(seq int64) []int64 { return o.posts[seq] }

// Delivered is when the rule's line with did key of was delivered (ok false: no delivery folded; only the needs-you
// lines' are kept).
func (o *Outbox) Delivered(of event.Of) (time.Time, bool) {
	at, ok := o.delivered[of]
	return at, ok
}

// Waits: the rule's line with did key of is queued, not delivered yet.
func (o *Outbox) Waits(of event.Of) bool {
	for _, q := range o.msgs {
		if q.msg.Of == of {
			return true
		}
	}
	return false
}

// Queued is the lines not delivered yet, oldest first.
func (o *Outbox) Queued() []*Line {
	out := make([]*Line, 0, len(o.msgs))
	for _, q := range o.msgs {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// Line is a line not delivered yet: its fact's seq and time, its data, whether it is a lead line (fact message,
// posted to the inbox whatever today's lead name; fact briefed: a role brief), whether it is a brief's line (fact
// briefed with a line: a newer brief ends it), whether it is an answer's line (fact answered: RetireAnswers ends it
// once its question is settled another way), where its delivery is (state), the seq of the delivery
// intent whose post carries it (0: none; only that post's result settles it, and a post refused before a byte
// goes again with exactly its members), and its last refusal before a byte went out (refused: the reason; refusedPost:
// it was an inbox post, so the next attempt to an idle identified agent types it; folded from fact failed). Those are
// the fold's. Held (why the last delivery pass held it; "": not held; not folded) and Sending (a deliverLine is
// sending it now) are the delivery's, live. obor status and the line's needs-you say why it waits (Why).
type Line struct {
	seq         int64
	at          time.Time
	msg         event.Message
	lead        bool
	brief       bool
	answer      bool
	state       State
	post        int64
	refused     string
	refusedPost bool

	Held    string
	Sending bool
}

// Seq is the line's fact's seq.
func (q *Line) Seq() int64 { return q.seq }

// At is the line's fact's time.
func (q *Line) At() time.Time { return q.at }

// Msg is the line's data.
func (q *Line) Msg() event.Message { return q.msg }

// IsLead: a lead line (fact message or stopped).
func (q *Line) IsLead() bool { return q.lead }

// IsBrief: a brief's line (fact briefed).
func (q *Line) IsBrief() bool { return q.brief }

// IsAnswer: an answer's line (fact answered).
func (q *Line) IsAnswer() bool { return q.answer }

// State is where the line's delivery is.
func (q *Line) State() State { return q.state }

// Post is the seq of the delivery intent whose post carries the line (0: none).
func (q *Line) Post() int64 { return q.post }

// Refused is the reason of the line's last refusal before a byte went out ("": none).
func (q *Line) Refused() string { return q.refused }

// RefusedPost: that refusal was an inbox post's.
func (q *Line) RefusedPost() bool { return q.refusedPost }

// Why is why q waits, as obor status and its needs-you say it: the last pass's hold, else its last refusal.
func (q *Line) Why() string {
	if q.Held != "" {
		return q.Held
	}
	if q.refused != "" {
		return "its delivery was refused before a byte went out: " + q.refused
	}
	return ""
}

// State is where a queued line's delivery is (one field, its moves all in Next). A line sent or retired
// leaves the outbox.
type State uint8

const (
	Waiting        State = iota // nothing went out: a later pass delivers it (held, or refused before a byte)
	Sending                     // its delivery is claimed or its intent is in the log: a byte may have gone out
	SendingSettled              // sending, and its question was settled meanwhile: a refusal before a byte retires it
	Sent                        // delivered (a byte went out, an error after it too)
	Retired                     // ended undelivered: its question settled another way, or a newer brief replaced it
)

// Event is what moves a queued line (Next).
type Event uint8

const (
	Claimed   Event = iota // a delivery claims it (app's deliverLine, leadPost), or its intent is folded
	Delivered              // its post's result: sent, also "sent: <error>" after a byte went out
	Refused                // fact failed: refused before any byte went out
	Settled                // its question was settled another way (RetireAnswers)
	Closed                 // its intent's result RetiredResult: closed unsent (a settled line's open delivery)
)

// Next is the state a line in s moves to on e (the case → test table: app's TestLineStateTable).
func (s State) Next(e Event) State {
	switch {
	case s == Sent || s == Retired: // ended: nothing moves it
		return s
	case e == Delivered:
		return Sent
	case e == Closed:
		return Retired
	case e == Claimed && s == Waiting:
		return Sending
	case e == Refused && s == SendingSettled: // nothing went out and it is wanted no more
		return Retired
	case e == Refused:
		return Waiting
	case e == Settled && s == Waiting:
		return Retired
	case e == Settled: // a byte may have gone out: it may have landed, so it stays until its outcome
		return SendingSettled
	}
	return s
}

// Move applies e to q: a line sent or retired leaves the outbox.
func (o *Outbox) Move(q *Line, e Event) {
	q.state = q.state.Next(e)
	if q.state == Sent || q.state == Retired {
		delete(o.msgs, q.seq)
	}
}

// Brief is a role agent's open brief: its fact's seq, when its line was delivered (else the fact's time), whether its
// pane worked since (its line delivered; since its stop, when it had one), whether that work is in the log (fact
// worked), its agent's stop on a usage limit in force (nil: none), and whether a turn that ended that stop is
// followed (its start seen, its end not yet: app's stopped.go turnDecide). Those are the fold's (worked also
// SawWork's). Logging (app's logWorked records its fact worked now: its claim, not folded) and where its agent was
// last seen (Term, PaneID, Lost; app's gone) are the delivery's, live.
type Brief struct {
	seq      int64
	at       time.Time
	worked   bool
	logged   bool
	stop     *RoleStop
	followed bool

	Logging bool
	Term    string    // the terminal (identity.TermOf) its agent was last identified in ("": not seen since replay)
	PaneID  string    // that pane's id then
	Lost    time.Time // since when its agent is not identified while connected (zero: identified, or not connected)
}

// Seq is the brief's fact's seq.
func (b *Brief) Seq() int64 { return b.seq }

// At is when the brief's line was delivered, else its fact's time: its age counts from it.
func (b *Brief) At() time.Time { return b.at }

// Worked: the brief's agent worked since its line was delivered (since its stop, when it had one).
func (b *Brief) Worked() bool { return b.worked }

// Logged: that work is in the log (fact worked).
func (b *Brief) Logged() bool { return b.logged }

// Followed: a turn that ended the brief's stop is followed (its start seen, its end not yet).
func (b *Brief) Followed() bool { return b.followed }

// Stop is a copy of the brief's agent's stop in force (nil: none).
func (b *Brief) Stop() *RoleStop {
	if b.stop == nil {
		return nil
	}
	s := *b.stop
	return &s
}

// RoleStop is a briefed role agent's stop on a usage limit (app's stopwake.go): its fact's seq and time, the lead's
// line, the transcript's turn it ended ("": its hook reported it), the reset (Unix seconds; 0: unknown), its first
// wake's time (zero: none yet) and whether its alarm line is recorded.
type RoleStop struct {
	Seq     int64
	At      time.Time
	Line    string
	Turn    string
	Reset   int64
	Woke    time.Time
	Alarmed bool
}

// SawWork notes the work seen of agent's open brief: working after the brief's line was delivered (or with no line)
// is the work the brief asked for (app's sawWork decides it is its agent's pane, working).
func (o *Outbox) SawWork(agent string) {
	b := o.briefs[agent]
	if b == nil {
		return
	}
	if q := o.msgs[b.seq]; q == nil || q.state != Waiting {
		b.worked = true
	}
}

// Woke folds a wake of agent's stopped brief at at (a continue result; app's foldWake): its stop's first wake.
func (o *Outbox) Woke(agent string, at time.Time) {
	if b := o.briefs[agent]; b != nil && b.stop != nil && b.stop.Woke.IsZero() && !at.Before(b.stop.At) {
		b.stop.Woke = at
	}
}

// Worked is fact worked's data: the briefed agent and the seq of the brief its work was for.
type Worked struct {
	To    string `json:"to"`
	Brief int64  `json:"brief"`
}

// LineData is a delivery intent's data: the line's agent (the post's target), and the seqs of the lines its post
// carries (none: its own line alone, one line with no batch).
type LineData struct {
	To    string  `json:"to"`
	Lines []int64 `json:"lines,omitempty"`
}

// members is the lines a delivery intent of seq's key carries.
func (d LineData) members(seq int64) []int64 {
	if len(d.Lines) == 0 {
		return []int64{seq}
	}
	return d.Lines
}

// Stopped is fact stopped's data: the agent, the error and its details, and, for a role agent, the lead's line
// (Message: to the lead, the line, the brief it ends), with the call that recorded it and its reply. A worker's: its
// open task (Message.Task) and the needs-you of it the fact puts in force (NeedsYou, a watchdog fact's data). A role
// agent's usage limit with its brief open: the brief it stops (Brief, its seq), the transcript's turn (Turn, Codex)
// and the reset (Reset, Unix seconds; 0: unknown).
type Stopped struct {
	Agent    string          `json:"agent"`
	Error    string          `json:"error"`
	Details  string          `json:"details,omitempty"`
	NeedsYou *event.FactData `json:"needs_you,omitempty"`
	Brief    int64           `json:"brief,omitempty"`
	Turn     string          `json:"turn,omitempty"`
	Reset    int64           `json:"reset,omitempty"`
	event.Message
}

// Turned is fact turned's data: the role agent, the seq of its brief, and whether the evidence was the turn's end
// (Worked: the brief's work is done, its finished line follows when idle; false: the turn has only begun, the stop in
// force ended, the brief follows the turn until its end; app's turnDecide).
type Turned struct {
	To     string `json:"to"`
	Brief  int64  `json:"brief"`
	Worked bool   `json:"worked,omitempty"`
}

// Ended is what a record folded into the outbox means for the project's other state, for app's foldMsg to apply in
// this order: Of, the did key of a rule's line (zero: none), at At, the record's time; Reply, the stored reply of the
// call that recorded it (nil: none); Msg, a message or briefed fact's data (its Drive tells a drive came back, its
// Review ends that review request: nil, another record); Stopped, a stopped fact's data (its worker's needs-you);
// Answered, the task whose question an answer ends ("": none); BriefEnd, the role agent whose brief the record ended
// ("": none). A role agent's stop's alarm (a message's Stop) is the outbox's own: folded here.
type Ended struct {
	Of       event.Of
	At       time.Time
	Reply    *Reply
	Msg      *event.Message
	Stopped  *Stopped
	Answered string
	BriefEnd string
}

// Reply is a call's stored reply: the socket method and the call id it is stored under, and the reply.
type Reply struct {
	Method, Call, Text string
}

// Fold folds r, a record of the project's lines (Task event.MsgTask; app's foldMsg retires the answers a record of
// another task settles first, RetireAnswers). oldOf is the did key of an older message (its own field,
// no Of; app's oldLineOf), read only for such a message.
func (o *Outbox) Fold(r event.Record, oldOf func(json.RawMessage) event.Of) Ended {
	if r.Task != event.MsgTask {
		return Ended{}
	}
	e := Ended{At: event.RecordTime(r)}
	switch {
	case r.Kind == event.KindFact && (r.Step == event.StepMessage || r.Step == event.StepBriefed):
		var m event.Message
		_ = json.Unmarshal(r.Data, &m)
		if m.Of.Kind == "" && r.Step == event.StepMessage {
			m.Of = oldOf(r.Data)
		}
		e.Of = m.Of // a rule's line: its did key (rules.go)
		if m.Call != "" {
			method := wire.MethodSend
			switch {
			case r.Step == event.StepBriefed:
				method = wire.MethodBrief
			case m.Driver: // obor tell-lead's
				method = wire.MethodTellLead
			}
			e.Reply = &Reply{Method: method, Call: m.Call, Text: m.Reply}
		}
		if m.Stop != 0 {
			o.roleAlarmed(m.Stop)
		}
		if m.Line != "" {
			o.msgs[r.Seq] = &Line{seq: r.Seq, at: e.At, msg: m, lead: r.Step == event.StepMessage,
				brief: r.Step == event.StepBriefed}
		}
		if r.Step == event.StepBriefed {
			o.briefs[m.To] = &Brief{seq: r.Seq, at: e.At}
		}
		for _, seq := range m.Supersedes { // delivered no more (its 24 h needs-you ends with it)
			delete(o.msgs, seq)
		}
		if m.Ends != "" {
			delete(o.briefs, m.Ends)
			e.BriefEnd = m.Ends // the brief's end
		}
		e.Msg = &m
	case r.Kind == event.KindFact && r.Step == event.StepStopped: // a role agent's: the lead's line, ending its brief; a
		// worker's: its task's needs-you (app's foldStop)
		var d Stopped
		_ = json.Unmarshal(r.Data, &d)
		e.Reply = &Reply{Method: wire.MethodStopped, Call: d.Call, Text: d.Reply}
		e.Stopped = &d
		if d.Line != "" {
			o.msgs[r.Seq] = &Line{seq: r.Seq, at: e.At, msg: d.Message, lead: true}
		}
		if d.Ends != "" {
			delete(o.briefs, d.Ends)
			e.BriefEnd = d.Ends // the stop that ended the brief
		}
		if b := o.briefs[d.Agent]; b != nil && d.Brief != 0 && b.seq == d.Brief { // a usage limit: the brief stays
			// open, stopped; the work before the stop is not its finish
			b.stop = &RoleStop{Seq: r.Seq, At: e.At, Line: d.Line, Turn: d.Turn, Reset: d.Reset}
			b.worked, b.logged, b.followed = false, false, false
		}
	case r.Kind == event.KindFact && r.Step == event.StepTurned:
		var d Turned
		_ = json.Unmarshal(r.Data, &d)
		if b := o.briefs[d.To]; b != nil && b.seq == d.Brief { // the turn's start: followed, its work not seen yet
			// (a lingering status of the stopped turn is not); its end: the work done
			b.stop = nil
			b.worked, b.logged, b.followed = d.Worked, d.Worked, !d.Worked
		}
	case r.Kind == event.KindFact && r.Step == event.StepAnswered:
		var m event.Message
		_ = json.Unmarshal(r.Data, &m)
		e.Reply = &Reply{Method: wire.MethodResume, Call: m.Call, Text: m.Reply}
		if !m.Driver { // a drive's answers answer no question: each is its own line
			o.RetireAnswers(m.Task, r.Seq) // a newer answer (or a stalled worker's resume) ends an older undelivered one
			e.Answered = m.Task
		}
		o.msgs[r.Seq] = &Line{seq: r.Seq, at: e.At, msg: m, answer: true}
	case r.Kind == event.KindFact && r.Step == event.StepWorked:
		var d Worked
		_ = json.Unmarshal(r.Data, &d)
		if b := o.briefs[d.To]; b != nil && b.seq == d.Brief { // an older brief's work does not arm a newer one
			b.worked, b.logged = true, true
		}
	case r.Kind == event.KindFact && r.Step == event.StepFailed: // refused before a byte: its reason kept
		d := r.Fact()
		seq, _ := strconv.ParseInt(strings.TrimPrefix(d.Step, StepDeliver), 10, 64)
		if q := o.msgs[seq]; q != nil {
			q.refused, q.refusedPost = strings.TrimPrefix(d.Reason, d.Step+": "), d.Via == ViaInbox
			o.Move(q, Refused)
		}
	case strings.HasPrefix(r.Step, StepDeliver): // a post's result settles exactly the lines its intent names
		seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, StepDeliver), 10, 64)
		switch r.Kind {
		case event.KindIntent:
			var d LineData
			_ = json.Unmarshal(r.Data, &d)
			var members []int64
			for _, m := range d.members(seq) {
				if q := o.msgs[m]; q != nil && q.post == 0 { // a line delivered already is no member
					q.post = seq
					o.Move(q, Claimed)
					members = append(members, m)
				}
			}
			if len(members) > 0 {
				o.posts[seq] = members
			}
		case event.KindResult:
			members, ok := o.posts[seq]
			if !ok {
				members = []int64{seq}
			}
			delete(o.posts, seq)
			for _, m := range members {
				if q := o.msgs[m]; q != nil && (q.post == seq || q.post == 0 && m == seq) {
					o.settleLine(q, r)
				}
			}
		}
	}
	return e
}

// RetireAnswers settles task's undelivered answer lines but keep's: their question was settled another
// way (app's settles, or a newer answer or a stalled worker's resume came), so a line is never delivered late and obor
// status lists it no more. A waiting line retires; a sending one may have landed and stays until its outcome (as a
// brief's does): sent, or retired by a refusal before a byte or by its open delivery's close (app's deliverLine).
// Folded live and on replay alike.
func (o *Outbox) RetireAnswers(task string, keep int64) {
	for seq, q := range o.msgs {
		if q.answer && q.msg.Task == task && seq != keep {
			o.Move(q, Settled)
		}
	}
}

// settleLine folds r, the result of the post that carried q, as q's delivery, or as its close unsent (RetiredResult).
func (o *Outbox) settleLine(q *Line, r event.Record) {
	var res string
	if _ = json.Unmarshal(r.Data, &res); res == RetiredResult {
		o.Move(q, Closed)
		return
	}
	if q.msg.Of.Kind == RuleNeeds { // a needs-you's line delivered: its next repeat counts from now
		o.delivered[q.msg.Of] = event.RecordTime(r)
	}
	if b := o.briefs[q.msg.To]; b != nil && b.seq == q.seq { // the brief's line delivered: its age starts
		b.at = event.RecordTime(r)
	}
	o.Move(q, Delivered)
}

// roleAlarmed marks the role agent's stop seq as having its alarm line (a message fact's Stop).
func (o *Outbox) roleAlarmed(seq int64) {
	for _, b := range o.briefs {
		if b.stop != nil && b.stop.Seq == seq {
			b.stop.Alarmed = true
		}
	}
}

// BriefStatus is the open briefs as obor status lists them, by agent: a brief whose line is still queued is not
// delivered.
func (o *Outbox) BriefStatus() []wire.BriefStatus {
	var out []wire.BriefStatus
	for agent, b := range o.briefs {
		_, queued := o.msgs[b.seq]
		out = append(out, wire.BriefStatus{To: agent, Since: b.at.Unix(), Delivered: !queued, Worked: b.worked})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].To < out[j].To })
	return out
}

// MessageStatus is the lines not delivered yet as obor status lists them, at now.
func (o *Outbox) MessageStatus(now time.Time) []wire.MessageStatus {
	out := []wire.MessageStatus{}
	for _, q := range o.Queued() {
		out = append(out, wire.MessageStatus{To: q.msg.To, From: q.msg.From, Line: q.msg.Line,
			Age: int64(now.Sub(q.at) / time.Second), Held: q.Why()})
	}
	return out
}
