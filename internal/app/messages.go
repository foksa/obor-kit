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
)

// Lines to a project's agents (T-466; docs/state-model.md row 47): the two kinds the lead still gets, a brief's, and
// an answer's (obor answer <T-xx> "<text>": fact answered{to: the task's worker, line "[obor answer] <text>"}, T-511;
// answer.go).
//   - obor send <project> "<text>" (a lead or the owner of another project): fact message{to: the lead, line
//     "[obor <sender project>] <text>"} in the target project's log, recorded by the serve holding it.
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
// (RV-50 #1); a delivery a crash cut (its intent pending) is delivered again too: a line may reach its agent twice,
// never not at all. A typed line herdr refuses because the pane is blocked (blocked, agent_blocked) is such a refusal,
// retried on a later pass; a posted one cannot land in a dialog at all (a session behind an open permission dialog reads it
// when its turn continues, and it cannot answer the dialog). A line waiting longer than msgWait is the project's
// needs-you until no such line waits.

// MsgTask is the task key of a project's lines: their facts, deliveries and needs-you.
const MsgTask = "msg"

const (
	MethodSend  = "send"  // Call.Root the target project, Call.From the sender project, Call.Note the text
	MethodBrief = "brief" // Call.To the role agent, Call.Note the brief ("": the restart's first prompt carried it)

	StepMessage    = "message"    // fact: a line to type (data: Message)
	StepBriefed    = "briefed"    // fact: a role agent was briefed (data: Message; Line "" when its start carried it)
	StepWorked     = "worked"     // fact: a briefed agent's pane worked since its brief (data: workedData)
	StepSuperseded = "superseded" // fact (older logs; now briefed's supersedes): a brief ends a line (data: supersededData)
	stepDeliver    = "deliver."   // prompt intent/result (P) deliver.<seq>: the line of fact <seq> delivered (posted or typed)
)

// msgWait is how long a line may wait for its delivery before it needs the owner.
const msgWait = 24 * time.Hour

// Message is a line fact's data: the agent it goes to and the line typed, who sent it, the brief it ends, and the
// call that recorded it with its reply.
type Message struct {
	To    string `json:"to"`
	Line  string `json:"line,omitempty"`
	From  string `json:"from,omitempty"`
	Ends  string `json:"ends,omitempty"`
	Needs int64  `json:"needs,omitempty"` // the seq of the needs-you fact this line announces
	Task  string `json:"task,omitempty"`  // answered: the task whose question it answers; a watch's line: the task or comma list watched
	Watch int64  `json:"watch,omitempty"` // the seq of the watch fact this line ends (mergewatch.go)
	Stop  int64  `json:"stop,omitempty"`  // the seq of the role agent's stop this alarm line is for (stopwake.go)
	Call  string `json:"call,omitempty"`
	Reply string `json:"reply,omitempty"`
	// Supersedes: briefed's, the seqs of the agent's undelivered brief lines this brief ends (T-580)
	Supersedes []int64 `json:"supersedes,omitempty"`
}

// MessageStatus is one line not yet delivered (obor status): to whom, from whom, the line, and its age in seconds.
type MessageStatus struct {
	To   string `json:"to"`
	From string `json:"from,omitempty"`
	Line string `json:"line"`
	Age  int64  `json:"age"`
}

// outbox is one project's lines to its agents (proj.out; T-610): leadAgent, the lead's agent (roles.lead.agent), and
// leadKind, its kind as configured (its role's type, else agents.Default): its lines' inbox; msgs, the lines not
// delivered yet by their fact's seq; lined, the needs-you facts (by seq) whose line to the lead is recorded; lining,
// those whose line a pass is recording now (needsLines' claim, not folded); briefs, the open briefs by role agent.
// Its fields are under App.mu like the rest of proj; only this file names its maps (the other files read it through
// lead, brief and allBriefs).
//
// claim makes a line's delivery claim (deliverLine: typing set) and a brief's supersede of the agent's undelivered
// brief lines (briefCall: the choice to the append) exclude each other (RV-61 #3). Lock order: claim, then App.mu;
// never claim while holding mu (briefCall's append takes mu inside it).
type outbox struct {
	claim     sync.Mutex
	leadAgent string
	leadKind  string
	msgs      map[int64]*queued
	lined     map[int64]bool
	lining    map[int64]bool
	briefs    map[string]*brief
}

// newOutbox is an empty outbox of the lead agent lead, its configured kind leadKind.
func newOutbox(lead, leadKind string) outbox {
	return outbox{leadAgent: lead, leadKind: leadKind, msgs: map[int64]*queued{}, lined: map[int64]bool{},
		lining: map[int64]bool{}, briefs: map[string]*brief{}}
}

// lead is the project's lead agent ("": none configured).
func (o *outbox) lead() string { return o.leadAgent }

// brief is agent's open brief (nil: none); its fields are under App.mu.
func (o *outbox) brief(agent string) *brief { return o.briefs[agent] }

// allBriefs is the open briefs by role agent, in no order (under App.mu).
func (o *outbox) allBriefs() iter.Seq2[string, *brief] { return maps.All(o.briefs) }

// queued is a line not delivered yet: its fact's seq and time, its data, whether its delivery may have begun (typing),
// whether it is a lead line (fact message, posted to the inbox whatever today's lead name; fact briefed: a role
// brief), and whether it is a brief's line (fact briefed with a line: a newer brief ends it).
type queued struct {
	seq    int64
	at     time.Time
	msg    Message
	typing bool
	lead   bool
	brief  bool
}

// brief is a role agent's open brief: its fact's seq, when its line was delivered (else the fact's time), whether its
// pane worked since (its line delivered; since its stop, when it had one), whether that work is in the log (fact
// worked), its agent's stop on a usage limit in force (nil: none; T-569), and whether a turn that ended that stop is
// followed (its start seen, its end not yet: T-571, stopped.go turnDecide).
type brief struct {
	seq      int64
	at       time.Time
	worked   bool
	logged   bool
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

// lineData is a delivery intent's data: the line's agent.
type lineData struct {
	To string `json:"to"`
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
			p.out.lined[m.Needs] = true
		}
		if m.Watch != 0 {
			p.watchEnded(m.Task, m.Watch)
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
	case strings.HasPrefix(r.Step, stepDeliver):
		seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, stepDeliver), 10, 64)
		if q := p.out.msgs[seq]; q != nil && r.Kind == KindIntent {
			q.typing = true
		} else if r.Kind == KindResult {
			if q != nil {
				if b := p.out.briefs[q.msg.To]; b != nil && b.seq == seq { // the brief's line delivered: its age starts
					b.at = recordTime(r)
				}
			}
			delete(p.out.msgs, seq)
		}
	}
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
func (a *App) logWorked(p *proj) {
	a.mu.Lock()
	var worked []workedData
	for agent, b := range p.out.briefs {
		if b.worked && !b.logged {
			worked = append(worked, workedData{To: agent, Brief: b.seq})
		}
	}
	a.mu.Unlock()
	sort.Slice(worked, func(i, j int) bool { return worked[i].To < worked[j].To })
	for _, w := range worked {
		a.record(p, Record{Task: MsgTask, Step: StepWorked, Kind: KindFact}, w)
	}
}

// deliver is one pass over p's lines: the seen work of briefs not in the log yet recorded (logWorked's retry), the
// briefed Codex agents' stops read from their transcripts (roleTurns), the finished briefs' lines recorded (none
// while a stop is in force), the lead's oldest line posted when its inbox is recorded, each role agent's oldest
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
	taken := map[string]bool{}
	for _, q := range p.queuedLines() {
		if !taken[q.msg.To] {
			taken[q.msg.To] = true
			oldest = append(oldest, *q)
		}
	}
	a.mu.Unlock()
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
	case w.State != Identified || !Ready(w.Pane.Status):
		return "", "", false
	}
	post, ok = a.via(fx, w, q.msg.To)
	if post != "" {
		return post, q.msg.To, ok
	}
	return "", w.Pane.ID, ok
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
			var err error
			began, err = fx.transmit(ctx, post, target, q.msg.Line, false)
			return sentResult(began, err)
		})
	if err != nil && !began { // nothing went out: the next pass delivers it
		a.record(p, Record{Task: MsgTask, Step: StepFailed, Kind: KindFact},
			FactData{Machine: Machine, Reason: key.Step + ": " + err.Error(), Step: key.Step})
	}
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

// needsLines queues, once per needs-you fact in force, a line to p's lead naming it and the commands that answer it
// (T-497: the herdr notification reaches the owner, the lead is the one who acts). A pass claims each fact under mu before it records (the loop's notify and the watchdog's run at once). The message
// fact carries the needs-you's seq, so a replay finds the line recorded and queues none again. The needs-you of the project's own lines
// gets none (a lead line waiting would only queue another one).
func (a *App) needsLines() {
	a.mu.Lock()
	type todo struct {
		r   Record
		ans string // the commands that answer it, in its task's state now
	}
	var out []todo
	p := a.p
	if p != nil && p.merging && p.out.leadAgent != "" {
		for _, r := range p.needsYou() {
			if r.Task != MsgTask && !p.out.lined[r.Seq] && !p.out.lining[r.Seq] {
				p.out.lining[r.Seq] = true // claimed: a pass running at once (the loop's and the watchdog's) skips it
				out = append(out, todo{r, p.answerPath(r)})
			}
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
	m := Message{To: p.out.leadAgent, Line: "[obor " + c.From + "] " + c.Note, From: c.From, Call: c.CallID,
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
	// A newer brief makes an older undelivered brief obsolete (T-575): the agent's undelivered brief lines are named
	// in the new brief's fact, whose fold retires them (T-580: one append, so a crash or a refused append cannot
	// bring one back). A line whose delivery is claimed (its typing set: it may have landed) stays; a lead line is
	// never superseded. The outbox's claim from the choice to the append: no delivery claims a chosen line meanwhile.
	p.out.claim.Lock()
	a.mu.Lock()
	var old []int64
	for _, q := range p.queuedLines() {
		if q.brief && q.msg.To == c.To && q.msg.To != p.out.leadAgent && !q.typing {
			old = append(old, q.seq)
		}
	}
	a.mu.Unlock()
	m := Message{To: c.To, From: from, Call: c.CallID, Supersedes: old,
		Reply: "BRIEF RECORDED for " + c.To + " (" + a.unnamed(p, c.To, "the lead hears when it finishes") + ")"}
	if c.Note != "" {
		m.Line = "[obor " + from + "] " + c.Note
		m.Reply = "BRIEFED " + c.To + " (" + a.unnamed(p, c.To, "delivered when its pane is idle; the lead hears when it finishes") + ")"
	}
	if len(old) > 0 {
		briefs := "briefs"
		if len(old) == 1 {
			briefs = "brief"
		}
		m.Reply += fmt.Sprintf(" (replaces %d undelivered %s)", len(old), briefs)
	}
	ok := a.record(p, Record{Task: MsgTask, Step: StepBriefed, Kind: KindFact}, m)
	p.out.claim.Unlock()
	if !ok {
		return "", fmt.Errorf("brief: the event log of %s refused it", p.name)
	}
	if m.Line == "" { // the restart's start carried the brief: the agent may be working on it already
		a.mu.Lock()
		if w := a.whereIs(p, c.To); w.State == Identified {
			a.sawWork(p, w.Pane)
		}
		a.mu.Unlock()
		a.logWorked(p)
	}
	a.wake()
	return m.Reply, nil
}

// unnamed is a reply's note on a line to agent: when serve, connected to herdr, cannot identify agent's pane in p
// (none runs it, or two claim it), that its line is queued until it can; else note.
func (a *App) unnamed(p *proj, agent, note string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch w := a.whereIs(p, agent); {
	case w.State == Ambiguous:
		return "queued: " + notIdentified(agent, w)
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

// messageStatus is p's lines not delivered yet as obor status lists them (under mu).
func (p *proj) messageStatus(now time.Time) []MessageStatus {
	out := []MessageStatus{}
	for _, q := range p.queuedLines() {
		out = append(out, MessageStatus{To: q.msg.To, From: q.msg.From, Line: q.msg.Line,
			Age: int64(now.Sub(q.at) / time.Second)})
	}
	return out
}
