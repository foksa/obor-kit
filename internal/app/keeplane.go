// keeplane.go: a keep lane's agent between its tasks (T-1031, config.LaneKeep). A keep lane's merged task stops no
// agent (afterList); its next dispatch prepares the lane (clean: lane.prepare requires it) and sends the work job's wake
// to the live agent (nextDispatch: no agent.start), or starts one as a fresh lane does when the lane has none. The
// handoff rule stops a kept agent that is idle between tasks with its session past handoff_tokens; the next dispatch's
// agent.start is its fresh start (one starter per lane: a rule's start can never race a dispatch's).
package app

import (
	"cmp"
	"context"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"time"

	"obor/internal/config"
	"obor/internal/project"
)

// ruleHandoff is the keep lane handoff rule's name: its key in the rule pass.
const ruleHandoff = "lane.handoff"

// keepLive is the pane of mt's keep lane's live agent (under mu): mt's agent identified in its lane's worktree, and no
// agent.start of mt pending (a pending start is nextStart's to settle). ok false: none, the dispatch starts one.
func (a *App) keepLive(p *proj, mt mtask) (port.Pane, bool) {
	if p.log.Open(event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepStartWork, mt.starts+1)}) {
		return port.Pane{}, false
	}
	w, _ := a.agentOf(p, port.Lane{Dir: p.laneDir(mt), Agent: mt.agent})
	if w.State != Identified {
		return port.Pane{}, false
	}
	return w.Pane, true
}

// kept: mt is a keep lane's dispatch whose lane has a live agent (keepLive; under mu): its work job goes to that
// session, no agent.start.
func (a *App) kept(p *proj, mt mtask) bool {
	if !mt.disp.Keep {
		return false
	}
	_, ok := a.keepLive(p, mt)
	return ok
}

// keepWaits: mt's keep lane dispatch, its work not sent yet, waits for the handoff rule (under mu): the lane's live
// agent is past handoff_tokens, so its job goes to a fresh session once the rule stopped this one (never typed into a
// session past the threshold, T-1031).
func (a *App) keepWaits(p *proj, mt mtask) bool {
	pane, ok := a.keepLive(p, mt)
	return ok && a.pastHandoff(p, pane)
}

// pastHandoff: the session of the agent in pane is larger than the project's handoff_tokens (under mu; 0: off). Its
// transcript is the recorded session's, else the one herdr names; none readable is not past.
func (a *App) pastHandoff(p *proj, pane port.Pane) bool {
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return false
	}
	limit := cfg.HandoffTokens()
	if limit <= 0 {
		return false
	}
	tokens := a.opt.Tokens
	if tokens == nil {
		tokens = transcriptTokens
	}
	n, ok := tokens(cmp.Or(a.transcript(p, pane), pane.Transcript))
	return ok && int64(n) > limit
}

// transcriptTokens is a transcript's context size (project.ContextTokens; ok false: none read).
func transcriptTokens(path string) (int, bool) {
	n, _, _, ok := project.ContextTokens(path)
	return n, ok
}

// keepBetween is keep lane lane's state between tasks (under mu): the key of its last ended keep task (the merged or
// dropped fact's seq) and ok when no task holds its agent now: each of the lane's tasks is ended (merged, dropped,
// rejected), waiting, or dispatched with no work sent and no start (a dispatch keepWaits holds). ok false: a task is
// mid-way on the lane (its work sent, submitted, in the gate or review, sent back), or none ran there in keep mode.
func (p *proj) keepBetween(lane string) (int64, bool) {
	var key int64
	for _, t := range p.loopTasks() {
		mt := p.folded.loop[t]
		if mt.lane != lane {
			continue
		}
		switch {
		case mt.state == stMerged || mt.state == stDropped:
			if mt.disp.Keep {
				key = max(key, mt.mergedSeq, mt.droppedSeq)
			}
		case states[mt.state].ended, mt.state == stWaiting:
		case mt.state == stDispatched && mt.work == "" && !mt.started && !mt.paused():
		default:
			return 0, false
		}
	}
	return key, key != 0
}

// handoffRule is the keep lane handoff rule (T-1031, rule tier). Condition: a keep lane of the config between tasks
// (keepBetween), its agent live in the lane (identified in its worktree), idle, its session past handoff_tokens
// (pastHandoff), no claim on the lane's tasks and the project's pause not in force. Action: agent.stop of that agent
// (stopAgent: a gone one is done); the lane's next dispatch starts a fresh one (nextDispatch, as a fresh lane does).
// Keyed by the lane's last ended keep task; done when the lane runs no agent past the threshold.
func handoffRule(_ context.Context, a *App, p *proj, fx *Loop, now time.Time) (int64, action, bool) {
	if fx == nil || fx.Agents == nil {
		return 0, action{}, false
	}
	cfg, err := config.LoadDaemon(p.root)
	if err != nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !p.dispatching || p.paused(now) != nil {
		return 0, action{}, false
	}
	for _, l := range cfg.Lanes() {
		if l.Mode != config.LaneKeep {
			continue
		}
		key, between := p.keepBetween(l.Name)
		agent := cfg.AgentName("worker", l.Name)
		if !between || agent == "" || p.laneBusy(l.Name) {
			continue
		}
		w, _ := a.agentOf(p, port.Lane{Dir: l.Dir, Agent: agent})
		if w.State != Identified || !Ready(w.Pane.Status) || !a.pastHandoff(p, w.Pane) {
			continue
		}
		return key, action{call: func(ctx context.Context) error {
			line, _, err := a.stopAgent(ctx, p, fx, agent, false, true)
			if err != nil {
				return fmt.Errorf("keep lane %s: %s not stopped for its handoff: %w", l.Name, agent, err)
			}
			a.logf("%s: keep lane %s's agent past handoff_tokens, between tasks: %s (its next task starts it fresh)",
				p.name, l.Name, line)
			return nil
		}}, true
	}
	return 0, action{}, false
}

// laneBusy: a step or claim of a task on lane is in flight (under mu, p.busy's).
func (p *proj) laneBusy(lane string) bool {
	for _, t := range p.loopTasks() {
		if p.folded.loop[t].lane == lane && p.busy(t) != "" {
			return true
		}
	}
	return false
}
