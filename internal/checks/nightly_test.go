package checks

import (
	"errors"
	"testing"
)

// a non-gating command's result line carries the command's own summary — its last "<name>: " line —
// not the wrapper's trailer: make appends `make: *** [check-race] Error 1` to a failed recipe, and the line is what the
// nightly's result (and last-full-check) carries. The WARN shape and the rc stay as they were.
func TestNonGatingLine(t *testing.T) {
	t.Parallel()
	const raceSummary = "race: 1 data race report(s), 1 package(s) with failing tests under -race"
	for _, c := range []struct {
		name string
		cmd  string // the command's name: its summary line starts "<cmd>: "
		rc   int
		text string // what it printed
		line string // the result line the nightly carries
	}{
		{"a failing race command: its race line, not make's trailer", "race", 2,
			"==================\nWARNING: DATA RACE\nRead at 0x00c0000b4010 by goroutine 7:\n" + raceSummary + "\n" +
				"make: *** [check-race] Error 1\n",
			"WARN race (rc 2, non-gating): 1 data race report(s), 1 package(s) with failing tests under -race"},
		{"the race build did not run: its race line, not make's trailer", "race", 2,
			"race: the race build did not run (FAIL)\nmake: *** [check-race] Error 2\n",
			"WARN race (rc 2, non-gating): the race build did not run (FAIL)"},
		{"a passing command whose summary is not its last line: that line", "check_budget", 0,
			"check_budget: budget OK: 12 packages\n24 git processes\n",
			"check_budget (non-gating) budget OK: 12 packages"},
		{"no \"<name>: \" line at all: the last line", "slow_b", 1,
			"FAIL b-1 a method is gone\n0 pass, 1 fail\n",
			"WARN slow_b (rc 1, non-gating): 0 pass, 1 fail"},
		{"no line at all: rc", "slow_c", 4, " \n\n", "WARN slow_c (rc 4, non-gating): rc=4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := nonGatingLine(c.cmd, c.rc, c.text); got != c.line {
				t.Errorf("line = %q, want %q", got, c.line)
			}
		})
	}
}

// nightly's backup step as a function over wire.Backup's answer, so no test starts a nightly (and git) to see
// which reply prunes days: an error is the evidence line that makes the nightly RED and prunes nothing; an empty reply
// (a project with no database) is nothing at all; a BACKUP path is its base name in the result line and the sweep's
// permission.
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
