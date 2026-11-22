package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/wire"
)

// Lines to a lead whose kind has no inbox (design "Lines to the lead"): the delivery pass holds them (routeOf:
// hookHeld) and the lead's UserPromptSubmit and Stop hooks ask for them (wire.MethodLeadLines), handing them over as
// context at a prompt or as the block of a turn's end. The call delivers them under a post's records (intent
// deliver.<head> naming its members, via hook; result sent; each other member's own result), so obor status, the
// 24 h needs-you and a needs-you line's repeat read one delivery whichever path made it, and a line handed out once
// is never handed out or posted again.

// hookLead: p's lead takes its lines through its hooks (under mu): its kind (its identified pane's, else its
// configured one) is known and has no inbox.
func (a *App) hookLead(p *proj) bool {
	if p.kinds == nil || p.out.Lead() == "" {
		return false
	}
	kind := p.out.LeadKind()
	if w := a.whereIs(p, p.out.Lead()); w.State == Identified && w.Pane.Agent != "" {
		kind = w.Pane.Agent
	}
	k := p.kind(kind)
	return k != nil && !k.Caps().Inbox
}

// hookHeld is routeOf's hold of a line to lead, which takes its lines through its hooks.
func hookHeld(lead string) string {
	return lead + "'s kind has no inbox: its hooks hand it its lines at its next prompt or turn end"
}

// leadLinesCall answers the socket's lead_lines request (the lead's UserPromptSubmit or Stop hook): the lines queued
// for the lead, one per row, recorded as delivered before the reply goes ("": none). One post's worth: an open post's
// members (a crash cut its result: handed again, as a post is posted again), else every line queued for the lead. Only
// while the lead takes its lines through its hooks (hookLead): a lead whose kind has an inbox is refused, its lines
// are posted there. A repeated call id gets the stored reply (a reply lost on the way is handed again; a restart
// forgets it). No ack: sent means handed to the hook, as a post's means handed to the socket.
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
	case !a.hookLead(p):
		a.mu.Unlock()
		return "", fmt.Errorf("%s: %s's kind has an inbox: its lines are posted there", wire.MethodLeadLines, lead)
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
	text, err := a.handLines(p, qs)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	p.replies[verdictStep(wire.MethodLeadLines, c.CallID)] = text
	a.mu.Unlock()
	return text, nil
}

// handLines records the delivery of the post leadPost makes of qs (the lead's lines, claimed) as the hook's: its
// intent, its result sent, each other member's own result; its text, one line per row ("": none to hand).
func (a *App) handLines(p *proj, qs []outbox.Line) (string, error) {
	if len(qs) == 0 {
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
