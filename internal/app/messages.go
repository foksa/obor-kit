package app

import (
	"errors"
	"fmt"
	"strings"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/wire"
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
// (the herdr call of job delivery). A line held (its agent busy, not identified; a lead line whose inbox record is not
// live) notes why (outbox.Line.Held: obor status, the line's needs-you). The oldest first and one per agent per pass, under
// deliver.<seq> (P, a wake's transport: prompt.go).
//
// A queued line is in one state (outbox.State; its moves: State.Next): waiting (nothing went out; why: the
// pass's hold, else its last refusal's reason), sending (its delivery claimed or its intent in the log: a byte may
// have gone out), sent and retired (both leave the outbox). A refusal before any byte went out (no inbox record, a
// refused dial, a refused typing) is fact failed{step: deliver.<seq>, reason, via: inbox for a post}: the line waits
// again with that reason (obor status, its needs-you) and is delivered again on a later pass, also after a restart,
// typed when the refused one was a post to an idle identified agent (a lead line goes by its hooks or waits); a post
// serve's own stop cut before a byte records nothing and waits the same. An answer's line retires once its
// question is settled another way (outbox.RetireAnswers): waiting, at once; sending, it is marked, and then a refusal before a
// byte retires it, a delivery sends it, and a later pass closes its open delivery unsent (result outbox.RetiredResult). A
// delivery a crash cut (its intent pending) is delivered again too: a line may reach its agent twice, never not at
// all. A typed line herdr refuses because the pane is blocked (blocked, agent_blocked) is such a refusal,
// retried on a later pass; a posted one cannot land in a dialog at all (a session behind an open permission dialog reads it
// when its turn continues, and it cannot answer the dialog). A line waiting longer than msgWait is the project's
// needs-you until no such line waits.

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

// settles: r settles its task's question another way: the worker submitted or took its job (its job_next, its first
// fetch or a refetch), asked again, the task was unblocked, dropped, rejected or merged (a pruned task keeps only its
// merged fact, which ends the question a lead's answer to it would otherwise reopen).
func settles(r event.Record) bool {
	if r.Task == event.MsgTask {
		return false
	}
	if r.Kind == event.KindResult {
		m, _ := verdictMethod(r.Step)
		return m == wire.MethodReject
	}
	return r.Kind == event.KindFact && (r.Step == event.StepSubmitted || r.Step == event.StepDelivered ||
		r.Step == event.StepRefetched || r.Step == event.StepUnblocked || r.Step == event.StepDropped ||
		r.Step == event.StepMerged || isAsk(r))
}

// unsettledAnswer: task has an answer line queued, not delivered and not settled yet (under mu): waiting, or being
// sent (a refusal before a byte would put it back to wait). What a worker's refetch of its job settles: a waiting line
// retires, one being sent becomes SendingSettled (its refusal before a byte retires it, its delivery stands).
func (p *proj) unsettledAnswer(task string) bool {
	for _, q := range p.out.Queued() {
		if s := q.State(); q.IsAnswer() && q.Msg().Task == task && (s == outbox.Waiting || s == outbox.Sending) {
			return true
		}
	}
	return false
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
	if err := leadOr(p.cfgNow, wire.MethodSend, "sends", c.Envelope); err != nil {
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
	a.mu.Lock()
	mode, inbox := a.leadLines(p)
	a.mu.Unlock()
	switch {
	case mode == agents.LinesHooks && !inbox:
		m.Reply = "SENT to " + p.out.Lead() + " in " + p.name + " (queued: " + p.out.Lead() + "'s kind has no inbox: " +
			byHooks + ")"
	case !a.inboxed(p.out.Lead()):
		what := "no session recorded for " + p.out.Lead() // its kind has no inbox: its lines wait for one
		how := ""                                         // a kind whose hooks act on the answers: they hand the line meanwhile
		if inbox {
			what = noLiveInbox(p.out.Lead())
		}
		if mode == agents.LinesHooks {
			how = "; " + byHooks
		}
		m.Reply = "SENT to " + p.out.Lead() + " in " + p.name + " (queued: " + what +
			how + "; `obor restart " + p.out.Lead() + "` once it is idle registers it, or quit it and run `obor lead`)"
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("send: the event log of %s refused it", p.name)
	}
	a.wake()
	return m.Reply, nil
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
