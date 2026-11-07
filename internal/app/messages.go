package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"sort"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/store"
)

// Lines to a project's agents (T-466; docs/state-model.md row 47): the two kinds the lead still gets, a brief's, and
// an answer's (obor resume <T-xx> "<text>": fact answered{to: the task's worker, line "[obor answer] <text>"}, T-511;
// answer.go).
//   - obor send <project> "<text>" (a lead or the owner of another project; the project by a known name or a path,
//     T-646): fact message{to: the lead, line "[obor <sender project>] <text> (reply: obor send <sender> "…")"} in the
//     target project's log, recorded by the serve holding it.
//   - obor brief <role-agent> "<text>": fact briefed{to: the role agent, line "[obor <caller>] <text>"}; a review's
//     fresh start records the same fact without a line (its first prompt carried the brief).
//     A new brief of that agent also ends its undelivered brief lines (T-575: a newer brief makes an older
//     undelivered one obsolete; lead lines and needs-you lines are never superseded, and a line whose delivery is in
//     flight may have landed and stays): its fact names their seqs (supersedes) and its fold retires them, live and on
//     replay, so the brief and its supersede are one append (T-580; an older log's fact superseded{seq, by} still
//     retires its line).
//     Once that agent's pane has worked after the brief (after its line was delivered) and is idle again, serve records
//     message{to: the lead, line "[obor] <agent> finished (briefed <age> ago)", ends: the agent}: the brief is over.
//
// The first work serve sees after a brief is recorded as fact worked{to, brief: the brief's seq} as soon as the
// observation's caller releases mu (a herdr event, a snapshot, a fresh start's brief; each delivery pass retries a
// failed write), so a restart keeps it: an agent that finished while serve was down gets its finished line on
// reconnect (RV-50 #3). The finished line's age counts from when the brief's line was delivered (or the fresh start).
//
// deliver posts each line to the lead into its Claude session's inbox socket (T-485: recorded by its SessionStart
// hook; no pane, no idle rule, nothing typed into the pane the owner writes in) and delivers each role agent's line
// when its pane is idle (Ready: the rule of job delivery, no Escape, no screen read) as the pane's kind takes it
// (T-499, posted): a kind with an inbox (agents.Caps.Inbox) gets it posted to its session's inbox like the lead's when
// that inbox is the live session's in its pane (liveSession), else typed into its pane as its job wakes are (T-746: a
// line to an idle agent is never held on a missing or mismatched record); any other kind gets it typed into its pane
// (the herdr call of job delivery). A line held (its agent busy, not identified; a lead line with no inbox record)
// notes why (queued.held: obor status, the line's needs-you). The oldest first and one per agent per pass, under
// deliver.<seq> (P, a wake's transport: prompt.go).
//
// A queued line is in one state (T-748, queued.state; its moves: lineState.next): waiting (nothing went out; why: the
// pass's hold, else its last refusal's reason), sending (its delivery claimed or its intent in the log: a byte may
// have gone out), sent and retired (both leave the outbox). A refusal before any byte went out (no inbox record, a
// refused dial, a refused typing) is fact failed{step: deliver.<seq>, reason, via: inbox for a post}: the line waits
// again with that reason (obor status, its needs-you) and is delivered again on a later pass, also after a restart
// (RV-50 #1), typed when the refused one was a post to an idle identified agent (a lead line is posted again); a post
// serve's own stop cut before a byte records nothing (T-660) and waits the same. An answer's line retires once its
// question is settled another way (retireAnswers): waiting, at once; sending, it is marked, and then a refusal before a
// byte retires it, a delivery sends it, and a later pass closes its open delivery unsent (result retiredResult). A
// delivery a crash cut (its intent pending) is delivered again too: a line may reach its agent twice, never not at
// all. A typed line herdr refuses because the pane is blocked (blocked, agent_blocked) is such a refusal,
// retried on a later pass; a posted one cannot land in a dialog at all (a session behind an open permission dialog reads it
// when its turn continues, and it cannot answer the dialog). A line waiting longer than msgWait is the project's
// needs-you until no such line waits.

const (
	stepDeliver = "deliver." // prompt intent/result (P) deliver.<seq>: the line of fact <seq> delivered (posted or typed)
)

// msgWait is how long a line may wait for its delivery before it needs the owner.
const msgWait = 24 * time.Hour

// A needs-you line to the lead is posted again while the needs-you stays in force and unanswered (T-621): 30 min
// after the line's delivery, then every needsAgainEvery after each repeat's delivery (T-624: no repeat while a line
// of that needs-you waits undelivered). Constants, not config; the deadline is folded from the delivery's record.
const (
	needsAgain      = 30 * time.Minute
	needsAgainEvery = 60 * time.Minute
)

// outbox is one project's lines to its agents (proj.out; T-610): leadAgent, the lead's agent (config.AgentName), and
// leadKind, its kind as configured (its role's type, else agents.Default): its lines' inbox; msgs, the lines not
// delivered yet by their fact's seq; delivered, when each needs-you line was delivered, by its did key (T-624: the
// repeat's clock, folded from the delivery's result); briefs, the open briefs by role agent. Its fields are under
// App.mu like the rest of proj; only this file names its maps (the other files read it through lead, brief and
// allBriefs). A line's delivery claim (deliverLine: its state sending) and a brief's supersede of the agent's
// undelivered brief lines (recordBrief: the choice and the append in one App.mu section) exclude each other (RV-61 #3).
type outbox struct {
	leadAgent string
	leadKind  string
	msgs      map[int64]*queued
	delivered map[event.Of]time.Time
	briefs    map[string]*brief
	posts     map[int64][]int64 // a delivery intent's members by its key's seq, its result not folded yet (T-654)
}

// newOutbox is an empty outbox of the lead agent lead, its configured kind leadKind.
func newOutbox(lead, leadKind string) outbox {
	return outbox{leadAgent: lead, leadKind: leadKind, msgs: map[int64]*queued{}, delivered: map[event.Of]time.Time{},
		briefs: map[string]*brief{}, posts: map[int64][]int64{}}
}

// lead is the project's lead agent ("": none configured).
func (o *outbox) lead() string { return o.leadAgent }

// brief is agent's open brief (nil: none); its fields are under App.mu.
func (o *outbox) brief(agent string) *brief { return o.briefs[agent] }

// allBriefs is the open briefs by role agent, in no order (under App.mu).
func (o *outbox) allBriefs() iter.Seq2[string, *brief] { return maps.All(o.briefs) }

// queued is a line not delivered yet: its fact's seq and time, its data, whether it is a lead line (fact message,
// posted to the inbox whatever today's lead name; fact briefed: a role brief), whether it is a brief's line (fact
// briefed with a line: a newer brief ends it), whether it is an answer's line (fact answered: retireAnswers ends it
// once its question is settled another way, T-746), where its delivery is (state, T-748), the seq of the delivery
// intent whose post carries it (0: none; T-654: only that post's result settles it, and a post refused before a byte
// goes again with exactly its members), why the last delivery pass held it ("": not held; T-746: not folded), its last
// refusal before a byte went out (refused: the reason; refusedPost: it was an inbox post, so the next attempt to an
// idle identified agent types it; folded from fact failed), and whether a deliverLine is sending it now (sending:
// live). obor status and the line's needs-you say why it waits (why).
type queued struct {
	seq         int64
	at          time.Time
	msg         event.Message
	lead        bool
	brief       bool
	answer      bool
	state       lineState
	post        int64
	held        string
	refused     string
	refusedPost bool
	sending     bool
}

// lineState is where a queued line's delivery is (T-748: one field, its moves all in next; RV-90). A line sent or
// retired leaves the outbox.
type lineState uint8

const (
	lineWaiting        lineState = iota // nothing went out: a later pass delivers it (held, or refused before a byte)
	lineSending                         // its delivery is claimed or its intent is in the log: a byte may have gone out
	lineSendingSettled                  // sending, and its question was settled meanwhile: a refusal before a byte retires it
	lineSent                            // delivered (a byte went out, an error after it too)
	lineRetired                         // ended undelivered: its question settled another way, or a newer brief replaced it
)

// lineEvent is what moves a queued line (next).
type lineEvent uint8

const (
	lineClaimed   lineEvent = iota // a delivery claims it (deliverLine, leadPost), or its intent is folded
	lineDelivered                  // its post's result: sent, also "sent: <error>" after a byte went out
	lineRefused                    // fact failed: refused before any byte went out
	lineSettled                    // its question was settled another way (retireAnswers)
	lineClosed                     // its intent's result "retired": closed unsent (a settled line's open delivery)
)

// next is the state a line in s moves to on e (T-748; the case → test table: messagestate_test.go).
func (s lineState) next(e lineEvent) lineState {
	switch {
	case s == lineSent || s == lineRetired: // ended: nothing moves it
		return s
	case e == lineDelivered:
		return lineSent
	case e == lineClosed:
		return lineRetired
	case e == lineClaimed && s == lineWaiting:
		return lineSending
	case e == lineRefused && s == lineSendingSettled: // nothing went out and it is wanted no more
		return lineRetired
	case e == lineRefused:
		return lineWaiting
	case e == lineSettled && s == lineWaiting:
		return lineRetired
	case e == lineSettled: // a byte may have gone out: it may have landed, so it stays until its outcome (as T-575)
		return lineSendingSettled
	}
	return s
}

// move applies e to q (under mu): a line sent or retired leaves the outbox.
func (p *proj) move(q *queued, e lineEvent) {
	q.state = q.state.next(e)
	if q.state == lineSent || q.state == lineRetired {
		delete(p.out.msgs, q.seq)
	}
}

// why is why q waits, as obor status and its needs-you say it: the last pass's hold, else its last refusal.
func (q *queued) why() string {
	if q.held != "" {
		return q.held
	}
	if q.refused != "" {
		return "its delivery was refused before a byte went out: " + q.refused
	}
	return ""
}

// viaInbox is fact failed's Via for a line whose refused delivery was an inbox post (T-748): the next attempt types it.
const viaInbox = "inbox"

// retiredResult is the result that closes a settled line's open delivery unsent (lineClosed).
const retiredResult = "retired: its question was settled while its delivery was open"

// brief is a role agent's open brief: its fact's seq, when its line was delivered (else the fact's time), whether its
// pane worked since (its line delivered; since its stop, when it had one), whether that work is in the log (fact
// worked; logging: being recorded), its agent's stop on a usage limit in force (nil: none; T-569), whether a turn
// that ended that stop is followed (its start seen, its end not yet: T-571, stopped.go turnDecide), and where its agent
// was last seen (gone, T-732).
type brief struct {
	seq      int64
	at       time.Time
	worked   bool
	logged   bool
	logging  bool // logWorked records its fact worked now (its claim, not folded)
	stop     *roleStop
	followed bool
	term     string    // the terminal (termOf) its agent was last identified in ("": not seen since replay; T-732)
	paneID   string    // that pane's id then
	lost     time.Time // since when its agent is not identified while connected (zero: identified, or not connected)
}

// roleStop is a briefed role agent's stop on a usage limit (stopwake.go): its fact's seq and time, the lead's line, the
// transcript's turn it ended ("": its hook reported it), the reset (Unix seconds; 0: unknown), its first wake's time
// (zero: none yet) and whether its alarm line is recorded.
type roleStop struct {
	seq     int64
	at      time.Time
	line    string
	turn    string
	reset   int64
	woke    time.Time
	alarmed bool
}

// workedData is fact worked's data: the briefed agent and the seq of the brief its work was for.
type workedData struct {
	To    string `json:"to"`
	Brief int64  `json:"brief"`
}

// lineData is a delivery intent's data: the line's agent (the post's target), and the seqs of the lines its post
// carries (T-654; none: its own line alone, as before T-654 and for a single line).
type lineData struct {
	To    string  `json:"to"`
	Lines []int64 `json:"lines,omitempty"`
}

// members is the lines a delivery intent of seq's key carries.
func (d lineData) members(seq int64) []int64 {
	if len(d.Lines) == 0 {
		return []int64{seq}
	}
	return d.Lines
}

// foldMsg folds r, a record of the project's lines.
func (p *proj) foldMsg(r event.Record) {
	if settles(r) { // the question is settled another way: its answer ends (T-746, T-748)
		p.retireAnswers(r.Task, 0)
	}
	if r.Task != event.MsgTask {
		return
	}
	switch {
	case r.Kind == event.KindFact && (r.Step == event.StepMessage || r.Step == event.StepBriefed):
		var m event.Message
		_ = json.Unmarshal(r.Data, &m)
		if m.Of.Kind == "" && r.Step == event.StepMessage {
			m.Of = p.oldLineOf(r.Data)
		}
		if m.Of.Kind != "" { // a rule's line: its did key (rules.go, T-866)
			p.folded.did[m.Of] = event.RecordTime(r)
		}
		if m.Call != "" {
			method := wire.MethodSend
			switch {
			case r.Step == event.StepBriefed:
				method = wire.MethodBrief
			case m.Driver: // obor tell-lead's (T-761)
				method = wire.MethodTellLead
			}
			p.folded.calls[verdictStep(method, m.Call)] = m.Reply
		}
		if m.Drive != 0 {
			p.foldDriveLine(m)
		}
		if m.Stopped != "" {
			p.foldStopLine(m)
		}
		if m.Review != 0 {
			// T-906: a request ended by a failed --after still ends here, but only --rm and a firing count as review
			// ends that suppress the on-merge rule (review.on.merge). T-909: idle counts this end too (its own marker).
			p.reviewEnded(m.Review)
			p.folded.reviewIdleEnd = idleEnd{r.Seq, event.RecordTime(r)}
		}
		if m.Stop != 0 {
			p.roleAlarmed(m.Stop)
		}
		if m.Line != "" {
			p.out.msgs[r.Seq] = &queued{seq: r.Seq, at: event.RecordTime(r), msg: m, lead: r.Step == event.StepMessage,
				brief: r.Step == event.StepBriefed}
		}
		if r.Step == event.StepBriefed {
			p.out.briefs[m.To] = &brief{seq: r.Seq, at: event.RecordTime(r)}
		}
		for _, seq := range m.Supersedes { // delivered no more (its 24 h needs-you ends with it)
			delete(p.out.msgs, seq)
		}
		if m.Ends != "" {
			delete(p.out.briefs, m.Ends)
			p.folded.briefEnds[m.Ends] = idleEnd{r.Seq, event.RecordTime(r)} // T-868: the brief's end
		}
	case r.Kind == event.KindFact && r.Step == event.StepStopped: // a role agent's: the lead's line, ending its brief; a worker's:
		// its task's needs-you (stopped.go)
		var d stoppedData
		_ = json.Unmarshal(r.Data, &d)
		p.folded.calls[verdictStep(wire.MethodStopped, d.Call)] = d.Reply
		p.foldStop(r, d)
		if d.Line != "" {
			p.out.msgs[r.Seq] = &queued{seq: r.Seq, at: event.RecordTime(r), msg: d.Message, lead: true}
		}
		if d.Ends != "" {
			delete(p.out.briefs, d.Ends)
			p.folded.briefEnds[d.Ends] = idleEnd{r.Seq, event.RecordTime(r)} // T-868: the stop that ended the brief
		}
		if b := p.out.briefs[d.Agent]; b != nil && d.Brief != 0 && b.seq == d.Brief { // a usage limit: the brief stays
			// open, stopped; the work before the stop is not its finish
			b.stop = &roleStop{seq: r.Seq, at: event.RecordTime(r), line: d.Line, turn: d.Turn, reset: d.Reset}
			b.worked, b.logged, b.followed = false, false, false
		}
	case r.Kind == event.KindFact && r.Step == event.StepTurned:
		var d turnedData
		_ = json.Unmarshal(r.Data, &d)
		if b := p.out.briefs[d.To]; b != nil && b.seq == d.Brief { // the turn's start: followed, its work not seen yet
			// (a lingering status of the stopped turn is not); its end: the work done
			b.stop = nil
			b.worked, b.logged, b.followed = d.Worked, d.Worked, !d.Worked
		}
	case r.Kind == event.KindFact && r.Step == event.StepAnswered:
		var m event.Message
		_ = json.Unmarshal(r.Data, &m)
		p.folded.calls[verdictStep(wire.MethodResume, m.Call)] = m.Reply
		if !m.Driver { // a drive's answers (T-761) answer no question: each is its own line
			p.retireAnswers(m.Task, r.Seq) // a newer answer (or a stalled worker's resume) ends an older undelivered one
			p.answered(m.Task)
		}
		p.out.msgs[r.Seq] = &queued{seq: r.Seq, at: event.RecordTime(r), msg: m, answer: true}
	case r.Kind == event.KindFact && r.Step == event.StepWorked:
		var d workedData
		_ = json.Unmarshal(r.Data, &d)
		if b := p.out.briefs[d.To]; b != nil && b.seq == d.Brief { // an older brief's work does not arm a newer one
			b.worked, b.logged = true, true
		}
	case r.Kind == event.KindFact && r.Step == event.StepFailed: // refused before a byte: its reason kept (T-748)
		d := r.Fact()
		seq, _ := strconv.ParseInt(strings.TrimPrefix(d.Step, stepDeliver), 10, 64)
		if q := p.out.msgs[seq]; q != nil {
			q.refused, q.refusedPost = strings.TrimPrefix(d.Reason, d.Step+": "), d.Via == viaInbox
			p.move(q, lineRefused)
		}
	case strings.HasPrefix(r.Step, stepDeliver): // T-654: a post's result settles exactly the lines its intent names
		seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, stepDeliver), 10, 64)
		switch r.Kind {
		case event.KindIntent:
			var d lineData
			_ = json.Unmarshal(r.Data, &d)
			var members []int64
			for _, m := range d.members(seq) {
				if q := p.out.msgs[m]; q != nil && q.post == 0 { // a line delivered already is no member
					q.post = seq
					p.move(q, lineClaimed)
					members = append(members, m)
				}
			}
			if len(members) > 0 {
				p.out.posts[seq] = members
			}
		case event.KindResult:
			members, ok := p.out.posts[seq]
			if !ok {
				members = []int64{seq}
			}
			delete(p.out.posts, seq)
			for _, m := range members {
				if q := p.out.msgs[m]; q != nil && (q.post == seq || q.post == 0 && m == seq) {
					p.settleLine(q, r)
				}
			}
		}
	}
}

// settles: r settles its task's question another way (T-746, T-748): the worker submitted or asked again, the task
// was unblocked, dropped or rejected.
func settles(r event.Record) bool {
	if r.Task == event.MsgTask {
		return false
	}
	if r.Kind == event.KindResult {
		m, _ := verdictMethod(r.Step)
		return m == wire.MethodReject
	}
	return r.Kind == event.KindFact && (r.Step == event.StepSubmitted || r.Step == event.StepUnblocked ||
		r.Step == event.StepDropped || isAsk(r))
}

// retireAnswers settles task's undelivered answer lines but keep's (under mu; T-746, T-748): their question was
// settled another way (settles, or a newer answer or a stalled worker's resume came), so a line is never delivered
// late and obor status lists it no more. A waiting line retires; a sending one may have landed and stays until its
// outcome (as T-575 keeps a brief's): sent, or retired by a refusal before a byte or by its open delivery's close
// (deliverLine). Folded live and on replay alike.
func (p *proj) retireAnswers(task string, keep int64) {
	for seq, q := range p.out.msgs {
		if q.answer && q.msg.Task == task && seq != keep {
			p.move(q, lineSettled)
		}
	}
}

// settleLine folds r, the result of the post that carried q, as q's delivery, or as its close unsent (retiredResult).
func (p *proj) settleLine(q *queued, r event.Record) {
	var res string
	if _ = json.Unmarshal(r.Data, &res); res == retiredResult {
		p.move(q, lineClosed)
		return
	}
	if q.msg.Of.Kind == ruleNeeds { // a needs-you's line delivered: its next repeat counts from now (T-624)
		p.out.delivered[q.msg.Of] = event.RecordTime(r)
	}
	if b := p.out.briefs[q.msg.To]; b != nil && b.seq == q.seq { // the brief's line delivered: its age starts
		b.at = event.RecordTime(r)
	}
	p.move(q, lineDelivered)
}

// sawWork notes pane's status for an open brief of the agent whoIs finds in it (no label read, RV-53 #4): working
// after the brief's line was delivered (or with no line) is the work the brief asked for (under mu, after reconcile).
func (a *App) sawWork(p *proj, pane port.Pane) {
	agent, ok := p.reg.whoIs(pane)
	b := p.out.briefs[agent]
	if !ok || b == nil || pane.Agent == "" || Ready(pane.Status) {
		return
	}
	if q := p.out.msgs[b.seq]; q == nil || q.state != lineWaiting {
		b.worked = true
	}
}

// Poster posts a line into agent's session through its inbox socket (T-485), as kind posts it (its pane's, B1): the
// lead's lines, and the lines and job wakes of an agent whose kind has an inbox (T-499). began: a byte of it went out (an error after that may have
// delivered it); an error with began false (no record, a refused dial) delivered nothing.
type Poster interface {
	Post(ctx context.Context, kind, agent, line string) (began bool, err error)
}

// logWorked records fact worked for each open brief of p whose work was seen and is not in the log yet (not under mu).
// Each brief is claimed under mu (logging) while its fact is recorded: two passes at once (the merge loop's and the
// watchdog's) never both record it; a refused record leaves it for the next pass (logged is the fact's fold's).
func (a *App) logWorked(p *proj) {
	type claim struct {
		w workedData
		b *brief
	}
	a.mu.Lock()
	var worked []claim
	for agent, b := range p.out.briefs {
		if b.worked && !b.logged && !b.logging {
			b.logging = true
			worked = append(worked, claim{workedData{To: agent, Brief: b.seq}, b})
		}
	}
	a.mu.Unlock()
	sort.Slice(worked, func(i, j int) bool { return worked[i].w.To < worked[j].w.To })
	for _, c := range worked {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepWorked, Kind: event.KindFact}, c.w)
		a.mu.Lock()
		c.b.logging = false
		a.mu.Unlock()
	}
}

// deliver is one pass over p's lines: the seen work of briefs not in the log yet recorded (logWorked's retry), the
// briefed Codex agents' stops read from their transcripts (roleTurns), the finished briefs' lines recorded (idle, or
// their agent gone: T-732; none while a stop is in force), the lead's lines posted as one per target when its inbox is
// recorded (T-651, T-654), each role agent's oldest line delivered when its pane is idle (posted or typed as its kind takes it), the stopped role agents' wakes and
// alarms (roleWakes), and the needs-you of lines waiting longer than msgWait.
func (a *App) deliver(ctx context.Context, p *proj, fx *Loop) {
	a.closeSettled(ctx, p)
	if fx.Typer == nil && fx.Inbox == nil {
		return
	}
	now := a.opt.Now()
	a.logWorked(p)
	a.roleTurns(ctx, p)

	a.mu.Lock()
	var done []event.Message
	for agent, b := range p.out.briefs {
		w := a.whereIs(p, agent)
		gone := a.gone(p, agent, b, w, now) // noted on every pass: where its agent was last seen
		if b.worked && b.stop == nil && (w.State == Identified && Ready(w.Pane.Status) || gone) {
			done = append(done, event.Message{To: p.out.leadAgent, From: "obor", Ends: agent,
				Line: fmt.Sprintf("[obor] %s finished (briefed %s ago)", agent, Age(now.Sub(b.at)))})
		}
	}
	a.mu.Unlock()
	sort.Slice(done, func(i, j int) bool { return done[i].Ends < done[j].Ends })
	for _, m := range done {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
	}

	a.mu.Lock()
	var oldest []queued // each agent's oldest line
	var targets []string
	leadLines := map[string][]queued{} // the lines queued for the lead by their target (T-651: one post; T-654: per target)
	taken := map[string]bool{}
	for _, q := range p.queuedLines() {
		if q.lead || q.msg.To == p.out.leadAgent {
			if leadLines[q.msg.To] == nil {
				targets = append(targets, q.msg.To)
			}
			leadLines[q.msg.To] = append(leadLines[q.msg.To], *q)
		} else if !taken[q.msg.To] {
			taken[q.msg.To] = true
			oldest = append(oldest, *q)
		}
	}
	a.mu.Unlock()
	for _, to := range targets { // the target of the oldest line first
		a.mu.Lock()
		post, target, ok := a.route(p, fx, leadLines[to][0])
		a.mu.Unlock()
		if ok {
			a.deliverLead(ctx, p, fx, leadLines[to], post, target)
		}
	}
	for _, q := range oldest {
		// Resolved right before its own delivery (RV-53-verify V3): an earlier delivery of this pass may have moved,
		// duplicated or busied its agent.
		a.mu.Lock()
		post, target, ok := a.route(p, fx, q)
		a.mu.Unlock()
		if ok {
			a.deliverLine(ctx, p, fx, q, post, target)
		}
	}
	a.roleWakes(ctx, p, fx)
	a.msgNeedsYou(p, now)
}

// closeSettled closes unsent every settled open delivery of p (T-749, RV-91 #1): a line whose question was settled
// while its own delivery's intent is in the log with no result (a serve restart cut it) gets result retiredResult,
// whatever its agent's pane (busy, not identified, ambiguous, its lane released) and whatever route it would take;
// it then leaves obor status and its needs-you. Through App.do: a result this process kept unwritten (the effect
// ran) is written instead, and a delivery whose effect runs now (its claim before its intent, or its transmit) is
// left to its outcome. A close the log refuses is tried again at the next pass.
func (a *App) closeSettled(ctx context.Context, p *proj) {
	a.mu.Lock()
	var keys []event.Key
	for _, q := range p.queuedLines() {
		k := event.Key{Task: event.MsgTask, Step: stepDeliver + strconv.FormatInt(q.seq, 10)}
		if q.state == lineSendingSettled && q.post == q.seq && p.exec[k].IsZero() && p.log.Open(k) {
			keys = append(keys, k)
		}
	}
	a.mu.Unlock()
	for _, k := range keys {
		_, _ = a.do(ctx, p, event.Intent{Key: k, Class: event.ClassPrompt},
			func(context.Context) (any, error) { return retiredResult, nil })
	}
}

// goneAfter is how long a worked brief's agent may stay unidentified with its pane still running an agent before its
// brief ends (T-732): a short reconnect, or a relabel after a serve restart, does not end it.
const goneAfter = time.Minute

// gone: agent of brief b, resolved now as w, is gone (under mu; T-732): its last pane runs no agent (at its shell, or
// closed), its only labelled pane is at its shell (a brief replayed from the log: no pane seen yet), or no pane has
// identified it for goneAfter while serve is connected. It notes where the agent is seen (b.term, b.paneID, b.lost).
// Two panes claiming it (Ambiguous) are no gone agent.
func (a *App) gone(p *proj, agent string, b *brief, w Where, now time.Time) bool {
	switch {
	case w.State == Identified:
		b.term, b.paneID, b.lost = termOf(w.Pane), w.Pane.ID, time.Time{}
		return false
	case w.State == Ambiguous || w.Why == notConnected:
		b.lost = time.Time{}
		return false
	}
	if b.lost.IsZero() {
		b.lost = now
	}
	if b.term != "" {
		if o := a.terminal(p, b.term, b.paneID); o == nil || o.pane.Agent == "" {
			return true
		}
	}
	if _, ok := a.labelledShell(p, agent); ok {
		return true
	}
	return now.Sub(b.lost) >= goneAfter
}

// route is how q's line goes out now (under mu): posted to its agent's session (post: the kind whose inbox takes it;
// a lead line, or a ready agent whose kind has an inbox and whose inbox record is its pane's live session's), or
// typed into its ready agent's pane (post "", target: the pane's address, never herdr's name, RV-53 #5): via. A ready
// agent whose kind has an inbox but whose record cannot take the line gets it typed, as its job wakes are (T-746: a
// line to an idle worker is delivered, never held on a session mismatch); so does one whose last post of the line was
// refused before a byte went out (T-748: a refused dial of a dead socket is not tried again and again). ok false: held, no intent, nothing typed
// (delivered already, a lead line whose inbox is not recorded, its agent not identified (two panes claim it, or none
// proves it) or not ready); the line's held notes why (obor status, its needs-you).
func (a *App) route(p *proj, fx *Loop, q queued) (post, target string, ok bool) {
	m := p.out.msgs[q.seq]
	if m == nil {
		return "", "", false
	}
	post, target, m.held = a.routeOf(p, fx, q)
	return post, target, m.held == ""
}

// routeOf is route's choice for q (under mu): its post and target, or why it is held ("": it goes out).
func (a *App) routeOf(p *proj, fx *Loop, q queued) (post, target, held string) {
	to := q.msg.To
	lead := q.lead || to == p.out.leadAgent // a lead line, or a brief whose agent has since become the lead
	w := a.whereIs(p, to)
	switch {
	case lead && (fx.Inbox == nil || !a.inboxed(to)):
		return "", "", "no inbox recorded for " + to + ": its SessionStart hook records one"
	case lead && w.State != Identified: // a lead line goes out whatever its pane: through the lead's configured kind
		return p.out.leadKind, to, ""
	case lead: // its record is its own latest SessionStart, fresher than herdr's view: no pane check (DD-v0.13 R1)
		return w.Pane.Agent, to, ""
	case q.brief && q.msg.Fresh && w.State == Identified:
		return a.freshRoute(p, fx, q, w)
	case q.brief && to != p.integrator && fx.Agents != nil && w.State == Unavailable:
		if pane, ok := a.shellPane(p, to); ok { // a stopped role agent: started fresh on the brief (T-672)
			return startRoute, pane.ID, ""
		}
		return "", "", to + " is not running and no shell pane of it is there to start it in"
	case w.State == Ambiguous:
		return "", "", notIdentified(to, w)
	case w.State != Identified:
		return "", "", to + " is not identified: " + Dash(w.Why)
	case !Ready(w.Pane.Status):
		return "", "", busyHeld(to, w.Pane.ID)
	}
	post, inboxed := a.via(fx, p, w, to)
	switch {
	case post != "" && inboxed && !(q.refusedPost && fx.Typer != nil): // after a refused post: typed (T-748)
		return post, to, ""
	case fx.Typer != nil:
		return "", w.Pane.ID, ""
	case post != "":
		return "", "", fmt.Sprintf("no inbox recorded for the session in %s's pane %s, and serve types no lines", to,
			w.Pane.ID)
	}
	return "", "", "serve types no lines"
}

// busyHeld is routeOf's hold of a line to agent, busy in pane.
func busyHeld(agent, pane string) string {
	return fmt.Sprintf("%s is busy in pane %s (delivered once it is idle)", agent, pane)
}

// freshRoute is routeOf's choice for q, a fresh brief's line to its running agent w (under mu; T-794): stopped, then
// started fresh on the line in its pane (deliverLine), busy or idle. Held while its last stop failed less than
// roleStopRetry ago (a newer fresh brief is a line never refused: not held), or with no agent starts.
func (a *App) freshRoute(p *proj, fx *Loop, q queued, w Where) (post, target, held string) {
	to := q.msg.To
	switch {
	case fx.Agents == nil:
		return "", "", "serve starts no agents: " + to + "'s fresh brief waits"
	case !p.roleAgentOf(to):
		return "", "", to + " is no role agent serve starts fresh"
	case q.refused != "" && a.opt.Now().Sub(p.roleFail[to]) < roleStopRetry:
		return "", "", fmt.Sprintf("its stop failed: tried again %s after the failure", Age(roleStopRetry))
	}
	return freshStart, w.Pane.ID, ""
}

// freshStart is route's post for a fresh brief whose role agent runs (freshRoute): stopped (stopRoleNow), then started
// as a stopped one is (startRoute).
const freshStart = "fresh"

// lineWaits: the rule's line with did key of is queued, not delivered yet (under mu).
func (p *proj) lineWaits(of event.Of) bool {
	for _, q := range p.out.msgs {
		if q.msg.Of == of {
			return true
		}
	}
	return false
}

// queuedLines is p's lines not delivered yet, oldest first (under mu).
func (p *proj) queuedLines() []*queued {
	out := make([]*queued, 0, len(p.out.msgs))
	for _, q := range p.out.msgs {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// posted: a running agent of kind (herdr's, its pane's) gets serve's lines and job wakes posted to its session's inbox
// (its bridge's Deliver posts, agents.Caps.Inbox), not typed into its pane (T-499).
func posted(kind string) bool {
	k, ok := agents.Lookup(kind)
	return ok && k.Caps().Inbox
}

// inboxed: agent's session inbox is recorded (Options.Inboxed; nil: assumed, the post says otherwise).
func (a *App) inboxed(agent string) bool {
	return a.inboxedAt(nil, agent, Where{})
}

// inboxedAt: agent's session inbox is recorded and, when w identifies agent's pane, the record's session is that
// pane's or names none, or herdr reports none for the pane (DD-v0.13 A3: a record another session of that name wrote
// is not this pane's; R1: an unknown session cannot tell).
func (a *App) inboxedAt(p *proj, agent string, w Where) bool {
	if a.opt.Inboxed == nil {
		return true
	}
	session, ok := a.opt.Inboxed(agent)
	if !ok || session == "" || w.State != Identified || w.Pane.ID == "" { // Where{}: no pane to check (a lead line)
		return ok
	}
	live := p.liveSession(agent, w.Pane)
	return live == "" || sameSession(live, session)
}

// liveSession is the session running in pane, agent's (under mu; T-746): its recorded current session when that
// session was reported from pane (its SessionStart hook ran there: first-hand, and fresher than herdr's view), else
// herdr's agent_session for pane ("": unknown). herdr's value can be stale (axis-alies T-1421: it named a session
// with no transcript while the pane ran the one its hook recorded), so it decides only when the record is not pane's.
func (p *proj) liveSession(agent string, pane port.Pane) string {
	if s, ok := p.session(agent); ok && s.Pane != "" && s.Pane == pane.ID {
		return s.ID
	}
	return pane.Session
}

// deliverLine posts q's line to target's session through kind post's inbox (the lead, or an agent whose kind has
// one) or, post "", types it into target (its pane) under its delivery key (P, transmit). Refused before a byte went
// out: fact failed, no result, delivered again on a later pass; a byte went out and then an error: result "sent: …".
// A post serve's own stop cut before a byte (T-660): nothing recorded, the line still queued. sent: its delivery's
// result is in the log and it went out (not closed unsent); a line another deliverLine is sending now is left to it.
func (a *App) deliverLine(ctx context.Context, p *proj, fx *Loop, q queued, post, target string) (sent bool) {
	key := event.Key{Task: event.MsgTask, Step: stepDeliver + strconv.FormatInt(q.seq, 10)}
	// The claim (RV-61 #3): a brief recorded since route superseded the line: nothing is sent; else its claim (sending)
	// keeps any brief from superseding it from here on (the line may reach the pane).
	a.mu.Lock()
	m := p.out.msgs[q.seq]
	closing := false
	if m != nil && m.sending { // obor spec set's delivery and the pass's at once (T-821): one sends it
		m = nil
	}
	if m != nil {
		closing = m.state == lineSendingSettled // settled while a delivery was open (a crash cut it): closed unsent
		p.move(m, lineClaimed)
		m.sending = true
	}
	a.mu.Unlock()
	if m == nil {
		return false
	}
	defer func() {
		a.mu.Lock()
		m.sending = false
		a.mu.Unlock()
	}()
	if post == freshStart && !closing { // T-794: the running agent stopped first (serve's role stop), then started
		if err := a.freshStop(ctx, p, fx, q, key); err != nil {
			return false
		}
		post = startRoute
	}
	began := false
	_, err := a.do(ctx, p, event.Intent{Key: key, Class: event.ClassPrompt, Data: lineData{To: q.msg.To}},
		func(ctx context.Context) (any, error) {
			if closing {
				return retiredResult, nil
			}
			if post == startRoute { // the stopped agent's fresh start in its pane carries the line (T-672)
				_, err := fx.Agents.Launch(ctx, port.Launch{Agent: q.msg.To, Pane: target, Fresh: true, Prompt: q.msg.Line})
				if err != nil {
					return nil, fmt.Errorf("%s not started on its brief: %w", q.msg.To, err)
				}
				return sentStart, nil
			}
			var err error
			began, err = fx.transmit(ctx, post, target, q.msg.Line, false)
			return sentResult(began, err)
		})
	if err != nil && !began { // nothing went out: the next pass delivers it
		if cutByServe(ctx, err) { // serve is stopping: the post was cut, not refused (T-660)
			a.shutDown(event.MsgTask + " " + event.StepFailed + "{" + key.Step + ": " + err.Error() + "}")
			return false
		}
		via := "" // a refused post: the next attempt types it (T-748)
		if post != "" && post != startRoute {
			via = viaInbox
		}
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
			event.FactData{Machine: Machine, Reason: key.Step + ": " + err.Error(), Step: key.Step, Via: via})
	}
	return err == nil && !closing
}

// freshStop stops q's agent before its fresh brief's start (deliverLine, T-794; q claimed): serve's one stop decision
// (stopAgent; the pane resolved now, the agent interrupted if it works). A failed stop starts the agent's retry clock
// (roleFail, freshRoute) and records fact failed for the line (key: its delivery), so the brief stays queued, its
// needs-you raised (msgNeedsYou) and the stop tried again; nothing is started.
func (a *App) freshStop(ctx context.Context, p *proj, fx *Loop, q queued, key event.Key) error {
	line, _, err := a.stopAgent(ctx, p, fx, q.msg.To, false, true)
	if err == nil {
		a.logf("%s: %s stopped for its fresh brief: %s", p.name, q.msg.To, line)
		return nil
	}
	if !cutByServe(ctx, err) {
		a.mu.Lock()
		p.roleFail[q.msg.To] = a.opt.Now()
		a.mu.Unlock()
	}
	switch {
	case cutByServe(ctx, err):
		a.shutDown(event.MsgTask + " " + event.StepFailed + "{" + key.Step + ": " + err.Error() + "}")
	default:
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
			event.FactData{Machine: Machine, Reason: key.Step + ": " + q.msg.To + " not stopped for its fresh brief: " +
				err.Error(), Step: key.Step})
	}
	return err
}

// deliverLead posts qs, the lines queued for one lead target (oldest first), as one message, one line per row, in
// order (T-651). A post's intent names its members and its target (T-654); its result settles exactly those members
// (the fold), so a line queued later waits for its own post. A post still open (its intent pending: refused before a
// byte, or cut by a crash; or its result kept unwritten in this process) goes again with exactly its members, or its
// kept result is written without a transmit, before any new post of the target (a recovery that transmitted nothing
// lets the rest go out in the same pass). A post refused before a byte went out: fact failed for each member, posted
// again as one at the next pass. Once the post's result is in the log each other member gets its own delivery
// result "sent" (the log's per-line record; its fold settles nothing more).
func (a *App) deliverLead(ctx context.Context, p *proj, fx *Loop, qs []queued, post, target string) {
	key := func(seq int64) event.Key {
		return event.Key{Task: event.MsgTask, Step: stepDeliver + strconv.FormatInt(seq, 10)}
	}
	for {
		head, lines, recovered := a.leadPost(p, qs, key)
		if len(lines) == 0 {
			return
		}
		members := make([]int64, len(lines))
		text := make([]string, len(lines))
		for i, q := range lines {
			members[i], text[i] = q.seq, q.msg.Line
		}
		began := false
		_, err := a.do(ctx, p, event.Intent{Key: key(head), Class: event.ClassPrompt, Data: lineData{To: lines[0].msg.To, Lines: members}},
			func(ctx context.Context) (any, error) {
				var err error
				began, err = fx.transmit(ctx, post, target, strings.Join(text, "\n"), false)
				return sentResult(began, err)
			})
		if err != nil && !began && !recovered { // nothing went out: the next pass posts them again, as one
			if cutByServe(ctx, err) { // serve is stopping: the post was cut, not refused (T-660)
				what := event.MsgTask + " " + event.StepFailed + "{" + key(head).Step + ": " + err.Error() + "}"
				if len(lines) > 1 { // one line is named as its own record; a batched post says how many it was
					what += fmt.Sprintf(" for %d lines", len(lines))
				}
				a.shutDown(what)
			} else {
				for _, q := range lines {
					a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
						event.FactData{Machine: Machine, Reason: key(q.seq).Step + ": " + err.Error(), Step: key(q.seq).Step})
				}
			}
		}
		if err != nil { // the post's result is not in the log: its members wait with it
			return
		}
		for _, q := range lines {
			if q.seq != head {
				_, _ = a.do(ctx, p, event.Intent{Key: key(q.seq), Class: event.ClassPrompt, Data: lineData{To: q.msg.To}},
					func(context.Context) (any, error) { return "sent", nil })
			}
		}
		if !recovered {
			return
		}
	}
}

// leadPost is the post deliverLead makes next of qs (one target's lines), its members claimed (sending): the open post
// of a line of qs (its key's seq, its members still queued), else every line of qs not delivered yet under the
// oldest's seq. recovered: the open post's result is kept unwritten (App.do writes it, nothing is transmitted).
func (a *App) leadPost(p *proj, qs []queued, key func(int64) event.Key) (head int64, lines []queued, recovered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, q := range qs {
		if m := p.out.msgs[q.seq]; m != nil && m.post != 0 {
			head = m.post
			break
		}
	}
	if head != 0 {
		for _, seq := range p.out.posts[head] {
			if m := p.out.msgs[seq]; m != nil && m.post == head {
				lines = append(lines, *m)
			}
		}
		_, recovered = p.unwritten[key(head)]
	} else {
		for _, q := range qs {
			if m := p.out.msgs[q.seq]; m != nil {
				lines = append(lines, *m)
			}
		}
		if len(lines) > 0 {
			head = lines[0].seq
		}
	}
	for _, q := range lines {
		p.move(p.out.msgs[q.seq], lineClaimed)
	}
	return head, lines, recovered
}

// msgNeedsYou keeps p's needs-you for lines waiting longer than msgWait: one for the oldest such line (a newer oldest
// replaces it), ended (resumed) once none waits.
func (a *App) msgNeedsYou(p *proj, now time.Time) {
	a.mu.Lock()
	reason := ""
	for _, q := range p.queuedLines() {
		if q.msg.Fresh && q.refused != "" { // a fresh brief's stop or start failed (T-794): at once, naming the agent
			reason = fmt.Sprintf("fresh brief to %s from %s not started: %s (the next delivery tries again; obor brief %s "+
				"--fresh briefs it anew)", q.msg.To, q.msg.From, q.refused, q.msg.To)
			break
		}
		if now.Sub(q.at) >= msgWait {
			reason = fmt.Sprintf("line to %s from %s waiting over %s for its delivery: %s", q.msg.To, q.msg.From,
				Age(msgWait), q.msg.Line)
			if why := q.why(); why != "" { // why the last pass held it (T-746; two panes claiming its agent among
				// them), or its last refusal (T-748)
				reason += " (undelivered: " + why + ")"
			}
			break
		}
	}
	a.mu.Unlock()
	a.settleNeeds(p, event.MsgTask, reason)
}

// ruleNeeds is the needs-you line's rule in the rule pass (rules.go, T-927).
const ruleNeeds = "needs"

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

// needsRule lines p's lead about each needs-you fact in force, naming the commands that answer it (T-497: the herdr
// notification reaches the owner, the lead is the one who acts), and lines it again while it stays in force (T-621):
// 30 min after the first line's delivery, then 60 after each repeat's (T-624: the delivery's result, folded; never
// while its last line waits undelivered), the line ending in " (again, <its age>)". The needs-you of the project's
// own lines gets none (a lead line waiting would only queue another one), nor a closed task's watchdog fact (T-635:
// one older than its end's clear). Key: needsKey(the needs-you's seq, the line's number); the oldest needs-you due
// goes first, the next one in the next pass (the line's record wakes it).
func needsRule(_ context.Context, a *App, p *proj, _ *Loop, now time.Time) (int64, action, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.out.lead() == "" {
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
			if p.lineWaits(last) {
				continue
			}
			base, ok := p.out.delivered[last]
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
		return needsKey(r.Seq, n), action{line: p.needsLine(r, n, now)}, true
	}
	return 0, action{}, false
}

// needsLine is line n of needs-you fact r at now (under mu): "[obor] needs-you T-xx (<lane>): <reason> → <the
// commands that answer it, in its task's state now>", a repeat adding " (again, <its age>)".
func (p *proj) needsLine(r event.Record, n int, now time.Time) string {
	d := r.Fact()
	line := "[obor] needs-you " + r.Task
	if d.Lane != "" {
		line += " (" + d.Lane + ")"
	}
	line += ": " + oneLine(d.Reason)
	if ans := p.answerPath(r); ans != "" {
		line += " → " + ans
	}
	if n > 0 {
		line += " (again, " + Age(now.Sub(event.RecordTime(r))) + ")"
	}
	return line
}

// oldLineOf is the did key of a needs-you's or a watch's line recorded before T-927 (its own field, no Of; under
// mu): needs, the needs-you's seq and the lines of it folded so far; watch, the watch's seq. Compat until v0.29
// (change rule 5).
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

// Age is d as obor status writes ages: 42s, 5m, 3h, 2d.
func Age(d time.Duration) string {
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return strconv.FormatInt(max(s, 0), 10) + "s"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m"
	case s < 86400:
		return strconv.FormatInt(s/3600, 10) + "h"
	}
	return strconv.FormatInt(s/86400, 10) + "d"
}

// sendLine is the line obor send posts in the target project (T-466): the sender and the text, then the reply in one
// command (T-646: the target lead answers with a known name instead of a path).
func sendLine(from, text string) string {
	return "[obor " + from + "] " + text + ` (reply: obor send ` + from + ` "…")`
}

// sendCall answers the socket's send request (a lead or the owner of another project): the line for this project's
// lead, posted to its Claude session. A call id is required and a repeated one gets the stored reply.
func (a *App) sendCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodSend + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodSend, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodSend, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("send: a lead or the owner sends, not %s (role %s)", Dash(c.Agent), c.Role)
	case strings.TrimSpace(c.Note) == "":
		return "", errors.New("send: a text is required")
	case c.From == "":
		return "", errors.New("send: the sender project is required")
	}
	m := event.Message{To: p.out.leadAgent, Line: sendLine(c.From, c.Note), From: c.From, Call: c.CallID,
		Reply: "SENT to " + p.out.leadAgent + " in " + p.name + " (posted to its session)"}
	if !a.inboxed(p.out.leadAgent) {
		m.Reply = "SENT to " + p.out.leadAgent + " in " + p.name + " (queued: no session recorded for " + p.out.leadAgent +
			"; `obor restart " + p.out.leadAgent + "` once it is idle registers it, or quit it and run `obor lead`)"
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("send: the event log of %s refused it", p.name)
	}
	a.wake()
	return m.Reply, nil
}

// briefCall answers the socket's brief request (the lead or the owner): Call.To, a role agent, is briefed; with a
// text, its line is delivered to that agent when its pane is idle (posted or typed as its kind takes it, deliver).
// The new brief ends the agent's undelivered brief lines (T-575; its fact's supersedes, T-580) and says so in its
// reply. The lead hears once the agent has finished. A brief to an idle deep dive moves its worktree to the
// integration branch's head first (briefMove, T-853; a publish running does not hold it, T-872); that git failing:
// refused, nothing recorded.
// Call.Fresh (obor brief --fresh, T-794): the brief, once recorded, starts the agent fresh with its line as the first
// prompt, stopping it first when it runs (routeOf, deliverLine); a refused call stops nothing.
func (a *App) briefCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodBrief + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodBrief, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodBrief, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("brief: the lead or the owner briefs, not %s (role %s)", Dash(c.Agent), c.Role)
	case c.To == "":
		return "", errors.New("brief: the role agent is required")
	case c.To == p.out.leadAgent:
		return "", fmt.Errorf("brief: %s is the lead: obor send reaches another project's lead", c.To)
	case c.Fresh && strings.TrimSpace(c.Note) == "":
		return "", errors.New("brief: --fresh takes a text: the fresh session's first prompt")
	}
	from := c.Agent
	if from == "" {
		from = "owner"
	}
	a.mu.Lock()
	wtRole, restartable := p.wtRole(c.To), !c.Fresh || p.roleAgentOf(c.To)
	a.mu.Unlock()
	if !restartable {
		return "", fmt.Errorf("brief: %s is no role agent serve starts fresh (not the integrator or a worker)", c.To)
	}
	if wtRole != "" { // the reviewer or the deep dive
		ctx, cancel := context.WithTimeout(context.Background(), verifyGitWait)
		err := a.briefMove(ctx, p, wtRole, c.To)
		cancel()
		if err != nil {
			return "", fmt.Errorf("brief: %s's worktree: %v", c.To, err)
		}
	}

	reply, ok := a.recordBrief(p, c.To, from, c.Note, c.CallID, c.Prompt, c.Fresh, nil)
	if !ok {
		return "", fmt.Errorf("brief: the event log of %s refused it", p.name)
	}
	a.wake() // a fresh brief's stop and start are the delivery's (deliverLine), in the merge loop's pass
	return reply, nil
}

// recordBrief records fact briefed of the role agent to from from (note "": the restart's or a review's fresh start
// carried the brief; call "": no call; prompt: that start's first prompt, its text when there is no line): its reply,
// false when the log refused it. The new brief ends the agent's undelivered brief lines (T-575; its fact's supersedes,
// T-580). Without a note, a pane already identified as the agent's may be working on it: that work is noted at once.
// The text sent is a row of the project's prompts table (T-665): the line for a brief, the start's prompt for a start.
// fresh: its line starts the agent fresh, the running agent stopped first (T-794). v: a verify's brief (nil: none; T-924,
// reviewverify.go): its tasks and head in the fact (Task, Reviewed), and not recorded (false) while the agent has an
// open brief, decided in the a.mu section that records it.
func (a *App) recordBrief(p *proj, to, from, note, call, prompt string, fresh bool, v *verifyBrief) (string, bool) {
	// A newer brief makes an older undelivered brief obsolete (T-575): the agent's undelivered brief lines are named
	// in the new brief's fact, whose fold retires them (T-580: one append, so a crash or a refused append cannot
	// bring one back). A line whose delivery is claimed (sending: it may have landed) stays; a lead line is
	// never superseded. The choice and the append are one a.mu section: no delivery claims a chosen line meanwhile.
	a.mu.Lock()
	if v != nil && p.out.brief(to) != nil { // a brief recorded since the verify's check: the next pass looks again
		a.mu.Unlock()
		return "", false
	}
	var old []int64
	for _, q := range p.queuedLines() {
		if q.brief && q.msg.To == to && q.msg.To != p.out.leadAgent && q.state == lineWaiting {
			old = append(old, q.seq)
		}
	}
	m := event.Message{To: to, From: from, Call: call, Supersedes: old, Fresh: fresh && note != "",
		Reply: "BRIEF RECORDED for " + to + " (" + a.unnamedIn(p, to, "the lead hears when it finishes") + ")"}
	if v != nil {
		m.Task, m.Reviewed = strings.Join(v.tasks, ","), v.head
	}
	if note != "" {
		m.Line = "[obor " + from + "] " + note
		how := "delivered when its pane is idle; the lead hears when it finishes"
		if m.Fresh {
			how = "serve stops it and starts it fresh with this brief; the lead hears when it finishes"
		}
		m.Reply = "BRIEFED " + to + " (" + a.unnamedIn(p, to, how) + ")"
	}
	if len(old) > 0 {
		briefs := "briefs"
		if len(old) == 1 {
			briefs = "brief"
		}
		m.Reply += fmt.Sprintf(" (replaces %d undelivered %s)", len(old), briefs)
	}
	seq, ok := a.recordIn(p, event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact}, m)
	a.mu.Unlock()
	if !ok {
		return "", false
	}
	if m.Line != "" {
		a.prompt(p, seq, store.PromptBrief, to, "", "", m.Line, 0)
	} else {
		a.prompt(p, seq, store.PromptStart, to, "", "", prompt, 0)
	}
	if m.Line == "" { // the restart's start carried the brief: the agent may be working on it already
		a.mu.Lock()
		if w := a.whereIs(p, to); w.State == Identified {
			a.sawWork(p, w.Pane)
		}
		a.mu.Unlock()
		a.logWorked(p)
	}
	return m.Reply, true
}

// unnamed is a reply's note on a line to agent: when serve, connected to herdr, cannot identify agent's pane in p
// (none runs it, or two claim it), that its line is queued until it can; else note.
func (a *App) unnamed(p *proj, agent, note string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.unnamedIn(p, agent, note)
}

// unnamedIn is unnamed under mu.
func (a *App) unnamedIn(p *proj, agent, note string) string {
	switch w := a.whereIs(p, agent); {
	case w.State == Ambiguous:
		return "queued: " + notIdentified(agent, w)
	case w.State == Unavailable && agent != p.integrator && a.stoppedAt(p, agent):
		return "stopped: serve starts it fresh with this brief; the lead hears when it finishes"
	case a.connected && w.State == Unavailable:
		return "queued: no pane runs " + agent + "; `obor restart " + agent + "` starts it"
	}
	return note
}

// merges: p has a merge loop, the only place its lines are delivered (design "Only for projects with a merge loop").
func (a *App) merges(p *proj) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.merging
}

// callProj is the project at c.Root and the stored reply of c's call id for method, if any.
func (a *App) callProj(method string, c wire.Call) (p *proj, reply string, stored bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p = a.projAt(c.Root)
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(method, c.CallID)]
	}
	return p, reply, stored
}

// briefStatus is p's open briefs as obor status lists them, by agent (under mu): a brief whose line is still queued is
// not delivered.
func (p *proj) briefStatus() []wire.BriefStatus {
	var out []wire.BriefStatus
	for agent, b := range p.out.allBriefs() {
		_, queued := p.out.msgs[b.seq]
		out = append(out, wire.BriefStatus{To: agent, Since: b.at.Unix(), Delivered: !queued, Worked: b.worked})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].To < out[j].To })
	return out
}

// messageStatus is p's lines not delivered yet as obor status lists them (under mu).
func (p *proj) messageStatus(now time.Time) []wire.MessageStatus {
	out := []wire.MessageStatus{}
	for _, q := range p.queuedLines() {
		out = append(out, wire.MessageStatus{To: q.msg.To, From: q.msg.From, Line: q.msg.Line,
			Age: int64(now.Sub(q.at) / time.Second), Held: q.why()})
	}
	return out
}
