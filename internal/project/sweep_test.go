package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/store"
)

// sweepCfg is a temp project root whose config names lanes L-01..L-02 and the default roles.
func sweepCfg(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"prefix":"pj","lanes":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// sweepFile writes path ("x\n") with mtime now-age, creating its directory.
func sweepFile(t *testing.T, now time.Time, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// sweepDay is a day in the fixture's month, named as a daily backup.
func sweepDay(day int) string {
	return time.Date(2026, 10, day, 3, 0, 0, 0, time.UTC).Format("obor-2006-01-02.db.gz")
}

// TestSweep is the nightly sweep's table (T-1085, T-1098), one case per rule of its tests.
func TestSweep(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		test func(t *testing.T, cfg *config.Config, now time.Time)
	}{
		{"9 days of .db.gz, old uncompressed and herd backups: the newest 7 stay", func(t *testing.T, cfg *config.Config, now time.Time) {
			dir := filepath.Join(StateDir(cfg.Root), "backup")
			for day := 1; day <= 9; day++ {
				sweepFile(t, now, filepath.Join(dir, sweepDay(day)), 30*24*time.Hour)
			}
			old1 := filepath.Join(dir, "obor-2026-09-01.db")
			old2 := filepath.Join(dir, "obor-2026-09-02.db")
			herd := filepath.Join(dir, "herd-2026-10-20.db")
			oldTmp := filepath.Join(dir, "obor-2026-09-01.db.gz.tmp")
			freshTmp := filepath.Join(dir, "obor-2026-11-09.db.gz.tmp")
			for _, p := range []string{old1, old2, herd, oldTmp} {
				sweepFile(t, now, p, 2*24*time.Hour)
			}
			sweepFile(t, now, freshTmp, time.Hour)
			if lines := Sweep(cfg, now, true); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			for day := 1; day <= 2; day++ { // older than the newest 7
				gone(t, filepath.Join(dir, sweepDay(day)))
			}
			for day := 3; day <= 9; day++ {
				there(t, filepath.Join(dir, sweepDay(day)))
			}
			for _, p := range []string{old1, old2, herd, oldTmp} {
				gone(t, p)
			}
			there(t, freshTmp) // a backup being written now
		}},
		{"files of gone lanes and agents go, the live ones stay", func(t *testing.T, cfg *config.Config, now time.Time) {
			state := StateDir(cfg.Root)
			live := []string{
				filepath.Join(state, "run-changed-L-01"),
				filepath.Join(state, "lane-changed-files-L-02"),
				filepath.Join(state, "lanes", "L-01.plugin"),
				filepath.Join(state, "lanes", "L-02.setup"),
				filepath.Join(state, "inbox", "L-01"),
				filepath.Join(state, "inbox", "L-01.drive"),
				filepath.Join(state, "inbox", "lead"),
				filepath.Join(state, "restart-pj-l-01.log"),
				filepath.Join(state, "restart-pj-lead.log"),
				filepath.Join(state, "restart-L-01.lock"),
			}
			dead := []string{
				filepath.Join(state, "run-changed-O-03"),
				filepath.Join(state, "lane-changed-files-O-03"),
				filepath.Join(state, "lanes", "L-06.plugin"),
				filepath.Join(state, "lanes", "L-06.setup"),
				filepath.Join(state, "inbox", "L-05"),
				filepath.Join(state, "inbox", "advisor"),
				filepath.Join(state, "restart-hk-int.log"),
				filepath.Join(state, "restart-O-03.lock"),
			}
			unparsed := filepath.Join(state, "run-changed-") // no lane named: stays
			for _, p := range append(append(live, dead...), unparsed) {
				sweepFile(t, now, p, time.Hour)
			}
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			for _, p := range live {
				there(t, p)
			}
			for _, p := range dead {
				gone(t, p)
			}
			there(t, unparsed)
		}},
		{"retired state files go", func(t *testing.T, cfg *config.Config, now time.Time) {
			state := StateDir(cfg.Root)
			for _, p := range []string{filepath.Join(state, "receipts", "x"), filepath.Join(state, "merges.log"),
				filepath.Join(state, "merge-changed-files"), filepath.Join(state, "serve-changed-files"),
				filepath.Join(state, "conformance.json"), filepath.Join(cfg.Root, ".obor", "cache", "x")} {
				sweepFile(t, now, p, time.Hour)
			}
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, filepath.Join(state, "receipts"))
			gone(t, filepath.Join(state, "merges.log"))
			gone(t, filepath.Join(state, "merge-changed-files"))
			gone(t, filepath.Join(state, "serve-changed-files"))
			gone(t, filepath.Join(state, "conformance.json"))
			gone(t, filepath.Join(cfg.Root, ".obor", "cache"))
		}},
		{"drafts: 15 days old goes, 13 days old stays", func(t *testing.T, cfg *config.Config, now time.Time) {
			dir := filepath.Join(cfg.Root, ".obor", "drafts")
			old, fresh := filepath.Join(dir, "T-01.md"), filepath.Join(dir, "T-02.md")
			sweepFile(t, now, old, 15*24*time.Hour)
			sweepFile(t, now, fresh, 13*24*time.Hour)
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, old)
			there(t, fresh)
		}},
		{"every never-touched name stays, and the stop-failure log keeps only its fresh entries", func(t *testing.T, cfg *config.Config, now time.Time) {
			state := StateDir(cfg.Root)
			db, err := store.OpenRW(store.File(cfg.Root)) // the project's store: the sweep never touches it
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			kept := []string{
				filepath.Join(state, "serve.flock"),
				filepath.Join(state, "serve.log"),
				filepath.Join(state, "logs", "nightly-check.log"),
				filepath.Join(state, "kinds", "opencode.json"),
				filepath.Join(state, "opencode.json"),
				filepath.Join(state, "last-full-check"),
				filepath.Join(state, "background.jsonl"),
			}
			for _, p := range kept {
				sweepFile(t, now, p, 2*365*24*time.Hour)
			}
			stopFailure := filepath.Join(state, StopFailureLog)
			if err := os.WriteFile(stopFailure, []byte("{\"at\":\"2026-10-01T00:00:00Z\",\"error\":\"old\"}\n"+
				"{\"at\":\"2026-11-01T00:00:00Z\",\"error\":\"fresh\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			there(t, store.File(cfg.Root))
			for _, p := range kept {
				there(t, p)
			}
			b, err := os.ReadFile(stopFailure)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "old") || !strings.Contains(string(b), "fresh") {
				t.Errorf("stop-failure log = %q, want only the fresh entry", b)
			}
		}},
		{"logs: a task's step logs are serve's, never the sweep's; the retired names go", func(t *testing.T, cfg *config.Config, now time.Time) {
			logs := filepath.Join(StateDir(cfg.Root), "logs")
			kept := []string{
				filepath.Join(logs, "T-1-check.log"), // a landed task still running its after-merge steps
				filepath.Join(logs, "T-1-after_merge.log"),
				filepath.Join(logs, "T-100-check.log"),
				filepath.Join(logs, "nightly-check.log"), // its next pass removes it (T-1094)
				filepath.Join(logs, "serve.log"),
				filepath.Join(logs, "release-0.32.0.log"),
			}
			retired := []string{
				filepath.Join(logs, "milestone-check.log"),
				filepath.Join(logs, "promote-retest-check.log"),
				filepath.Join(logs, "conformance-turn-1.log"),
			}
			for _, p := range append(kept, retired...) {
				sweepFile(t, now, p, time.Hour)
			}
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			for _, p := range kept {
				there(t, p)
			}
			for _, p := range retired {
				gone(t, p)
			}
		}},
		{"logs: flakes 8 days old go, 6 days old stay; launch.log and an old restart log go", func(t *testing.T, cfg *config.Config, now time.Time) {
			state := StateDir(cfg.Root)
			flakes := filepath.Join(state, "flakes.jsonl")
			if err := os.MkdirAll(state, 0o755); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf("{\"time\":%d,\"test\":\"TestOld\"}\n{\"time\":%d,\"test\":\"TestRecent\"}\n",
				now.Add(-8*24*time.Hour).Unix(), now.Add(-6*24*time.Hour).Unix())
			if err := os.WriteFile(flakes, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			launch := filepath.Join(state, "launch.log")
			oldRestart := filepath.Join(state, "restart-pj-l-01.log")
			freshRestart := filepath.Join(state, "restart-pj-lead.log")
			sweepFile(t, now, launch, 15*24*time.Hour)
			sweepFile(t, now, oldRestart, 15*24*time.Hour)
			sweepFile(t, now, freshRestart, 13*24*time.Hour)
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, launch)
			gone(t, oldRestart)
			there(t, freshRestart)
			b, err := os.ReadFile(flakes)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "TestOld") || !strings.Contains(string(b), "TestRecent") {
				t.Errorf("flakes.jsonl = %q, want only the 6-day-old entry", b)
			}
		}},
		{"a removal the sweep cannot make is one line naming it, and the sweep goes on", func(t *testing.T, cfg *config.Config, now time.Time) {
			stuck := filepath.Join(StateDir(cfg.Root), "backup", "herd-2026-11-01.db") // a directory: its removal needs write on it
			sweepFile(t, now, filepath.Join(stuck, "x"), time.Hour)
			if err := os.Chmod(stuck, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(stuck, 0o755) })
			if f, err := os.Create(filepath.Join(stuck, ".probe")); err == nil { // root ignores the mode
				f.Close()
				t.Skip("running as root: modes are not enforced")
			}
			dead := filepath.Join(StateDir(cfg.Root), "run-changed-O-03")
			sweepFile(t, now, dead, time.Hour)
			lines := Sweep(cfg, now, true)
			if len(lines) != 1 || !strings.Contains(lines[0], filepath.Base(stuck)) {
				t.Fatalf("Sweep = %q, want one line naming %s", lines, filepath.Base(stuck))
			}
			there(t, stuck)
			gone(t, dead) // the other steps still ran
		}},
		{"7 old .db days and today's backup: the oldest goes", func(t *testing.T, cfg *config.Config, now time.Time) {
			days := sweepBackupDays(t, cfg, now)
			if lines := Sweep(cfg, now, true); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, days[0])
			for _, p := range days[1:] {
				there(t, p)
			}
		}},
		{"7 old .db days, the backup failed: all stay, the other steps still run", func(t *testing.T, cfg *config.Config, now time.Time) {
			days := sweepBackupDays(t, cfg, now)
			dead := filepath.Join(StateDir(cfg.Root), "run-changed-O-03")
			sweepFile(t, now, dead, time.Hour)
			if lines := Sweep(cfg, now, false); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			for _, p := range days {
				there(t, p) // without the guard the oldest would go: the newest 7 do not cover it
			}
			gone(t, dead)
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t, sweepCfg(t), now) })
	}
}

// sweepBackupDays writes 7 old uncompressed daily backups (obor-2026-11-01.db … obor-2026-11-07.db) and today's
// obor-2026-11-09.db.gz, then returns them, oldest first (T-1145: one date more than backupKeep keeps).
func sweepBackupDays(t *testing.T, cfg *config.Config, now time.Time) []string {
	t.Helper()
	dir := filepath.Join(StateDir(cfg.Root), "backup")
	var out []string
	for day := 1; day <= 7; day++ {
		p := filepath.Join(dir, fmt.Sprintf("obor-2026-11-%02d.db", day))
		sweepFile(t, now, p, 2*24*time.Hour)
		out = append(out, p)
	}
	today := filepath.Join(dir, "obor-2026-11-09.db.gz")
	sweepFile(t, now, today, time.Hour)
	return append(out, today)
}

// TestSweepNoPrefixFailsClosed: agent names cannot be derived, so the per-lane/agent step is a line and removes
// nothing (an empty derived name is never a default).
func TestSweepNoPrefixFailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"lanes":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
	file := filepath.Join(StateDir(root), "restart-hk-int.log")
	sweepFile(t, now, file, time.Hour)
	lines := Sweep(cfg, now, false)
	if len(lines) == 0 || !strings.Contains(lines[0], "prefix") {
		t.Fatalf("Sweep = %q, want a line naming the missing prefix", lines)
	}
	there(t, file)
}

// there and gone are this file's assertions.
func there(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("no %s: %v", path, err)
	}
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s still exists", path)
	}
}
