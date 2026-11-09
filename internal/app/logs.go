// logs.go: a task's step logs go when the task ends (T-1098).
package app

import (
	"os"
	"path/filepath"
	"strings"

	"obor/internal/project"
)

// endTaskLogs removes task's step logs (.obor/state/logs/<task>-*.log) once the fold has it ended (mtask.ended): merged
// with its after-merge steps done, or dropped; a rejected task is not ended. The id is the name's prefix, so T-10's
// logs are never T-100's. A log is for a person, never a step: a failed removal is one serve line, and the log stays
// for the night's sweep (project.Sweep). Once per end (the marker is the end's record), so a task dispatched again and
// ended again has its new logs removed too. An end a crash cut or serve was down for is the sweep's (T-1098). Under
// mu, as its callers are.
func (a *App) endTaskLogs(p *proj, task string) {
	mt := p.folded.loop[task]
	if mt == nil || !mt.ended() {
		return
	}
	end := mt.mergedSeq
	if mt.state == stDropped {
		end = mt.droppedSeq
	}
	if prev, seen := p.logsGone[task]; seen && prev == end {
		return
	}
	if p.logsGone == nil {
		p.logsGone = map[string]int64{}
	}
	p.logsGone[task] = end
	dir := filepath.Join(project.StateDir(p.root), "logs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logf("%s: logs of %s: %v", p.name, task, err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, task+"-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			a.logf("%s: logs of %s: %v", p.name, task, err)
		}
	}
}
