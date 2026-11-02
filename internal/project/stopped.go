package project

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// A stopped project (T-709): `obor stop` put it to sleep (no serve, no agents, not in the machine's project list) and
// `obor start` wakes it. The marker is <root>/.obor/state/stopped; while it is there every obor hook and the git hooks
// stand down, serve and the herdr plugin's restore leave the project alone, and obor init says to run obor start.

// StoppedLine is the line obor whoami, obor status, obor monitor and obor web show for a stopped project.
const StoppedLine = "project stopped: obor start"

// StoppedFile is root's marker.
func StoppedFile(root string) string { return filepath.Join(StateDir(root), "stopped") }

// Stopped: root (the main checkout) holds the marker.
func Stopped(root string) bool {
	_, err := os.Stat(StoppedFile(root))
	return err == nil
}

// MarkStopped writes the marker: when and why, one "key: value" line each.
func MarkStopped(root, why string, now time.Time) error {
	path := StoppedFile(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteAtomic(path, fmt.Appendf(nil, "at: %s\nwhy: %s\n", now.UTC().Format(time.RFC3339), why))
}

// ClearStopped removes the marker; removed false when there was none.
func ClearStopped(root string) (removed bool, err error) {
	err = os.Remove(StoppedFile(root))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
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
