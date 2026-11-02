// jobstart.go: starting a job for a task, its prompt text and the integrator pane.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"strings"
	"time"
	"unicode/utf8"
)

// job sends the integrator a review or resolve job (rows 25, 36) through the one start sequence (startJob: the start
// runs under the job's intent and carries its wake). Each job's first start (start.<job step>) is a fresh session of
// the integrator (T-602), also when one runs, so a job being answered is never cut off: it waits (false, nothing
// recorded) while another task's job is out, then while the integrator's pane is not idle, for at most Gone; past
// that the start forces (stops a turn that hangs or is blocked: the verdict is recorded). Its intent records the
// integrator's pane and session then; a pending one (a crash, a failed start) starts fresh again only when the
// snapshot shows that no new session runs (none runs, or the same session as at the intent), else it no-ops
// ("running": no second start of the job). A later start of the job (start.<job step>.2 … once its start is done
// and the integrator is gone again) runs when the snapshot shows it does not run, as before. An integrator that
// runs, or a start that did not carry the wake, gets the job's wake typed (send); a job whose prompt failed is sent
// again under its key.
func (a *App) job(ctx context.Context, p *proj, fx *Loop, mt mtask, jobStep string) bool {
	key := event.Key{Task: mt.task, Attempt: mt.attempt, Step: jobStep}
	start := event.Key{Task: mt.task, Attempt: mt.attempt, Step: stepStart + jobStep}
	for n := 2; p.log.Done(start); n++ {
		start.Step = nth(stepStart+jobStep, n)
	}
	replay := p.log.Open(start)
	a.mu.Lock()
	w := a.whereIs(p, p.integrator)
	d := startData{Agent: p.integrator}
	if w.State == Identified {
		d.Pane, d.Ran, d.Session = w.Pane.ID, true, w.Pane.Session
	}
	if r, ok := p.log.Intent(start); ok { // a pending start: its recorded data
		d = startData{}
		_ = json.Unmarshal(r.Data, &d)
	}
	// the job's first start: a new session, unless a pending one shows its session started
	fresh := start.Step == stepStart+jobStep && (!replay || freshAgain(d, w))
	force, wait := false, false
	if fresh {
		force, wait = a.integratorWait(p, mt.task, w)
	}
	a.mu.Unlock()
	if w.State == Ambiguous && !replay { // no third pane started, nothing sent
		return a.heldJob(ctx, p, fx, mt, key, w)
	}
	if wait {
		return false // the running session answers another job, or ends its turn: a later pass starts this one
	}
	if force {
		a.logf("merge loop of %s: %s: integrator %s still %s %s after the last verdict: started fresh with force",
			p.name, JobID(key), p.integrator, w.Pane.Status, a.opt.Gone)
	}
	data := promptData{Agent: p.integrator, Patch: mt.cand.Patch}
	if fresh || w.State != Identified || replay {
		if !a.startJob(ctx, p, mt, event.Intent{Key: start, Class: event.ClassStart, Data: d}, key, data,
			func(ctx context.Context, first string) (any, error) {
				s, err := a.startIntegrator(ctx, fx.Agents, p.root, port.Launch{Prompt: first, Fresh: fresh, Force: force})
				switch {
				case err != nil:
					return nil, fmt.Errorf("integrator %s not started: %w", p.integrator, err)
				case s.Line == "running":
					return s.Line, nil
				}
				return startOut{Line: s.Line, Agent: p.integrator, Pane: s.Pane, Terminal: s.Terminal, Kind: s.Kind,
					Session: s.Session}, nil
			}) {
			return false
		}
		if p.log.Done(key) {
			return true // the start carried its wake
		}
	}
	at := a.opt.Now()
	if !a.send(ctx, p, fx, mt, key, JobID(key), data, wakeText,
		fmt.Sprintf("job %s not sent to %s", JobID(key), p.integrator)) {
		return false
	}
	a.mu.Lock()
	p.timers[mt.task] = &jobTimer{job: JobID(key), sent: at}
	a.mu.Unlock()
	return true
}

// freshAgain: a pending first start of an integrator job (its intent's data d) starts a fresh session again: the
// snapshot (w) shows no integrator, or the session the intent saw (the start did not replace it). A session id
// unknown at the intent or now cannot tell: no second start (it no-ops, the wake typed), as a new session does.
func freshAgain(d startData, w Where) bool {
	switch {
	case w.State != Identified:
		return true
	case !d.Ran || d.Session == "" || w.Pane.Session == "":
		return false
	}
	return w.Pane.Session == d.Session
}

// idleWait is since when the integrator's pane has been identified and not Ready: the busy interval belongs to the
// pane, not to a job (T-606), so two queued jobs share it and one Gone bounds the wait for both.
type idleWait struct {
	since time.Time
}

// integratorWait is whether a fresh start of p's integrator (w) for task's job would cut off a job it answers now
// (under mu, T-602): wait while another task's integrator job is out (awaiting-accept, resolving; one a needs-you
// left is the owner's, it waits for no one); then, while the integrator's pane is identified and not idle (it ends
// its turn after a verdict), wait for at most Gone from the first such look, then force: the start stops that turn.
// The interval is the pane's, not the job's (T-606): alternating jobs cannot postpone it by replacing each other.
func (a *App) integratorWait(p *proj, task string, w Where) (force, wait bool) {
	for _, mt := range p.folded.loop {
		if mt.task != task && mt.openJob() && mt.state != stNeedsYou {
			p.idleWait = idleWait{}
			return false, true
		}
	}
	if w.State != Identified || Ready(w.Pane.Status) {
		p.idleWait = idleWait{}
		return false, false
	}
	now := a.opt.Now()
	if p.idleWait.since.IsZero() {
		p.idleWait = idleWait{since: now}
	}
	if now.Sub(p.idleWait.since) < a.opt.Gone {
		return false, true
	}
	return true, false
}

// errNoCarry is a start's outcome that did not carry its job's wake (refused, "running", a replay's stored result):
// the job's intent stays pending for the caller's send.
var errNoCarry = errors.New("the start did not carry the job's wake")

// startJob is the one start sequence of an agent that fetches its jobs (docs/state-model.md rows 4–6): the worker's
// work job (agent.start under work[.N]) and the integrator's review and resolve jobs (start.<job step> under the
// job), their numbered and recovery variants. The job's intent (P, data: the job's prompt data) is written first, so
// the job is open while its agent starts (job_next hands it out, a verdict or task_done answers it); the start in (S)
// runs under it, launch starting the agent with the job's wake as its first prompt (first; a kind that takes no first
// prompt ignores it). A start that started its agent (a startOut, not refused) is the job's delivery: result "sent
// (start prompt)", its delivery timers start. Any other outcome leaves the job's intent pending: the caller types its
// wake (harmless after a start that did carry it). False: the start failed (counted) or nothing was decided.
func (a *App) startJob(ctx context.Context, p *proj, mt mtask, in event.Intent, job event.Key, data promptData,
	launch func(ctx context.Context, first string) (any, error)) bool {
	data.Job = JobID(job)
	at := a.opt.Now()
	var startErr error
	_, err := a.do(ctx, p, event.Intent{Key: job, Class: event.ClassPrompt, Data: data}, func(ctx context.Context) (any, error) {
		carried := false
		_, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
			v, err := launch(ctx, wakeLine(data.Job))
			out, ok := v.(startOut)
			carried = err == nil && ok && out.Refused == ""
			return v, err
		})
		switch {
		case err != nil:
			startErr = err
			return nil, err
		case !carried:
			return nil, errNoCarry
		}
		return sentStart, nil
	})
	switch {
	case startErr != nil:
		return a.failed(ctx, p, mt, in.Step, startErr)
	case err == nil:
		a.mu.Lock()
		p.timers[mt.task] = &jobTimer{job: data.Job, sent: at}
		a.mu.Unlock()
	}
	return err == nil || errors.Is(err, errNoCarry)
}

// reviewText is the review job (row 25): the spec to review (the rev the gate put into the candidate's message,
// T-521), the worker's report, the candidate and its patch hash.
func (a *App) reviewText(p *proj, mt mtask) (string, error) {
	c := mt.cand
	row, _, err := specOf(p, mt.task)
	if err != nil {
		return "", err
	}
	spec := "No spec is stored for " + mt.task + ".\n"
	switch {
	case mt.gateRev > 0 && row.Rev == mt.gateRev:
		spec = fmt.Sprintf("Spec to review: rev %d (the one the candidate's merge message contains):%s\n", mt.gateRev,
			specText(row))
	case mt.gateRev > 0:
		spec = fmt.Sprintf("Spec to review: rev %d, in the candidate's merge message (git log -1 --format=%%B %s); the lead "+
			"changed it after the gate, the current one:%s\n", mt.gateRev, c.SHA, specText(row))
	case row.Rev > 0:
		spec = fmt.Sprintf("The gate merged no spec; the current one:%s\n", specText(row))
	}
	return fmt.Sprintf("Review %s for obor serve (attempt %d, lane %s): the gate is green (%s).\n%s"+
		"Candidate: %s, the target %s plus the lane, pinned at refs/obor/candidate/%s; %s\nPatch hash: %s\n"+
		"The worker's report:\n%s\n"+
		"%s%s",
		mt.task, mt.attempt, mt.lane, c.Checks, spec, c.SHA, c.Target, mt.task, diffText(c), c.Patch, mt.report,
		questionsText(mt.report), decisionText(mt)), nil
}

// questionsText is the review job's part for the report's Open questions: section (T-649; "": it leaves nothing
// open): the section verbatim, cut like the diff at InlineDiff bytes, and what to do with it: decide a question
// the spec's goal and decisions answer in the accept note, hold one that changes scope, a contract, config or a
// protected file.
func questionsText(report string) string {
	q := openQuestions(report)
	if q == "" {
		return ""
	}
	if len(q) > port.InlineDiff {
		q = clip(q, port.InlineDiff) + fmt.Sprintf("\n(cut at %d bytes: the whole section is in the report above)", port.InlineDiff)
	}
	return "The worker's open questions (decide one the spec's goal and decisions answer: decided: <what> — <why> in " +
		"the accept note; hold one that changes scope, a contract, config or a protected file):\n" + q +
		"\n(end of the open questions)\n"
}

// diffText is the review job's diff part (T-602): the stat, then the whole diff when the gate kept it (at most
// InlineDiff bytes: no git diff call needed), else the stat and the git diff to read it with.
func diffText(c port.Candidate) string {
	if c.Diff == "" {
		return fmt.Sprintf("read it with git diff %s %s.\n%s\n", c.Target, c.SHA, c.Stat)
	}
	return fmt.Sprintf("its stat, then its whole diff (git diff %s %s, as the gate made it: no need to run it):\n%s\n"+
		"%s\n(end of the whole diff)\n", c.Target, c.SHA, c.Stat, strings.TrimRight(c.Diff, "\n"))
}

// decisionText is the line of the lead's decision on mt's last obor resume of the attempt ("": none), for the
// integrator's next job (T-511).
func decisionText(mt mtask) string {
	if mt.decision == "" {
		return ""
	}
	return mt.decision + "\n"
}

// resolveText is the resolve job (row 25): the conflict and where it is open. The steps are the role text's.
func resolveText(mt mtask) string {
	line, _, _ := strings.Cut(mt.line, "(resolve there")
	return fmt.Sprintf("Resolve %s's merge for obor serve (attempt %d, lane %s): %s\n%s",
		mt.task, mt.attempt, mt.lane, strings.TrimSpace(line), decisionText(mt))
}

// subjectOf is the subject text of the lane's commit and its merge (T-355): the task spec's title (the store's, T-440;
// never the checkout on disk, T-360), else the report's summary.
func subjectOf(title, report string) string {
	if title != "" {
		return title
	}
	return summaryOf(report)
}

// summaryOf is the report's summary for the lane's commit: the rest of its Summary line, else the line after it,
// without a list marker, cut at a word boundary within 100 characters.
func summaryOf(report string) string {
	lines := strings.Split(report, "\n")
	for i, l := range lines {
		t := strings.TrimLeft(strings.TrimSpace(l), "#* ")
		if !strings.HasPrefix(strings.ToLower(t), "summary") {
			continue
		}
		s := strings.TrimSpace(strings.TrimLeft(t[len("summary"):], ":* "))
		if s == "" && i+1 < len(lines) {
			s = strings.TrimSpace(lines[i+1])
		}
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "- "), "* "))
		if len(s) > 100 {
			s = s[:100]
			if i := strings.LastIndexByte(s, ' '); i > 0 {
				s = s[:i]
			} else {
				for !utf8.ValidString(s) {
					s = s[:len(s)-1]
				}
			}
			s = strings.TrimSpace(s)
		}
		if s != "" {
			return s
		}
	}
	return "merged by obor serve"
}
