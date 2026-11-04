package app

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/version"
	"slices"
	"sort"
	"strings"
	"time"
)

// Status is the app's state now.
func (a *App) Status() wire.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.opt.Now()
	st := wire.Status{Since: a.since.UTC().Format(time.RFC3339), Connected: a.connected, HerdrNote: a.herdrNote()}
	if p := a.p; p != nil {
		ps := wire.ProjectStatus{Name: p.name, Root: p.root, Merging: p.merging, Agents: []wire.AgentStatus{}, Tasks: []wire.TaskStatus{},
			Facts: []event.Record{}}
		if err := p.log.Err(); err != nil {
			ps.LogError = err.Error()
		} else if p.folded.lr.err != nil {
			ps.LogError = "lane rows: " + p.folded.lr.err.Error()
		}
		for _, o := range a.obs {
			if o.pane.Agent == "" || a.projectOf(o.pane) != p {
				continue
			}
			as := wire.AgentStatus{Machine: Machine, Workspace: a.spaces[o.pane.Workspace], Pane: o.pane.ID, Name: a.shownAs(p, o.pane),
				Agent: o.pane.Agent, Status: o.pane.Status, Age: int64(now.Sub(o.since) / time.Second)}
			if l, ok := p.laneOf(o.pane.Cwd); ok {
				as.Lane, as.Task = l.Name, l.Task
			}
			if agent, ok := p.reg.whoIs(o.pane); ok {
				if s, ok := p.session(agent); ok {
					as.Session, as.Transcript, as.Started = s.ID, s.Transcript, s.Started
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
		for _, mt := range p.folded.loop {
			if mt.state != stMerged {
				ts := wire.TaskStatus{Task: mt.task, Lane: mt.lane, Attempt: mt.attempt, State: mt.shown(),
					Phase: a.phaseOf(p, mt, steps), Stage: a.stageOf(p, mt, steps), Effort: mt.disp.Effort}
				if p.running.Task == mt.task {
					ts.Running = p.running.Step
				}
				if role := a.holdRole(p, mt); role != "" {
					ts.Hold = wire.MergeHold(role)
				}
				if mt.is(stWaiting) {
					ts.After, ts.Why = mt.disp.After, p.waitLine[mt.task].line
					ts.Level, ts.Types = mt.disp.Level, mt.disp.Types
				}
				key, open := mt.workerJob()
				if !open {
					key, open = mt.integratorJob()
				}
				if open {
					ts.Job = JobID(key)
					ts.Delivered = p.folded.delivered[ts.Job] != 0
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
		ps.Messages = p.messageStatus(now)
		if s := p.reviewStatus(now); s != "" && p.folded.review != nil {
			ps.Review = "review request " + s
		} else {
			ps.Review = s
		}
		if q := p.folded.review; q != nil {
			ps.ReviewRequest = &wire.ReviewRequestStatus{After: q.after, Waiting: p.reviewWaits(q.after), Held: q.held,
				Note: q.note, By: q.by, Since: q.at.Unix()}
		}
		ps.Briefs = p.briefStatus()
		ps.Stopped = a.stoppedRoles(p)
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
		ps.Pending = p.pendingSteps(intents)
		ps.Scratch, ps.ScratchQueue = p.scratchQueue()
		if ps.ScratchQueue == nil {
			ps.ScratchQueue = []wire.ScratchWait{}
		}
		sets := make([]string, 0, len(p.folded.watches))
		for set := range p.folded.watches {
			sets = append(sets, set)
		}
		sort.Slice(sets, func(i, j int) bool { return p.folded.watches[sets[i]].seq < p.folded.watches[sets[j]].seq })
		for _, set := range sets {
			w := p.folded.watches[set]
			ps.Watches = append(ps.Watches, wire.WatchStatus{Task: set, By: w.by, Note: w.note, Since: w.at.Unix()})
		}
		if st := p.idleEpoch(p.idleNow()); st != nil {
			ps.Idle = &wire.IdleStatus{Since: st.at.Unix(), Last: st.last, Acked: st.ack, Why: st.why}
		}
		sort.Slice(ps.Tasks, func(i, j int) bool { return ps.Tasks[i].Task < ps.Tasks[j].Task })
		sort.Slice(ps.Facts, func(i, j int) bool { return ps.Facts[i].Seq < ps.Facts[j].Seq })
		st.Project = &ps
	}
	return st
}

// commandStep is a record's step as the running and pending lines show it (T-657, T-662): the merge loop's command
// steps — gate, publish the merge, after_merge, the nightly check — by their base name, the nightly one with no task.
// false: another step's record (a prompt, a start) runs no command the lines show.
func commandStep(r event.Record) (step, task string, ok bool) {
	if strings.HasPrefix(r.Step, event.StepNightly+".") {
		return event.StepNightly, "", true
	}
	switch step, _ = event.StepN(r.Step); step {
	case event.StepGate, event.StepPublish, event.StepAfterMerge:
		return step, r.Task, true
	}
	return "", "", false
}

// runningSteps is the steps that run without an agent, in seq order: the open intents of the merge loop's command
// steps whose effect runs now (p.exec, T-662) and, last, a pending self-restart (p.restarting). Since is the start of
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
		out = append(out, wire.RunningStep{Step: step, Task: task, Command: p.scratchCommand(r.Key()), Since: at.Unix()})
	}
	if !p.restarting.IsZero() {
		out = append(out, wire.RunningStep{Step: event.StepServeRestart, Since: p.restarting.Unix()})
	}
	return out
}

// pendingSteps is the steps that wait, in seq order (T-662): the merge loop's command steps whose intent has no result
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
// mt of r's task (zero: the loop holds none), by the rules the verbs themselves check (T-775): the one obor resume form
// resumeFits takes (its text where its action needs one; with no default, the owner's choice of --regate or
// --sendback; a worker's question also --regate), then obor drop when the task's state takes one (mtask.may); a
// watchdog fact its pane first; a loop error none ("": fix what it names). p (nil: none) is r's project, under mu.
func answerPath(p *proj, r event.Record, mt mtask) string {
	d := r.Fact()
	var cmds []string
	switch {
	case r.Task == event.ReviewTask: // a failed review start's: a new request ends it
		return "obor review [--after T-a,T-b]"
	case projectKey(r.Task): // a GREEN nightly ends a nightly's; a line typed, a line's
		return ""
	case strings.HasPrefix(d.Reason, reasonAsks):
		return resumeCmds(p, r.Task, mt)
	case !d.Merge:
		if !mt.needs.held() && (d.Reason == reasonStalled || d.Cause == causeStopped) && p != nil {
			cmds = append(cmds, resumeCmds(p, r.Task, mt)) // the worker gets the line (T-746)
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

// requestTimeout bounds a socket request's effect (a resolved's Git). It outlasts the connection's 10 s: an effect
// the caller stopped waiting for still completes, its reply stored for the repeated call id.
const requestTimeout = time.Minute

// Listen answers the app socket's requests on ln until it is closed: one request line per connection. A request's
// effect runs under the daemon's lifetime and requestTimeout; once Serve has ended, a connection is closed unanswered.
// Serve binds the socket only once its project is loaded (cmd/obor/serve.go, Loaded; T-659): a request never waits
// for the replay, a client retries the socket that is not there yet inside its own deadline.
func (a *App) Listen(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		stopped := a.stopped
		if !stopped {
			a.reqs.Add(1)
		}
		a.mu.Unlock()
		if stopped {
			c.Close()
			continue
		}
		go func() {
			defer a.reqs.Done()
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second)) // the request line
			b, err := bufio.NewReader(c).ReadBytes('\n')
			if err != nil {
				return
			}
			var req wire.Request
			var ans wire.Answer
			unreadable := json.Unmarshal(b, &req) != nil
			if !unreadable && req.Call != nil {
				a.noteSkew(*req.Call)
			}
			switch {
			case unreadable:
				ans.Error = "unreadable request"
			case req.Method == "status":
				st := a.Status()
				ans.Status = &st
			case req.Method == wire.MethodSpecSet || req.Method == wire.MethodPause || req.Method == wire.MethodSend || req.Method == wire.MethodBrief ||
				req.Method == wire.MethodWatch || req.Method == wire.MethodReview || req.Method == wire.MethodVerify || req.Method == wire.MethodStopped || req.Method == wire.MethodTurn || req.Method == wire.MethodSession ||
				req.Method == wire.MethodExit || req.Method == wire.MethodConfig || req.Method == wire.MethodTellLead || req.Method == wire.MethodIdle:
				if req.Call == nil {
					ans.Error = req.Method + ": no call"
					break
				}
				do := map[string]func(wire.Call) (string, error){wire.MethodSpecSet: a.specSet, wire.MethodPause: a.pauseCall,
					wire.MethodSend: a.sendCall, wire.MethodBrief: a.briefCall, wire.MethodWatch: a.watchCall, wire.MethodReview: a.reviewCall, wire.MethodVerify: a.verifyCall,
					wire.MethodStopped: a.stoppedCall, wire.MethodTurn: a.turnCall, wire.MethodSession: a.sessionCall,
					wire.MethodExit: a.exitCall, wire.MethodConfig: a.configCall, wire.MethodTellLead: a.tellLeadCall,
					wire.MethodIdle: a.idleCall}[req.Method]
				var err error
				if ans.Reply, err = do(*req.Call); err != nil {
					ans.Error = err.Error()
				}
			case req.Method == wire.MethodBackup:
				if req.Call == nil {
					ans.Error = req.Method + ": no call"
					break
				}
				var err error
				if ans.Reply, err = a.backup(*req.Call); err != nil {
					ans.Error = err.Error()
				}
			case req.Method == wire.MethodAccept || req.Method == wire.MethodSendback || req.Method == wire.MethodReject ||
				req.Method == wire.MethodResolved || req.Method == wire.MethodHold || req.Method == wire.MethodResume || req.Method == wire.MethodDrop ||
				req.Method == wire.MethodDispatch || req.Method == wire.MethodJobNext || req.Method == wire.MethodTaskDone ||
				req.Method == wire.MethodDrive || req.Method == wire.MethodLand:
				if req.Call == nil {
					ans.Error = req.Method + ": no call"
					break
				}
				var reply string
				var err error
				switch req.Method {
				case wire.MethodResume:
					reply, err = a.resume(*req.Call)
				case wire.MethodDrop:
					reply, err = a.drop(*req.Call)
				case wire.MethodJobNext:
					reply, err = a.jobNext(*req.Call)
				case wire.MethodTaskDone:
					reply, err = a.taskDone(*req.Call)
				case wire.MethodDispatch:
					ctx, cancel := context.WithTimeout(a.life, requestTimeout)
					reply, err = a.dispatchCall(ctx, *req.Call)
					cancel()
				case wire.MethodDrive:
					ctx, cancel := context.WithTimeout(a.life, requestTimeout)
					reply, err = a.driveCall(ctx, *req.Call)
					cancel()
				case wire.MethodLand:
					reply, err = a.landCall(*req.Call)
				default:
					ctx, cancel := context.WithTimeout(a.life, requestTimeout)
					reply, err = a.verdict(ctx, req.Method, *req.Call)
					cancel()
				}
				if err != nil {
					ans.Error = err.Error()
				}
				ans.Reply = reply
			default:
				ans.Error = "unknown method " + req.Method
			}
			if ans.Error != "" && req.Call != nil {
				if skew := versionSkew(*req.Call); skew != "" {
					ans.Error += fmt.Sprintf(" (caller %s runs %s, serve %s)", callerOf(*req.Call), skew, version.Version)
				}
			}
			out, _ := json.Marshal(ans)
			// The answer is written once its effect is done, which may wait for a stop (T-825): its own bound, not the
			// request line's.
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, _ = c.Write(append(out, '\n'))
		}()
	}
}

// versionSkew is c's caller's obor as text when it differs from this serve's (T-826): its version, or "an older obor"
// for a call that carried none (a binary before the field); "" when they match. A difference alone refuses nothing:
// obor-kit's dev builds differ after every merge, so only a refusal names it and serve's log says it once.
func versionSkew(c wire.Call) string {
	switch {
	case c.Version == version.Version:
		return ""
	case c.Version == "":
		return "an older obor"
	}
	return "obor " + c.Version
}

// noteSkew writes serve's one log line for a call whose caller runs another obor: the first per (agent, version), not
// each call (T-826).
func (a *App) noteSkew(c wire.Call) {
	skew := versionSkew(c)
	if skew == "" {
		return
	}
	agent := callerOf(c)
	key := agent + "\x00" + c.Version
	a.mu.Lock()
	if a.skews == nil {
		a.skews = map[string]bool{}
	}
	seen := a.skews[key]
	a.skews[key] = true
	a.mu.Unlock()
	if !seen {
		a.logf("caller %s runs %s, serve %s", agent, skew, version.Version)
	}
}

// herdrNote is the status note for the server serve is connected to: its version and protocol when it speaks a newer
// protocol than obor's, else none. It follows each snapshot, so a herdr started or upgraded after serve shows at once.
// The caller holds a.mu.
func (a *App) herdrNote() string {
	if !a.connected || a.herdrProt <= a.herdrOwn {
		return ""
	}
	return fmt.Sprintf("herdr %s, protocol %d (newer than obor's %d)", a.herdrVer, a.herdrProt, a.herdrOwn)
}
