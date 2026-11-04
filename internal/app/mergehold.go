package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"time"

	"obor/internal/config"
)

// The merge hold (T-718): the reviewer and the deep dive read the main checkout, so the merge loop's publish (the step
// that moves the integration branch) waits while one of them works — its agent has an open brief (a brief waiting to
// be delivered, or one it works on: messages.go) or its pane is not idle (roleIdle) — and the code stays put under
// them for their turn. Only the publish waits: gates, jobs and dispatch go on. A task held holdCap goes ahead anyway
// and the lead gets one line ("[obor] merge of T-x went ahead: <role> still working after 30m"). obor status shows
// "merge hold: <role>" on the waiting task (its phase, T-788). The hold is live state: nothing is recorded, a replay folds the
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

// holdAgentsNow is the hold's agents by the current config (config.LoadDaemon, the load reconcile's names use), kept
// in p.holders: a role renamed, added or removed while serve runs counts at the next check; a config that does not
// load keeps the last good set (T-730; under mu).
func (p *proj) holdAgentsNow() []holdAgent {
	if cfg, err := config.LoadDaemon(p.root); err == nil {
		p.holders = holdAgents(cfg)
	}
	return p.holders
}

// holdAgent: agent is one of the hold's agents by the current config (holdAgentsNow; under mu).
func (p *proj) holdAgent(agent string) bool {
	for _, h := range p.holdAgentsNow() {
		if h.agent == agent {
			return true
		}
	}
	return false
}

// holder is the first role whose agent works now ("": none) (under mu).
func (a *App) holder(p *proj) string {
	for _, h := range p.holdAgentsNow() {
		if !a.roleIdle(p, h.agent) {
			return h.role
		}
	}
	return ""
}

// Admission (T-730): a start that reads the main checkout (a review's start, obor review verify, a brief to a hold
// agent) passes one serve-owned point first. It waits while a publish runs (the head it reads is the published one),
// and once admitted no publish starts (scratchStep refuses one) until its brief is recorded and released: from then
// the hold above keeps the code still while the agent works. The admission, the hold and a publish's start are decided
// together under App.mu at the publish's actual start (scratchStep, T-733), so one of them is first. A call waits at most admitWait; the merge loop's review start does not wait, it
// tries again on the pass after the publish (the publish's end wakes the loop).

// admitWait bounds a call's wait for the running publish: well inside the socket's 10 s deadline, so the brief's git
// and its record still fit.
const admitWait = 6 * time.Second

// publishing: the merge loop's publish runs now (under mu).
func (p *proj) publishing() bool {
	base, _ := event.StepN(p.running.Step)
	return p.running.Task != "" && base == event.StepPublish
}

// admit waits until no publish runs, then admits one start: no publish starts until release, called once its brief is
// recorded (or it failed). ctx ending the wait: an error naming the publish, nothing admitted.
func (a *App) admit(ctx context.Context, p *proj) (release func(), err error) {
	for {
		a.mu.Lock()
		if !p.publishing() {
			p.admits++
			a.mu.Unlock()
			return func() { a.unadmit(p) }, nil
		}
		task, done := p.running.Task, p.published
		a.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, fmt.Errorf("the publish of %s is still running: try again", task)
		}
	}
}

// unadmit ends one admission and wakes the loop: a publish it held may start.
func (a *App) unadmit(p *proj) {
	a.mu.Lock()
	p.admits--
	a.mu.Unlock()
	a.wake()
}

// holding: task's publish waits for a working role now (under mu): pickAccepted's cheap filter. It starts the hold's
// clock but decides no cap: scratchStep's holdPublish, at the publish's start, is the decision that counts (T-733).
func (a *App) holding(p *proj, task string) bool {
	_, _, held := a.holdState(p, task)
	return held
}

// holdPublish decides task's publish at its start (under mu, with p.running: scratchStep): held (it waits for role),
// or not; capped is the role a publish goes ahead despite, once (its line to the lead), else "".
func (a *App) holdPublish(p *proj, task string) (held bool, capped string) {
	role, h, held := a.holdState(p, task)
	if role == "" || held || h.told {
		return held, ""
	}
	h.told = true
	p.holds[task] = h
	return false, role
}

// holdState is task's hold now (under mu): the working role ("": none, the hold is dropped), its hold (its clock
// started at the first check that saw the role working) and whether it still waits (inside holdCap).
func (a *App) holdState(p *proj, task string) (role string, h mergeHold, held bool) {
	role = a.holder(p)
	if role == "" {
		delete(p.holds, task)
		return "", mergeHold{}, false
	}
	now := a.opt.Now()
	h, ok := p.holds[task]
	if !ok {
		h.since = now
	}
	h.role = role
	p.holds[task] = h
	return role, h, now.Sub(h.since) < holdCap
}

// holdLine records the lead's line of a publish that went ahead after holdCap: false when the log refused it.
func (a *App) holdLine(p *proj, task, role string) bool {
	m := event.Message{To: p.out.lead(), From: "obor",
		Line: "[obor] merge of " + task + " went ahead: " + role + " still working after " + Age(holdCap)}
	return a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
}

// holdRole is the role holding mt's merge ("": none): an accepted task, not publishing, whose publish waits now (under
// mu; obor status shows wire.MergeHold of it).
func (a *App) holdRole(p *proj, mt *mtask) string {
	h, ok := p.holds[mt.task]
	if !ok || h.told || mt.state != stAccepted || p.running.Task == mt.task {
		return ""
	}
	return a.holder(p)
}
