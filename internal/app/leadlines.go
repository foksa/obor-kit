package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/wire"
)

// Lines to a lead whose session has no live inbox (design "Lines to the lead"): the delivery pass holds them (routeOf:
// hookHeld) and the lead's UserPromptSubmit and Stop hooks ask for them (wire.MethodLeadLines), handing them over as
// context at a prompt or as the block of a turn's end. The call delivers them under a post's records (intent
// deliver.<head> naming its members, via hook; result sent; each other member's own result), so obor status, the
// 24 h needs-you and a needs-you line's repeat read one delivery whichever path made it, and a line handed out once
// is never handed out or posted again. A line is claimed (Sending) by one path at a time: the pass posts no line a
// hook call is handing, and a hook call hands no line the pass is posting.
//
// The hook's request names the instant its call ends (wire.LeadLinesReq.UntilMS): lines are recorded as handed only
// while serve answers before then, and a request read after it hands nothing, leaving the lines queued for the next
// hook or the inbox.

// leadLines is how lines reach p's lead now (under mu; agents.LeadLines): its kind (its identified pane's, else its
// configured one) and whether its inbox record is live (Options.Inboxed, project.InboxLive); inbox: that kind has an
// inbox. An unknown kind or no lead: status.
func (a *App) leadLines(p *proj) (mode string, inbox bool) {
	lead := p.out.Lead()
	if p.kinds == nil || lead == "" {
		return agents.LinesStatus, false
	}
	kind := p.out.LeadKind()
	if w := a.whereIs(p, lead); w.State == Identified && w.Pane.Agent != "" {
		kind = w.Pane.Agent
	}
	k := p.kind(kind)
	if k == nil {
		return agents.LinesStatus, false
	}
	c := k.Caps()
	return agents.LeadLines(c, c.Inbox && a.inboxed(lead)), c.Inbox
}

// hookHeld is why a line to p's lead waits for its hooks (under mu; "": its lines do not go through them,
// leadLines): its hook transport acts on the hooks' answers (agents.Caps.HookAnswers) and its kind has no inbox, or
// its inbox record is not live (none, its socket gone, or a refused send). A kind with neither a live inbox nor the answers keeps its lead's
// lines waiting (obor status, the 24 h needs-you).
func (a *App) hookHeld(p *proj) string {
	mode, inbox := a.leadLines(p)
	switch {
	case mode != agents.LinesHooks:
		return ""
	case inbox:
		return "no inbox recorded for " + p.out.Lead() + ": " + byHooks
	}
	return p.out.Lead() + "'s kind has no inbox: " + byHooks
}

// byHooks is hookHeld's clause of how a lead's held lines reach it.
const byHooks = "its hooks hand it its lines at its next prompt or turn end"

// leadLinesCall answers the socket's lead_lines request (the lead's UserPromptSubmit or Stop hook): the lines queued
// for the lead, one per row, recorded as delivered before the reply goes ("": nothing to hand, or the call ended
// first). One post's worth: an open post's members (a crash cut its result: handed again, as a post is posted again),
// else every line queued for the lead that no post is sending. Only while the lead takes its lines through its hooks
// (hookHeld): any other lead is refused (its inbox is live, its lines posted there; or its hooks do not act on the
// answers, its lines waiting). The request names the instant its call ends (UntilMS): past it nothing is recorded as
// handed, and the lines stay queued for the next hook or the inbox. A repeated call id gets the stored reply (a reply
// lost on the way is handed again; a restart forgets it). No ack: sent means handed to the hook, as a post's means
// handed to the socket.
func (a *App) leadLinesCall(c wire.LeadLinesReq) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodLeadLines + ": a call id is required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	if p == nil {
		a.mu.Unlock()
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodLeadLines, c.Root)
	}
	if reply, stored := p.replies[verdictStep(wire.MethodLeadLines, c.CallID)]; stored {
		a.mu.Unlock()
		return reply, nil
	}
	lead := p.out.Lead()
	switch {
	case c.Role != "lead" || c.Agent == "" || c.Agent != lead:
		a.mu.Unlock()
		return "", fmt.Errorf("%s: the lead's hooks ask for its lines, not %s (role %s)", wire.MethodLeadLines,
			Dash(c.Agent), Dash(c.Role))
	case a.hookHeld(p) == "":
		a.mu.Unlock()
		return "", fmt.Errorf("%s: %s takes no lines through its hooks (its inbox is live, or its hooks do not act on "+
			"the answers)", wire.MethodLeadLines, lead)
	}
	// The claim: no other call hands these lines meanwhile (the delivery pass holds them: hookHeld).
	var qs []outbox.Line
	for _, q := range p.out.Queued() {
		if (q.IsLead() || q.Msg().To == lead) && !q.Sending {
			q.Sending = true
			qs = append(qs, *q)
		}
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		for _, q := range qs {
			if m := p.out.Line(q.Seq()); m != nil {
				m.Sending = false
			}
		}
		a.mu.Unlock()
	}()
	text, err := a.handLines(p, qs, c.UntilMS)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	p.replies[verdictStep(wire.MethodLeadLines, c.CallID)] = text
	a.mu.Unlock()
	return text, nil
}

// handLines records the delivery of the post leadPost makes of qs (the lead's lines, claimed) as the hook's: its
// intent, its result sent, each other member's own result; its text, one line per row ("": none to hand). untilMS is
// the instant the hook's call ends (epoch milliseconds, 0: no deadline): past it nothing is recorded as handed, and
// qs stay queued for the next hook or the inbox.
func (a *App) handLines(p *proj, qs []outbox.Line, untilMS int64) (string, error) {
	if len(qs) == 0 || a.callEnded(untilMS) {
		return "", nil
	}
	head, lines, _ := a.leadPost(p, qs, lineKey) // a result kept unwritten is written: the lines go again
	if len(lines) == 0 {
		return "", nil
	}
	members := make([]int64, len(lines))
	text := make([]string, len(lines))
	for i, q := range lines {
		members[i], text[i] = q.Seq(), q.Msg().Line
	}
	ctx := context.Background()
	_, err := a.do(ctx, p, event.Intent{Key: lineKey(head), Class: event.ClassPrompt,
		Data: outbox.LineData{To: lines[0].Msg().To, Lines: members, Via: outbox.ViaHook}},
		func(context.Context) (any, error) { return "sent", nil })
	if err != nil {
		return "", fmt.Errorf("%s: the event log of %s refused the delivery: %v", wire.MethodLeadLines, p.name, err)
	}
	a.membersSent(ctx, p, lines, head)
	a.wake() // the pass ends the 24 h needs-you of a line delivered here
	return strings.Join(text, "\n"), nil
}

// callEnded is the hook's call ended at untilMS (epoch milliseconds; 0: no deadline, an older binary): the hook is
// gone, so nothing it asked for may be recorded as handed.
func (a *App) callEnded(untilMS int64) bool {
	return untilMS > 0 && !a.opt.Now().Before(time.UnixMilli(untilMS))
}
