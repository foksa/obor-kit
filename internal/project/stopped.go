package project

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A stopped project (T-709): `obor stop` put it to sleep (no serve, no agents, not in the machine's project list) and
// `obor start` wakes it. The marker is <root>/.obor/state/stopped; while it is there every obor hook and the git hooks
// stand down, serve and the herdr plugin's restore leave the project alone, and obor init says to run obor start.
// obor start moves the marker to <root>/.obor/state/starting first (serve may start then) and removes that once obor
// stop's pause is ended (T-711): an obor start that failed before that finds it on its rerun.

// StoppedLine is the line obor whoami, obor status and obor web show for a stopped project.
const StoppedLine = "project stopped: obor start"

// StoppedFile is root's marker.
func StoppedFile(root string) string { return filepath.Join(StateDir(root), "stopped") }

// StartingFile is root's marker while obor start runs (moved from StoppedFile, removed once start is done).
func StartingFile(root string) string { return filepath.Join(StateDir(root), "starting") }

// Stopped: root (the main checkout) holds the marker.
func Stopped(root string) bool {
	_, err := os.Stat(StoppedFile(root))
	return err == nil
}

// StopMarker is the marker's content: when and why the project stopped, and the pause in force that obor stop
// replaced with its own (T-711; PauseWhy "": none; PauseUntil 0: until cleared). obor start restores that pause while
// it has not expired.
type StopMarker struct {
	At         time.Time
	Why        string
	PauseWhy   string
	PauseUntil int64
}

// WriteStopMarker writes the marker: one "key: value" line each (at, why; pause and pause_until when a pause was
// replaced).
func WriteStopMarker(root string, m StopMarker) error {
	path := StoppedFile(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	oneLine := strings.NewReplacer("\r", " ", "\n", " ")
	b := fmt.Appendf(nil, "at: %s\nwhy: %s\n", m.At.UTC().Format(time.RFC3339), oneLine.Replace(m.Why))
	if m.PauseWhy != "" {
		b = fmt.Appendf(b, "pause: %s\npause_until: %d\n", oneLine.Replace(m.PauseWhy), m.PauseUntil)
	}
	return WriteAtomic(path, b)
}

// readStopMarker parses a marker file; an unknown or malformed line is skipped.
func readStopMarker(path string) (StopMarker, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return StopMarker{}, err
	}
	var m StopMarker
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch k {
		case "at":
			m.At, _ = time.Parse(time.RFC3339, v)
		case "why":
			m.Why = v
		case "pause":
			m.PauseWhy = v
		case "pause_until":
			m.PauseUntil, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return m, nil
}

// BeginStart is obor start's first step: the marker moved to the starting file, then that read (moved false: there
// was no marker; pending: a starting file is there, moved now or left by an earlier obor start). Neither file: zero.
func BeginStart(root string) (m StopMarker, moved, pending bool, err error) {
	err = os.Rename(StoppedFile(root), StartingFile(root))
	switch {
	case err == nil:
		moved = true
	case !errors.Is(err, os.ErrNotExist):
		return m, false, false, err
	}
	m, err = readStopMarker(StartingFile(root))
	if errors.Is(err, os.ErrNotExist) {
		return StopMarker{}, false, false, nil
	}
	return m, moved, err == nil, err
}

// EndStart removes the starting file (none: nil).
func EndStart(root string) error {
	if err := os.Remove(StartingFile(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Unregister removes the project at root from the machine's list (every entry naming that root), under the list's
// lock as Register writes it: changed false when it held none.
func Unregister(root string, env Env) (changed bool, err error) {
	lock := NewLock(filepath.Join(StateHome(env), "projects.lock"))
	ctx, cancel := context.WithTimeout(context.Background(), registerWait)
	defer cancel()
	if err := lock.Wait(ctx, registerWait); err != nil {
		return false, fmt.Errorf("the project list: %v", err)
	}
	defer lock.Unlock()
	before := readProjects(env)
	list := map[string]string{}
	for name, r := range before {
		if filepath.Clean(r) != filepath.Clean(Physical(root)) {
			list[name] = r
		}
	}
	if maps.Equal(before, list) {
		return false, nil
	}
	return true, writeProjects(env, list)
}
