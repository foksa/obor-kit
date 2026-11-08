package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"obor/internal/app/event"
	"obor/internal/project"
	"obor/internal/store"
)

// BackupKeep is how many daily backups a project keeps.
const BackupKeep = 7

// BackupDir is a project's database backups: <root>/.obor/state/backup/obor-<YYYY-MM-DD>.db.
func BackupDir(root string) string { return filepath.Join(root, ".obor", "state", "backup") }

// Backup is obor nightly's backup of root's database (keeping the newest 7): through the serve at sock when it has
// the project loaded, else directly on a read-only open (WAL: a consistent copy while another serve writes). A
// project without a database ("": serve never loaded it) has nothing to back up. The prompts table's 14-day prune
// (T-665) is serve's, its one writer: a read-only backup here prunes nothing.
func Backup(ctx context.Context, sock, root string, now time.Time) (string, error) {
	if reply, err := Verdict(ctx, sock, &BackupReq{Envelope{Root: root}}); err == nil {
		return reply, nil
	}
	if _, err := os.Stat(event.DBFile(root)); os.IsNotExist(err) {
		return "", nil
	}
	db, err := store.OpenRO(event.DBFile(root))
	if err != nil {
		return "", err
	}
	defer db.Close()
	path, err := store.BackupDaily(db, BackupDir(root), now, BackupKeep)
	if err != nil {
		return "", err
	}
	PruneStopFailure(root, now)
	return "BACKUP " + path, nil
}

// PruneStopFailure drops stop-failure entries older than the check logs' 14-day retention period.
func PruneStopFailure(root string, now time.Time) {
	path := filepath.Join(project.StateDir(root), project.StopFailureLog)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cutoff := now.Add(-14 * 24 * time.Hour)
	var kept bytes.Buffer
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			At string `json:"at"`
		}
		if json.Unmarshal(line, &entry) == nil {
			if at, err := time.Parse(time.RFC3339, entry.At); err == nil && at.Before(cutoff) {
				continue
			}
		}
		kept.Write(line)
		kept.WriteByte('\n')
	}
	_ = os.WriteFile(path, kept.Bytes(), 0o600)
}
