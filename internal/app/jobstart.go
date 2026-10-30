// jobstart.go: starting a job for a task, its prompt text and the integrator pane.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// job sends the integrator a review or resolve job (rows 25, 36): started first when the snapshot shows it does not
// run, through the one start sequence (startJob: the start runs under the job's intent and carries its wake). The
// start's key is start.<job step>, start.<job step>.2 … once a start of that job is done and the integrator is gone
// again; a start a crash or a failure left open is run with its recorded data (it no-ops when the integrator runs:
// "running"). An integrator that runs, or a start that did not carry the wake, gets the job's wake typed (send); a job
// whose prompt failed is sent again under its key.
func (a *App) job(ctx context.Context, p *proj, fx *Loop, mt mtask, jobStep string) bool {
	key := Key{mt.task, mt.attempt, jobStep}
	a.mu.Lock()
	w := a.whereIs(p, p.integrator)
	a.mu.Unlock()
	start := Key{mt.task, mt.attempt, stepStart + jobStep}
	for n := 2; p.log.Done(start); n++ {
		start.Step = nth(stepStart+jobStep, n)
	}
	if w.State == Ambiguous && !p.log.Open(start) { // no third pane started, nothing sent
		return a.heldJob(ctx, p, fx, mt, key, w)
	}
	data := promptData{Agent: p.integrator, Patch: mt.cand.Patch}
	if w.State != Identified || p.log.Open(start) {
		d := startData{Agent: p.integrator}
		if r, ok := p.log.Intent(start); ok { // a pending start: its recorded data
			d = startData{}
			_ = json.Unmarshal(r.Data, &d)
		}
		if !a.startJob(ctx, p, mt, Intent{Key: start, Class: ClassStart, Data: d}, key, data,
			func(ctx context.Context, first string) (any, error) {
				s, err := a.startIntegrator(ctx, fx.Agents, p.root, first)
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
func (a *App) startJob(ctx context.Context, p *proj, mt mtask, in Intent, job Key, data promptData,
	launch func(ctx context.Context, first string) (any, error)) bool {
	data.Job = JobID(job)
	at := a.opt.Now()
	var startErr error
	_, err := a.do(ctx, p, Intent{Key: job, Class: ClassPrompt, Data: data}, func(ctx context.Context) (any, error) {
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
		return "sent (start prompt)", nil
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
		"Candidate: %s, the target %s plus the lane, pinned at refs/obor/candidate/%s; read it with "+
		"git diff %s %s.\n%s\nPatch hash: %s\nThe worker's report:\n%s\n"+
		"%sAnswer with one obor tool: accept (patch %s, a note optional) | sendback (why: what to fix) | reject (why).",
		mt.task, mt.attempt, mt.lane, c.Checks, spec, c.SHA, c.Target, mt.task,
		c.Target, c.SHA, c.Stat, c.Patch, mt.report, decisionText(mt), c.Patch), nil
}

// decisionText is the line of the lead's decision on mt's last obor resume of the attempt ("": none), for the
// integrator's next job (T-511).
func decisionText(mt mtask) string {
	if mt.decision == "" {
		return ""
	}
	return mt.decision + "\n"
}

// resolveText is the resolve job (row 25): the conflict and where it is open.
func resolveText(mt mtask) string {
	line, _, _ := strings.Cut(mt.line, "(resolve there")
	return fmt.Sprintf("Resolve %s's merge for obor serve (attempt %d, lane %s): %s\n%s"+
		"Resolve the conflict in that worktree and commit the merge there (git commit -m \"Merge %s: <one line>\"), "+
		"then call the obor resolved tool. Cannot: git merge --abort there, then the obor reject tool (why).",
		mt.task, mt.attempt, mt.lane, strings.TrimSpace(line), decisionText(mt), mt.task)
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
