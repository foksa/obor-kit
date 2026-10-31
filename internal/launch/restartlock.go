package launch

import (
	"path/filepath"

	"obor/internal/project"
)

// RestartLock is the per-lane restart lock, <state>/restart-<lane>.lock (lane "": the agent's): held while obor stops
// and starts the lane's agent, so a second restart of the lane waits for the first.
func RestartLock(root, lane, agent string) *project.Lock {
	if lane == "" {
		lane = agent
	}
	return project.NewLock(filepath.Join(project.StateDir(root), "restart-"+project.SafeName(lane)+".lock"))
}
