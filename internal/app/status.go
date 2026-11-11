package app

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
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
				Agent: o.pane.Agent, Status: o.pane.Status, Age: int64(now.Sub(o.since) / time.Second)}
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
		ps.ReviewOpen = a.reviewOpen(p)
		ps.Briefs = p.out.BriefStatus()
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
		for _, set := range p.openWatches() {
			w := p.folded.watches[set]
			ps.Watches = append(ps.Watches, wire.WatchStatus{Task: set, By: w.by, Note: w.note, Since: w.at.Unix()})
		}
		if n := p.idleNow(); n.ok {
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

// requestTimeout bounds a socket request's effect (a resolved's Git). It outlasts the connection's 10 s: an effect
// the caller stopped waiting for still completes, its reply stored for the repeated call id.
const requestTimeout = time.Minute

// Listen answers the app socket's requests on ln until it is closed: one request line per connection. A request's
// effect runs under the daemon's lifetime and requestTimeout; once Serve has ended, a connection is closed unanswered.
// Serve binds the socket only once its project is loaded (cmd/obor/serve.go, Loaded): a request never waits
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
			var env *wire.Envelope // the caller, read first from any request that carries a call
			unreadable := json.Unmarshal(b, &req) != nil
			if !unreadable && len(req.Call) > 0 && string(req.Call) != "null" { // null: no call, as before the split
				env = &wire.Envelope{}
				if unreadable = json.Unmarshal(req.Call, env) != nil; unreadable {
					env = nil
				} else {
					a.noteSkew(req.Method, *env)
				}
			}
			do, known := a.handlers()[req.Method]
			switch {
			case unreadable:
				ans.Error = "unreadable request"
			case req.Method == "status":
				st := a.Status()
				ans.Status = &st
			case req.Method == wire.MethodEvents: // a stream of lines, its own writes
				var r *wire.EventsReq
				if env != nil {
					r = &wire.EventsReq{}
					_ = json.Unmarshal(req.Call, r) // its envelope read: its fields are numbers and a bool
				}
				a.feed(c, r)
				return
			case !known:
				ans.Error = "unknown method " + req.Method
			case env == nil:
				ans.Error = req.Method + ": no call"
			default:
				var err error
				if ans.Reply, err = do(req.Method, req.Call); err != nil {
					ans.Error = err.Error()
				}
			}
			if ans.Error != "" && env != nil {
				if skew := versionSkew(*env); skew != "" {
					ans.Error += fmt.Sprintf(" (caller %s runs %s, serve %s)", callerOf(*env), skew, version.Version)
				}
			}
			out, _ := json.Marshal(ans)
			// The answer is written once its effect is done, which may wait for a stop: its own bound, not the
			// request line's.
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, _ = c.Write(append(out, '\n'))
		}()
	}
}

// handler answers one method's request: its call's JSON decoded into the method's request type (handle).
type handler func(method string, call json.RawMessage) (string, error)

// handlers is the socket's methods but status and events (Listen's own): each decodes the call into its request type
// and answers it; dispatch, drive and the integrator's verdicts run under the daemon's lifetime and
// requestTimeout.
func (a *App) handlers() map[string]handler {
	verdict := handle(func(c wire.VerdictReq) (string, error) {
		ctx, cancel := context.WithTimeout(a.life, requestTimeout)
		defer cancel()
		return a.verdict(ctx, c)
	})
	return map[string]handler{
		wire.MethodAccept:      verdict,
		wire.MethodSendback:    verdict,
		wire.MethodReject:      verdict,
		wire.MethodResolved:    verdict,
		wire.MethodHold:        verdict,
		wire.MethodResume:      handle(a.resume),
		wire.MethodDrop:        handle(a.drop),
		wire.MethodJobNext:     handle(a.jobNext),
		wire.MethodTaskDone:    handle(a.taskDone),
		wire.MethodDispatch:    handle(timed(a, a.dispatchCall)),
		wire.MethodDrive:       handle(timed(a, a.driveCall)),
		wire.MethodLand:        handle(a.landCall),
		wire.MethodRestartJob:  handle(a.restartJob),
		wire.MethodSpecSet:     handle(a.specSet),
		wire.MethodPause:       handle(a.pauseCall),
		wire.MethodSend:        handle(a.sendCall),
		wire.MethodBrief:       handle(a.briefCall),
		wire.MethodWatch:       handle(a.watchCall),
		wire.MethodReview:      handle(a.reviewCall),
		wire.MethodStopped:     handle(a.stoppedCall),
		wire.MethodTurn:        handle(a.turnCall),
		wire.MethodSession:     handle(a.sessionCall),
		wire.MethodStarted:     handle(a.startedCall),
		wire.MethodExit:        handle(a.exitCall),
		wire.MethodConfig:      handle(a.configCall),
		wire.MethodTellLead:    handle(a.tellLeadCall),
		wire.MethodIdle:        handle(a.idleCall),
		wire.MethodReviewClose: handle(a.reviewCloseCall),
		wire.MethodBackup:      handle(a.backup),
		wire.MethodWhere:       handle(a.whereCall),
	}
}

// handle is f as a handler: the call decoded into f's request type R; a VerdictReq's Verb is the method it came under.
func handle[R any](f func(R) (string, error)) handler {
	return func(method string, call json.RawMessage) (string, error) {
		var r R
		if err := json.Unmarshal(call, &r); err != nil {
			return "", errors.New("unreadable request")
		}
		if v, ok := any(&r).(*wire.VerdictReq); ok {
			v.Verb = method
		}
		return f(r)
	}
}

// timed is f under the daemon's lifetime and requestTimeout: a dispatch's and a drive's effect outlives a caller that
// stopped waiting.
func timed[R any](a *App, f func(context.Context, R) (string, error)) func(R) (string, error) {
	return func(r R) (string, error) {
		ctx, cancel := context.WithTimeout(a.life, requestTimeout)
		defer cancel()
		return f(ctx, r)
	}
}

// versionSkew is c's caller's obor as text when it differs from this serve's: its version, or "an older obor"
// for a call that carried none (a binary before the field); "" when they match. A difference alone refuses nothing:
// obor-kit's dev builds differ after every merge, so only a refusal names it and serve's log says it once.
func versionSkew(c wire.Envelope) string {
	switch {
	case c.Version == version.Version:
		return ""
	case c.Version == "":
		return "an older obor"
	}
	return "obor " + c.Version
}

// noteSkew writes serve's one log line for a call whose caller runs another obor: the first per (agent, version), not
// each call. It also keeps, per agent, the version of the other release its calls carry (behind): a session request
// clears it — a hook's, a plugin's, or a start's of the session the agent runs, a repeat of one already recorded
// included — and so does a started request, a launcher's confirmed start (startedCall). A call of serve's own release
// never clears it: one agent calls through several processes, and the line must not flicker between them. A started
// request names the agent a launcher started, not a caller beside it: its reporter's release is not that agent's, so it
// holds nothing here and logs nothing.
func (a *App) noteSkew(method string, c wire.Envelope) {
	if method == wire.MethodStarted {
		return
	}
	if c.Agent != "" { // the owner's own CLI is not an agent: nothing to restart
		other := version.Release(c.Version) != version.Release(version.Version)
		a.mu.Lock()
		if method == wire.MethodSession {
			delete(a.behind, c.Agent) // a session began: the process before it is not the one running now
		}
		if other {
			if a.behind == nil {
				a.behind = map[string]string{}
			}
			a.behind[c.Agent] = c.Version
		}
		a.mu.Unlock()
	}
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

// started ends agent's skew entry: a start (fresh or resumed) runs a new process for it, so the release its earlier
// calls carried is not the one serving its calls now, and the line naming it asks for a restart that happened. A start
// that names no agent ends nothing. Under mu.
func (a *App) started(agent string) {
	if agent == "" {
		return
	}
	a.mu.Lock()
	delete(a.behind, agent)
	a.mu.Unlock()
}

// startedCall answers the socket's started request: a launcher's start of the envelope's agent is confirmed — the process
// that runs now is that start's, whatever session it reports (a fresh Codex start with no prompt has none yet) — so
// serve ends the agent's skew entry. It records no session: a session is the session request's.
func (a *App) startedCall(c wire.StartedReq) (string, error) {
	if c.Agent == "" {
		return "", errors.New(wire.MethodStarted + ": the agent is required")
	}
	if a.projAt(c.Root) == nil {
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodStarted, c.Root)
	}
	a.started(c.Agent)
	return "STARTED " + c.Agent, nil
}

// skewNote is the status note for the agents in a pane whose calls carry another obor release than serve's (behind):
// "pj-l-01 runs obor v0.33.0, pj-rev runs an older obor (serve v0.34.0)", agents in name order; "" when none. seen is
// the agents serve observes in a pane now: one that left is not named. The caller holds a.mu.
func (a *App) skewNote(seen map[string]bool) string {
	var names []string
	for name := range a.behind {
		if seen[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" runs "+versionSkew(wire.Envelope{Version: a.behind[name]}))
	}
	return strings.Join(parts, ", ") + " (serve " + version.Version + ")"
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
