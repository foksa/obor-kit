package app

import (
	"cmp"
	"context"
	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A worker stopped on a usage limit continues by itself. Its stop (stopped.go: fact stopped, a worker's
// needs-you of cause stopped) carries Limit and, when the agent's kind read one, its Reset (FactData). Each merge-loop
// pass (stopWakes):
//   - wake: wakeAfter past the reset, or, a reset unknown, every wakeEvery since the stop, the worker gets a continue
//     wake for its open job, delivered as a job's wake is (prompt.go: its session's inbox, else typed; never into a
//     pane herdr reports blocked), under the key continue.<the wake's time, Unix seconds>: one per wake time, a replay
//     finds it done, and a time passed while serve was down wakes once at the next pass;
//   - alarm: still stopped wakeAlarm after the stop's first wake, needs-you{<the stop>; still stopped 30m after the
//     reset wake} (cause stopped) once, the lead told as for any needs-you, the owner toasted by the needs rule.
//
// The stop ends with the worker's next turn as before (turnCall, turnStop); a limit hit again is a new stop with its
// own reset. A stop that is no limit (billing, unknown) gets no wake: the needs-you it is.
//
// A role agent (not the lead) stopped on a usage limit with its brief open (stopped.go roleStop: the brief
// stays open, the stop on it) gets the same, from deliver's pass (roleWakes): the wake, a line to the agent under the
// key continue.<the wake's time>.<agent> of the project's lines, sent as the worker's is; the alarm, still stopped
// wakeAlarm after the first wake: the lead's line "<the stop's line>; still stopped 30m after the reset wake" (fact
// message, its Stop the stop's seq) and the owner's toast, one rule action's call (stopAlarmRule), once. A
// Codex role agent's stop, its next turn's start and that turn's end are read from its rollout (roleTurns).
// With no brief open there is nothing to continue: the stopped line only. The lead is never woken.

const (
	wakeAfter    = 2 * time.Minute  // a known reset: the wake this long after it
	wakeEvery    = 30 * time.Minute // a reset unknown: a wake each period since the stop
	wakeAlarm    = 30 * time.Minute // still stopped this long after the stop's first wake: the alarm
	stepContinue = "continue."      // prompt intent (P) continue.<Unix seconds>: a stopped worker's wake at that time
	stillStopped = "; still stopped 30m after the reset wake"
	continueText = "Your usage limit has reset: continue where your turn stopped (" + wakeText + " for your job)."
	roleContinue = "[obor] Your usage limit has reset: continue your brief where your turn stopped."
)

// wakeDue is the time the stop f's latest wake is due at, as of now (ok false: none is due yet). Its Reset known:
// wakeAfter past it. Unknown: the last whole wakeEvery since the stop.
func wakeDue(f event.Record, now time.Time) (time.Time, bool) {
	return wakeAt(f.Fact().Reset, event.RecordTime(f), now)
}

// wakeAt is wakeDue for a stop at since with reset (Unix seconds; 0: unknown).
func wakeAt(reset int64, since, now time.Time) (time.Time, bool) {
	if reset > 0 {
		due := time.Unix(reset, 0).Add(wakeAfter)
		return due, !now.Before(due)
	}
	n := now.Sub(since) / wakeEvery
	if n < 1 {
		return time.Time{}, false
	}
	return since.Add(n * wakeEvery), true
}

// continueKey is the key of task's wake due at due.
func continueKey(task string, due time.Time) event.Key {
	return event.Key{Task: task, Step: stepContinue + strconv.FormatInt(due.Unix(), 10)}
}

// wakeTo is one wake to send: the task ("": a role agent's), the job named, the agent, the key and the line.
type wakeTo struct {
	task, job, agent string
	key              event.Key
	line             string
}

// stopWakes is one pass of the stopped workers' wakes and alarms (see above): the alarms due are recorded, then the
// wakes due are sent, each outside mu.
func (a *App) stopWakes(ctx context.Context, p *proj, fx *Loop) {
	now := a.opt.Now()
	var wakes []wakeTo
	a.mu.Lock()
	for _, task := range p.openTasks() {
		f, ok := p.stop(task)
		if !ok || !f.Fact().Limit || p.ended[task] {
			continue
		}
		w := p.tasks[task]
		if woke := p.folded.woke[task]; !woke.IsZero() && !woke.Before(event.RecordTime(f)) && now.Sub(woke) >= wakeAlarm &&
			!p.alarmed(task) {
			a.fact(p, task, w, verdict{step: event.StepNeedsYou, reason: f.Fact().Reason + stillStopped, cause: causeStopped,
				pane: w.agent})
		}
		due, ok := wakeDue(f, now)
		mt := p.folded.loop[task]
		if !ok || mt == nil {
			continue
		}
		job, open := mt.workerJob()
		key := continueKey(task, due)
		if !open || p.log.Done(key) {
			continue
		}
		wakes = append(wakes, wakeTo{task: task, job: JobID(job), agent: cmp.Or(w.lane.Agent, mt.agent), key: key,
			line: jobLine(JobID(job), continueText)})
	}
	a.mu.Unlock()
	for _, wk := range wakes {
		if ctx.Err() != nil {
			return
		}
		a.sendWake(ctx, p, fx, wk)
	}
}

// sendWake sends the continue wake wk to its agent's pane as whereIs resolves it, as a job's wake goes (send): not
// identified, or its pane blocked (a dialog would take the text), nothing is sent and a later pass tries again; an
// error before a byte went out leaves the intent pending, sent again by a later pass.
func (a *App) sendWake(ctx context.Context, p *proj, fx *Loop, wk wakeTo) {
	a.mu.Lock()
	w := a.whereIs(p, wk.agent)
	post, inboxed := a.via(fx, p, w, wk.agent)
	a.mu.Unlock()
	if w.State != Identified || w.Pane.Status == "blocked" {
		return
	}
	target := w.Pane.ID
	switch {
	case post != "" && inboxed:
		target = wk.agent
	case fx.Typer == nil:
		return
	default:
		post = ""
	}
	if _, err := a.do(ctx, p, event.Intent{Key: wk.key, Class: event.ClassPrompt, Data: promptData{Agent: wk.agent, Job: wk.job}},
		func(ctx context.Context) (any, error) {
			return sentResult(fx.transmit(ctx, post, target, wk.line, true))
		}); err == nil {
		a.logf("%s %s %s: the stopped agent %s woken", p.name, wk.key.Task, wk.key.Step, wk.agent)
	}
}

// foldWake keeps the time of task's first wake since its stops last ended, or of a role agent's stop's first wake (a
// continue result; under mu, folded).
func (p *proj) foldWake(r event.Record) {
	if r.Kind != event.KindResult || !strings.HasPrefix(r.Step, stepContinue) {
		return
	}
	if r.Task == event.MsgTask {
		_, agent, _ := strings.Cut(strings.TrimPrefix(r.Step, stepContinue), ".")
		p.out.Woke(agent, event.RecordTime(r))
		return
	}
	if p.folded.woke[r.Task].IsZero() {
		p.folded.woke[r.Task] = event.RecordTime(r)
	}
}

// alarmed: task's stop in force has had its alarm.
func (p *proj) alarmed(task string) bool {
	for _, f := range p.folded.active[task] {
		if isStop(f) && strings.HasSuffix(f.Fact().Reason, stillStopped) {
			return true
		}
	}
	return false
}

// roleTurns reads the transcripts of p's briefed role agents whose turns are read (Codex) and decides by turnDecide
// (stopped.go), as turnStop does a worker's (each read outside mu, the facts recorded after). Read: a stop in force
// read from the transcript, a turn followed since (until its end is read), or the brief's work seen with the pane
// idle. The stopped turn itself decides nothing; a later turn's start ends the stop (fact turned, the brief open, no
// finish armed), its end finishes the brief (fact turned, worked); an end on an error with the pane idle,
// not recorded for that turn, is a new stop (fact stopped, call id "turn.<id>"), as the StopFailure hook's for a
// Claude role agent (roleStop).
func (a *App) roleTurns(ctx context.Context, p *proj) {
	type read struct {
		agent      string
		pane       port.Pane
		transcript string // its recorded transcript (sessions.go)
	}
	var reads []read
	a.mu.Lock()
	for agent, b := range p.out.Briefs() {
		w := a.whereIs(p, agent)
		if agent == p.out.Lead() || w.State != Identified || !turnsRead(w.Pane.Agent) {
			continue
		}
		if s := b.Stop(); (s != nil && s.Turn != "") || (s == nil && (b.Followed() || (b.Worked() && Ready(w.Pane.Status)))) {
			reads = append(reads, read{agent, w.Pane, a.transcript(p, w.Pane)})
		}
	}
	a.mu.Unlock()
	sort.Slice(reads, func(i, j int) bool { return reads[i].agent < reads[j].agent })
	for _, rd := range reads {
		t, ok := a.panes.Turn(ctx, rd.pane, rd.transcript)
		if !ok {
			continue
		}
		a.mu.Lock()
		b := p.out.Brief(rd.agent)
		var fact any
		step := event.StepStopped
		_, done := p.folded.calls[verdictStep(wire.MethodStopped, "turn."+t.ID)]
		act := turnNone
		if b != nil {
			if s := b.Stop(); (s == nil || (s.Turn != "" && s.Turn != t.ID)) && counts(t, rd.pane.Status, done) {
				act = turnDecide(turnCase{stopped: s != nil, followed: b.Followed(), worked: b.Worked(), ended: t.Ended,
					failed: t.Error != ""})
			}
		}
		switch act {
		case turnResume, turnFinish:
			step, fact = event.StepTurned, outbox.Turned{To: rd.agent, Brief: b.Seq(), Worked: act == turnFinish}
		case turnStop:
			d, said := newStop(rd.agent, t.Error, t.Details, "turn."+t.ID)
			a.roleStop(p, &d, said, rd.pane.Agent, t.Reset)
			d.Turn = t.ID
			fact = d
		}
		a.mu.Unlock()
		if fact != nil && a.record(p, event.Record{Task: event.MsgTask, Step: step, Kind: event.KindFact}, fact) {
			a.logf("%s %s %s (turn %s)", p.name, step, rd.agent, t.ID)
		}
	}
}

// roleWakes is one pass of the stopped role agents' wakes (see above): the wakes due sent (sendWake), each outside mu.
// The alarm line and the owner's toast are the stop alarm's rule (stopAlarmRule, rules.go).
func (a *App) roleWakes(ctx context.Context, p *proj, fx *Loop) {
	now := a.opt.Now()
	var wakes []wakeTo
	a.mu.Lock()
	for agent, b := range p.out.Briefs() {
		s := b.Stop()
		if s == nil || agent == p.out.Lead() {
			continue
		}
		if due, ok := wakeAt(s.Reset, s.At, now); ok {
			key := event.Key{Task: event.MsgTask, Step: stepContinue + strconv.FormatInt(due.Unix(), 10) + "." + agent}
			if !p.log.Done(key) {
				wakes = append(wakes, wakeTo{agent: agent, key: key, line: roleContinue})
			}
		}
	}
	a.mu.Unlock()
	sort.Slice(wakes, func(i, j int) bool { return wakes[i].agent < wakes[j].agent })
	for _, wk := range wakes {
		if ctx.Err() != nil {
			return
		}
		a.sendWake(ctx, p, fx, wk)
	}
}

// ruleStopAlarm is the stopped role agent's alarm rule (stopwake.go): its stop's toast and the lead's line.
const ruleStopAlarm = "stopalarm"

// stopAlarmRule is the alarm's rule: the stop of a briefed role agent, woken, still stopped wakeAlarm later; key its
// stop's fact seq. The lead's line records first; the toast is its best-effort after. A stop whose alarm is
// already recorded is skipped, so the next stopped agent gets its turn (the rule's did is not enough: the condition
// would keep keying the first one).
func stopAlarmRule(_ context.Context, a *App, p *proj, _ *Loop, now time.Time) (int64, action, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.out.Lead() == "" {
		return 0, action{}, false
	}
	var s *outbox.RoleStop
	for agent, b := range p.out.Briefs() {
		st := b.Stop()
		if st == nil || agent == p.out.Lead() || st.Alarmed || st.Woke.IsZero() || now.Sub(st.Woke) < wakeAlarm {
			continue
		}
		if _, done := p.folded.did[event.Of{Kind: ruleStopAlarm, Seq: st.Seq}]; done {
			continue
		}
		if s == nil || st.Seq < s.Seq {
			s = st
		}
	}
	if s == nil {
		return 0, action{}, false
	}
	title, body := "obor: "+p.name+" needs you", strings.TrimPrefix(s.Line, "[obor] ")+stillStopped
	return s.Seq, action{line: s.Line + stillStopped,
		after: func(ctx context.Context) { a.toast(ctx, title, body) }}, true
}
