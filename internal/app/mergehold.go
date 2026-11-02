package app

import (
	"obor/internal/app/event"
	"time"

	"obor/internal/config"
)

// The merge hold (T-718): the reviewer and the deep dive read the main checkout, so the merge loop's publish (the step
// that moves the integration branch) waits while one of them works — its agent has an open brief (a brief waiting to
// be delivered, or one it works on: messages.go) or its pane is not idle (roleIdle) — and the code stays put under
// them for their turn. Only the publish waits: gates, jobs and dispatch go on. A task held holdCap goes ahead anyway
// and the lead gets one line ("[obor] merge of T-x went ahead: <role> still working after 30m"). obor status shows
// "merge held: <role> working" on the waiting task. The hold is live state: nothing is recorded, a replay folds the
// task accepted as before and the next pass decides the hold afresh (its clock starts again).

// holdCap is how long a task's publish waits for a working reviewer or deep dive before it goes ahead.
const holdCap = 30 * time.Minute

// holdRoles are the roles whose work holds the merge, in the order the hold names them.
var holdRoles = []string{"reviewer", "deep_dive"}

// holdAgent is a role whose agent's work holds the merge.
type holdAgent struct{ role, agent string }

// mergeHold is one task's hold: since when its publish waits, the role it waits for, and whether its cap's line to the
// lead was decided (its publish went ahead).
type mergeHold struct {
	since time.Time
	role  string
	told  bool
}

// holdAgents is cfg's configured reviewer and deep dive agents.
func holdAgents(cfg *config.Config) []holdAgent {
	var out []holdAgent
	for _, role := range holdRoles {
		if rc, ok := cfg.Role(role); ok && rc.Configured && rc.Agent != "" {
			out = append(out, holdAgent{role: role, agent: rc.Agent})
		}
	}
	return out
}

// holder is the first role whose agent works now ("": none) (under mu).
func (a *App) holder(p *proj) string {
	for _, h := range p.holders {
		if !a.roleIdle(p, h.agent) {
			return h.role
		}
	}
	return ""
}

// holdPublish decides task's publish (under mu): held (it waits for role), or not; capped is the role a publish goes
// ahead despite, once (its line to the lead), else "".
func (a *App) holdPublish(p *proj, task string) (held bool, capped string) {
	role := a.holder(p)
	if role == "" {
		delete(p.holds, task)
		return false, ""
	}
	now := a.opt.Now()
	h, ok := p.holds[task]
	if !ok {
		h.since = now
	}
	h.role = role
	switch {
	case now.Sub(h.since) < holdCap:
		p.holds[task] = h
		return true, ""
	case h.told:
		return false, ""
	}
	h.told = true
	p.holds[task] = h
	return false, role
}

// holdLine records the lead's line of a publish that went ahead after holdCap: false when the log refused it.
func (a *App) holdLine(p *proj, task, role string) bool {
	m := event.Message{To: p.out.lead(), From: "obor",
		Line: "[obor] merge of " + task + " went ahead: " + role + " still working after " + Age(holdCap)}
	return a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
}

// heldBy is obor status' hold of mt ("merge held: <role> working"; "": none): an accepted task, not publishing, whose
// publish waits now (under mu).
func (a *App) heldBy(p *proj, mt *mtask) string {
	h, ok := p.holds[mt.task]
	if !ok || h.told || mt.state != stAccepted || p.running.Task == mt.task {
		return ""
	}
	if role := a.holder(p); role != "" {
		return "merge held: " + role + " working"
	}
	return ""
}
