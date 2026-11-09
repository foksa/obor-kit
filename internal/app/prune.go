// prune.go: the nightly prune of the event log (T-1089, docs/research/event-log-pruning.md §3): serve's backup request
// deletes the records of the tasks that ended eventKeep ago, all but each one's last terminal fact, and the session
// facts as old that are no agent's current session. Serve's memory is not touched: the next start folds the smaller
// table. Package event owns the task rows' SQL, store the session rows'; this file owns which tasks.
package app

import (
	"database/sql"
	"maps"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/store"
	"slices"
	"strings"
	"time"
)

// eventKeep is how long the log keeps an ended task's records after its end, and an old session's (T-1089): longer
// than promptKeep, so a prompt row never names a deleted fact.
const eventKeep = 30 * 24 * time.Hour

// pruneBatch is how many tasks one transaction of the prune deletes: a kill between batches leaves fewer tasks pruned,
// and the next night goes on.
const pruneBatch = 500

// pruneLog prunes p's log at now (serve's nightly backup, after the backup and prunePrompts): one line when it deleted
// something, a FAIL line per part that failed. The backup stands either way.
func (a *App) pruneLog(p *proj, now time.Time) { a.pruneLogIn(p, now, pruneBatch) }

// pruneLogIn is pruneLog with at most batch tasks per transaction. The tasks are chosen under mu, at the log's last
// seq (the horizon: nothing at or above it is deleted); the per-task check and the deletes run outside it.
func (a *App) pruneLogIn(p *proj, now time.Time, batch int) {
	cutoff := now.Add(-eventKeep)
	a.mu.Lock()
	horizon := p.log.LastSeq()
	cands := p.pruneCandidates(cutoff)
	a.mu.Unlock()
	a.hook(hookPruneSelected)
	db := p.log.DB()
	keep, whole, err := pruneCheck(db, cands, horizon)
	if err == nil {
		var starts []string
		starts, err = startsNeed(db, keep)
		for _, task := range starts {
			delete(keep, task)
		}
		whole = append(whole, starts...)
		slices.Sort(whole)
	}
	if err != nil {
		a.logf("FAIL prune of %s: %v", p.name, err)
		keep = nil
	}
	tasks := make([]string, 0, len(keep))
	for task := range keep {
		tasks = append(tasks, task)
	}
	slices.Sort(tasks)
	var rows int64
	pruned := 0
	for chunk := range slices.Chunk(tasks, max(batch, 1)) {
		part := make(map[string]int64, len(chunk))
		for _, task := range chunk {
			part[task] = keep[task]
		}
		n, err := event.PruneTasks(db, horizon, part)
		if err != nil {
			a.logf("FAIL prune of %s: %v", p.name, err)
			break
		}
		rows, pruned = rows+n, pruned+len(chunk)
	}
	sessions, err := store.PruneSessions(db, cutoff, horizon)
	if err != nil {
		a.logf("FAIL prune of %s: %v", p.name, err)
	}
	if rows > 0 || sessions > 0 {
		a.logf("%s pruned %d records of %d tasks ended over %s ago (%d kept whole: %v) and %d session records",
			p.name, rows, pruned, eventKeep, len(whole), whole, sessions)
	}
}

// pruneCandidates is p's tasks the prune may delete at cutoff (under mu), each as the fold holds it now: dropped, or
// merged with its after-merge steps done, no needs-you held or due, its end before cutoff; no lanes row names it, no
// agent's start row was recorded by it, its record is not the idle key (idleEnd, T-1101), idle or not now (T-1102), no step of it runs or drop
// of it is decided, and with a review open it was not dispatched after the review started (fixCandidates would link
// it). A rejected task is not ended for this: the owner
// still owes it a drop.
func (p *proj) pruneCandidates(cutoff time.Time) map[string]mtask {
	pinned := map[string]bool{}
	for _, row := range p.folded.lr.rows {
		pinned[row.Task] = true
	}
	for _, agent := range p.reg.StartedAgents() {
		if row, _ := p.reg.Start(agent); row.Task != "" {
			pinned[row.Task] = true
		}
	}
	// The task whose record is the idle key (T-1101), idle or not now: a needs-you that suppresses idle keeps its end
	// (T-1102); a retained newer end supersedes it.
	if _, task := p.idleEnd(); task != "" {
		pinned[task] = true
	}
	review := int64(-1) // the open review's start seq (-1: none open)
	if last := p.folded.rv.last(); last.open() != "" {
		review = last.seq
	}
	out := map[string]mtask{}
	for task, mt := range p.folded.loop {
		switch {
		case mt.state != stDropped && (mt.state != stMerged || mt.after < len(afterSteps)):
		case mt.needs.due() || mt.needs.held():
		case mt.endedAt.IsZero() || !mt.endedAt.Before(cutoff):
		case pinned[task] || p.held[task] != "" || p.ended[task]:
		case review >= 0 && mt.dispSeq > review:
		default:
			out[task] = *mt
		}
	}
	return out
}

// pruneCheck is the candidates whose records may go, each with the seq of the one record that stays (its last
// terminal fact below horizon), and those kept whole, sorted: a task is pruned only when folding that fact alone gives
// the task the fold holds (endsAlike) and it has no merged fact but that one (T-789: merged, dispatched again,
// dropped; its merge would leave the reviews' merge list).
func pruneCheck(db *sql.DB, cands map[string]mtask, horizon int64) (map[string]int64, []string, error) {
	keep := map[string]int64{}
	if len(cands) == 0 {
		return keep, nil, nil
	}
	var facts []event.Record
	for _, step := range []string{event.StepMerged, event.StepDropped} {
		recs, err := event.Facts(db, step)
		if err != nil {
			return keep, nil, err
		}
		facts = append(facts, recs...)
	}
	last := map[string]event.Record{} // each candidate's last terminal fact below horizon
	merges := map[string]int{}        // and how many merged facts it has
	for _, r := range facts {
		if _, ok := cands[r.Task]; !ok || r.Seq >= horizon {
			continue
		}
		if r.Step == event.StepMerged {
			merges[r.Task]++
		}
		if r.Seq > last[r.Task].Seq {
			last[r.Task] = r
		}
	}
	var whole []string
	for task, want := range cands {
		r, ok := last[task]
		n := merges[task]
		if !ok || n > 1 || n == 1 && r.Step != event.StepMerged || !endsAlike(r, want) {
			whole = append(whole, task)
			continue
		}
		keep[task] = r.Seq
	}
	slices.Sort(whole)
	return keep, whole, nil
}

// endsAlike: folding r alone gives a task equal to want on what its outcome's readers ask of it (state, attempt,
// head, the merged and dropped facts' seqs, the drop's rev and reason, the end's time; a merged task's after-merge
// steps).
func endsAlike(r event.Record, want mtask) bool {
	q := &proj{folded: newFolded()}
	q.foldLoop(r)
	got := q.folded.loop[r.Task]
	if got == nil {
		return false
	}
	same := got.state == want.state && got.attempt == want.attempt && got.head == want.head &&
		got.mergedSeq == want.mergedSeq && got.droppedSeq == want.droppedSeq && got.droppedRev == want.droppedRev &&
		got.dropped == want.dropped && got.endedAt.Equal(want.endedAt)
	if want.state == stMerged {
		same = same && got.after == want.after
	}
	return same
}

// startsNeed is the tasks of keep whose start results the agents' start rows need (sorted): folding every start result
// but those of the tasks pruned must give the rows folding all of them gives. A pruned start that displaced another
// agent's row on its pane would bring that row back (axis-alies' oracle run), one that was its agent's current row
// would take it away. The tasks with start results are tried together, then by halves, so a store whose prune changes
// no row folds the start results twice.
func startsNeed(db *sql.DB, keep map[string]int64) ([]string, error) {
	recs, err := event.ResultsUnder(db, event.StepStartWork, strings.TrimSuffix(stepStart, "."))
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, r := range recs {
		if _, ok := keep[r.Task]; ok {
			has[r.Task] = true
		}
	}
	tasks := slices.Sorted(maps.Keys(has))
	full := startRows(recs, nil)
	pruned := map[string]bool{}
	var need []string
	var try func(part []string)
	try = func(part []string) {
		if len(part) == 0 {
			return
		}
		trial := maps.Clone(pruned)
		for _, task := range part {
			trial[task] = true
		}
		switch {
		case maps.Equal(startRows(recs, trial), full):
			pruned = trial
		case len(part) == 1:
			need = append(need, part[0])
		default:
			try(part[:len(part)/2])
			try(part[len(part)/2:])
		}
	}
	try(tasks)
	return need, nil
}

// startRows is the agents' start rows that folding recs, start results, gives without those of the tasks in skip
// (foldStart on an empty project: no model ref, no verdict count, as both sides of startsNeed's comparison fold them).
func startRows(recs []event.Record, skip map[string]bool) map[string]identity.StartRow {
	q := &proj{reg: identity.New(), folded: newFolded()}
	for _, r := range recs {
		if !skip[r.Task] {
			q.foldStart(r)
		}
	}
	out := map[string]identity.StartRow{}
	for _, agent := range q.reg.StartedAgents() {
		out[agent], _ = q.reg.Start(agent)
	}
	return out
}
