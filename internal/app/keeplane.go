// keeplane.go: a keep lane's agent between its tasks (config.LaneKeep). A keep lane's merged task stops no
// agent (afterList); its next dispatch prepares the lane (clean: lane.prepare requires it) and sends the work job's wake
// to the live agent (nextDispatch: no agent.start) when its recorded start runs the dispatch's model under the same
// config revision (startFits; else the dispatch stops it first, keepStop), or starts one as a fresh
// lane does when the lane has none. The
// handoff rule stops a kept agent that is idle between tasks with its session past handoff_tokens; the next dispatch's
// agent.start is its fresh start (one starter per lane: a rule's start can never race a dispatch's).
package app

import (
	"cmp"
	"context"
	"fmt"
	"obor/internal/agents"
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

// keptPane is the pane of mt's keep lane's live agent when its work job goes to that session, no agent.start (under
// mu): mt is a keep lane's dispatch, the lane's agent is live (keepLive) and its recorded start runs mt's dispatch's
// model (startFits). ok false: the dispatch starts the agent (a mismatched one is stopped first: keepStop).
func (a *App) keptPane(p *proj, mt mtask, cfg *config.Config) (port.Pane, bool) {
	if !mt.disp.Keep {
		return port.Pane{}, false
	}
	pane, ok := a.keepLive(p, mt)
	if !ok {
		return port.Pane{}, false
	}
	if !p.startFits(cfg, mt.agent, pane, mt.disp.Type) {
		return port.Pane{}, false
	}
	return pane, true
}

// kept: mt's work job goes to its keep lane's live agent (keptPane; under mu), no agent.start.
func (a *App) kept(p *proj, mt mtask, cfg *config.Config) bool {
	_, ok := a.keptPane(p, mt, cfg)
	return ok
}

// keepWaits: mt's keep lane dispatch, its work not sent yet, waits for the handoff rule (under mu): the lane's kept
// agent (keptPane) is past handoff_tokens, so its job goes to a fresh session once the rule stopped this one (never
// typed into a session past the threshold).
func (a *App) keepWaits(p *proj, mt mtask, cfg *config.Config) bool {
	pane, ok := a.keptPane(p, mt, cfg)
	return ok && a.pastHandoff(p, cfg, pane)
}

// keepStop is the agent a dispatch of keep lane l running model ref ref stops before its start (dispatchData.Stop):
// l's agent, live in its worktree, when its recorded start does not run ref (startFits). "": l is no keep
// lane, its agent is not live (the dispatch starts one), or it runs ref (its work job goes to it).
func (a *App) keepStop(p *proj, cfg *config.Config, l port.Lane, agent, ref string) string {
	if !cfg.KeepLane(l.Name) {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	w, _ := a.agentOf(p, port.Lane{Dir: l.Dir, Agent: agent})
	if w.State != Identified || p.startFits(cfg, agent, w.Pane, ref) {
		return ""
	}
	return agent
}

// startFits: agent, live in pane, runs model ref ref as serve's own start of it recorded (under mu): its start
// row (agent.start's result, foldStart) names the model ref the start ran (its dispatch's) with ref's model and effort,
// the kind of ref's model, and a session that is pane's now (liveSession: the agent the start launched, not one
// started since by hand), and the config revision the start was launched under is the config's now (a model's id or
// a kind's args edited since would make a new start of ref another launch). Unknown or unprovable is no fit: no start
// row (an agent serve did not start), no ref (the integrator's), no config revision (an older start), a start
// that recorded no session id (it cannot prove the live agent is the start's), or no session known for the pane
// now (none recorded from it, and herdr reporting none).
func (p *proj) startFits(cfg *config.Config, agent string, pane port.Pane, ref string) bool {
	row, ok := p.reg.Start(agent)
	if !ok || row.Ref == "" || row.Config == "" || !sameSession(p.liveSession(agent, pane), row.Session) ||
		row.Config != cfg.Rev() {
		return false
	}
	typ, effort := config.ParseModelRef(ref)
	ran, ranEffort := config.ParseModelRef(row.Ref)
	lt, ok := cfg.Model(typ)
	return ok && typ != "" && typ == ran && effort == ranEffort && lt.Kind != "" && lt.Kind == row.Kind
}

// pastHandoff: the session of the agent in pane is larger than the project's handoff_tokens (under mu; 0: off). Its
// transcript and kind are the recorded session's, else the transcript herdr names for the pane's agent kind; none
// readable is not past. A transcript's size is that one kind's read (project.ContextOf, bounded for the lock).
func (a *App) pastHandoff(p *proj, cfg *config.Config, pane port.Pane) bool {
	limit := cfg.HandoffTokens()
	if limit <= 0 {
		return false
	}
	path, kind := a.sessionFile(p, pane)
	var n int
	var ok bool
	if tokens := a.opt.Tokens; tokens != nil {
		n, ok = tokens(path)
	} else {
		n, ok = transcriptTokens(a.p.kinds(), kind, path)
	}
	return ok && int64(n) > limit
}

// sessionPane is pane overlaid with its agent's recorded current session and the kind that reads it (under mu): when
// the pane's agent has a recorded session with a kind, its recorded transcript (when any) takes pane.Transcript and
// the session's kind is returned; else pane and pane.Agent.
func (p *proj) sessionPane(pane port.Pane) (port.Pane, string) {
	if agent, ok := p.reg.WhoIs(pane); ok {
		if s, ok := p.session(agent); ok && s.Kind != "" {
			pane.Transcript = cmp.Or(s.Transcript, pane.Transcript)
			return pane, s.Kind
		}
	}
	return pane, pane.Agent
}

// sessionFile is the transcript of pane's agent and the kind that reads it (under mu): its recorded current session's
// (its transcript, else the one herdr names, read as the session's kind), else herdr's for the pane's agent kind.
func (a *App) sessionFile(p *proj, pane port.Pane) (path, kind string) {
	pane, kind = p.sessionPane(pane)
	return a.activity.Transcript(p.kind(kind), pane), kind
}

// transcriptTokens is a transcript's context size as kind reads it (project.ContextOf; ok false: none read).
func transcriptTokens(kinds agents.Kinds, kind, path string) (int, bool) {
	c, ok := project.ContextOf(kinds, kind, path)
	return c.Tokens, ok
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

// handoffRule is the keep lane handoff rule (rule tier). Condition: a keep lane of the config between tasks
// (keepBetween), its agent live in the lane (identified in its worktree), idle, its session past handoff_tokens
// (pastHandoff), no claim on the lane's tasks and the project's pause not in force. Action: agent.stop of that agent
// (stopAgent: a gone one is done); the lane's next dispatch starts a fresh one (nextDispatch, as a fresh lane does).
// Keyed by the lane's last ended keep task; done when the lane runs no agent past the threshold.
func handoffRule(_ context.Context, a *App, p *proj, fx *Loop, now time.Time) (int64, action, bool) {
	if fx == nil || fx.Agents == nil {
		return 0, action{}, false
	}
	cfg := fx.Cfg
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
		if w.State != Identified || !Ready(w.Pane.Status) || !a.pastHandoff(p, cfg, w.Pane) {
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
