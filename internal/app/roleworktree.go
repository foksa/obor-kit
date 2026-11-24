package app

import (
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"time"

	"obor/internal/config"
)

// Role worktrees: the reviewer and the deep dive read their own worktree,
// <lanes.dir>/<role> (config.RoleWorktree), detached at the head serve hands them, so a merge never waits for them
// (the integrator has one too, which serve does not move: a review job checks out its candidate there itself).
// Serve moves a role worktree (port.Review.Detach, which adds it when it is missing and gives it a lane's ready/setup)
// at the start that hands the role a
// head, to the sha that start read (no publish admission): the reviewer's at a review's start and at each
// verify once it is idle (never under its turn), to the head it reviews; the deep dive's at a brief while it is
// idle, to the integration branch's head. A brief to a working deep dive leaves its worktree where it is (the code
// does not move under its turn), and a plain brief to the reviewer never moves it (its head is the reviewed one). The
// role may check out any commit there (the guard); its files go to the main checkout's reviews.dir.

// wtAgent is a role agent whose role worktree serve moves.
type wtAgent struct{ role, agent string }

// wtRoles are the roles whose role worktree serve moves to the head it hands them, in order.
var wtRoles = []string{"reviewer", "deep_dive"}

// wtAgents is cfg's configured agents of wtRoles.
func wtAgents(cfg *config.Config) []wtAgent {
	var out []wtAgent
	for _, role := range wtRoles {
		if rc, ok := cfg.Role(role); ok && rc.Configured && rc.Agent != "" {
			out = append(out, wtAgent{role: role, agent: rc.Agent})
		}
	}
	return out
}

// wtAgentsNow is the role worktree agents by cfg, kept in p.wtAgents: a role renamed, added or removed while serve
// runs counts at the next call that has the config; a nil cfg (a config that does not load) keeps the last good set
// (under mu).
func (p *proj) wtAgentsNow(cfg *config.Config) []wtAgent {
	if cfg != nil {
		p.wtAgents = wtAgents(cfg)
	}
	return p.wtAgents
}

// wtRole is the role of agent when it is a role worktree agent by the current config ("": none; under mu).
func (p *proj) wtRole(cfg *config.Config, agent string) string {
	for _, w := range p.wtAgentsNow(cfg) {
		if w.agent == agent {
			return w.role
		}
	}
	return ""
}

// briefMove moves role's worktree to the integration branch's head for a brief to its agent: only the
// deep dive's, only while it is idle (roleIdle: no open brief, its pane idle or not running). An error names what
// failed; the brief is then refused.
func (a *App) briefMove(ctx context.Context, p *proj, fx *Loop, role, agent string) error {
	a.mu.Lock()
	idle := a.roleIdle(p, agent)
	a.mu.Unlock()
	if role != "deep_dive" || !idle {
		return nil
	}
	if fx.Workflow == nil {
		return fmt.Errorf("the merge loop has no workflow")
	}
	head, err := fx.Workflow.Head(ctx, fx.Cfg.MainBranch())
	if err != nil {
		return err
	}
	return fx.Workflow.Detach(ctx, fx.Cfg.RoleWorktree(role), head)
}

// Admission: a call that commits in the main checkout (config_set) passes one serve-owned point first.
// It waits while a publish runs (both move main there), and once admitted no publish starts (scratchStep refuses one)
// until its commit is in and it released. The admission and a publish's start are decided together under App.mu at
// the publish's actual start (scratchStep), so one of them is first. A call waits at most admitWait. A start
// that hands a role a head (a review's start, a verify, a brief to the reviewer or the deep dive) is not admitted:
// it reads the head once and uses that sha after (the merges, the files, Detach, the brief), the role reads
// its own worktree, and a publish moves only main in the main checkout; a merge published meanwhile falls into the
// next review's scope.

// admitWait bounds a call's wait for the running publish: well inside the socket's 10 s deadline, so the commit and
// its record still fit.
const admitWait = 6 * time.Second

// publishing: the merge loop's publish runs now (under mu).
func (p *proj) publishing() bool {
	base, _ := event.StepN(p.running.Step)
	return p.running.Task != "" && base == event.StepPublish
}

// admit waits until no publish runs, then admits one call: no publish starts until release, called once its commit is
// in (or it failed). ctx ending the wait: an error naming the publish, nothing admitted. A restart on a new obor binary
// claimed (p.restartClaim): refused, nothing admitted (the restart would cancel the commit). what names the call
// (p.admitOps: a pending restart's wait names it).
func (a *App) admit(ctx context.Context, p *proj, what string) (release func(), err error) {
	for {
		a.mu.Lock()
		if p.restartClaim {
			a.mu.Unlock()
			return nil, errors.New("serve is restarting on a new obor binary: try again in a moment")
		}
		if !p.publishing() {
			p.admitOps.add(what)
			a.mu.Unlock()
			return func() { a.unadmit(p, what) }, nil
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

// admitting: an admission is in force (under mu): no publish starts; the restart's wait reads the same admitOps.
func (p *proj) admitting() bool { return len(p.admitOps) > 0 }

// unadmit ends one admission (what, as admit named it) and wakes the loop: a publish it held may start.
func (a *App) unadmit(p *proj, what string) {
	a.mu.Lock()
	p.admitOps.del(what)
	a.mu.Unlock()
	a.wake()
}
