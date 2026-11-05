package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"time"

	"obor/internal/config"
)

// Role worktrees (T-853, E2; T-718's merge hold deleted): the reviewer and the deep dive read their own worktree,
// <lanes_dir>/<role> (config.RoleWorktree), detached at the head serve hands them, so a merge never waits for them
// (the integrator has one too, which serve does not move: a review job checks out its candidate there itself).
// Serve moves a role worktree (ReviewGit.Detach, which adds it when it is missing) at the admission point of the start
// that hands the role a head: the reviewer's at a review's start and at each verify once it is idle (T-856: never
// under its turn), to the head it reviews; the deep dive's at a brief while it is idle, to the
// integration branch's head. A brief to a working deep dive leaves its worktree where it is (the code does not move
// under its turn), and a plain brief to the reviewer never moves it (its head is the reviewed one). The role may check out any commit there (the guard); its files go to
// the main checkout's paths.reviews.

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

// wtAgentsNow is the role worktree agents by the current config (config.LoadDaemon, the load reconcile's names use),
// kept in p.wtAgents: a role renamed, added or removed while serve runs counts at the next check; a config that does
// not load keeps the last good set (T-730; under mu).
func (p *proj) wtAgentsNow() []wtAgent {
	if cfg, err := config.LoadDaemon(p.root); err == nil {
		p.wtAgents = wtAgents(cfg)
	}
	return p.wtAgents
}

// wtRole is the role of agent when it is a role worktree agent by the current config ("": none; under mu).
func (p *proj) wtRole(agent string) string {
	for _, w := range p.wtAgentsNow() {
		if w.agent == agent {
			return w.role
		}
	}
	return ""
}

// briefMove moves role's worktree to the integration branch's head for a brief to its agent (once admitted): only the
// deep dive's, only while it is idle (roleIdle: no open brief, its pane idle or not running). An error names what
// failed; the brief is then refused.
func (a *App) briefMove(ctx context.Context, p *proj, role, agent string) error {
	a.mu.Lock()
	idle := a.roleIdle(p, agent)
	a.mu.Unlock()
	if role != "deep_dive" || !idle {
		return nil
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return err
	}
	fx, err := a.opt.Loop(p.root)
	if err == nil && fx.Review == nil {
		err = fmt.Errorf("the merge loop has no review git")
	}
	if err != nil {
		return err
	}
	head, err := fx.Review.Head(ctx, cfg.Data.Branches.Main)
	if err != nil {
		return err
	}
	return fx.Review.Detach(ctx, cfg.RoleWorktree(role), head)
}

// Admission (T-730): a start that hands a role a head (a review's start, a fix round's verify, a brief to a role worktree
// agent) passes one serve-owned point first. It waits while a publish runs (the head it reads is the published one),
// and once admitted no publish starts (scratchStep refuses one) until its brief is recorded and released. The
// admission and a publish's start are decided together under App.mu at the publish's actual start (scratchStep,
// T-733), so one of them is first. A call waits at most admitWait; the merge loop's review start does not wait, it
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
