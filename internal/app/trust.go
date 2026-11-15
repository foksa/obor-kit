// trust.go: a serve-made fresh start held by a "Trust this folder?" dialog (port.TrustDialog) is a started agent: its
// caller records the start as a successful one, then the trust needs-you naming the pane, which the trust rule ends
// once the pane no longer shows the dialog. A resumed start at the dialog is a failed start (launch's): its callers
// take their failed-start path.
package app

import (
	"cmp"
	"context"
	"errors"
	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"time"
)

// ruleTrust is the trust rule's name: its key in the rule pass.
const ruleTrust = "trust.answered"

// trustScreen is how many screen lines the trust rule reads: launch's look at a failed start's screen.
const trustScreen = 60

// trustHold is a start held by a trust dialog: the pane it runs in (its port.Started's) and the guidance (the
// port.TrustDialog's Hint, agents.TrustHint), the trust needs-you's reason as the launch said it.
type trustHold struct {
	pane, hint string
}

// trustHeld is a launch's result s, err read for a trust dialog: a start held by one (port.TrustHeld) is a start, its
// hold and no error; any other result is err as it is, no hold.
func trustHeld(s port.Started, err error) (*trustHold, error) {
	if td := port.TrustHeld(err); td != nil {
		return &trustHold{pane: s.Pane, hint: td.Hint}, nil
	}
	return nil, err
}

// trustNeeds records the trust needs-you (task key trust) of agent's start held as h, its reason h's guidance: one
// per pane, none when one for h's pane is in force. The start is the caller's to record first, as a start that
// succeeded.
func (a *App) trustNeeds(p *proj, agent string, h *trustHold) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.trustShown(h.pane) {
		return
	}
	a.logf("%s: %s", p.name, h.hint)
	a.recordLocked(p, event.Record{Task: event.TrustTask, Step: event.StepNeedsYou, Kind: event.KindFact},
		event.FactData{Machine: Machine, Pane: h.pane, Agent: agent, Reason: h.hint})
}

// trustShown: a trust needs-you for pane is in force (under mu).
func (p *proj) trustShown(pane string) bool {
	for _, f := range p.folded.active[event.TrustTask] {
		if f.Step == event.StepNeedsYou && f.Fact().Pane == pane {
			return true
		}
	}
	return false
}

// trustAnswered ends the trust needs-you of pane (a resumed fact of task key trust, its fold).
func (p *proj) trustAnswered(pane string) {
	var kept []event.Record
	for _, f := range p.folded.active[event.TrustTask] {
		if f.Fact().Pane != pane {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		delete(p.folded.active, event.TrustTask)
		return
	}
	p.folded.active[event.TrustTask] = kept
}

// trustRule is the trust rule. Condition: a trust needs-you in force (the oldest first) whose pane's screen no longer
// shows the dialog (agents.TrustDialog), or whose pane is gone; a screen that cannot be read is no answer. Action:
// fact resumed naming the pane, which ends that needs-you (the needs rule lines it again only while it is in
// force). Keyed by the needs-you's seq; done when it is no longer in force.
func trustRule(ctx context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool) {
	if a.opt.Screen == nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	fs := append([]event.Record(nil), p.folded.active[event.TrustTask]...)
	a.mu.Unlock()
	for _, f := range fs {
		pane := f.Fact().Pane
		screen, err := a.opt.Screen(ctx, pane, trustScreen)
		switch {
		case err != nil && !port.NotFound(err):
			continue
		case err == nil && agents.TrustDialog(screen):
			continue
		}
		return f.Seq, action{call: func(context.Context) error {
			if !a.record(p, event.Record{Task: event.TrustTask, Step: event.StepResumed, Kind: event.KindFact},
				event.FactData{Machine: Machine, Pane: pane}) {
				return cmp.Or(p.log.Err(), errors.New("the event log refused fact "+event.StepResumed))
			}
			a.logf("%s: the trust dialog in pane %s is answered", p.name, pane)
			return nil
		}}, true
	}
	return 0, action{}, false
}
