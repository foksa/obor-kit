// project.go: loading a project, opening its log and folding records into its state.
package app

import (
	"encoding/json"
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
	log, recs, err := a.openLog(root)
	if err != nil {
		return nil, err
	}
	rc, _ := cfg.Role("integrator")
	p := &proj{root: root, name: name, log: log, active: map[string][]Record{}, woke: map[string]time.Time{}, tasks: map[string]*watch{},
		retry: map[Key]retry{}, integrator: cfg.AgentName("integrator", ""), lead: cfg.AgentName("lead", ""), leadKind: roleKind(cfg, "lead"),
		msgs: map[int64]*queued{}, lined: map[int64]bool{}, lining: map[int64]bool{}, briefs: map[string]*brief{}, watches: map[string]*mergeWatch{},
		calls: map[string]string{}, merging: a.opt.Loop != nil && rc.Configured,
		loop: map[string]*mtask{}, timers: map[string]*jobTimer{},
		nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		unwritten: map[Key]json.RawMessage{}, scratch: make(chan struct{}, 1), held: map[string]string{}, ended: map[string]bool{},
		delivered: map[string]bool{}, reg: newRegistry(),
		lr: laneRows{rows: map[string]store.LaneRow{}, submitted: map[string]int64{}}}
	for _, r := range recs {
		p.foldRecord(r)
	}
	p.lr.live = true
	if err := p.syncLanes(); err != nil {
		a.logf("project %s: lanes table: %v", name, err)
	}
	pending := len(log.Pending())
	// What a crash left open (docs/state-model.md "Crash classes"): an ended task's intents are settled as nothing;
	// every other pending intent is the loop's to run again, its effect checking first what happened (a prompt is
	// sent again: prompt.go).
	settled := a.settleEnded(p, "")
	a.logf("project %s (%s): %d records replayed, %d tasks with watchdog facts, %d merge tasks, %d lanes, %d pending intents (%d settled)",
		name, root, len(recs), len(p.active), len(p.loop), len(p.lr.rows), pending, settled)
	return p, nil
}

// openLog opens root's database as its one writer and replays its event log.
func (a *App) openLog(root string) (*Log, []Record, error) {
	db, err := store.OpenRW(DBFile(root))
	if err != nil {
		return nil, nil, err
	}
	log, recs, err := OpenLog(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return log, recs, nil
}

// foldRecord folds one record into p: the watchdog facts, the integrator's jobs and the merge loop's tasks. An ended
// task's intent settled as nothing (settleEnded) has no outcome to fold.
func (p *proj) foldRecord(r Record) {
	if notRunResult(r) {
		return
	}
	if r.Kind == KindFact {
		p.fold(r)
	}
	p.foldJob(r)
	p.foldLoop(r)
	p.foldLanes(r)
	p.foldStart(r)
	p.foldNightly(r)
	p.foldPause(r)
	p.foldSession(r)
	p.foldMsg(r)
	p.foldWatch(r)
	p.foldWake(r)
	if p.lr.live {
		_ = p.syncLanes() // a failed write is tried again (the check's pass logs it)
	}
}

// fold folds a watchdog fact (a merge-loop needs-you is the loop's: foldLoop).
func (p *proj) fold(r Record) {
	switch r.Step {
	case StepStalled, StepNeedsYou:
		if !r.Fact().Merge {
			p.active[r.Task] = append(p.active[r.Task], r)
		}
	case StepResumed:
		p.resume(r.Task)
	}
}

// resume ends task's watchdog facts that its worker's activity ends (a resumed fact's fold): all but a question
// (asks: obor answer, the task's submission or a drop ends it, state model row 12), with the retries of their failed
// notifications.
func (p *proj) resume(task string) {
	delete(p.woke, task)
	var kept []Record
	keep := map[Key]bool{}
	for _, f := range p.active[task] {
		if isAsk(f) {
			kept = append(kept, f)
			keep[notifyKey(f)] = true
		}
	}
	if len(kept) == 0 {
		p.clear(task)
		return
	}
	p.active[task] = kept
	for k := range p.retry {
		if k.Task == task && !keep[k] {
			delete(p.retry, k)
		}
	}
}

// clear drops task's watchdog facts in force and the retries of their failed notifications.
func (p *proj) clear(task string) {
	delete(p.active, task)
	delete(p.woke, task)
	for k := range p.retry {
		if k.Task == task {
			delete(p.retry, k)
		}
	}
}

// inForce: the fact r is still in force.
func (p *proj) inForce(r Record) bool {
	if mt := p.loop[r.Task]; mt != nil && mt.needs.Seq == r.Seq && r.Seq != 0 {
		return true
	}
	for _, f := range p.active[r.Task] {
		if f.Seq == r.Seq {
			return true
		}
	}
	return false
}

// asks: task has a worker's question in force (needs-you{asks: …}).
func (p *proj) asks(task string) bool {
	for _, f := range p.active[task] {
		if isAsk(f) {
			return true
		}
	}
	return false
}

// isAsk: the fact f is a worker's question.
func isAsk(f Record) bool {
	return f.Step == StepNeedsYou && strings.HasPrefix(f.Fact().Reason, reasonAsks)
}

// watched: task has a watchdog fact in force that its worker's activity ends: any but a question (asks: obor answer
// ends it, T-511).
func (p *proj) watched(task string) bool {
	for _, f := range p.active[task] {
		if !isAsk(f) {
			return true
		}
	}
	return false
}

// has: task has an active fact step{reason}.
func (p *proj) has(task, step, reason string) bool {
	for _, f := range p.active[task] {
		if f.Step == step && f.Fact().Reason == reason {
			return true
		}
	}
	return false
}

// setLanes installs p's lanes as read now. A task a lane took since the last read starts with no timer running; a
// task no lane holds open any more (submitted and in the merge loop, lane freed or moved on) has no watchdog in force
// (nothing recorded).
func (p *proj) setLanes(ls []Lane) {
	p.lanes = ls
	open := map[string]Lane{}
	for _, l := range ls {
		if l.Task != "" && !p.loop[l.Task].inLoop() {
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
	for task := range p.active {
		if _, ok := open[task]; !ok && task != LoopTask && task != NightlyTask && task != MsgTask && task != DiskTask {
			p.clear(task)
		}
	}
}

// roleKind is role's kind as configured: its type's kind, else agents.Default (as launch starts it).
func roleKind(cfg *config.Config, role string) string {
	rc, _ := cfg.Role(role)
	if lt, ok := cfg.LaneType(rc.Type); ok && lt.Kind != "" {
		return lt.Kind
	}
	return agents.Default
}
