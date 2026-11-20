package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"obor/internal/project"
)

// The start record: each start obor makes (done, the one success path) records the pane it started the agent in and
// that agent's pid (the leader of the pane's foreground group right after the start), one file per agent,
// <state>/started/<agent>: "<pane> <pid>". The restore reads it to tell an agent obor started from one the terminal
// started on its own (herdr's resume after its restart: a new process, so another pid).

// StartedFile is agent's start record in the project at root.
func StartedFile(root, agent string) string {
	return filepath.Join(project.StateDir(root), "started", agent)
}

// RecordStart writes agent's start record: obor started it in pane as process pid.
func RecordStart(root, agent, pane string, pid int) error {
	path := StartedFile(root, agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return project.WriteAtomic(path, []byte(pane+" "+strconv.Itoa(pid)+"\n"))
}

// startRecord is agent's start record: the pane and pid of obor's last start of it (ok false: none, or unreadable).
func startRecord(root, agent string) (pane string, pid int, ok bool) {
	b, err := os.ReadFile(StartedFile(root, agent))
	if err != nil {
		return "", 0, false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return "", 0, false
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return f[0], n, true
}

// recordStart records s's start: the agent that leads pane's foreground group now. A pane at its shell, or one the
// terminal gives no process info for, records nothing (said); a write that fails is said.
func (l *Launcher) recordStart(ctx context.Context, s Start, pane string) {
	if l.Cfg == nil {
		return
	}
	pi, err := l.H.ProcessInfo(ctx, pane)
	switch {
	case err != nil:
		err = fmt.Errorf("no process info: %w", err)
	case pi.FgPGID == 0 || pi.AtShell():
		err = errors.New("no agent in the foreground")
	default:
		err = RecordStart(l.Cfg.Root, s.Agent, pane, pi.FgPGID)
	}
	if err != nil {
		l.say("warning: the start of %s in pane %s not recorded: %v (a restore after a terminal restart may restart it)",
			s.Agent, pane, err)
	}
}
