package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"obor/internal/config"
	"obor/internal/store"
)

// Jobs by fetch (docs/state-model.md rows 5–8 and 16–17). Every agent fetches its
// jobs (T-437): a job's prompt is its wake, one constant line with no content ([obor job <id>] call the obor job_next
// tool), harmless typed twice, lost or eaten by a dialog. The agent fetches the job with obor mcp's job_next tool (a
// socket request here); its first fetch is fact delivered{job, via: mcp}, the job's only delivery. A worker finishes
// only by task_done, its report the argument: fact submitted carries it, and the review job, the merge subject's
// fallback and obor status read it there.

// wakeText is a job's wake for an agent that fetches it; its [obor job <id>] tag goes before it (jobLine).
const wakeText = wire.WakeText

// wakeLine is job's wake as one line with its tag, as send types it: a fresh start's first prompt
// (Launch.Prompt).
func wakeLine(job string) string { return wire.WakeLine(job) }

// viaMCP is fact delivered's Via when the agent's job_next fetched the job.
const viaMCP = "mcp"

// workerJob is mt's open worker job: the attempt's work job (dispatched, or working on it; open from its intent,
// which comes before the start that carries its wake: startJob), else its last fix job once sent; also while a
// needs-you holds one of those. False: none.
func (mt *mtask) workerJob() (event.Key, bool) {
	st := mt.state // its phase, also while a needs-you holds it
	switch {
	case (st == stDispatched || st == stWorking) && mt.work != "":
		return event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}, true
	case st == stWorking && mt.fixed:
		return event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepFix, mt.fixes)}, true
	}
	return event.Key{}, false
}

// integratorJob is mt's open integrator job: the one a verdict answers (openJob), open from its intent, which comes
// before the start that carries its wake (startJob). False: none.
func (mt *mtask) integratorJob() (event.Key, bool) { return mt.jobKey(), mt.openJob() }

// jobNext answers job_next (row 6): the caller's open job, "none", or superseded{current} when the call names an
// older job id. The caller is the job's agent: the integrator for its jobs, the lane's worker for a work or fix job
// (OBOR_LANE, when set, its lane; its pane, when herdr shows it, in the lane). The first fetch of a job is fact
// delivered{job, via: mcp}; a repeat records nothing, and a repeated call id gets the stored reply. Every fetch keeps
// its text in the prompts table: a refetch whose text changed adds a row (T-670).
func (a *App) jobNext(c wire.JobNextReq) (string, error) {
	a.verdicts.Lock() // two fetches never both record a delivery
	defer a.verdicts.Unlock()
	if c.CallID == "" {
		return "", errors.New("job_next: a call id is required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply string
	var stored bool
	if p != nil {
		reply, stored = p.replies[verdictStep(wire.MethodJobNext, c.CallID)]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("job_next: no served project at %s", c.Root)
	case stored:
		return reply, nil
	}
	a.mu.Lock()
	mt, key, ok, err := a.callerJob(p, c)
	delivered := p.folded.delivered[JobID(key)].seq
	a.mu.Unlock()
	if err != nil {
		return "", err
	}
	r := wire.JobReply{State: "none"}
	switch {
	case !ok:
	case c.Job != "" && c.Job != JobID(key):
		r = wire.JobReply{State: "superseded", Current: JobID(key)}
	default:
		text, rev, err := a.fetchText(p, mt, key)
		if err != nil {
			return "", fmt.Errorf("job_next: %s: %v", JobID(key), err)
		}
		r = wire.JobReply{State: "job", Job: JobID(key), Task: key.Task, Attempt: key.Attempt, Step: key.Step, Text: text}
		agent := mt.agent
		if base, _ := event.StepN(key.Step); base == event.StepReview || base == event.StepResolve {
			agent = p.integrator
		}
		// The first fetch records the delivery; every fetch after it records nothing (the job is delivered) but keeps
		// the text: a refetch whose text changed since the newest row (a new spec rev, a restart) gets its own row
		// (T-670), an identical one none.
		seq := delivered
		if seq == 0 {
			var ok bool
			seq, ok = a.recordSeq(p, event.Record{Task: key.Task, Attempt: key.Attempt, Step: event.StepDelivered, Kind: event.KindFact},
				event.FactData{Lane: mt.lane, Machine: Machine, Pane: c.Pane, Agent: agent, Job: JobID(key), Merge: true,
					Via: viaMCP, Call: c.CallID, Rev: rev})
			if !ok {
				return "", fmt.Errorf("job_next: the event log of %s refused it", p.name)
			}
		}
		a.prompt(p, seq, store.PromptJob, agent, JobID(key), key.Task, text, rev)
		a.wake()
	}
	b, _ := json.Marshal(r) // strings and an int: cannot fail
	a.mu.Lock()
	p.replies[verdictStep(wire.MethodJobNext, c.CallID)] = string(b)
	a.mu.Unlock()
	return string(b), nil
}

// callerJob is c's open job in p (under mu): its task and key, false when it has none; an error when the caller may not fetch it.
func (a *App) callerJob(p *proj, c wire.JobNextReq) (mtask, event.Key, bool, error) {
	switch c.Role {
	case "integrator":
		if c.Agent == "" || c.Agent != p.integrator {
			return mtask{}, event.Key{}, false, fmt.Errorf("job_next: only the integrator (%s) fetches its jobs, not %s",
				p.integrator, Dash(c.Agent))
		}
		// The job the call names (its wake's id) when it is open, else the oldest open one; never a held one (a
		// needs-you holds it: the owner's, T-602): a fresh session started for one job gets that job.
		var best *mtask
		var seq int64
		var bestKey event.Key
		for _, t := range p.loopTasks() { // equal seqs (no intent found): the first task by name
			mt := p.folded.loop[t]
			k, ok := mt.integratorJob()
			if !ok || mt.paused() {
				continue
			}
			if c.Job != "" && JobID(k) == c.Job {
				return *mt, k, true, nil
			}
			// The intent's seq, with its result or not: a sent job has one too, so the oldest is the log's order,
			// never the map's (T-606).
			r, _ := p.log.IntentOf(k)
			if best == nil || r.Seq < seq {
				best, bestKey, seq = mt, k, r.Seq
			}
		}
		if best == nil {
			return mtask{}, event.Key{}, false, nil
		}
		return *best, bestKey, true, nil
	case "worker":
		var found *mtask
		var key event.Key
		for _, t := range p.loopTasks() { // two jobs for one lane or agent: the first task by name (T-615)
			mt := p.folded.loop[t]
			k, ok := mt.workerJob()
			if ok && (c.Lane != "" && mt.lane == c.Lane || c.Lane == "" && mt.agent == c.Agent) {
				found, key = mt, k
				break
			}
		}
		if found == nil {
			return mtask{}, event.Key{}, false, nil
		}
		if c.Agent == "" || c.Agent != found.agent && c.Agent != p.workerIn(found.lane) {
			return mtask{}, event.Key{}, false, fmt.Errorf("job_next: not your job: %s is %s's in lane %s, not %s", JobID(key),
				found.agent, found.lane, Dash(c.Agent))
		}
		if o := a.obs[c.Pane]; c.Pane != "" && o != nil && !inDir(o.pane.Cwd, p.laneDir(*found)) {
			return mtask{}, event.Key{}, false, fmt.Errorf("job_next: pane %s is not in lane %s (%s)", c.Pane, found.lane,
				o.pane.Cwd)
		}
		return *found, key, true, nil
	}
	return mtask{}, event.Key{}, false, fmt.Errorf("job_next: no jobs for the role %s", Dash(c.Role))
}

// restartJob answers restart_job (T-959): the job obor restart of c's agent carries as its wake, by callerJob's own
// predicate and order ("" none). A client parsing the status JSON's step strings could only guess: a task's job is
// its worker's or its integrator's, and a held one is no job a restart may wake its agent on.
func (a *App) restartJob(c wire.RestartJobReq) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(c.Root)
	if p == nil {
		return "", fmt.Errorf("restart_job: no served project at %s", c.Root)
	}
	_, key, ok, err := a.callerJob(p, wire.JobNextReq{Envelope: c.Envelope, Lane: c.Lane}) // no job named, no pane
	switch {
	case err != nil:
		return "", err
	case !ok:
		return "", nil
	}
	return JobID(key), nil
}

// fetchText is the text of mt's job key (outside mu): the worker prompt with the spec, the fix job (with the spec when
// its rev changed), the review or resolve job; and the spec rev a worker's job carries (0: none).
func (a *App) fetchText(p *proj, mt mtask, key event.Key) (string, int, error) {
	base, _ := event.StepN(key.Step)
	fx, err := a.loopOf(p)
	if err != nil {
		return "", 0, err
	}
	if base != event.StepWork && base != event.StepFix {
		if base == event.StepReview {
			text, err := a.reviewText(p, mt, fx.Kit)
			return text, 0, err
		}
		text, err := resolveText(p, mt, fx.Kit)
		return text, 0, err
	}
	row, _, err := specOf(p, mt.task)
	if err != nil {
		return "", 0, err
	}
	if base == event.StepFix {
		read, err := fixRead(p, fx.Kit)
		if err != nil {
			return "", 0, err
		}
		return fixText(mt, row, read), row.Rev, nil
	}
	text, err := workText(p, mt, fx.Kit)
	if err != nil {
		return "", 0, err
	}
	text += movedFix(mt)
	if row.Rev > 0 {
		text += specText(row)
	}
	return text, row.Rev, nil
}

// movedFix is the work job's part for an attempt a move started (T-778) while the attempt it moved had a fix
// outstanding (dispatchData.Fix): what to fix, as the fix job said it; "": none.
func movedFix(mt mtask) string {
	if mt.disp.Fix == "" {
		return ""
	}
	return fmt.Sprintf("\n\n%s moved to you (attempt %d) while it was sent back to the previous worker: %s\nFix it "+
		"on the lane's branch and run the tests; when finished, call task_done with your report.", mt.task, mt.attempt,
		mt.disp.Fix)
}

// fixRead is the fix job's line for a worker started fresh (T-603): the work job carried the role texts, so only a
// session that did not get it needs the paths. The project's worker doc only when the main checkout has one.
func fixRead(p *proj, kit string) (string, error) {
	cfg, err := config.Load(p.root)
	if err != nil {
		return "", err
	}
	paths := []string{filepath.Join(kit, "templates", "agents", "worker.md")}
	doc := filepath.Join(cfg.Root, cfg.Data.Paths["agents"], "worker.md")
	if _, err := os.Stat(doc); err == nil {
		paths = append(paths, doc)
	}
	return "If this session did not get the work job (a fresh start), read " + strings.Join(paths, " and ") +
		" first.", nil
}

// workerIn is the worker agent of lane as the lanes were last read (its derived name, T-1031; under mu).
func (p *proj) workerIn(lane string) string {
	for _, l := range p.lanes {
		if l.Name == lane {
			return l.Agent
		}
	}
	return ""
}

// laneDir is mt's lane's worktree: the dispatch's, else the lanes as last read (under mu).
func (p *proj) laneDir(mt mtask) string {
	if mt.disp.Dir != "" {
		return mt.disp.Dir
	}
	for _, l := range p.lanes {
		if l.Name == mt.lane {
			return l.Dir
		}
	}
	return ""
}

// taskDone answers task_done (rows 16–17): the worker's report submits its task: fact submitted{lane, agent, pane,
// call id, report}, then the merge loop gates it. Refused (nothing recorded): not the caller's task, no attempt open
// for it, the report empty or a section missing, or the job fetched at an older spec rev than the store's (T-819: the
// refusal records spec.seen and names the rev and the set's note, so the next task_done submits). A Blocked: report
// submits nothing: it records the watchdog fact needs-you{asks: <what it needs>} (row 16a; the task stays working, the
// owner is notified) and is never refused for the rev. A repeated call id gets the stored reply, or the stored
// refusal of an older rev (T-821).
func (a *App) taskDone(c wire.TaskDoneReq) (string, error) {
	a.hook(hookTaskDoneWaits)
	a.submitMu.Lock() // one submission decision at a time
	defer a.submitMu.Unlock()
	if c.CallID == "" || c.Task == "" && c.Lane == "" {
		return "", errors.New("task_done: a call id and a task or the caller's lane are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	var reply, refusal string
	var stored, refused bool
	if p != nil {
		reply, stored = p.folded.calls[verdictStep(wire.MethodTaskDone, c.CallID)]
		refusal, refused = p.folded.refusals[verdictStep(wire.MethodTaskDone, c.CallID)]
		if c.Task == "" { // obor mcp names the caller's lane (OBOR_LANE): its row's task (T-439)
			c.Task = p.laneTask(c.Lane)
		}
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("task_done: no served project at %s", c.Root)
	case refused: // a stale rev's refusal (T-821): a retry of its lost reply, or a replay, gets it again
		return "", errors.New(refusal)
	case stored:
		return reply, nil
	case c.Task == "":
		return "", fmt.Errorf("task_done: lane %s holds no task in obor serve", c.Lane)
	case !p.merging:
		return "", mergeOff(p)
	case c.Role != "worker":
		return "", fmt.Errorf("task_done: a worker submits its task, not %s (role %s)", Dash(c.Agent), Dash(c.Role))
	}
	if _, err := config.Load(p.root); err != nil { // a project whose config no longer reads takes no submission
		return "", fmt.Errorf("task_done: %v", err)
	}
	report := strings.TrimSpace(c.Report)
	asks, blocked := blockedAsk(report)
	if why := reportRefusal(report, config.ReportSections); why != "" && !blocked {
		return "", fmt.Errorf("task_done: %s", why)
	}
	ctx, cancel := context.WithTimeout(a.life, requestTimeout)
	ls, err := a.lanes.Lanes(ctx, p.root)
	cancel()
	if err != nil {
		return "", fmt.Errorf("task_done: lanes of %s: %v", p.name, err)
	}
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	busy := p.busy(c.Task)
	if !p.ended[c.Task] { // a step holding a working task is the one that sent this job: its task_done is early, not busy
		busy = ""
	}
	dispatching := p.dispatching
	var pane string
	if w, _ := a.agentOf(p, port.Lane{Dir: p.laneDir(mt), Agent: mt.agent}); w.State == Identified {
		pane = w.Pane.ID
	}
	a.mu.Unlock()
	var l port.Lane
	for _, x := range ls {
		if x.Name == mt.lane {
			l = x
		}
	}
	switch {
	case mt.task == "":
		return "", fmt.Errorf("task_done: %s is not open in obor serve", c.Task)
	case c.Agent == "" || c.Agent != mt.agent && c.Agent != l.Agent || c.Lane != "" && c.Lane != mt.lane:
		return "", fmt.Errorf("task_done: not your task (%s is %s's in lane %s, not %s's)", c.Task, mt.agent, mt.lane,
			Dash(c.Agent))
	case !mt.may(vSubmit) || mt.is(stDispatched) && mt.base == "" && dispatching: // no dispatch adapters: no prepare runs
		return "", fmt.Errorf("task_done: %s is %s, not open for a submission", c.Task, mt.shown())
	case busy != "":
		return "", busyErr("task_done", c.Task, busy)
	case l.Name == "":
		return "", fmt.Errorf("task_done: lane %s is not in the lane table", mt.lane)
	case l.Task != c.Task:
		return "", fmt.Errorf("task_done: lane %s holds %s, not %s", l.Name, Dash(l.Task), c.Task)
	case blocked:
		return a.ask(p, c.Task, l, asks)
	}
	// A job carried at an older spec rev than the store's: the lead changed the spec under the worker (T-819). The
	// refusal shows the newer rev (and what the set changed), nothing is submitted, and the fact marks it seen for
	// this attempt, so the worker's next task_done submits. The fact keeps the call id and the refusal (T-821): the
	// same call id, retried after a lost reply or replayed, is refused the same; only a new call submits.
	row, hasSpec, err := specOf(p, c.Task)
	if err != nil {
		return "", fmt.Errorf("task_done: %v", err)
	}
	if hasSpec && mt.specRev > 0 && row.Rev > mt.specRev {
		refusal := "task_done: " + specRevLine(c.Task, row.Rev, row.Note)
		if !a.record(p, event.Record{Task: c.Task, Attempt: mt.attempt, Step: event.StepSpecSeen, Kind: event.KindFact},
			event.FactData{Rev: row.Rev, Call: c.CallID, Reason: refusal}) {
			return "", fmt.Errorf("task_done: the event log of %s refused it", p.name)
		}
		return "", errors.New(refusal)
	}
	a.hook(hookTaskDoneCheck)
	attempt := mt.attempt
	if mt.paused() { // a needs-you's task_done is the next attempt
		attempt++
	}
	if !a.record(p, event.Record{Task: c.Task, Attempt: attempt, Step: event.StepSubmitted, Kind: event.KindFact},
		submitData{Lane: l.Name, Agent: mt.agent, Pane: pane, Call: c.CallID, Report: report}) {
		return "", fmt.Errorf("task_done: the event log of %s refused it", p.name)
	}
	a.wake()
	if ok, state := a.receipt(p, c.Task, wire.MethodTaskDone, c.CallID); !ok { // the fold ignored the submission: the task was dropped or ended after the decision
		return "", fmt.Errorf("task_done: superseded: %s is %s", c.Task, state)
	}
	return taskDoneReply, nil
}

// receipt is whether the fold kept the call's receipt of method for task, and the task's shown state (task_done's and
// land's check after their record: a fold that ignored the submission leaves no receipt, T-938).
func (a *App) receipt(p *proj, task, method, callID string) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := "gone"
	if m := p.folded.loop[task]; m != nil {
		state = m.shown()
	}
	_, ok := p.folded.calls[verdictStep(method, callID)]
	return ok, state
}

// taskDoneReply is task_done's answer once the task is submitted.
const taskDoneReply = "submitted"

// askedReply is task_done's answer to a Blocked: report.
const askedReply = "not submitted: the owner is told what you need; stop and wait for the answer"

// ask records the watchdog fact needs-you{asks: what} for task in lane l (row 16a), once while one is in force: the
// owner is notified. Its worker's activity does not end it (T-511: the Stop hook's turn after the report is no
// answer); obor resume, the task's submission (its lane no longer holds it open) or a drop does.
func (a *App) ask(p *proj, task string, l port.Lane, what string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := verdict{step: event.StepNeedsYou, reason: reasonAsks + what}
	if w, _ := a.agentOf(p, l); w.State == Identified {
		v.pane = w.Pane
	}
	w := p.tasks[task]
	if w == nil {
		w = &watch{lane: l}
		p.tasks[task] = w
	}
	if !p.has(task, event.StepNeedsYou, v.reason) && !a.fact(p, task, w, v) {
		return "", fmt.Errorf("task_done: the event log of %s refused it", p.name)
	}
	a.wake() // the needs rule lines and toasts it at the next pass
	return askedReply, nil
}

// reasonAsks starts the reason of a worker's question (needs-you{asks: …}).
const reasonAsks = "asks: "

// blockedAsk is what a Blocked: report asks for (the rest of its first Blocked: line, else the next non-empty line),
// and whether report has a Blocked: line.
func blockedAsk(report string) (string, bool) {
	lines := strings.Split(report, "\n")
	for i, l := range lines {
		rest, ok := strings.CutPrefix(l, "Blocked:")
		if !ok {
			continue
		}
		for _, n := range append([]string{rest}, lines[i+1:]...) {
			if n = strings.TrimSpace(n); n != "" {
				return n, true
			}
		}
		return "(see its pane)", true
	}
	return "", false
}

// reportRefusal is why report does not submit its task ("": it does): empty, or a section of sections with no heading
// in it.
func reportRefusal(report string, sections []string) string {
	if report == "" {
		return "the report is empty: pass your final report (" + strings.Join(sections, ", ") + ") as report"
	}
	if lack := MissingSections(report, sections); len(lack) > 0 {
		return "the report has no section " + strings.Join(lack, ", ") + " (a Name: label each)"
	}
	return ""
}

// MissingSections is the sections text has no heading for (a Name: label, a ## Name or **Name** heading at the start
// of a line, any case).
func MissingSections(text string, sections []string) []string {
	lines := strings.Split(text, "\n")
	var lack []string
	for _, name := range sections {
		if name != "" && !slices.ContainsFunc(lines, headingRe(name).MatchString) {
			lack = append(lack, name)
		}
	}
	return lack
}

// headingRe is the heading of a report's labelled section name: a Name: label, a ## Name or **Name** heading at the
// start of a line, any case.
func headingRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)` + headingPattern(regexp.QuoteMeta(name)))
}

// headingPattern is headingRe's pattern for name already quoted, or for several names as an alternation.
func headingPattern(q string) string {
	return `^\s*(?:#{1,6}\s+` + q + `|\*\*` + q + `:?\*\*|` + q + `\s*:)`
}

// reportSections are the headings a report's section ends at: the report's own sections (config.ReportSections, the
// worker template's list, one place), and Blocked and Concerns, which a worker writes but a submitted report's section
// list does not name (T-808). Concerns is optional, so the template spells it "Concerns (optional):" too.
var reportSections = slices.Concat(config.ReportSections, []string{"Blocked", "Concerns", "Concerns (optional)"})

// reportHeadingRe is the heading of any of reportSections, in headingRe's forms: a Name: label, a ## Name or **Name**
// heading at the start of a line, any case.
var reportHeadingRe = func() *regexp.Regexp {
	q := make([]string, len(reportSections))
	for i, n := range reportSections {
		q[i] = regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`(?i)` + headingPattern("(?:"+strings.Join(q, "|")+")"))
}()

// reportSection is the body of text's labelled section name: the rest of its heading line, then the lines up to the
// next heading that names one of the report's own sections (reportSections), trimmed. Labels and nested headings inside
// the section stay in it: a Summary's own "Tests:" or "## Notes" is no section (T-808). "" when text has no heading for
// name.
func reportSection(text, name string) string {
	lines := strings.Split(text, "\n")
	re := headingRe(name)
	start := slices.IndexFunc(lines, re.MatchString)
	if start < 0 {
		return ""
	}
	loc := re.FindStringIndex(lines[start])
	body := []string{strings.TrimSpace(strings.TrimLeft(lines[start][loc[1]:], ":*# "))}
	for _, l := range lines[start+1:] {
		if reportHeadingRe.MatchString(l) {
			break
		}
		body = append(body, l)
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}

// noOpenQuestionRe is an Open questions section that leaves nothing open: its first word is none or n/a (whatever
// follows: "none new.", "none (the lead decided X).", "None — answered"), a lone - (a "- item" is a bullet list), or
// exactly no followed by nothing or punctuation ("No."; "no idea whether X" stays a question). Case-insensitive; a
// longer word ("Nonetheless") is no match.
var noOpenQuestionRe = regexp.MustCompile(`(?i)^(?:(?:none|n/a)(?:$|[^\p{L}\p{N}])|-$|no(?:$|\s*[^\p{L}\p{N}\s]))`)

// openQuestions is report's Open questions: section when it leaves something open — anything not starting with the
// word none, n/a, - or no (see noOpenQuestionRe), verbatim; "" when it leaves nothing open (T-616, T-649).
func openQuestions(report string) string {
	q := reportSection(report, "Open questions")
	if q == "" || noOpenQuestionRe.MatchString(q) {
		return ""
	}
	return q
}

// clip is s cut to n bytes at a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:max(n, 0)]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
