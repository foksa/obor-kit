package wire

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"obor/internal/app/event"
	"obor/internal/store"
)

// BackupDir is a project's database backups: <root>/.obor/state/backup/obor-<YYYY-MM-DD>.db.gz.
func BackupDir(root string) string { return filepath.Join(root, ".obor", "state", "backup") }

// Backup is obor nightly's backup of root's database: through the serve at sock when it has the project loaded, else
// directly on a read-only open (WAL: a consistent copy while another serve writes). A project without a database
// ("": serve never loaded it) has nothing to back up. The prompts table's 14-day prune is serve's, its one
// writer: a read-only backup here prunes nothing. The older backups and the rest of .obor/state are project.Sweep's,
// which obor nightly runs right after this.
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
	path, err := store.BackupDaily(db, BackupDir(root), now)
	if err != nil {
		return "", err
	}
	return "BACKUP " + path, nil
}
