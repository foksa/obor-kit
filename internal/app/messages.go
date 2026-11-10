package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"sort"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/store"
)

// Lines to a project's agents (docs/state-model.md row 47): the two kinds the lead still gets, a brief's, and
// an answer's (obor resume <T-xx> "<text>": fact answered{to: the task's worker, line "[obor answer] <text>"};
// answer.go).
//   - obor send <project> "<text>" (a lead or the owner of another project; the project by a known name or a path):
//     fact message{to: the lead, line "[obor <sender project>] <text> (reply: obor send <sender> "…")"} in the
//     target project's log, recorded by the serve holding it.
//   - obor brief <role-agent> "<text>": fact briefed{to: the role agent, line "[obor <caller>] <text>"}; a review's
//     fresh start records the same fact without a line (its first prompt carried the brief).
//     A new brief of that agent also ends its undelivered brief lines (a newer brief makes an older
//     undelivered one obsolete; lead lines and needs-you lines are never superseded, and a line whose delivery is in
//     flight may have landed and stays): its fact names their seqs (supersedes) and its fold retires them, live and on
//     replay, so the brief and its supersede are one append.
//     Once that agent's pane has worked after the brief (after its line was delivered) and is idle again, serve records
//     message{to: the lead, line "[obor] <agent> finished (briefed <age> ago)", ends: the agent}: the brief is over.
//
// The first work serve sees after a brief is recorded as fact worked{to, brief: the brief's seq} as soon as the
// observation's caller releases mu (a herdr event, a snapshot, a fresh start's brief; each delivery pass retries a
// failed write), so a restart keeps it: an agent that finished while serve was down gets its finished line on
// reconnect. The finished line's age counts from when the brief's line was delivered (or the fresh start).
//
// deliver posts each line to the lead into its Claude session's inbox socket (recorded by its SessionStart
// hook; no pane, no idle rule, nothing typed into the pane the owner writes in) and delivers each role agent's line
// when its pane is idle (Ready: the rule of job delivery, no Escape, no screen read) as the pane's kind takes it
// (posted): a kind with an inbox (agents.Caps.Inbox) gets it posted to its session's inbox like the lead's when
// that inbox is the live session's in its pane (liveSession), else typed into its pane as its job wakes are (a
// line to an idle agent is never held on a missing or mismatched record); any other kind gets it typed into its pane
// (the herdr call of job delivery). A line held (its agent busy, not identified; a lead line with no inbox record)
// notes why (outbox.Line.Held: obor status, the line's needs-you). The oldest first and one per agent per pass, under
// deliver.<seq> (P, a wake's transport: prompt.go).
//
// A queued line is in one state (outbox.State; its moves: State.Next): waiting (nothing went out; why: the
// pass's hold, else its last refusal's reason), sending (its delivery claimed or its intent in the log: a byte may
// have gone out), sent and retired (both leave the outbox). A refusal before any byte went out (no inbox record, a
// refused dial, a refused typing) is fact failed{step: deliver.<seq>, reason, via: inbox for a post}: the line waits
// again with that reason (obor status, its needs-you) and is delivered again on a later pass, also after a restart,
// typed when the refused one was a post to an idle identified agent (a lead line is posted again); a post
// serve's own stop cut before a byte records nothing and waits the same. An answer's line retires once its
// question is settled another way (outbox.RetireAnswers): waiting, at once; sending, it is marked, and then a refusal before a
// byte retires it, a delivery sends it, and a later pass closes its open delivery unsent (result outbox.RetiredResult). A
// delivery a crash cut (its intent pending) is delivered again too: a line may reach its agent twice, never not at
// all. A typed line herdr refuses because the pane is blocked (blocked, agent_blocked) is such a refusal,
// retried on a later pass; a posted one cannot land in a dialog at all (a session behind an open permission dialog reads it
// when its turn continues, and it cannot answer the dialog). A line waiting longer than msgWait is the project's
// needs-you until no such line waits.

// msgWait is how long a line may wait for its delivery before it needs the owner.
const msgWait = 24 * time.Hour

// A needs-you line to the lead is posted again while the needs-you stays in force and unanswered: 30 min
// after the line's delivery, then every needsAgainEvery after each repeat's delivery (no repeat while a line
// of that needs-you waits undelivered). Constants, not config; the deadline is folded from the delivery's record.
const (
	needsAgain      = 30 * time.Minute
	needsAgainEvery = 60 * time.Minute
)

// foldMsg folds r, a record of the project's lines, into the outbox (outbox.Fold; first, the answers a record of
// another task settles retire) and applies what the record ended to the rest of p (outbox.Ended), in this order: the
// rule line's did key, the call's stored reply, a drive's come-back line, a review request's end, a worker's stop's
// needs-you, an answer's question, a brief's end.
func (p *proj) foldMsg(r event.Record) {
	if settles(r) { // the question is settled another way: its answer ends
		p.out.RetireAnswers(r.Task, 0)
	}
	if r.Task != event.MsgTask {
		return
	}
	e := p.out.Fold(r, p.oldLineOf)
	if e.Of.Kind != "" { // a rule's line: its did key (rules.go)
		p.folded.did[e.Of] = e.At
	}
	if e.Reply != nil {
		p.folded.calls[verdictStep(e.Reply.Method, e.Reply.Call)] = e.Reply.Text
	}
	if m := e.Msg; m != nil && m.Drive != 0 {
		p.foldDriveLine(*m)
	}
	if m := e.Msg; m != nil && m.Review != 0 {
		// A request ended by a failed --after ends here too, but only --rm and a firing count as review
		// ends that suppress the on-merge rule (review.on.merge). Idle counts this end too (its own marker).
		p.reviewEnded(m.Review)
		p.folded.reviewIdleEnd = idleEnd{r.Seq, e.At}
	}
	if e.Stopped != nil {
		p.foldStop(r, *e.Stopped)
	}
	if e.Answered != "" {
		p.answered(e.Answered)
	}
	if e.BriefEnd != "" {
		p.folded.briefEnds[e.BriefEnd] = idleEnd{r.Seq, e.At} // the brief's end, or the stop that ended it
	}
}

// settles: r settles its task's question another way: the worker submitted or asked again, the task
// was unblocked, dropped, rejected or merged (a pruned task keeps only its merged fact, which ends the question
// a lead's answer to it would otherwise reopen).
func settles(r event.Record) bool {
	if r.Task == event.MsgTask {
		return false
	}
	if r.Kind == event.KindResult {
		m, _ := verdictMethod(r.Step)
		return m == wire.MethodReject
	}
	return r.Kind == event.KindFact && (r.Step == event.StepSubmitted || r.Step == event.StepUnblocked ||
		r.Step == event.StepDropped || r.Step == event.StepMerged || isAsk(r))
}

// sawWork notes pane's status for an open brief of the agent whoIs finds in it (no label read): working
// after the brief's line was delivered (or with no line) is the work the brief asked for (outbox.SawWork; under mu,
// after reconcile).
func (a *App) sawWork(p *proj, pane port.Pane) {
	agent, ok := p.reg.WhoIs(pane)
	if !ok || pane.Agent == "" || Ready(pane.Status) {
		return
	}
	p.out.SawWork(agent)
}

// Poster posts a line into agent's session through its inbox socket, as kind posts it (its pane's, B1): the
// lead's lines, and the lines and job wakes of an agent whose kind has an inbox. began: a byte of it went out (an error after that may have
// delivered it); an error with began false (no record, a refused dial) delivered nothing.
type Poster interface {
	Post(ctx context.Context, kind, agent, line string) (began bool, err error)
}

// logWorked records fact worked for each open brief of p whose work was seen and is not in the log yet (not under mu).
// Each brief is claimed under mu (logging) while its fact is recorded: two passes at once (the merge loop's and the
// watchdog's) never both record it; a refused record leaves it for the next pass (logged is the fact's fold's).
func (a *App) logWorked(p *proj) {
	type claim struct {
		w outbox.Worked
		b *outbox.Brief
	}
	a.mu.Lock()
	var worked []claim
	for agent, b := range p.out.Briefs() {
		if b.Worked() && !b.Logged() && !b.Logging {
			b.Logging = true
			worked = append(worked, claim{outbox.Worked{To: agent, Brief: b.Seq()}, b})
		}
	}
	a.mu.Unlock()
	sort.Slice(worked, func(i, j int) bool { return worked[i].w.To < worked[j].w.To })
	for _, c := range worked {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepWorked, Kind: event.KindFact}, c.w)
		a.mu.Lock()
		c.b.Logging = false
		a.mu.Unlock()
	}
}

// deliver is one pass over p's lines: the seen work of briefs not in the log yet recorded (logWorked's retry), the
// briefed Codex agents' stops read from their transcripts (roleTurns), the finished briefs' lines recorded (idle, or
// their agent gone; none while a stop is in force), the lead's lines posted as one per target when its inbox is
// recorded, each role agent's oldest line delivered when its pane is idle (posted or typed as its kind takes it), the stopped role agents' wakes and
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
	var reviewed map[string]string // the finished agents' review files (finishedReview): by agent, the review id + "[-verify]"
	for agent, b := range p.out.Briefs() {
		w := a.whereIs(p, agent)
		gone := a.gone(p, agent, b, w, now) // noted on every pass: where its agent was last seen
		if b.Worked() && b.Stop() == nil && (w.State == Identified && Ready(w.Pane.Status) || gone) {
			done = append(done, event.Message{To: p.out.Lead(), From: "obor", Ends: agent,
				Line: fmt.Sprintf("[obor] %s finished (briefed %s ago)", agent, Age(now.Sub(b.At())))})
			if file := p.folded.rv.briefFile(agent, b.Seq()); file != "" {
				if reviewed == nil {
					reviewed = map[string]string{}
				}
				reviewed[agent] = file
			}
		}
	}
	a.mu.Unlock()
	if len(reviewed) > 0 { // the review files are read outside mu, once, as the lines are composed
		if s, err := reviewConfig(p.root); err == nil {
			for i, m := range done {
				if file := reviewed[m.Ends]; file != "" && m.Ends == s.Agent {
					done[i].Line = finishedReview(p.root, s.Reviews, file, m.Ends)
				}
			}
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Ends < done[j].Ends })
	for _, m := range done {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
	}

	a.mu.Lock()
	var oldest []outbox.Line // each agent's oldest line
	var targets []string
	leadLines := map[string][]outbox.Line{} // the lines queued for the lead by their target (one post per target)
	taken := map[string]bool{}
	for _, q := range p.out.Queued() {
		if q.IsLead() || q.Msg().To == p.out.Lead() {
			if leadLines[q.Msg().To] == nil {
				targets = append(targets, q.Msg().To)
			}
			leadLines[q.Msg().To] = append(leadLines[q.Msg().To], *q)
		} else if !taken[q.Msg().To] {
			taken[q.Msg().To] = true
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
		// Resolved right before its own delivery: an earlier delivery of this pass may have moved,
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

// closeSettled closes unsent every settled open delivery of p: a line whose question was settled
// while its own delivery's intent is in the log with no result (a serve restart cut it) gets result outbox.RetiredResult,
// whatever its agent's pane (busy, not identified, ambiguous, its lane released) and whatever route it would take;
// it then leaves obor status and its needs-you. Through App.do: a result this process kept unwritten (the effect
// ran) is written instead, and a delivery whose effect runs now (its claim before its intent, or its transmit) is
// left to its outcome. A close the log refuses is tried again at the next pass.
func (a *App) closeSettled(ctx context.Context, p *proj) {
	a.mu.Lock()
	var keys []event.Key
	for _, q := range p.out.Queued() {
		k := event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(q.Seq(), 10)}
		if q.State() == outbox.SendingSettled && q.Post() == q.Seq() && p.exec[k].IsZero() && p.log.Open(k) {
			keys = append(keys, k)
		}
	}
	a.mu.Unlock()
	for _, k := range keys {
		_, _ = a.do(ctx, p, event.Intent{Key: k, Class: event.ClassPrompt},
			func(context.Context) (any, error) { return outbox.RetiredResult, nil })
	}
}

// goneAfter is how long a worked brief's agent may stay unidentified with its pane still running an agent before its
// brief ends: a short reconnect, or a relabel after a serve restart, does not end it.
const goneAfter = time.Minute

// gone: agent of brief b, resolved now as w, is gone (under mu): its last pane runs no agent (at its shell, or
// closed), its only labelled pane is at its shell (a brief replayed from the log: no pane seen yet), or no pane has
// identified it for goneAfter while serve is connected. It notes where the agent is seen (b.Term, b.PaneID, b.Lost).
// Two panes claiming it (Ambiguous) are no gone agent.
func (a *App) gone(p *proj, agent string, b *outbox.Brief, w Where, now time.Time) bool {
	switch {
	case w.State == Identified:
		b.Term, b.PaneID, b.Lost = identity.TermOf(w.Pane), w.Pane.ID, time.Time{}
		return false
	case w.State == Ambiguous || w.Why == notConnected:
		b.Lost = time.Time{}
		return false
	}
	if b.Lost.IsZero() {
		b.Lost = now
	}
	if b.Term != "" {
		if o := a.terminal(p, b.Term, b.PaneID); o == nil || o.pane.Agent == "" {
			return true
		}
	}
	if _, ok := a.labelledShell(p, agent); ok {
		return true
	}
	return now.Sub(b.Lost) >= goneAfter
}

// route is how q's line goes out now (under mu): posted to its agent's session (post: the kind whose inbox takes it;
// a lead line, or a ready agent whose kind has an inbox and whose inbox record is its pane's live session's), or
// typed into its ready agent's pane (post "", target: the pane's address, never herdr's name): via. A ready
// agent whose kind has an inbox but whose record cannot take the line gets it typed, as its job wakes are (a
// line to an idle worker is delivered, never held on a session mismatch); so does one whose last post of the line was
// refused before a byte went out (a refused dial of a dead socket is not tried again and again). ok false: held, no intent, nothing typed
// (delivered already, a lead line whose inbox is not recorded, its agent not identified (two panes claim it, or none
// proves it) or not ready); the line's held notes why (obor status, its needs-you).
func (a *App) route(p *proj, fx *Loop, q outbox.Line) (post, target string, ok bool) {
	m := p.out.Line(q.Seq())
	if m == nil {
		return "", "", false
	}
	post, target, m.Held = a.routeOf(p, fx, q)
	return post, target, m.Held == ""
}

// routeOf is route's choice for q (under mu): its post and target, or why it is held ("": it goes out).
func (a *App) routeOf(p *proj, fx *Loop, q outbox.Line) (post, target, held string) {
	to := q.Msg().To
	lead := q.IsLead() || to == p.out.Lead() // a lead line, or a brief whose agent has since become the lead
	w := a.whereIs(p, to)
	switch {
	case lead && (fx.Inbox == nil || !a.inboxed(to)):
		return "", "", "no inbox recorded for " + to + ": its SessionStart hook records one"
	case lead && w.State != Identified: // a lead line goes out whatever its pane: through the lead's configured kind
		return p.out.LeadKind(), to, ""
	case lead: // its record is its own latest SessionStart, fresher than herdr's view: no pane check
		return w.Pane.Agent, to, ""
	case q.IsBrief() && q.Msg().Fresh && w.State == Identified:
		return a.freshRoute(p, fx, q, w)
	case q.IsBrief() && to != p.integrator && fx.Agents != nil && w.State == Unavailable:
		if pane, ok := a.shellPane(p, to); ok { // a stopped role agent: started fresh on the brief
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
	case post != "" && inboxed && !(q.RefusedPost() && fx.Typer != nil): // after a refused post: typed
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

// freshRoute is routeOf's choice for q, a fresh brief's line to its running agent w (under mu): stopped, then
// started fresh on the line in its pane (deliverLine), busy or idle. Held while its last stop failed less than
// roleStopRetry ago (a newer fresh brief is a line never refused: not held), or with no agent starts.
func (a *App) freshRoute(p *proj, fx *Loop, q outbox.Line, w Where) (post, target, held string) {
	to := q.Msg().To
	switch {
	case fx.Agents == nil:
		return "", "", "serve starts no agents: " + to + "'s fresh brief waits"
	case !p.roleAgentOf(to):
		return "", "", to + " is no role agent serve starts fresh"
	case q.Refused() != "" && a.opt.Now().Sub(p.roleFail[to]) < roleStopRetry:
		return "", "", fmt.Sprintf("its stop failed: tried again %s after the failure", Age(roleStopRetry))
	}
	return freshStart, w.Pane.ID, ""
}

// freshStart is route's post for a fresh brief whose role agent runs (freshRoute): stopped (stopRoleNow), then started
// as a stopped one is (startRoute).
const freshStart = "fresh"

// posted: a running agent of kind (herdr's, its pane's) gets serve's lines and job wakes posted to its session's inbox
// (its bridge's Deliver posts, agents.Caps.Inbox), not typed into its pane.
func posted(kind string) bool {
	k, ok := agents.Lookup(kind)
	return ok && k.Caps().Inbox
}

// inboxed: agent's session inbox is recorded (Options.Inboxed; nil: assumed, the post says otherwise).
func (a *App) inboxed(agent string) bool {
	return a.inboxedAt(nil, agent, Where{})
}

// inboxedAt: agent's session inbox is recorded and, when w identifies agent's pane, the record's session is that
// pane's or names none, or herdr reports none for the pane (a record another session of that name wrote
// is not this pane's; an unknown session cannot tell).
func (a *App) inboxedAt(p *proj, agent string, w Where) bool {
	if a.opt.Inboxed == nil {
		return true
	}
	session, ok := a.opt.Inboxed(agent)
	if !ok || session == "" || w.State != Identified || w.Pane.ID == "" { // Where{}: no pane to check (a lead line)
		return ok
	}
	live := p.liveSession(agent, w.Pane)
	return live == "" || identity.SameSession(live, session)
}

// liveSession is the session running in pane, agent's (under mu): its recorded current session when that
// session was reported from pane (its SessionStart hook ran there: first-hand, and fresher than herdr's view), else
// herdr's agent_session for pane ("": unknown). herdr's value can be stale (it can name a session
// with no transcript while the pane runs the one its hook recorded), so it decides only when the record is not pane's.
func (p *proj) liveSession(agent string, pane port.Pane) string {
	if s, ok := p.session(agent); ok && s.Pane != "" && s.Pane == pane.ID {
		return s.ID
	}
	return pane.Session
}

// sameSession: a and b, each a session as liveSession gives it (a recorded id, or herdr's value: an id or its
// transcript's path), name one session. An unknown one ("") is no session: never the same.
func sameSession(a, b string) bool {
	return identity.SameSession(a, b) || identity.SameSession(b, a)
}

// deliverLine posts q's line to target's session through kind post's inbox (the lead, or an agent whose kind has
// one) or, post "", types it into target (its pane) under its delivery key (P, transmit). Refused before a byte went
// out: fact failed, no result, delivered again on a later pass; a byte went out and then an error: result "sent: …".
// A post serve's own stop cut before a byte: nothing recorded, the line still queued. sent: its delivery's
// result is in the log and it went out (not closed unsent); a line another deliverLine is sending now is left to it.
func (a *App) deliverLine(ctx context.Context, p *proj, fx *Loop, q outbox.Line, post, target string) (sent bool) {
	key := event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(q.Seq(), 10)}
	// The claim: a brief recorded since route superseded the line: nothing is sent; else its claim (sending)
	// keeps any brief from superseding it from here on (the line may reach the pane).
	a.mu.Lock()
	m := p.out.Line(q.Seq())
	closing := false
	if m != nil && m.Sending { // obor spec set's delivery and the pass's at once: one sends it
		m = nil
	}
	if m != nil {
		closing = m.State() == outbox.SendingSettled // settled while a delivery was open (a crash cut it): closed unsent
		p.out.Move(m, outbox.Claimed)
		m.Sending = true
	}
	a.mu.Unlock()
	if m == nil {
		return false
	}
	defer func() {
		a.mu.Lock()
		m.Sending = false
		a.mu.Unlock()
	}()
	if post == freshStart && !closing { // the running agent stopped first (serve's role stop), then started
		if err := a.freshStop(ctx, p, fx, q, key); err != nil {
			return false
		}
		post = startRoute
	}
	began := false
	_, err := a.do(ctx, p, event.Intent{Key: key, Class: event.ClassPrompt, Data: outbox.LineData{To: q.Msg().To}},
		func(ctx context.Context) (any, error) {
			if closing {
				return outbox.RetiredResult, nil
			}
			if post == startRoute { // the stopped agent's fresh start in its pane carries the line
				_, err := fx.Agents.Launch(ctx, port.Launch{Agent: q.Msg().To, Pane: target, Fresh: true, Prompt: q.Msg().Line})
				if err != nil {
					return nil, fmt.Errorf("%s not started on its brief: %w", q.Msg().To, err)
				}
				return sentStart, nil
			}
			var err error
			began, err = fx.transmit(ctx, post, target, q.Msg().Line, false)
			return sentResult(began, err)
		})
	if err != nil && !began { // nothing went out: the next pass delivers it
		if cutByServe(ctx, err) { // serve is stopping: the post was cut, not refused
			a.shutDown(event.MsgTask + " " + event.StepFailed + "{" + key.Step + ": " + err.Error() + "}")
			return false
		}
		via := "" // a refused post: the next attempt types it
		if post != "" && post != startRoute {
			via = outbox.ViaInbox
		}
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
			event.FactData{Machine: Machine, Reason: key.Step + ": " + err.Error(), Step: key.Step, Via: via})
	}
	return err == nil && !closing
}

// freshStop stops q's agent before its fresh brief's start (deliverLine; q claimed): serve's one stop decision
// (stopAgent; the pane resolved now, the agent interrupted if it works). A failed stop starts the agent's retry clock
// (roleFail, freshRoute) and records fact failed for the line (key: its delivery), so the brief stays queued, its
// needs-you raised (msgNeedsYou) and the stop tried again; nothing is started.
func (a *App) freshStop(ctx context.Context, p *proj, fx *Loop, q outbox.Line, key event.Key) error {
	line, _, err := a.stopAgent(ctx, p, fx, q.Msg().To, false, true)
	if err == nil {
		a.logf("%s: %s stopped for its fresh brief: %s", p.name, q.Msg().To, line)
		return nil
	}
	if !cutByServe(ctx, err) {
		a.mu.Lock()
		p.roleFail[q.Msg().To] = a.opt.Now()
		a.mu.Unlock()
	}
	switch {
	case cutByServe(ctx, err):
		a.shutDown(event.MsgTask + " " + event.StepFailed + "{" + key.Step + ": " + err.Error() + "}")
	default:
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
			event.FactData{Machine: Machine, Reason: key.Step + ": " + q.Msg().To + " not stopped for its fresh brief: " +
				err.Error(), Step: key.Step})
	}
	return err
}

// deliverLead posts qs, the lines queued for one lead target (oldest first), as one message, one line per row, in
// order. A post's intent names its members and its target; its result settles exactly those members
// (the fold), so a line queued later waits for its own post. A post still open (its intent pending: refused before a
// byte, or cut by a crash; or its result kept unwritten in this process) goes again with exactly its members, or its
// kept result is written without a transmit, before any new post of the target (a recovery that transmitted nothing
// lets the rest go out in the same pass). A post refused before a byte went out: fact failed for each member, posted
// again as one at the next pass. Once the post's result is in the log each other member gets its own delivery
// result "sent" (the log's per-line record; its fold settles nothing more).
func (a *App) deliverLead(ctx context.Context, p *proj, fx *Loop, qs []outbox.Line, post, target string) {
	key := func(seq int64) event.Key {
		return event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(seq, 10)}
	}
	for {
		head, lines, recovered := a.leadPost(p, qs, key)
		if len(lines) == 0 {
			return
		}
		members := make([]int64, len(lines))
		text := make([]string, len(lines))
		for i, q := range lines {
			members[i], text[i] = q.Seq(), q.Msg().Line
		}
		began := false
		_, err := a.do(ctx, p, event.Intent{Key: key(head), Class: event.ClassPrompt, Data: outbox.LineData{To: lines[0].Msg().To, Lines: members}},
			func(ctx context.Context) (any, error) {
				var err error
				began, err = fx.transmit(ctx, post, target, strings.Join(text, "\n"), false)
				return sentResult(began, err)
			})
		if err != nil && !began && !recovered { // nothing went out: the next pass posts them again, as one
			if cutByServe(ctx, err) { // serve is stopping: the post was cut, not refused
				what := event.MsgTask + " " + event.StepFailed + "{" + key(head).Step + ": " + err.Error() + "}"
				if len(lines) > 1 { // one line is named as its own record; a batched post says how many it was
					what += fmt.Sprintf(" for %d lines", len(lines))
				}
				a.shutDown(what)
			} else {
				for _, q := range lines {
					a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
						event.FactData{Machine: Machine, Reason: key(q.Seq()).Step + ": " + err.Error(), Step: key(q.Seq()).Step})
				}
			}
		}
		if err != nil { // the post's result is not in the log: its members wait with it
			return
		}
		for _, q := range lines {
			if q.Seq() != head {
				_, _ = a.do(ctx, p, event.Intent{Key: key(q.Seq()), Class: event.ClassPrompt, Data: outbox.LineData{To: q.Msg().To}},
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
func (a *App) leadPost(p *proj, qs []outbox.Line, key func(int64) event.Key) (head int64, lines []outbox.Line, recovered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, q := range qs {
		if m := p.out.Line(q.Seq()); m != nil && m.Post() != 0 {
			head = m.Post()
			break
		}
	}
	if head != 0 {
		for _, seq := range p.out.Post(head) {
			if m := p.out.Line(seq); m != nil && m.Post() == head {
				lines = append(lines, *m)
			}
		}
		_, recovered = p.unwritten[key(head)]
	} else {
		for _, q := range qs {
			if m := p.out.Line(q.Seq()); m != nil {
				lines = append(lines, *m)
			}
		}
		if len(lines) > 0 {
			head = lines[0].Seq()
		}
	}
	for _, q := range lines {
		p.out.Move(p.out.Line(q.Seq()), outbox.Claimed)
	}
	return head, lines, recovered
}

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
				Age(msgWait), q.Msg().Line)
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
	body = d.Reason
	if !projectKey(r.Task) {
		body = fmt.Sprintf("%s in %s: %s", r.Task, d.Lane, d.Reason)
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

// oldLineOf is the did key of a needs-you's or a watch's line that carries no Of (its own field; under mu): needs,
// the needs-you's seq and the lines of it folded so far; watch, the watch's seq.
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

// sendLine is the line obor send posts in the target project: the sender and the text, then the reply in one
// command (the target lead answers with a known name instead of a path).
func sendLine(from, text string) string {
	return "[obor " + from + "] " + text + ` (reply: obor send ` + from + ` "…")`
}

// sendCall answers the socket's send request (a lead or the owner of another project): the line for this project's
// lead, posted to its Claude session. A call id is required and a repeated one gets the stored reply.
func (a *App) sendCall(c wire.SendReq) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodSend + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodSend, c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodSend, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	}
	if err := leadOr(p.root, wire.MethodSend, c.Envelope, fmt.Sprintf("send: a lead or the owner sends, not %s (role %s)", Dash(c.Agent),
		c.Role)); err != nil {
		return "", err
	}
	switch {
	case strings.TrimSpace(c.Text) == "":
		return "", errors.New("send: a text is required")
	case c.From == "":
		return "", errors.New("send: the sender project is required")
	}
	m := event.Message{To: p.out.Lead(), Line: sendLine(c.From, c.Text), From: c.From, Call: c.CallID,
		Reply: "SENT to " + p.out.Lead() + " in " + p.name + " (posted to its session)"}
	if !a.inboxed(p.out.Lead()) {
		m.Reply = "SENT to " + p.out.Lead() + " in " + p.name + " (queued: no session recorded for " + p.out.Lead() +
			"; `obor restart " + p.out.Lead() + "` once it is idle registers it, or quit it and run `obor lead`)"
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("send: the event log of %s refused it", p.name)
	}
	a.wake()
	return m.Reply, nil
}

// briefCall answers the socket's brief request (the lead or the owner): BriefReq.To, a role agent, is briefed; with a
// text, its line is delivered to that agent when its pane is idle (posted or typed as its kind takes it, deliver).
// The new brief ends the agent's undelivered brief lines (its fact's supersedes) and says so in its
// reply. The lead hears once the agent has finished. A brief to an idle deep dive moves its worktree to the
// integration branch's head first (briefMove; a publish running does not hold it); that git failing:
// refused, nothing recorded.
// BriefReq.Fresh (obor brief --fresh): the brief, once recorded, starts the agent fresh with its line as the
// first prompt, stopping it first when it runs (routeOf, deliverLine); a refused call stops nothing.
func (a *App) briefCall(c wire.BriefReq) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodBrief + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodBrief, c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodBrief, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	}
	if err := leadOr(p.root, wire.MethodBrief, c.Envelope, fmt.Sprintf("brief: the lead or the owner briefs, not %s (role %s)", Dash(c.Agent),
		c.Role)); err != nil {
		return "", err
	}
	switch {
	case c.To == "":
		return "", errors.New("brief: the role agent is required")
	case c.To == p.out.Lead():
		return "", fmt.Errorf("brief: %s is the lead: obor send reaches another project's lead", c.To)
	case c.Fresh && strings.TrimSpace(c.Text) == "":
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

	reply, ok := a.recordBrief(p, c.To, from, c.Text, c.CallID, c.Prompt, c.Fresh, nil)
	if !ok {
		return "", fmt.Errorf("brief: the event log of %s refused it", p.name)
	}
	a.wake() // a fresh brief's stop and start are the delivery's (deliverLine), in the merge loop's pass
	return reply, nil
}

// recordBrief records fact briefed of the role agent to from from (note "": the restart's or a review's fresh start
// carried the brief; call "": no call; prompt: that start's first prompt, its text when there is no line): its reply,
// false when the log refused it. The new brief ends the agent's undelivered brief lines (its fact's supersedes).
// Without a note, a pane already identified as the agent's may be working on it: that work is noted at once.
// The text sent is a row of the project's prompts table: the line for a brief, the start's prompt for a start.
// fresh: its line starts the agent fresh, the running agent stopped first. v: a verify's brief (nil: none;
// reviewverify.go): its tasks and head in the fact (Task, Reviewed), and not recorded (false) while the agent has an
// open brief, decided in the a.mu section that records it.
func (a *App) recordBrief(p *proj, to, from, note, call, prompt string, fresh bool, v *verifyBrief) (string, bool) {
	// A newer brief makes an older undelivered brief obsolete: the agent's undelivered brief lines are named
	// in the new brief's fact, whose fold retires them (one append, so a crash or a refused append cannot
	// bring one back). A line whose delivery is claimed (sending: it may have landed) stays; a lead line is
	// never superseded. The choice and the append are one a.mu section: no delivery claims a chosen line meanwhile.
	a.mu.Lock()
	if v != nil && p.out.Brief(to) != nil { // a brief recorded since the verify's check: the next pass looks again
		a.mu.Unlock()
		return "", false
	}
	var old []int64
	for _, q := range p.out.Queued() {
		if q.IsBrief() && q.Msg().To == to && q.Msg().To != p.out.Lead() && q.State() == outbox.Waiting {
			old = append(old, q.Seq())
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
func (a *App) callProj(method string, c wire.Envelope) (p *proj, reply string, stored bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p = a.projAt(c.Root)
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(method, c.CallID)]
	}
	return p, reply, stored
}
