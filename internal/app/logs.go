// logs.go: a task's step logs go when the task ends (T-1098, T-1104).
package app

import (
	"os"
	"path/filepath"
	"strings"

	"obor/internal/project"
)

// endTaskLogs removes task's step logs (.obor/state/logs/<task>-*.log) when the fold has just ended it (mtask.ended):
// merged with its after-merge steps done, or dropped; a rejected task is not ended. was is the task's ended state
// before the fold, so a task still running its after-merge steps keeps its failed step's log and the transition is what
// removes it (T-1104: no per-task marker). The id is the name's prefix, so T-10's logs are never T-100's. A log is for a
// person, never a step: a failed removal is one serve line, and the next start's load tries again. A task the fold does
// not know keeps its logs (nothing says it ended). Under mu, as its callers are.
func (a *App) endTaskLogs(p *proj, task string, was bool) {
	if was || !p.taskEnded(task) {
		return
	}
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
