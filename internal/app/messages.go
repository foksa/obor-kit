package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"obor/internal/agents"
	"obor/internal/store"
)

// Lines to a project's agents (T-466; docs/state-model.md row 47): the two kinds the lead still gets, a brief's, and
// an answer's (obor answer <T-xx> "<text>": fact answered{to: the task's worker, line "[obor answer] <text>"}, T-511;
// answer.go).
//   - obor send <project> "<text>" (a lead or the owner of another project; the project by a known name or a path,
//     T-646): fact message{to: the lead, line "[obor <sender project>] <text> (reply: obor send <sender> "…")"} in the
//     target project's log, recorded by the serve holding it.
//   - obor brief <role-agent> "<text>": fact briefed{to: the role agent, line "[obor <caller>] <text>"}; obor restart
//     <role-agent> --fresh --prompt "<brief>" records the same fact without a line (the start carried the brief).
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
// (T-499, posted): a kind with an inbox (agents.Caps.Inbox) gets it posted to its session's inbox like the lead's, and
// it waits, untried, while that session has no inbox record; any other kind gets it typed into its pane (the herdr
// call of job delivery). The oldest first and one per agent per pass, under deliver.<seq> (P, a wake's transport:
// prompt.go). A refusal before any byte went out (no inbox record, a refused dial, a refused typing) is fact
// failed{step: deliver.<seq>}, and the line stays queued and is delivered again on a later pass, also after a restart
// (RV-50 #1); a post serve's own stop cut before a byte records nothing (T-660) and waits the same. A delivery a crash
// cut (its intent pending) is delivered again too: a line may reach its agent twice,
// never not at all. A typed line herdr refuses because the pane is blocked (blocked, agent_blocked) is such a refusal,
// retried on a later pass; a posted one cannot land in a dialog at all (a session behind an open permission dialog reads it
// when its turn continues, and it cannot answer the dialog). A line waiting longer than msgWait is the project's
// needs-you until no such line waits.

const (
	MethodSend  = "send"  // Call.Root the target project, Call.From the sender project, Call.Note the text
	MethodBrief = "brief" // Call.To the role agent, Call.Note the brief ("": the restart's first prompt carried it)

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

// MessageStatus is one line not yet delivered (obor status): to whom, from whom, the line, and its age in seconds.
type MessageStatus struct {
	To   string `json:"to"`
	From string `json:"from,omitempty"`
	Line string `json:"line"`
	Age  int64  `json:"age"`
}

// outbox is one project's lines to its agents (proj.out; T-610): leadAgent, the lead's agent (roles.lead.agent), and
// leadKind, its kind as configured (its role's type, else agents.Default): its lines' inbox; msgs, the lines not
// delivered yet by their fact's seq; lined, the needs-you facts (by seq) whose lines to the lead are recorded, and how
// many; lining, those whose line a pass is recording now (needsLines' claim, not folded); delivered, when the latest
// delivered line of a needs-you was delivered (T-624: the repeat's clock, folded from the delivery's result); briefs, the open briefs by role
// agent. Its fields are under App.mu like the rest of proj; only this file names its maps (the other files read it
// through lead, brief and allBriefs).
//
// claim makes a line's delivery claim (deliverLine: typing set) and a brief's supersede of the agent's undelivered
// brief lines (briefCall: the choice to the append) exclude each other (RV-61 #3). Lock order: claim, then App.mu;
// never claim while holding mu (briefCall's append takes mu inside it).
type outbox struct {
	claim     sync.Mutex
	leadAgent string
	leadKind  string
	msgs      map[int64]*queued
	lined     map[int64]int
	lining    map[int64]bool
	delivered map[int64]time.Time
	briefs    map[string]*brief
	posts     map[int64][]int64 // a delivery intent's members by its key's seq, its result not folded yet (T-654)
}

// newOutbox is an empty outbox of the lead agent lead, its configured kind leadKind.
func newOutbox(lead, leadKind string) outbox {
	return outbox{leadAgent: lead, leadKind: leadKind, msgs: map[int64]*queued{}, lined: map[int64]int{},
		lining: map[int64]bool{}, delivered: map[int64]time.Time{}, briefs: map[string]*brief{},
		posts: map[int64][]int64{}}
}

// lead is the project's lead agent ("": none configured).
func (o *outbox) lead() string { return o.leadAgent }

// brief is agent's open brief (nil: none); its fields are under App.mu.
func (o *outbox) brief(agent string) *brief { return o.briefs[agent] }

// allBriefs is the open briefs by role agent, in no order (under App.mu).
func (o *outbox) allBriefs() iter.Seq2[string, *brief] { return maps.All(o.briefs) }

// queued is a line not delivered yet: its fact's seq and time, its data, whether its delivery may have begun (typing),
// whether it is a lead line (fact message, posted to the inbox whatever today's lead name; fact briefed: a role
// brief), whether it is a brief's line (fact briefed with a line: a newer brief ends it), and the seq of the delivery
// intent whose post carries it (0: none; T-654: only that post's result settles it).
type queued struct {
	seq    int64
	at     time.Time
	msg    Message
	typing bool
	lead   bool
	brief  bool
	post   int64
}

// brief is a role agent's open brief: its fact's seq, when its line was delivered (else the fact's time), whether its
// pane worked since (its line delivered; since its stop, when it had one), whether that work is in the log (fact
// worked; logging: being recorded), its agent's stop on a usage limit in force (nil: none; T-569), and whether a turn
// that ended that stop is followed (its start seen, its end not yet: T-571, stopped.go turnDecide).
type brief struct {
	seq      int64
	at       time.Time
	worked   bool
	logged   bool
	logging  bool // logWorked records its fact worked now (its claim, not folded)
	stop     *roleStop
	followed bool
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

// supersededData is fact superseded's data: the seq of the undelivered brief line it ends, and the seq of the newer
// brief that replaced it.
type supersededData struct {
	Seq int64 `json:"seq"`
	By  int64 `json:"by"`
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
func (p *proj) foldMsg(r Record) {
	if r.Task != MsgTask {
		return
	}
	switch {
	case r.Kind == KindFact && (r.Step == StepMessage || r.Step == StepBriefed):
		var m Message
		_ = json.Unmarshal(r.Data, &m)
		if m.Call != "" {
			method := MethodSend
			if r.Step == StepBriefed {
				method = MethodBrief
			}
			p.folded.calls[verdictStep(method, m.Call)] = m.Reply
		}
		if m.Needs != 0 {
			p.out.lined[m.Needs]++
		}
		if m.Watch != 0 {
			p.watchEnded(m.Task, m.Watch)
		}
		if m.Review != 0 {
			p.reviewEnded(m.Review)
		}
		if m.Stop != 0 {
			p.roleAlarmed(m.Stop)
		}
		if m.Line != "" {
			p.out.msgs[r.Seq] = &queued{seq: r.Seq, at: recordTime(r), msg: m, lead: r.Step == StepMessage,
				brief: r.Step == StepBriefed}
		}
		if r.Step == StepBriefed {
			p.out.briefs[m.To] = &brief{seq: r.Seq, at: recordTime(r)}
		}
		for _, seq := range m.Supersedes { // delivered no more (its 24 h needs-you ends with it)
			delete(p.out.msgs, seq)
		}
		if m.Ends != "" {
			delete(p.out.briefs, m.Ends)
		}
	case r.Kind == KindFact && r.Step == StepStopped: // a role agent's: the lead's line, ending its brief; a worker's:
		// its task's needs-you (stopped.go)
		var d stoppedData
		_ = json.Unmarshal(r.Data, &d)
		p.folded.calls[verdictStep(MethodStopped, d.Call)] = d.Reply
		p.foldStop(r, d)
		if d.Line != "" {
			p.out.msgs[r.Seq] = &queued{seq: r.Seq, at: recordTime(r), msg: d.Message, lead: true}
		}
		if d.Ends != "" {
			delete(p.out.briefs, d.Ends)
		}
		if b := p.out.briefs[d.Agent]; b != nil && d.Brief != 0 && b.seq == d.Brief { // a usage limit: the brief stays
			// open, stopped; the work before the stop is not its finish
			b.stop = &roleStop{seq: r.Seq, at: recordTime(r), line: d.Line, turn: d.Turn, reset: d.Reset}
			b.worked, b.logged, b.followed = false, false, false
		}
	case r.Kind == KindFact && r.Step == StepTurned:
		var d turnedData
		_ = json.Unmarshal(r.Data, &d)
		if b := p.out.briefs[d.To]; b != nil && b.seq == d.Brief { // the turn's start: followed, its work not seen yet
			// (a lingering status of the stopped turn is not); its end: the work done
			b.stop = nil
			b.worked, b.logged, b.followed = d.Worked, d.Worked, !d.Worked
		}
	case r.Kind == KindFact && r.Step == StepAnswered:
		var m Message
		_ = json.Unmarshal(r.Data, &m)
		p.folded.calls[verdictStep(MethodAnswer, m.Call)] = m.Reply
		p.out.msgs[r.Seq] = &queued{seq: r.Seq, at: recordTime(r), msg: m}
		p.answered(m.Task)
	case r.Kind == KindFact && r.Step == StepWorked:
		var d workedData
		_ = json.Unmarshal(r.Data, &d)
		if b := p.out.briefs[d.To]; b != nil && b.seq == d.Brief { // an older brief's work does not arm a newer one
			b.worked, b.logged = true, true
		}
	case r.Kind == KindFact && r.Step == StepSuperseded: // an older log's: a newer brief ends an undelivered brief
		// line, as briefed's supersedes does now
		var d supersededData
		_ = json.Unmarshal(r.Data, &d)
		delete(p.out.msgs, d.Seq)
	case r.Kind == KindFact && r.Step == StepFailed:
		seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Fact().Step, stepDeliver), 10, 64)
		if q := p.out.msgs[seq]; q != nil {
			q.typing = false
		}
	case strings.HasPrefix(r.Step, stepDeliver): // T-654: a post's result settles exactly the lines its intent names
		seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, stepDeliver), 10, 64)
		switch r.Kind {
		case KindIntent:
			var d lineData
			_ = json.Unmarshal(r.Data, &d)
			var members []int64
			for _, m := range d.members(seq) {
				if q := p.out.msgs[m]; q != nil && q.post == 0 { // a line delivered already is no member
					q.typing, q.post = true, seq
					members = append(members, m)
				}
			}
			if len(members) > 0 {
				p.out.posts[seq] = members
			}
		case KindResult:
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

// settleLine folds r, the result of the post that carried q, as q's delivery (under mu).
func (p *proj) settleLine(q *queued, r Record) {
	if q.msg.Needs != 0 { // a needs-you line delivered: its next repeat counts from now (T-624)
		p.out.delivered[q.msg.Needs] = recordTime(r)
	}
	if b := p.out.briefs[q.msg.To]; b != nil && b.seq == q.seq { // the brief's line delivered: its age starts
		b.at = recordTime(r)
	}
	delete(p.out.msgs, q.seq)
}

// sawWork notes pane's status for an open brief of the agent whoIs finds in it (no label read, RV-53 #4): working
// after the brief's line was delivered (or with no line) is the work the brief asked for (under mu, after reconcile).
func (a *App) sawWork(p *proj, pane Pane) {
	agent, ok := p.reg.whoIs(pane)
	b := p.out.briefs[agent]
	if !ok || b == nil || pane.Agent == "" || Ready(pane.Status) {
		return
	}
	if q := p.out.msgs[b.seq]; q == nil || q.typing {
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
		a.record(p, Record{Task: MsgTask, Step: StepWorked, Kind: KindFact}, c.w)
		a.mu.Lock()
		c.b.logging = false
		a.mu.Unlock()
	}
}

// deliver is one pass over p's lines: the seen work of briefs not in the log yet recorded (logWorked's retry), the
// briefed Codex agents' stops read from their transcripts (roleTurns), the finished briefs' lines recorded (none
// while a stop is in force), the lead's lines posted as one per target when its inbox is recorded (T-651, T-654), each role agent's oldest
// line delivered when its pane is idle (posted or typed as its kind takes it), the stopped role agents' wakes and
// alarms (roleWakes), and the needs-you of lines waiting longer than msgWait.
func (a *App) deliver(ctx context.Context, p *proj, fx *Loop) {
	if fx.Typer == nil && fx.Inbox == nil {
		return
	}
	now := a.opt.Now()
	a.logWorked(p)
	a.roleTurns(ctx, p)

	a.mu.Lock()
	var done []Message
	for agent, b := range p.out.briefs {
		if w := a.whereIs(p, agent); b.worked && b.stop == nil && w.State == Identified && Ready(w.Pane.Status) {
			done = append(done, Message{To: p.out.leadAgent, From: "obor", Ends: agent,
				Line: fmt.Sprintf("[obor] %s finished (briefed %s ago)", agent, Age(now.Sub(b.at)))})
		}
	}
	a.mu.Unlock()
	sort.Slice(done, func(i, j int) bool { return done[i].Ends < done[j].Ends })
	for _, m := range done {
		a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, m)
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

// route is how q's line goes out now (under mu): posted to its agent's session (post: the kind whose inbox takes it;
// a lead line, or a ready agent whose kind has an inbox), or typed into its ready agent's pane (post "", target: the
// pane's address, never herdr's name, RV-53 #5): via. ok false: held, no intent, nothing typed (delivered already, its
// inbox not recorded or recorded by another session than its pane's, or its agent not identified (two panes claim
// it, or none proves it) or not ready).
func (a *App) route(p *proj, fx *Loop, q queued) (post, target string, ok bool) {
	if p.out.msgs[q.seq] == nil {
		return "", "", false
	}
	lead := q.lead || q.msg.To == p.out.leadAgent // a lead line, or a brief whose agent has since become the lead
	w := a.whereIs(p, q.msg.To)
	switch {
	case lead && w.State != Identified: // a lead line goes out whatever its pane: through the lead's configured kind
		return p.out.leadKind, q.msg.To, fx.Inbox != nil && a.inboxedAt(q.msg.To, Where{})
	case lead: // its record is its own latest SessionStart, fresher than herdr's view: no pane check (DD-v0.13 R1)
		return w.Pane.Agent, q.msg.To, fx.Inbox != nil && a.inboxedAt(q.msg.To, Where{})
	case q.brief && q.msg.To != p.integrator && fx.Agents != nil && w.State == Unavailable:
		if pane, ok := a.shellPane(p, q.msg.To); ok { // a stopped role agent: started fresh on the brief (T-672)
			return startRoute, pane.ID, true
		}
		return "", "", false
	case w.State != Identified || !Ready(w.Pane.Status):
		return "", "", false
	}
	post, ok = a.via(fx, w, q.msg.To)
	if post != "" {
		return post, q.msg.To, ok
	}
	return "", w.Pane.ID, ok
}

// needsLineWaits: a line announcing needs-you fact seq is queued, not delivered yet (under mu).
func (p *proj) needsLineWaits(seq int64) bool {
	for _, q := range p.out.msgs {
		if q.msg.Needs == seq {
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
	return a.inboxedAt(agent, Where{})
}

// inboxedAt: agent's session inbox is recorded and, when w identifies agent's pane, the record's session is that
// pane's or names none, or herdr reports none for the pane (DD-v0.13 A3: a record another session of that name wrote
// is not this pane's; R1: an unknown session cannot tell).
func (a *App) inboxedAt(agent string, w Where) bool {
	if a.opt.Inboxed == nil {
		return true
	}
	session, ok := a.opt.Inboxed(agent)
	return ok && (session == "" || w.State != Identified || w.Pane.Session == "" || sameSession(w.Pane.Session, session))
}

// deliverLine posts q's line to target's session through kind post's inbox (the lead, or an agent whose kind has
// one) or, post "", types it into target (its pane) under its delivery key (P, transmit). Refused before a byte went
// out: fact failed, no result, delivered again on a later pass; a byte went out and then an error: result "sent: …".
// A post serve's own stop cut before a byte (T-660): nothing recorded, the line still queued.
func (a *App) deliverLine(ctx context.Context, p *proj, fx *Loop, q queued, post, target string) {
	key := Key{Task: MsgTask, Step: stepDeliver + strconv.FormatInt(q.seq, 10)}
	// The claim (RV-61 #3): a brief recorded since route superseded the line: nothing is sent; else its typing claim
	// keeps any brief from superseding it from here on (the line may reach the pane).
	p.out.claim.Lock()
	a.mu.Lock()
	m := p.out.msgs[q.seq]
	if m != nil {
		m.typing = true
	}
	a.mu.Unlock()
	p.out.claim.Unlock()
	if m == nil {
		return
	}
	began := false
	_, err := a.do(ctx, p, Intent{Key: key, Class: ClassPrompt, Data: lineData{To: q.msg.To}},
		func(ctx context.Context) (any, error) {
			if post == startRoute { // the stopped agent's fresh start in its pane carries the line (T-672)
				_, err := fx.Agents.Launch(ctx, Launch{Agent: q.msg.To, Pane: target, Fresh: true, Prompt: q.msg.Line})
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
			a.shutDown(MsgTask + " " + StepFailed + "{" + key.Step + ": " + err.Error() + "}")
			return
		}
		a.record(p, Record{Task: MsgTask, Step: StepFailed, Kind: KindFact},
			FactData{Machine: Machine, Reason: key.Step + ": " + err.Error(), Step: key.Step})
	}
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
	key := func(seq int64) Key { return Key{Task: MsgTask, Step: stepDeliver + strconv.FormatInt(seq, 10)} }
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
		_, err := a.do(ctx, p, Intent{Key: key(head), Class: ClassPrompt, Data: lineData{To: lines[0].msg.To, Lines: members}},
			func(ctx context.Context) (any, error) {
				var err error
				began, err = fx.transmit(ctx, post, target, strings.Join(text, "\n"), false)
				return sentResult(began, err)
			})
		if err != nil && !began && !recovered { // nothing went out: the next pass posts them again, as one
			if cutByServe(ctx, err) { // serve is stopping: the post was cut, not refused (T-660)
				what := MsgTask + " " + StepFailed + "{" + key(head).Step + ": " + err.Error() + "}"
				if len(lines) > 1 { // one line is named as its own record; a batched post says how many it was
					what += fmt.Sprintf(" for %d lines", len(lines))
				}
				a.shutDown(what)
			} else {
				for _, q := range lines {
					a.record(p, Record{Task: MsgTask, Step: StepFailed, Kind: KindFact},
						FactData{Machine: Machine, Reason: key(q.seq).Step + ": " + err.Error(), Step: key(q.seq).Step})
				}
			}
		}
		if err != nil { // the post's result is not in the log: its members wait with it
			return
		}
		for _, q := range lines {
			if q.seq != head {
				_, _ = a.do(ctx, p, Intent{Key: key(q.seq), Class: ClassPrompt, Data: lineData{To: q.msg.To}},
					func(context.Context) (any, error) { return "sent", nil })
			}
		}
		if !recovered {
			return
		}
	}
}

// leadPost is the post deliverLead makes next of qs (one target's lines), its members claimed (typing): the open post
// of a line of qs (its key's seq, its members still queued), else every line of qs not delivered yet under the
// oldest's seq. recovered: the open post's result is kept unwritten (App.do writes it, nothing is transmitted).
func (a *App) leadPost(p *proj, qs []queued, key func(int64) Key) (head int64, lines []queued, recovered bool) {
	p.out.claim.Lock()
	defer p.out.claim.Unlock()
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
		p.out.msgs[q.seq].typing = true
	}
	return head, lines, recovered
}

// msgNeedsYou keeps p's needs-you for lines waiting longer than msgWait: one for the oldest such line (a newer oldest
// replaces it), ended (resumed) once none waits.
func (a *App) msgNeedsYou(p *proj, now time.Time) {
	a.mu.Lock()
	reason := ""
	for _, q := range p.queuedLines() {
		if now.Sub(q.at) >= msgWait {
			reason = fmt.Sprintf("line to %s from %s waiting over %s for its delivery: %s", q.msg.To, q.msg.From,
				Age(msgWait), q.msg.Line)
			if w := a.whereIs(p, q.msg.To); w.State == Ambiguous && !q.lead && q.msg.To != p.out.leadAgent {
				reason += " (" + notIdentified(q.msg.To, w) + ")"
			}
			break
		}
	}
	held, same := len(p.folded.active[MsgTask]) > 0, reason != "" && p.has(MsgTask, StepNeedsYou, reason)
	a.mu.Unlock()
	switch {
	case same, reason == "" && !held:
		return
	case held && !a.record(p, Record{Task: MsgTask, Step: StepResumed, Kind: KindFact}, FactData{Machine: Machine}):
		return
	case reason != "":
		a.record(p, Record{Task: MsgTask, Step: StepNeedsYou, Kind: KindFact}, FactData{Machine: Machine, Reason: reason})
	}
}

// needsLines queues a line to p's lead for each needs-you fact in force naming it and the commands that answer it
// (T-497: the herdr notification reaches the owner, the lead is the one who acts), and posts the same line again while
// the needs-you stays in force and unanswered (T-621): 30 min after the first line's delivery, then every 60, the line ending in
// " (again, <its age>)". A pass claims each fact under mu before it records (the loop's notify and the watchdog's run at
// once). The message fact carries the needs-you's seq, so a replay finds the lines recorded and queues none again. The
// repeat counts from the last line's delivery (T-624: 30 min after the first line's, 60 after a repeat's; outbox.
// delivered, folded) and waits while a line of that needs-you is undelivered. The needs-you of
// the project's own lines gets none (a lead line waiting would only queue another one).
func (a *App) needsLines() {
	a.mu.Lock()
	type todo struct {
		r     Record
		ans   string // the commands that answer it, in its task's state now
		n     int    // the lines already recorded when the pass claimed it
		again bool   // a repeat: the line ends in " (again, <age>)"
		age   time.Duration
	}
	var out []todo
	p := a.p
	now := a.opt.Now()
	if p != nil && p.merging && p.out.leadAgent != "" {
		for _, r := range p.needsYou() {
			if r.Task == MsgTask || p.out.lining[r.Seq] {
				continue
			}
			if p.closed(r.Task) && !r.Fact().Merge { // T-635: a watchdog fact of a closed task (one older than its end's clear)
				continue
			}
			n := p.out.lined[r.Seq]
			if n == 0 { // the first line, right away
				p.out.lining[r.Seq] = true
				out = append(out, todo{r: r, ans: p.answerPath(r), n: n})
				continue
			}
			if p.needsLineWaits(r.Seq) { // T-624: the last line is not delivered yet: no repeat piles up behind it
				continue
			}
			base, ok := p.out.delivered[r.Seq]
			if !ok { // no delivery recorded (an older log): the needs-you fact's time
				base = recordTime(r)
			}
			next := base.Add(needsAgain)
			if n > 1 {
				next = base.Add(needsAgainEvery)
			}
			if now.Before(next) {
				continue
			}
			p.out.lining[r.Seq] = true
			out = append(out, todo{r: r, ans: p.answerPath(r), n: n, again: true, age: now.Sub(recordTime(r))})
		}
	}
	a.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].r.Seq < out[j].r.Seq })
	queued := false
	for _, t := range out {
		d := t.r.Fact()
		line := "[obor] needs-you " + t.r.Task
		if d.Lane != "" {
			line += " (" + d.Lane + ")"
		}
		line += ": " + strings.Join(strings.Fields(d.Reason), " ")
		if t.ans != "" {
			line += " → " + t.ans
		}
		if t.again {
			line += " (again, " + Age(t.age) + ")"
		}
		m := Message{To: p.out.leadAgent, From: "obor", Line: line, Needs: t.r.Seq}
		if a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, m) {
			queued = true
		}
		// The claim ends either way: an appended line is in lined (its fold), even when record is false for the lanes
		// table only (RV-52-verify V1); a refused append leaves it out, so a later pass tries again.
		a.mu.Lock()
		delete(p.out.lining, t.r.Seq)
		a.mu.Unlock()
	}
	if queued {
		a.wake()
	}
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
func (a *App) sendCall(c Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(MethodSend + ": a call id is required")
	}
	p, reply, stored := a.callProj(MethodSend, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodSend, c.Root)
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
	m := Message{To: p.out.leadAgent, Line: sendLine(c.From, c.Note), From: c.From, Call: c.CallID,
		Reply: "SENT to " + p.out.leadAgent + " in " + p.name + " (posted to its session)"}
	if !a.inboxed(p.out.leadAgent) {
		m.Reply = "SENT to " + p.out.leadAgent + " in " + p.name + " (queued: no session recorded for " + p.out.leadAgent +
			"; `obor restart " + p.out.leadAgent + "` once it is idle registers it, or quit it and run `obor lead`)"
	}
	if !a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, m) {
		return "", fmt.Errorf("send: the event log of %s refused it", p.name)
	}
	a.wake()
	return m.Reply, nil
}

// briefCall answers the socket's brief request (the lead or the owner): Call.To, a role agent, is briefed; with a
// text, its line is delivered to that agent when its pane is idle (posted or typed as its kind takes it, deliver).
// The new brief ends the agent's undelivered brief lines (T-575; its fact's supersedes, T-580) and says so in its
// reply. The lead hears once the agent has finished.
func (a *App) briefCall(c Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(MethodBrief + ": a call id is required")
	}
	p, reply, stored := a.callProj(MethodBrief, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodBrief, c.Root)
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
	}
	from := c.Agent
	if from == "" {
		from = "owner"
	}
	reply, ok := a.recordBrief(p, c.To, from, c.Note, c.CallID, c.Prompt)
	if !ok {
		return "", fmt.Errorf("brief: the event log of %s refused it", p.name)
	}
	a.wake()
	return reply, nil
}

// recordBrief records fact briefed of the role agent to from from (note "": the restart's or a review's fresh start
// carried the brief; call "": no call; prompt: that start's first prompt, its text when there is no line): its reply,
// false when the log refused it. The new brief ends the agent's undelivered brief lines (T-575; its fact's supersedes,
// T-580). Without a note, a pane already identified as the agent's may be working on it: that work is noted at once.
// The text sent is a row of the project's prompts table (T-665): the line for a brief, the start's prompt for a start.
func (a *App) recordBrief(p *proj, to, from, note, call, prompt string) (string, bool) {
	// A newer brief makes an older undelivered brief obsolete (T-575): the agent's undelivered brief lines are named
	// in the new brief's fact, whose fold retires them (T-580: one append, so a crash or a refused append cannot
	// bring one back). A line whose delivery is claimed (its typing set: it may have landed) stays; a lead line is
	// never superseded. The outbox's claim from the choice to the append: no delivery claims a chosen line meanwhile.
	p.out.claim.Lock()
	a.mu.Lock()
	var old []int64
	for _, q := range p.queuedLines() {
		if q.brief && q.msg.To == to && q.msg.To != p.out.leadAgent && !q.typing {
			old = append(old, q.seq)
		}
	}
	a.mu.Unlock()
	m := Message{To: to, From: from, Call: call, Supersedes: old,
		Reply: "BRIEF RECORDED for " + to + " (" + a.unnamed(p, to, "the lead hears when it finishes") + ")"}
	if note != "" {
		m.Line = "[obor " + from + "] " + note
		m.Reply = "BRIEFED " + to + " (" + a.unnamed(p, to, "delivered when its pane is idle; the lead hears when it finishes") + ")"
	}
	if len(old) > 0 {
		briefs := "briefs"
		if len(old) == 1 {
			briefs = "brief"
		}
		m.Reply += fmt.Sprintf(" (replaces %d undelivered %s)", len(old), briefs)
	}
	seq, ok := a.recordSeq(p, Record{Task: MsgTask, Step: StepBriefed, Kind: KindFact}, m)
	p.out.claim.Unlock()
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
func (a *App) callProj(method string, c Call) (p *proj, reply string, stored bool) {
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
func (p *proj) briefStatus() []BriefStatus {
	var out []BriefStatus
	for agent, b := range p.out.allBriefs() {
		_, queued := p.out.msgs[b.seq]
		out = append(out, BriefStatus{To: agent, Since: b.at.Unix(), Delivered: !queued, Worked: b.worked})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].To < out[j].To })
	return out
}

// messageStatus is p's lines not delivered yet as obor status lists them (under mu).
func (p *proj) messageStatus(now time.Time) []MessageStatus {
	out := []MessageStatus{}
	for _, q := range p.queuedLines() {
		out = append(out, MessageStatus{To: q.msg.To, From: q.msg.From, Line: q.msg.Line,
			Age: int64(now.Sub(q.at) / time.Second)})
	}
	return out
}
