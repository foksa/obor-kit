package checks

import (
	"errors"
	"testing"
)

// T-1145: nightly's backup step as a function over wire.Backup's answer, so no test starts a nightly (and git) to see
// which reply prunes days: an error is the evidence line that makes the nightly RED and prunes nothing; an empty reply
// (a project with no database) is nothing at all; a BACKUP path is its base name in the result line and the sweep's
// permission (T-1086).
func TestBackupOutcome(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		reply    string
		err      error
		line     string // the result line
		failure  string // the evidence line
		backedUp bool
	}{
		{"an error is the evidence line and prunes nothing", "", errors.New("backup failed"),
			"", "backup: backup failed", false},
		{"no database (empty reply) is no line and prunes nothing", "", nil, "", "", false},
		{"a BACKUP path is its base name in the result line and lets the sweep prune", "BACKUP /p/.obor/state/backup/obor-2026-11-09.db.gz", nil,
			"backup obor-2026-11-09.db.gz", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			line, failure, backedUp := backupOutcome(c.reply, c.err)
			if line != c.line {
				t.Errorf("line = %q, want %q", line, c.line)
			}
			if failure != c.failure {
				t.Errorf("failure = %q, want %q", failure, c.failure)
			}
			if backedUp != c.backedUp {
				t.Errorf("backedUp = %v, want %v", backedUp, c.backedUp)
			}
		})
	}
}
