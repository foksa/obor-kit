// project.go: loading a project, opening its log and folding records into its state.
package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/store"
)

// load is the served project: its config's name and its replayed log (obor serve holds its serve.flock). Replay folds the facts into the active ones
// per task; their timers restart now (docs/state-model.md "Not recorded"). A needs-you replayed without a recorded
// notification is sent once herdr is connected (notify).
func (a *App) load(root string) (*proj, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return nil, err
	}
	name := cfg.Data.Name
	if name == "" {
		name = filepath.Base(root)
	}
	start := time.Now()
	log, recs, err := a.openLog(root)
	if err != nil {
		return nil, err
	}
	rc, _ := cfg.Role("integrator")
	p := &proj{root: root, name: name, log: log, tasks: map[string]*watch{},
		retry: map[event.Key]retry{}, integrator: cfg.AgentName("integrator", ""),
		out:     newOutbox(cfg.AgentName("lead", ""), roleKind(cfg, "lead")),
		merging: a.opt.Loop != nil && rc.Configured, timers: map[string]*jobTimer{}, replies: map[string]string{},
		nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		unwritten: map[event.Key]json.RawMessage{}, exec: map[event.Key]time.Time{}, scratch: make(chan struct{}, 1),
		held: map[string]string{}, ended: map[string]bool{}, roleFail: map[string]time.Time{},
		reg: newRegistry(), folded: newFolded(), wtAgents: wtAgents(cfg)} // wtAgents: the last good set (wtAgentsNow)
	for _, r := range recs {
		p.foldRecord(r)
	}
	p.folded.lr.live = true
	if err := p.syncLanes(); err != nil {
		a.logf("project %s: lanes table: %v", name, err)
	}
	pending := len(log.Pending())
	// What a crash left open (docs/state-model.md "Crash classes"): an ended task's intents are settled as nothing;
	// every other pending intent is the loop's to run again, its effect checking first what happened (a prompt is
	// sent again: prompt.go).
	settled := a.settleEnded(p, "")
	a.logf("project %s (%s): replayed %d records in %.2f s, %d tasks with watchdog facts, %d merge tasks, %d lanes, %d pending intents (%d settled)",
		name, root, len(recs), time.Since(start).Seconds(), len(p.folded.active), len(p.folded.loop), len(p.folded.lr.rows), pending, settled)
	return p, nil
}

// openLog opens root's database as its one writer and replays its event log.
func (a *App) openLog(root string) (*event.Log, []event.Record, error) {
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		return nil, nil, err
	}
	log, recs, err := event.OpenLog(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return log, recs, nil
}

// foldRecord folds one record into p: the watchdog facts, the integrator's jobs and the merge loop's tasks. An ended
// task's intent settled as nothing (settleEnded) has no outcome to fold.
func (p *proj) foldRecord(r event.Record) {
	if notRunResult(r) {
		return
	}
	if r.Kind == event.KindFact {
		p.fold(r)
	}
	p.foldJob(r)
	was := p.closed(r.Task)
	p.foldLoop(r)
	if !was && p.closed(r.Task) || r.Kind == event.KindFact && r.Step == event.StepDropped {
		p.clear(r.Task) // merged, dropped or rejected: its watchdog facts end, an asks too (state model rows 12/16a)
	}
	p.foldLanes(r)
	p.foldStart(r)
	p.foldNightly(r)
	p.foldPause(r)
	p.foldConfig(r)
	p.foldSession(r)
	p.foldMsg(r)
	p.foldWatch(r)
	p.folded.rv.fold(r)
	p.foldReview(r) // before idle: a review request in flight ends the idle epoch (T-818)
	p.foldIdle(r)
	p.foldWake(r)
	p.foldRoles(r)
	if p.folded.lr.live {
		_ = p.syncLanes() // a failed write is tried again (the check's pass logs it)
	}
}

// fold folds a watchdog fact (a merge-loop needs-you is the loop's: foldLoop); one of a closed task is discarded
// (T-636: recorded after its end, as a stop picked before it; a new dispatch reopens the task).
func (p *proj) fold(r event.Record) {
	switch r.Step {
	case event.StepStalled, event.StepNeedsYou:
		if !r.Fact().Merge && !p.closed(r.Task) {
			p.folded.active[r.Task] = append(p.folded.active[r.Task], r)
		}
	case event.StepResumed:
		p.resume(r.Task)
	case event.StepUnblocked:
		if p.asks(r.Task) { // obor resume --regate of a worker's question (T-775): the question ends with it
			p.answered(r.Task)
		}
	}
}

// resume ends task's watchdog facts that its worker's activity ends (a resumed fact's fold): all but a question
// (asks: obor resume, the task's submission or a drop ends it, state model row 12), with the retries of their failed
// notifications.
func (p *proj) resume(task string) {
	delete(p.folded.woke, task)
	var kept []event.Record
	keep := map[event.Key]bool{}
	for _, f := range p.folded.active[task] {
		if isAsk(f) {
			kept = append(kept, f)
			keep[notifyKey(f)] = true
		}
	}
	if len(kept) == 0 {
		p.clear(task)
		return
	}
	p.folded.active[task] = kept
	for k := range p.retry {
		if k.Task == task && !keep[k] {
			delete(p.retry, k)
		}
	}
}

// closed: task is merged, dropped or rejected (its merge loop ended; a new dispatch reopens it).
func (p *proj) closed(task string) bool {
	mt := p.folded.loop[task]
	return mt != nil && (mt.state == stMerged || mt.state == stDropped || mt.state == stRejected)
}

// clear drops task's watchdog facts in force and the retries of their failed notifications.
func (p *proj) clear(task string) {
	delete(p.folded.active, task)
	delete(p.folded.woke, task)
	for k := range p.retry {
		if k.Task == task {
			delete(p.retry, k)
		}
	}
}

// inForce: the fact r is still in force.
func (p *proj) inForce(r event.Record) bool {
	if mt := p.folded.loop[r.Task]; mt != nil && mt.needs.fact.Seq == r.Seq && r.Seq != 0 {
		return true
	}
	for _, f := range p.folded.active[r.Task] {
		if f.Seq == r.Seq {
			return true
		}
	}
	return false
}

// asks: task has a worker's question in force (needs-you{asks: …}).
func (p *proj) asks(task string) bool {
	for _, f := range p.folded.active[task] {
		if isAsk(f) {
			return true
		}
	}
	return false
}

// isAsk: the fact f is a worker's question.
func isAsk(f event.Record) bool {
	return f.Step == event.StepNeedsYou && strings.HasPrefix(f.Fact().Reason, reasonAsks)
}

// watched: task has a watchdog fact in force that its worker's activity ends: any but a question (asks: obor resume
// ends it, T-511).
func (p *proj) watched(task string) bool {
	for _, f := range p.folded.active[task] {
		if !isAsk(f) {
			return true
		}
	}
	return false
}

// has: task has an active fact step{reason}.
func (p *proj) has(task, step, reason string) bool {
	for _, f := range p.folded.active[task] {
		if f.Step == step && f.Fact().Reason == reason {
			return true
		}
	}
	return false
}

// setLanes installs p's lanes as read now. A task a lane took since the last read starts with no timer running; a
// task no lane holds open any more (submitted and in the merge loop, lane freed or moved on) has no watchdog in force
// (nothing recorded).
func (p *proj) setLanes(ls []port.Lane) {
	p.lanes = ls
	open := map[string]port.Lane{}
	for _, l := range ls {
		if l.Task != "" && !p.folded.loop[l.Task].inLoop() {
			open[l.Task] = l
		}
	}
	for task, w := range p.tasks {
		if l, ok := open[task]; !ok || l.Name != w.lane.Name {
			delete(p.tasks, task)
		}
	}
	for task, l := range open {
		if w := p.tasks[task]; w != nil {
			w.lane = l
		} else {
			p.tasks[task] = &watch{lane: l}
		}
	}
	for task := range p.folded.active {
		if _, ok := open[task]; !ok && !projectKey(task) {
			p.clear(task)
		}
	}
}

// projectKey: task is a key of the project's own facts (the merge loop's, the nightly's, the lines', the disk's, the
// review requests', a failed self-restart's), not a task's.
func projectKey(task string) bool {
	return task == event.LoopTask || task == event.NightlyTask || task == event.MsgTask || task == event.DiskTask || task == event.ReviewTask ||
		task == event.ServeTask
}

// roleKind is role's kind as configured: its type's kind, else agents.Default (as launch starts it).
func roleKind(cfg *config.Config, role string) string {
	rc, _ := cfg.Role(role)
	if lt, ok := cfg.Model(rc.Type); ok && lt.Kind != "" {
		return lt.Kind
	}
	return agents.Default
}
