package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"time"
)

// The jobs' timers (docs/state-model.md rows 7–8, 31; "Merge loop as built": Jobs). A job is delivered only by the
// agent's job_next (fetch.go). The timers are not recorded, so a replay starts them afresh.

// deliveryWait is how long a job may go undelivered after its prompt, and again after its retry (rows 7–8).
const deliveryWait = 5 * time.Minute

// jobTimer is the timers of one open integrator job, not recorded (a replay starts them afresh): the prompt and the
// last prompt of it (the retry), since when no integrator runs, and the quiet time once the job is delivered.
type jobTimer struct {
	job     string
	sent    time.Time
	retried bool
	gone    time.Time // zero: the integrator runs
	quietTimer
}

// watchJob is the next step of mt's open integrator job (under mu): its timers (docs/state-model.md rows 7–8, 31).
// Delivered by its job_next (or its verdict). Undelivered for deliveryWait: one retry, then the owner. Delivered, then the integrator ready and quiet with it for Idle (the stall
// timer's quiet time): one nudge, then the owner. No integrator for Gone: the job is lost, so the integrator is
// started again and the job sent anew under a new key, once; then the owner. Two panes claiming the integrator for
// Gone: the owner (unidentified).
func (a *App) watchJob(p *proj, mt mtask) step {
	now := a.opt.Now()
	key := mt.jobKey()
	t := a.timerOf(p, key)
	w := a.whereIs(p, p.integrator)
	if w.State != Identified {
		if t.gone.IsZero() {
			t.gone = now
		}
		switch {
		case now.Sub(t.gone) < a.opt.Gone:
			return nil
		case w.State == Ambiguous:
			return a.unidentified(p, mt, key, w)
		case mt.lost > 0:
			return func(context.Context, *Loop) bool {
				a.needsYou(p, &mt, causeStalled, fmt.Sprintf("job stalled: %s exited again with job %s open",
					p.integrator, JobID(key)))
				return false
			}
		}
		return func(context.Context, *Loop) bool {
			return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepLost, Kind: event.KindFact},
				event.FactData{Lane: mt.lane, Machine: Machine, Reason: "integrator exited", Job: JobID(key), Merge: true})
		}
	}
	t.gone = time.Time{}
	switch {
	case !mt.delivered:
		return a.undelivered(p, mt, t, key, p.integrator, func(ctx context.Context, fx *Loop, retry event.Key) bool {
			return a.reprompt(ctx, p, fx, mt, retry, "")
		})
	case !Ready(w.Pane.Status):
		t.quietTimer = quietTimer{} // working or blocked: no quiet time
		return nil
	}
	pane := w.Pane.ID
	return func(ctx context.Context, fx *Loop) bool { return a.sampleJob(ctx, p, fx, mt, t, pane) }
}

// unidentified is the owner's needs-you for mt's job key while two or more panes claim p's integrator (w): nothing is
// started or sent to either.
func (a *App) unidentified(p *proj, mt mtask, key event.Key, w Where) step {
	return func(context.Context, *Loop) bool {
		a.needsYou(p, &mt, causeStalled, fmt.Sprintf("job stalled: integrator %s with job %s open",
			notIdentified(p.integrator, w), JobID(key)))
		return false
	}
}

// heldJob holds mt's job key while two or more panes claim p's integrator (w): nothing is started or sent; after Gone
// the owner's needs-you names the panes.
func (a *App) heldJob(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key, w Where) bool {
	a.mu.Lock()
	t := a.timerOf(p, key)
	if t.gone.IsZero() {
		t.gone = a.opt.Now()
	}
	late := a.opt.Now().Sub(t.gone) >= a.opt.Gone
	a.mu.Unlock()
	if late {
		a.unidentified(p, mt, key, w)(ctx, fx)
	}
	return false
}

// timerOf is the timer of p's open job key (under mu): the one running, else a new one from now (a replay, or the job
// sent in an earlier process), retried when its retry is in the log.
func (a *App) timerOf(p *proj, key event.Key) *jobTimer {
	t := p.timers[key.Task]
	if t == nil || t.job != JobID(key) {
		now := a.opt.Now()
		t = &jobTimer{job: JobID(key), sent: now, retried: p.log.Done(retryKey(key))}
		p.timers[key.Task] = t
	}
	return t
}

// jobSent starts task's job timer for job, prompted at (under mu): the job's delivery timers run from at.
func (a *App) jobSent(p *proj, task, job string, at time.Time) {
	a.mu.Lock()
	p.timers[task] = &jobTimer{job: job, sent: at}
	a.mu.Unlock()
}

// undelivered is the delivery timer of mt's job key to agent (rows 7–8, under mu), the work, fix and integrator jobs'
// alike: nothing for deliveryWait after the prompt, then resend under retry.<step> once (the same job id: the agent's
// wake again; at once when a crash or a refusal left it pending), deliveryWait after that the owner: needs-you{job not
// delivered}.
func (a *App) undelivered(p *proj, mt mtask, t *jobTimer, key event.Key, agent string,
	resend func(ctx context.Context, fx *Loop, retry event.Key) bool) step {
	now := a.opt.Now()
	wait := now.Sub(t.sent) < deliveryWait
	switch {
	case !t.retried && (!wait || p.log.Open(retryKey(key))):
		return func(ctx context.Context, fx *Loop) bool {
			if resend(ctx, fx, retryKey(key)) {
				a.mu.Lock()
				t.sent, t.retried = now, true // the time it was decided: the wait counts from before the prompt
				a.mu.Unlock()
			}
			return false
		}
	case wait:
		return nil
	}
	return func(context.Context, *Loop) bool {
		a.needsYou(p, &mt, causeUndelivered, fmt.Sprintf("job not delivered: %s prompted twice to %s", JobID(key), agent))
		return false
	}
}

// watchFix is the delivery timer of mt's fix job key (under mu), as the work job's (watchWork): delivered by the
// worker's job_next, else undelivered. A delivered fix job waits for task_done.
func (a *App) watchFix(p *proj, mt mtask, key event.Key) step {
	if p.folded.delivered[JobID(key)] != 0 {
		return nil
	}
	t := a.timerOf(p, key)
	return a.undelivered(p, mt, t, key, mt.agent, func(ctx context.Context, fx *Loop, retry event.Key) bool {
		return a.sendFix(ctx, p, fx, mt, retry, key)
	})
}

// retryKey is the key of job's retry.
func retryKey(job event.Key) event.Key {
	return event.Key{Task: job.Task, Attempt: job.Attempt, Step: stepRetry + job.Step}
}

// sampleJob takes one sample of the ready integrator's activity (outside mu) for mt's delivered job: quiet with it for
// Idle → one nudge (the quiet time counts again from it), then the owner. The read holds nothing (T-861): it runs on
// most passes while the integrator idles with the job, so a hold of the task across it refused a verb given then
// ("T-1 has review running"); the task is held again only to act, and not when a verb changed it meanwhile (heldAgain).
func (a *App) sampleJob(ctx context.Context, p *proj, fx *Loop, mt mtask, t *jobTimer, pane string) bool {
	a.release(p, mt.task, mt.stepName())
	e := a.evidence(ctx, pane)
	now := a.opt.Now()
	a.mu.Lock()
	_, due := t.sample(now, e, a.opt.Idle)
	a.mu.Unlock()
	nudge := event.Key{Task: mt.task, Attempt: mt.attempt, Step: stepNudge + mt.job}
	switch {
	case !due, !a.heldAgain(p, mt):
	case !p.log.Done(nudge):
		if a.reprompt(ctx, p, fx, mt, nudge, "Reminder: this job is still open for "+mt.task+
			"; answer it with one obor tool (hold, with a why, when the owner must decide).\n") {
			a.mu.Lock()
			t.quietSince = now // the sample's time: the quiet time counts again from before the nudge
			a.mu.Unlock()
		}
	default:
		a.needsYou(p, &mt, causeStalled, fmt.Sprintf("job stalled: %s idle with job %s for %s after a nudge",
			p.integrator, JobID(mt.jobKey()), a.opt.Idle))
	}
	return false
}

// heldAgain holds mt's task again for its sample's act (begin), and only while the job sampled is still out with no
// block (a hold, a resume, a verdict given during the read changed it: nothing to nudge or report).
func (a *App) heldAgain(p *proj, mt mtask) bool {
	if !a.begin(p, mt) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := p.folded.loop[mt.task]
	return m.state == mt.state && m.job == mt.job && m.delivered && !m.needs.held() && !m.needs.closed()
}

// reprompt sends the integrator mt's open job's wake once more under key (a retry or a nudge, P: the same job id),
// lead before it.
func (a *App) reprompt(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key, lead string) bool {
	job := JobID(mt.jobKey())
	return a.send(ctx, p, fx, mt, key, job, promptData{Agent: p.integrator}, lead+wakeText,
		fmt.Sprintf("job %s not sent again to %s", job, p.integrator))
}
