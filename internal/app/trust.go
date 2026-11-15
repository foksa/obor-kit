// trust.go: a serve-made start held by a "Trust this folder?" dialog (port.TrustDialog) is a started agent: its
// caller records the start as a successful one, then the trust needs-you naming the pane, which the trust rule ends
// once the pane no longer shows the dialog.
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

// trustReason is the trust needs-you's reason: agent's start waits at the dialog in pane.
func trustReason(agent, pane string) string {
	return agent + " started and waits at a 'Trust this folder?' dialog in pane " + pane + ": answer it there: Enter = trust"
}

// trustNeeds records the trust needs-you (task key trust) of agent's start held by td: one per pane, none when one
// for td's pane is in force. The start is the caller's to record first, as a start that succeeded.
func (a *App) trustNeeds(p *proj, agent string, td *port.TrustDialog) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.trustShown(td.Pane) {
		return
	}
	a.logf("%s: %s", p.name, trustReason(agent, td.Pane))
	a.recordLocked(p, event.Record{Task: event.TrustTask, Step: event.StepNeedsYou, Kind: event.KindFact},
		event.FactData{Machine: Machine, Pane: td.Pane, Agent: agent, Reason: trustReason(agent, td.Pane)})
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
