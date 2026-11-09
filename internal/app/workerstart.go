// workerstart.go: serve starts a lane's worker again while its task's work is outstanding and the worker runs no more
// (T-874; a rule on the rule pass, rules.go): obor stop --force stopped it and obor start started only serve, or it
// exited by itself.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/config"
	"slices"
	"strings"
	"time"
)

// ruleWorkerStart is the worker start rule's name: its key in the rule pass.
const ruleWorkerStart = "worker.start"

// workerStartSettle is how long a worker's pane stays at its shell before the rule starts it: a restart by hand (obor
// restart's stop leaves the shell for a moment) is no stopped worker. Under Options.Gone's default minute, so a worker
// that exited is back before the watchdog's agent-exited needs-you.
const workerStartSettle = 30 * time.Second

// workerStartRule is the worker start rule. Condition: a task (the first by id) working, or dispatched with its lane
// prepared and its worker started (a dispatch's own start is nextStart's), no block on it, with a worker job
// outstanding (its work or fix job, or the fix job a sendback is still to send), whose worker runs no agent (its only
// labelled pane is at its shell: labelledShell), no stop step or other claim pending for it (busy), and the project's
// pause not in force (any reason: T-908; a model's pause does not hold it). Action: the worker started in that pane as
// a restart without --fresh starts it — its recorded session resumed where its kind resumes, else a new one — with
// that job's wake (launch carries the Prompt: typed into a resumed session, a new one's first prompt). Keyed by the
// task's dispatch fact; done when the pane runs an agent.
func workerStartRule(_ context.Context, a *App, p *proj, fx *Loop, now time.Time) (int64, action, bool) {
	if fx == nil || fx.Agents == nil {
		return 0, action{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !p.dispatching || p.paused(now) != nil {
		return 0, action{}, false
	}
	for _, task := range p.loopTasks() {
		mt := *p.folded.loop[task]
		job, open := mt.workerJob()
		if !open && mt.is(stWorking) && mt.work == "" { // a sendback's fix job, not sent to a worker at its shell
			job, open = event.Key{Task: task, Attempt: mt.attempt, Step: nth(event.StepFix, mt.fixes+1)}, true
		}
		if !open || mt.agent == "" || !(mt.is(stDispatched) && mt.started || mt.is(stWorking)) || p.busy(task) != "" {
			continue
		}
		pane, ok := a.labelledShell(p, mt.agent)
		if !ok {
			continue
		}
		typ, _ := config.ParseModelRef(mt.disp.Type) // the dispatch's type, as its start's (T-722)
		req := port.Launch{Agent: mt.agent, Pane: pane.ID, Prompt: wakeLine(JobID(job)), Effort: mt.disp.Effort, Type: typ}
		return mt.dispSeq, action{call: func(ctx context.Context) error {
			s, err := fx.Agents.Launch(ctx, req)
			if err != nil {
				return fmt.Errorf("%s's worker %s not started in %s: %w", task, req.Agent, req.Pane, err)
			}
			a.logf("%s: %s's worker started again for %s: %s", p.name, task, JobID(job), s.Line)
			return nil
		}}, true
	}
	return 0, action{}, false
}

// nextDispatch is a dispatched task's next step (under mu): a move's stop of the old worker (pickStop), prepare the
// lane, start the worker, send the work job, then watch it until it is delivered. A keep lane with a live agent
// (T-1031, keeplane.go) starts none: the work job's wake goes to that session, after the handoff rule stopped one past
// handoff_tokens. Nothing without the dispatch adapters.
func (a *App) nextDispatch(p *proj, mt mtask) step {
	if !p.dispatching {
		return nil
	}
	if s := a.pickStop(p, mt); s != nil || mt.halt.pending() { // a move's: the old worker stopped first (T-852)
		return s
	}
	switch {
	case mt.refused != "":
		return func(context.Context, *Loop) bool { return a.needsYou(p, &mt, mt.refusedCause(), mt.refused) }
	case mt.base == "":
		return func(ctx context.Context, fx *Loop) bool { return a.prepare(ctx, p, fx, mt) }
	case !mt.started && mt.disp.Keep && mt.work == "" && a.keepWaits(p, mt):
		return nil // the kept agent is past handoff_tokens: the handoff rule stops it, then it starts fresh (T-1031)
	case !mt.started && !a.kept(p, mt):
		return a.nextStart(p, mt)
	case mt.work == "": // started, or a keep lane's live agent: the wake typed into its session, no start (T-1031)
		return func(ctx context.Context, fx *Loop) bool {
			return a.sendWork(ctx, p, fx, mt, nth(event.StepWork, mt.works+1))
		}
	case p.log.Open(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}): // its prompt failed: sent again under its key
		return func(ctx context.Context, fx *Loop) bool { return a.sendWork(ctx, p, fx, mt, mt.work) }
	}
	return a.watchWork(p, mt)
}

// prepare runs lane.prepare (row 3): the lane on the task branch at the fact's target (a redo keeps the branch),
// lane_ready / lane_setup; its result is the lane's row (lanes.go). A refusal is final: needs-you{lane: why}.
func (a *App) prepare(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepPrepare, mt.prepares+1)}, Class: event.ClassGit,
		Data: port.PrepareInput{Task: mt.task, Dispatch: mt.disp.Dispatch, At: mt.at}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		out, err := fx.Workflow.Prepare(ctx, in.Data.(port.PrepareInput))
		var refused port.Refused
		if errors.As(err, &refused) {
			return prepareOut{Refused: refused.Line}, nil
		}
		if err != nil {
			return nil, err
		}
		return prepareOut{Base: out.Base}, nil
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// nextStart is agent.start's step (row 4, under mu): the snapshot's pane in the lane (an agent's, else one at its
// shell) and the agent in it are its intent's data. A pending start keeps its recorded pane while herdr still has it; a
// pane it names that is gone (its tab closed, a new labelled one made by obor setup pane) is chosen again, as a first
// start's is, and never the recorded id (T-783; one that runs an agent is cannot tell, startWorker). No pane in the
// lane for Gone: herdr's panes are read again (a cd or a rename in one gives no event, T-745, RV-101 #2) and the agent
// starts if that finds one; still none: needs-you{no pane in lane}. A failed read is no answer: nothing is recorded and
// the next pass reads again.
func (a *App) nextStart(p *proj, mt mtask) step {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepStartWork, mt.starts+1)}
	d := startData{Lane: mt.lane, Agent: mt.agent}
	pending := false
	if r, ok := p.log.Intent(key); ok { // a pending start: its recorded snapshot
		_ = json.Unmarshal(r.Data, &d)
		pending = true
	}
	if !pending || a.obs[d.Pane] == nil {
		o := a.placement(p, mt.disp.Dir, mt.agent)
		if o == nil {
			return a.waitPane(p, mt)
		}
		d.Pane, d.Ran, d.Session = o.pane.ID, o.pane.Agent != "", o.pane.Session
	}
	delete(p.nopane, mt.task)
	// The work job the start runs under: one open from an earlier start, else the next.
	work := event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepWork, mt.works+1)}
	if mt.work != "" && !p.log.Done(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}) {
		work.Step = mt.work
	}
	return func(ctx context.Context, fx *Loop) bool { return a.startWorker(ctx, p, fx, mt, key, d, work) }
}

// waitPane is nextStart's wait for a pane of the lane (under mu): nothing until opt.Gone has passed since the first pass
// without one, then herdr's panes are read again before the owner is told; the agent starts in a pane the read finds
// (the next pass: the wait ends only once the snapshot, with the read's cwd and label, places it there), and none is
// needs-you{no pane in lane}.
func (a *App) waitPane(p *proj, mt mtask) step {
	now := a.opt.Now()
	since, ok := p.nopane[mt.task]
	if !ok {
		p.nopane[mt.task] = now
		return nil
	}
	if now.Sub(since) < a.opt.Gone {
		return nil
	}
	return func(ctx context.Context, _ *Loop) bool {
		// The snapshot's cwd can be stale (a cd in the pane gives no event, T-745): read herdr's panes again before
		// the owner is told there is none. A failed or canceled read records nothing, so the task keeps waiting on
		// the same clock and the next pass tries again (RV-89 #1).
		panes, err := a.rereadPanes(ctx)
		if err != nil {
			return false
		}
		// The list answers which pane is in the lane now, and nothing else: a pane the stream closed since the read
		// is not raised again, and every other field and pane stays the stream's (RV-89 #2). One the stream still
		// has: its cwd is what changed, and its label where the snapshot's holds it back (labelled for another
		// agent: a rename gives no event either, RV-101 #2); the next pass starts the agent in it.
		a.mu.Lock()
		o := a.paneIn(p, panes, mt.disp.Dir, mt.agent)
		usable := false
		if o != nil && a.obs[o.ID] != nil { // not closed by the stream since the read: that wins
			s := &a.obs[o.ID].pane
			s.Cwd = o.Cwd
			if otherAgent(p.root, s.Label, []string{mt.agent}) {
				s.Label = o.Label
			}
			usable = a.placement(p, mt.disp.Dir, mt.agent) != nil
		}
		if o == nil || usable { // the read settled the wait: a pane the next pass starts in, or none to wait for
			delete(p.nopane, mt.task)
		}
		a.mu.Unlock()
		if o == nil {
			return a.needsYou(p, &mt, causeNoPane, "no pane in lane "+mt.lane+" ("+mt.disp.Dir+"): open one there")
		}
		return usable
	}
}

// placement is paneIn over the snapshot (under mu): the pane of p's group to start the lane at dir's agent in; nil:
// none. A place to launch into (Launch stops what runs there), never an identity: who runs in a pane is whoIs's.
func (a *App) placement(p *proj, dir string, names ...string) *seen {
	panes := make([]port.Pane, 0, len(a.obs))
	for _, o := range a.obs {
		panes = append(panes, o.pane)
	}
	if pane := a.paneIn(p, panes, dir, names...); pane != nil {
		return a.obs[pane.ID]
	}
	return nil
}

// paneIn is the pane of panes (the snapshot's, or a fresh read's) to start the lane at dir's agent in, ranked: the one
// the table identifies as one of names (whoIs, DD-v0.13 A5: the lane's agent, not a second session someone opened
// there) > the one labelled one of names (the label is the pane's identity, R-28 §2: `obor setup pane` labels a pane
// before any agent ran there, T-783) > the one running an agent > the one at its shell; ties by lowest id. A pane
// labelled for another agent of the project is never chosen (its label is that agent's, and the launch would refuse
// it); a label the config does not know (an old alias, T-777) names nobody and does not hold the pane back. nil: none
// (under mu). A place to launch into (Launch stops what runs there), never an identity: who runs in a pane is
// whoIs's.
func (a *App) paneIn(p *proj, panes []port.Pane, dir string, names ...string) *port.Pane {
	var best *port.Pane
	bestRank := 0
	for i := range panes {
		if !inDir(panes[i].Cwd, dir) || a.projectOf(panes[i]) != p || otherAgent(p.root, panes[i].Label, names) {
			continue
		}
		r := 1
		agent, identified := p.reg.WhoIs(panes[i])
		switch {
		case identified && agent != "" && slices.Contains(names, agent):
			r = 4
		case slices.Contains(names, panes[i].Label):
			r = 3
		case panes[i].Agent != "":
			r = 2
		}
		if best == nil || r > bestRank || r == bestRank && panes[i].ID < best.ID {
			best, bestRank = &panes[i], r
		}
	}
	return best
}

// otherAgent is whether label names an agent of root's project other than names (under mu): a lane's, a role's or an
// agents entry's name, as launch resolves who a label is (config.AgentLookup). A label the config does not know — an
// alias T-777 dropped — names nobody.
func otherAgent(root, label string, names []string) bool {
	if label == "" || slices.Contains(names, label) {
		return false
	}
	cfg, err := config.Load(root)
	if err != nil {
		return false
	}
	_, ok := cfg.AgentLookup(label, "")
	return ok
}

// startWorker runs agent.start (S): a fresh session of the lane's agent in its pane (Agents.Launch, force as the
// dispatch said). A replay (the intent pending) first reads the snapshot (startSeen): started → the result, no second
// start; not started → it starts again (Restart stops what is there); cannot tell → refused: needs-you{start: cannot
// tell …}, never a second start. A replay whose intent names another pane than d has its recorded pane gone, and its
// start chose one again (nextStart): nothing of the intent's pane is left to compare with, so it starts as a first
// start does there (T-783) when that pane is at its shell; one that runs an agent is cannot tell (moved): the intent
// stays the gone pane's (its key is pending), so a start there that ran and left no result cannot be told from any
// other agent, and a second start would stop a worker already at its job (RV-101 #1). A launch refusal (background
// tasks without force, the name held by another pane) is final: needs-you{start: why}. It runs under work job's intent
// and carries its wake (startJob).
func (a *App) startWorker(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key, d startData, work event.Key) bool {
	pending := p.log.Open(key)
	var rec startData
	if pending {
		if r, ok := p.log.Intent(key); ok {
			_ = json.Unmarshal(r.Data, &rec)
		}
	}
	replay := pending && rec.Pane == d.Pane
	moved := pending && rec.Pane != d.Pane // the recorded pane is gone: d.Pane was chosen again
	in := event.Intent{Key: key, Class: event.ClassStart, Data: d}
	return a.startJob(ctx, p, mt, in, work, promptData{Agent: mt.agent}, func(ctx context.Context, first string) (any, error) {
		if moved {
			if runs := a.runsIn(d.Pane); runs != "" {
				return startOut{Refused: "cannot tell whether the agent in " + d.Pane + " is the one started: the start " +
					"was recorded in " + rec.Pane + ", gone now, and " + d.Pane + " runs " + runs + ": look at the pane, " +
					"then obor resume " + mt.task}, nil
			}
		}
		if replay {
			switch a.startSeen(p, d) {
			case seenStarted: // the start that ran carried the same first prompt
				return startOut{Line: "started (seen in the snapshot)", Agent: d.Agent, Pane: d.Pane}, nil
			case seenUnknown:
				return startOut{Refused: "cannot tell whether the agent in " + d.Pane + " is the one started (herdr " +
					"reports no session id): look at the pane, then obor resume " + mt.task}, nil
			}
		}
		typ, _ := config.ParseModelRef(mt.disp.Type) // the dispatch's type, not the lane's configured one (T-722)
		s, err := fx.Agents.Launch(ctx, port.Launch{Agent: d.Agent, Pane: d.Pane, Fresh: true, Force: mt.disp.Force,
			Prompt: first, Effort: mt.disp.Effort, Type: typ})
		switch {
		case err != nil && startRefused(err):
			return startOut{Refused: err.Error()}, nil
		case err != nil:
			return nil, err
		}
		return startOut{Line: s.Line, Agent: d.Agent, Pane: d.Pane, Terminal: s.Terminal, Kind: s.Kind, Session: s.Session,
			Config: p.configRev()}, nil
	})
}

// What the snapshot says of a pending agent.start (startSeen).
const (
	seenNotStarted = iota
	seenStarted
	seenUnknown
)

// startSeen is the case table of a pending agent.start (docs/state-model.md row 4): pane d.Pane (of p) runs no
// agent now → not started; it runs one and none ran at the intent → started; one ran then too: its session id
// differs from the recorded one → started, the same → not started, unknown on either side → cannot tell. A session
// is herdr's session id (Pane.Session), never whether its transcript file is found.
func (a *App) startSeen(p *proj, d startData) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.obs[d.Pane]
	switch {
	case o == nil || o.pane.Agent == "" || a.projectOf(o.pane) != p:
		return seenNotStarted
	case !d.Ran:
		return seenStarted
	case d.Session == "" || o.pane.Session == "":
		return seenUnknown
	case o.pane.Session != d.Session:
		return seenStarted
	}
	return seenNotStarted
}

// runsIn is what runs in pane now, as the snapshot has it: "" for a pane at its shell (or gone), else its agent kind,
// its label and herdr's session id where known ("claude (label pj-l-01, session s1)").
func (a *App) runsIn(pane string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.obs[pane]
	if o == nil || o.pane.Agent == "" {
		return ""
	}
	var of []string
	if o.pane.Label != "" {
		of = append(of, "label "+o.pane.Label)
	}
	if o.pane.Session != "" {
		of = append(of, "session "+o.pane.Session)
	}
	if len(of) == 0 {
		return o.pane.Agent
	}
	return o.pane.Agent + " (" + strings.Join(of, ", ") + ")"
}

// startRefused: the launch refused for good: the agent has background tasks (no force), its name is held by another
// pane, or the start check refused it (a quarantined Codex install, T-1052: a needs-you, not a counted failure).
func startRefused(err error) bool {
	var refused port.Refused
	return errors.As(err, &refused) || port.ErrorCode(err) == "agent_name_taken" ||
		strings.Contains(err.Error(), "has background tasks")
}
