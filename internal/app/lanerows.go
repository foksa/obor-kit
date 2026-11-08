package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"slices"

	"obor/internal/config"
	"obor/internal/store"
)

// The lane projection (T-439): each lane's row in the store's lanes table, derived from the log alone. A successful
// lane.prepare puts its task on the lane (and off any other lane), as does a submission from a lane whose row does not
// hold its task (a task dispatched before serve dispatched: no branch or base known), a dropped fact takes it off; finished is when the
// attempt's task_done was recorded. serve is the only writer: it writes the table after the load's replay and after
// every record that changes a row (syncLanes), so hooks and CLIs read one row instead of folding the log. The table
// holds configured lanes only (T-865): a row comes from a dispatch, and the loop dispatches to a configured lane.

// laneRows are p's projected rows (under mu).
type laneRows struct {
	rows      map[string]store.LaneRow // by lane, Finished not yet set (rows)
	submitted map[string]int64         // by task: when its last submitted fact was recorded (unix seconds)
	written   []store.LaneRow          // the table as last written (nil and !synced: not yet)
	synced    bool
	err       error // the last write's error (nil: the table holds written)
	live      bool  // the load's replay is done: each fold writes the change
}

// foldLanes folds r into the projection (after foldLoop folded it).
func (p *proj) foldLanes(r event.Record) {
	if p.folded.lr.rows == nil {
		p.folded.lr.rows, p.folded.lr.submitted = map[string]store.LaneRow{}, map[string]int64{}
	}
	switch {
	case r.Kind == event.KindFact && r.Step == event.StepDropped:
		for lane, row := range p.folded.lr.rows {
			if row.Task == r.Task {
				delete(p.folded.lr.rows, lane)
			}
		}
	case r.Kind == event.KindFact && r.Step == event.StepSubmitted:
		p.folded.lr.submitted[r.Task] = event.RecordTime(r).Unix()
		var d submitData
		_ = json.Unmarshal(r.Data, &d)
		if d.Lane == "" || p.folded.lr.rows[d.Lane].Task == r.Task { // an ended task's submission never folds (foldRecord)
			return
		}
		// A submission from a lane no row gives the task (dispatched before serve dispatched): the task is there.
		for lane, row := range p.folded.lr.rows {
			if row.Task == r.Task {
				delete(p.folded.lr.rows, lane)
			}
		}
		p.folded.lr.rows[d.Lane] = store.LaneRow{Lane: d.Lane, Task: r.Task, Agent: d.Agent}
	case r.Kind == event.KindResult:
		step, _ := event.StepN(r.Step)
		mt := p.folded.loop[r.Task]
		if step != event.StepPrepare || mt == nil || mt.attempt != r.Attempt || !mt.is(stDispatched) && !mt.is(stDriving) {
			return
		}
		var d prepareOut
		_ = json.Unmarshal(r.Data, &d)
		if d.Base == "" || d.Refused != "" {
			return
		}
		for lane, row := range p.folded.lr.rows {
			if row.Task == r.Task {
				delete(p.folded.lr.rows, lane) // a task is in one lane
			}
		}
		p.folded.lr.rows[mt.disp.Lane] = store.LaneRow{Lane: mt.disp.Lane, Task: r.Task, Branch: mt.disp.Branch,
			Agent: mt.disp.Agent, Base: d.Base, Head: d.Head, DispatchedAt: mt.at.Unix(), Type: mt.disp.Type}
	}
}

// dropLaneRows deletes the rows of lanes the config does not name (T-865): the load's replay rebuilds them from old
// dispatch records (a lane T-765 removed from the config), and they would be the table's only rows without a type.
func (p *proj) dropLaneRows(cfg *config.Config) {
	lanes := make(map[string]bool, len(cfg.Lanes()))
	for _, l := range cfg.Lanes() {
		lanes[l.Name] = true
	}
	for lane := range p.folded.lr.rows {
		if !lanes[lane] {
			delete(p.folded.lr.rows, lane)
		}
	}
}

// laneTable is p's rows by lane, finished set: 0 while the task awaits its task_done, else when its last submission
// was recorded (the dispatch's time when it ended without one: a merge outside, a reject). Blocked is the fold's
// question in force (asks): the worker's Blocked: report, ended by the facts this package's fold ends it on (T-894: the
// Stop hook reads it instead of folding the log itself).
func (p *proj) laneTable() []store.LaneRow {
	out := make([]store.LaneRow, 0, len(p.folded.lr.rows))
	for _, row := range p.folded.lr.rows {
		if mt := p.folded.loop[row.Task]; mt != nil && !mt.awaitsTaskDone() && !mt.is(stDriving) { // a drive works on (T-751)
			row.Finished = p.folded.lr.submitted[row.Task]
			if row.Finished == 0 {
				row.Finished = max(row.DispatchedAt, 1)
			}
		}
		row.Blocked = p.asks(row.Task)
		row.Landed = p.landedTask(row.Task)
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b store.LaneRow) int {
		if a.Lane < b.Lane {
			return -1
		}
		if a.Lane > b.Lane {
			return 1
		}
		return 0
	})
	return out
}

// landedTask: the fold ended task on its target or off it (T-1012), merged (by the gate, or by hand: drive's
// merge-evidence read records the merged fact) or dropped, whatever a needs-you still holds; the lane row's landed
// fact, which port.FreeState reads. A rejected task has not landed: its work is still only in its lane.
func (p *proj) landedTask(task string) bool {
	mt := p.folded.loop[task]
	return mt != nil && (mt.state == stMerged || mt.state == stDropped)
}

// syncLanes writes p's rows when they differ from the table as last written (under mu); a failed write is kept in
// p.lr.err and tried again on the next change or check.
func (p *proj) syncLanes() error {
	if p.log == nil {
		return nil
	}
	rows := p.laneTable()
	if p.folded.lr.synced && p.folded.lr.err == nil && slices.Equal(rows, p.folded.lr.written) {
		return nil
	}
	if p.folded.lr.err = store.WriteLanes(p.log.DB(), rows); p.folded.lr.err != nil {
		return p.folded.lr.err
	}
	p.folded.lr.written, p.folded.lr.synced = rows, true
	return nil
}

// laneRowsErr is the error of the lanes table's last write, after one more try (under mu): a step whose record
// changed a row does not go on (a worker is not started on a missing or stale row) until the table is written.
func (p *proj) laneRowsErr() error {
	if p.folded.lr.err == nil {
		return nil
	}
	return p.syncLanes()
}

// laneTask is the task lane's row holds ("" none; under mu): obor mcp's task_done names only the caller's lane.
func (p *proj) laneTask(lane string) string { return p.folded.lr.rows[lane].Task }

// finishedIn: task's lane row reads it finished (laneTable, under mu): its task_done submitted it, or it ended without
// one and its row stays. A dispatch redoes no finished task in its lane (T-788: the lane table's done state before).
func (p *proj) finishedIn(task string) bool {
	for _, row := range p.laneTable() {
		if row.Task == task && row.Finished != 0 {
			return true
		}
	}
	return false
}
