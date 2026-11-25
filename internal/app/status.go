package app

import (
	"cmp"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"slices"
	"sort"
	"strings"
	"time"
)

// Status is the app's state now.
func (a *App) Status() wire.Status {
	now := a.opt.Now()
	cfg := a.servedCfg()          // the status's one read of the config
	done := a.doneRoles(cfg, now) // before the lock: it reads the panes' transcripts
	a.mu.Lock()
	defer a.mu.Unlock()
	st := wire.Status{Since: a.since.UTC().Format(time.RFC3339), Connected: a.connected, TerminalNote: a.terminalNote()}
	if p := a.p; p != nil {
		ps := wire.ProjectStatus{Name: p.name, Root: p.root, Merging: p.merging, Agents: []wire.AgentStatus{}, Tasks: []wire.TaskStatus{},
			Facts: []event.Record{}}
		if err := p.log.Err(); err != nil {
			ps.LogError = err.Error()
		} else if p.folded.lr.err != nil {
			ps.LogError = "lane rows: " + p.folded.lr.err.Error()
		}
		ps.Lanes = a.lanesNote(p, now)
		names := map[string]bool{} // the agents serve observes in a pane now: skewNote's seen
		for _, l := range p.lanes {
			if p.laneHolds(l.Name) == "" {
				ps.Free = append(ps.Free, l.Name)
			}
		}
		for _, o := range a.obs {
			if o.pane.Agent == "" || a.projectOf(o.pane) != p {
				continue
			}
			as := wire.AgentStatus{Machine: Machine, Group: o.pane.Group, Pane: o.pane.ID, Name: a.shownAs(p, o.pane),
				Agent: o.pane.Agent, Status: o.pane.Status, Note: o.pane.Note, Age: int64(now.Sub(o.since) / time.Second)}
			if _, ok := done[as.Name]; ok {
				as.Hint = roleStopHint(as.Name)
			}
			names[as.Name] = true
			if l, ok := p.laneOf(o.pane.Cwd); ok {
				as.Lane, as.Task = l.Name, l.Task
			}
			if agent, ok := p.reg.WhoIs(o.pane); ok {
				if s, ok := p.session(agent); ok {
					as.SetSession(s)
				}
			}
			ps.Agents = append(ps.Agents, as)
		}
		sort.Slice(ps.Agents, func(i, j int) bool { return ps.Agents[i].Pane < ps.Agents[j].Pane })
		for _, fs := range p.folded.active { // the needs-you among them: p.needsYou's
			for _, f := range fs {
				if f.Step != event.StepNeedsYou {
					ps.Facts = append(ps.Facts, f)
				}
			}
		}
		ps.Facts = append(ps.Facts, p.needsYou()...)
		intents := p.log.OpenIntents()
		steps := p.stepPhases(intents)
		// Each task's place in the pass's order (passOrder), shipped so the view's queue lists the waiting
		// dispatches in the order serve starts them instead of sorting them by id.
		rank := make(map[string]int, len(p.folded.loop))
		for i, task := range p.passOrder() {
			rank[task] = i + 1
		}
		for _, mt := range p.folded.loop {
			if mt.state != stMerged {
				waiting := mt.is(stWaiting) // the predicate the view's queue lists, never State
				ts := wire.TaskStatus{Task: mt.task, Lane: mt.lane, Attempt: mt.attempt, State: mt.shown(),
					Ended: states[mt.state].ended, Phase: a.phaseOf(p, mt, steps), Stage: a.stageOf(p, mt, steps),
					Effort: mt.disp.Effort, Waiting: waiting}
				row, hasSpec, err := specOf(p, mt.task)
				if err == nil && hasSpec { // the spec's external id
					ts.Ref = row.Spec.Ref
				}
				if p.running.Task == mt.task {
					ts.Running = p.running.Step
				} else if k, ok := p.tests[mt.task]; ok {
					ts.Running = k.Step
				}
				if at := p.folded.starts[mt.task]; !at.IsZero() {
					ts.Started = at.Unix()
				}
				if mt.halt.pending() {
					ts.Stopping = mt.halt.agent
				}
				if waiting {
					ts.Rank = rank[mt.task]
					ts.After, ts.Why = mt.disp.After, p.waitLine[mt.task].line
					ts.Level, ts.Types = mt.disp.Level, mt.disp.Types
					ts.Difficulty, ts.Model = p.waitDifficulty(mt), p.firstUnpaused(mt.disp.Types, now)
					ts.High = err == nil && hasSpec && highPriority(row.Spec)
				}
				key, open := mt.workerJob()
				if !open {
					key, open = mt.integratorJob()
				}
				if open {
					ts.Job = JobID(key)
					ts.Delivered = p.folded.delivered[ts.Job].seq != 0
				}
				ps.Tasks = append(ps.Tasks, ts)
			}
		}
		if held := p.paused(now); held != nil {
			c := *held
			ps.Paused = &c
		}
		for m := range p.folded.models {
			if held := p.modelPaused(m, now); held != nil {
				if ps.ModelPauses == nil {
					ps.ModelPauses = map[string]*event.Pause{}
				}
				c := *held
				ps.ModelPauses[m] = &c
			}
		}
		ps.Messages = p.out.MessageStatus(now)
		if s := p.reviewStatus(now); s != "" && p.folded.review != nil {
			ps.Review = "review request " + s
		} else {
			ps.Review = s
		}
		if q := p.folded.review; q != nil {
			ps.ReviewRequest = &wire.ReviewRequestStatus{After: q.after, Waiting: p.reviewWaits(q.after), Held: q.held,
				Note: q.note, By: q.by, Since: q.at.Unix()}
		}
		ps.ReviewOpen = a.reviewOpen(p, cfg)
		ps.Briefs = a.briefStatus(p, now)
		ps.Stopped = a.stoppedRoles(p, cfg)
		for _, f := range ps.Facts {
			if f.Step != event.StepNeedsYou {
				continue
			}
			if ans := p.answerPath(f); ans != "" {
				if ps.Answers == nil {
					ps.Answers = map[int64]string{}
				}
				ps.Answers[f.Seq] = ans
			}
		}
		ps.Running = p.runningSteps(intents)
		if n := len(ps.Running); n > 0 && ps.Running[n-1].Step == event.StepServeRestart {
			// what the pending restart waits for, as its pass decides it (follow.go); past followWait, the work it cuts
			holds, cut := a.restartHolds(p, now)
			ps.Running[n-1].Waits, ps.Running[n-1].Cuts = strings.Join(holds, ", "), strings.Join(cut, ", ")
		}
		ps.Pending = p.pendingSteps(intents)
		ps.Scratch, ps.ScratchQueue = p.scratchQueue()
		if ps.ScratchQueue == nil {
			ps.ScratchQueue = []wire.ScratchWait{}
		}
		for _, set := range p.openWatches() {
			w := p.folded.watches[set]
			ps.Watches = append(ps.Watches, wire.WatchStatus{Task: set, By: w.by, Note: w.note, Since: w.at.Unix()})
		}
		if n := p.idleNow(cfg); n.ok && n.end != 0 { // the first end keys it and ages it; before that there is nothing to show
			ps.Idle = &wire.IdleStatus{Since: n.at.Unix(), Last: n.last}
			if ack := p.folded.idleAck; ack.Key == n.end {
				ps.Idle.Acked, ps.Idle.Why = true, ack.Why
			}
		}
		ps.Queues = p.laneQueues()
		sort.Slice(ps.Tasks, func(i, j int) bool { return ps.Tasks[i].Task < ps.Tasks[j].Task })
		sort.Slice(ps.Facts, func(i, j int) bool { return ps.Facts[i].Seq < ps.Facts[j].Seq })
		st.Project = &ps
		st.Skew = a.skewNote(names)
	}
	return st
}

// commandStep is a record's step as the running and pending lines show it: the merge loop's command
// steps — gate, a lane test, publish the merge, after_merge, the nightly check — by their base name, the nightly one with no task.
// false: another step's record (a prompt, a start) runs no command the lines show.
func commandStep(r event.Record) (step, task string, ok bool) {
	if strings.HasPrefix(r.Step, event.StepNightly+".") {
		return event.StepNightly, "", true
	}
	switch step, _ = event.StepN(r.Step); step {
	case event.StepGate, event.StepTest, event.StepPublish, event.StepAfterMerge:
		return step, r.Task, true
	}
	return "", "", false
}

// runningSteps is the steps that run without an agent, in seq order: the open intents of the merge loop's command
// steps whose effect runs now (p.exec) and, last, a pending self-restart (p.restarting). Since is the start of
// that execution, not the intent's write time (a replay's is older). An intent with a result is no open intent; an
// open one not running is pending. Under mu.
func (p *proj) runningSteps(open []event.Record) []wire.RunningStep {
	var out []wire.RunningStep
	for _, r := range open {
		step, task, ok := commandStep(r)
		if !ok {
			continue
		}
		at, running := p.exec[r.Key()]
		if !running {
			continue
		}
		out = append(out, wire.RunningStep{Step: step, Task: task, Command: p.stepCommand(r.Key()), Since: at.Unix()})
	}
	if !p.restarting.IsZero() {
		out = append(out, wire.RunningStep{Step: event.StepServeRestart, Since: p.restarting.Unix()})
	}
	return out
}

// pendingSteps is the steps that wait, in seq order: the merge loop's command steps whose intent has no result
// and whose effect does not run now — one whose last try failed (Error, FailedAt: the failed fact's reason without its
// step prefix, and its time) or one the next pass or a replay has yet to run (no error: queued). Under mu.
func (p *proj) pendingSteps(open []event.Record) []wire.PendingStep {
	var out []wire.PendingStep
	for _, r := range open {
		step, task, ok := commandStep(r)
		if !ok {
			continue
		}
		if _, running := p.exec[r.Key()]; running {
			continue
		}
		ps := wire.PendingStep{Step: step, Task: task, Since: event.RecordTime(r).Unix()}
		switch {
		case r.Task == event.NightlyTask:
			if p.night.fails[r.Step] > 0 {
				ps.Error, ps.FailedAt = strings.TrimPrefix(p.night.failedWhy, r.Step+": "), p.night.failedAt.Unix()
			}
		case p.folded.loop[r.Task] != nil:
			if f, failed := p.folded.loop[r.Task].fails[r.Step]; failed {
				ps.Error, ps.FailedAt = strings.TrimPrefix(f.why, r.Step+": "), f.at.Unix()
			}
		}
		out = append(out, ps)
	}
	return out
}

// answerPath is the commands that answer the needs-you fact r (obor status, the lead's needs-you line), in the state
// mt of r's task (zero: the loop holds none), by the rules the verbs themselves check: the one obor resume form
// resumeFits takes (its text where its action needs one; with no default, the owner's choice of --regate or
// --sendback; a worker's question also --regate), then obor drop when the task's state takes one (mtask.may); a
// watchdog fact its pane first; a loop error none ("": fix what it names). A nightly full-check RED is the one
// project fact with a command: obor resume nightly. p (nil: none) is r's project, under mu.
func answerPath(p *proj, r event.Record, mt mtask) string {
	d := r.Fact()
	var cmds []string
	switch {
	case r.Task == event.ReviewTask: // a failed review start's: a new request ends it
		return "obor review start [--after T-a,T-b]"
	case r.Task == event.NightlyTask && strings.HasPrefix(d.Reason, redPrefix): // the lead's or the owner's why ends it
		return `obor resume nightly "<why>"`
	case projectKey(r.Task): // a line typed, a line's; the nightly's other needs-you
		return ""
	case strings.HasPrefix(d.Reason, reasonAsks):
		return resumeCmds(p, r.Task, mt)
	case !d.Merge:
		if !mt.needs.held() && (d.Reason == reasonStalled || d.Cause == causeStopped) && p != nil {
			cmds = append(cmds, resumeCmds(p, r.Task, mt)) // the worker gets the line
		}
	default:
		cmds = append(cmds, resumeCmds(p, r.Task, mt))
	}
	if mt.may(vDrop) {
		cmds = append(cmds, `obor drop `+r.Task+` "<why>"`)
	}
	cmds = slices.DeleteFunc(cmds, func(c string) bool { return c == "" })
	switch {
	case !d.Merge && len(cmds) == 0:
		return "its pane"
	case !d.Merge:
		return "its pane, or " + strings.Join(cmds, " | ")
	}
	return strings.Join(cmds, " | ")
}

// resumeCmds is the obor resume form mt's task takes now ("": none; under mu): resumeFits' default with its text
// placeholder, and each flag that chooses another action.
func resumeCmds(p *proj, task string, mt mtask) string {
	if p == nil && (mt.is(stWorking) || mt.is(stDispatched)) {
		return ""
	}
	def, choice, err := p.resumeFits(mt)
	if err != nil {
		return ""
	}
	resume := "obor resume " + task
	var cmds []string
	if def != "" {
		cmd := resume
		if p.resumeNeedsText(mt, def) {
			cmd += ` "<` + cmp.Or(map[string]string{actAnswer: "answer", actSendback: "why",
				wire.ResumeReprompt: "decision"}[def], "text") + `>"`
		}
		cmds = append(cmds, cmd)
	}
	for _, f := range choice {
		cmd := resume + " --" + f
		if f == wire.ResumeSendback {
			cmd += ` "<why>"`
		}
		cmds = append(cmds, cmd)
	}
	return strings.Join(cmds, " | ")
}

// answerPath is answerPath of p's needs-you r in the state p's merge loop holds of its task (under mu).
func (p *proj) answerPath(r event.Record) string {
	var mt mtask
	if m := p.folded.loop[r.Task]; m != nil {
		mt = *m
	}
	return answerPath(p, r, mt)
}

// terminalNote is the status note for the terminal serve is connected to: the adapter's sentence when it knows less
// than that terminal ("" otherwise). It follows each snapshot, so a terminal started or upgraded after serve shows at
// once. The caller holds a.mu.
func (a *App) terminalNote() string {
	if !a.connected {
		return ""
	}
	return a.termNote
}

// waitDifficulty is a waiting task's Difficulty level: its dispatch's level, else (a --model override, which names
// none) its stored spec's Difficulty line; "": neither. Under mu.
func (p *proj) waitDifficulty(mt *mtask) string {
	if mt.disp.Level != "" {
		return mt.disp.Level
	}
	row, ok, err := specOf(p, mt.task)
	if err != nil || !ok {
		return ""
	}
	return row.Spec.Level
}

// firstUnpaused is the model ref of types a dispatch runs on now: the first whose model obor pause --model does not
// hold (pickLane's choice; "": every one is held). Under mu.
func (p *proj) firstUnpaused(types []string, now time.Time) string {
	for _, t := range types {
		if typ, _ := config.ParseModelRef(t); p.modelPaused(typ, now) == nil {
			return t
		}
	}
	return ""
}
