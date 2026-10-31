// settle.go: settling tasks whose agents ended.
package app

import (
	"bytes"
	"encoding/json"
	"slices"
)

// notRun starts the result an ended task's pending intent is settled with: nothing ran under it, nothing will.
const notRun = "not run: "

// notRunResult: r is a result settleEnded wrote.
func notRunResult(r Record) bool {
	return r.Kind == KindResult && bytes.HasPrefix(r.Data, []byte(`"`+notRun))
}

// settleEnded settles as nothing every pending intent of an ended task (task; "": every task), never run again
// (docs/state-model.md "Replay"; T-400): result "not run: <task> <state>", folded as no outcome (foldRecord). The
// loop runs no step of a dropped task and only the after-merge steps of a merged one, so nothing else would close
// them: a superseded attempt's (a redispatch, a later submission) or the ended attempt's own (a prompt refused
// before typing, a start that failed). Kept: a merged attempt's after-merge steps (the loop runs them), the
// notification of any needs-you in force, the merge loop's or a watchdog's (notify shows it; p.needsYou is its
// source, merged or dropped alike), a verdict's (its call id's reply is its caller's), and one whose effect ran with
// its result unwritten (App.do writes that). Called at replay and live once a merged or dropped fact is
// folded (record); under mu when the app runs. It is the number of results written.
func (a *App) settleEnded(p *proj, task string) int {
	inForce := map[Key]bool{} // the notifications notify still owes: the same facts it picks from
	for _, f := range p.needsYou() {
		inForce[notifyKey(f)] = true
	}
	n := 0
	for _, r := range p.log.Pending() {
		mt := p.folded.loop[r.Task]
		k := r.key()
		_, lost := p.unwritten[k]
		switch {
		case task != "" && r.Task != task, mt == nil, lost, inForce[k]:
			continue
		case mt.state == stDropped:
		case mt.state != stMerged:
			continue
		case r.Attempt == mt.attempt && slices.Contains(afterSteps, r.Step):
			continue
		}
		if m, _ := verdictMethod(r.Step); m != "" {
			continue
		}
		res := Record{Task: r.Task, Attempt: r.Attempt, Step: r.Step, Kind: KindResult}
		res.Data, _ = json.Marshal(notRun + r.Task + " " + mt.state)
		res, err := p.log.Append(res, a.opt.Now())
		if err != nil {
			a.logf("FAIL event log of %s: %v", p.name, err)
			return n
		}
		p.foldRecord(res)
		n++
	}
	return n
}
