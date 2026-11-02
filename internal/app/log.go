package app

import (
	"fmt"
	"path/filepath"

	"obor/internal/store"
)

// backup is MethodBackup: the backup of a project this serve has loaded.
func (a *App) backup(c Call) (string, error) {
	root := filepath.Clean(c.Root)
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("backup: %s is not loaded by this serve", root)
	}
	now := a.opt.Now()
	path, err := store.BackupDaily(p.log.DB(), BackupDir(root), now, backupKeep)
	if err != nil {
		return "", err
	}
	a.prunePrompts(p, now)
	pruneStopFailure(root, now)
	return "BACKUP " + path, nil
}
