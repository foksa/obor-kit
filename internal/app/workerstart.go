// workerstart.go: serve starts a lane's worker again while its task's work is outstanding and the worker runs no more
// (T-874; a rule on the rule pass, rules.go): obor stop --force stopped it and obor start started only serve, or it
// exited by itself.
package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/config"
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
