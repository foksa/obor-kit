package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
)

// sweepMerged is a Merges of these merged facts in order (a test's event log).
func sweepMerged(ms ...Merge) Merges {
	return func(string) ([]Merge, error) { return ms, nil }
}

// sweepNoStore is a Merges of a project serve never made a store for (checks' sweepMerges).
func sweepNoStore(string) ([]Merge, error) { return nil, errors.New("no store") }

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

// TestSweep is the nightly sweep's table (T-1085), one case per row of the task's test table.
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
			if lines := Sweep(cfg, now, true, sweepMerged()); len(lines) != 0 {
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
			if lines := Sweep(cfg, now, false, sweepMerged()); len(lines) != 0 {
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
				filepath.Join(state, "merge-changed-files")} {
				sweepFile(t, now, p, time.Hour)
			}
			if lines := Sweep(cfg, now, false, sweepMerged()); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, filepath.Join(state, "receipts"))
			gone(t, filepath.Join(state, "merges.log"))
			gone(t, filepath.Join(state, "merge-changed-files"))
		}},
		{"drafts: 15 days old goes, 13 days old stays", func(t *testing.T, cfg *config.Config, now time.Time) {
			dir := filepath.Join(cfg.Root, ".obor", "drafts")
			old, fresh := filepath.Join(dir, "T-01.md"), filepath.Join(dir, "T-02.md")
			sweepFile(t, now, old, 15*24*time.Hour)
			sweepFile(t, now, fresh, 13*24*time.Hour)
			if lines := Sweep(cfg, now, false, sweepMerged()); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, old)
			there(t, fresh)
		}},
		{"every never-touched name stays, and the stop-failure log keeps only its fresh entries", func(t *testing.T, cfg *config.Config, now time.Time) {
			state := StateDir(cfg.Root)
			kept := []string{
				filepath.Join(state, "obor.db"),
				filepath.Join(state, "obor.db-wal"),
				filepath.Join(state, "obor.db-shm"),
				filepath.Join(state, "serve.flock"),
				filepath.Join(state, "serve.log"),
				filepath.Join(state, "logs", "nightly-check.log"),
				filepath.Join(state, "kinds", "opencode.json"),
				filepath.Join(state, "opencode.json"),
				filepath.Join(state, "conformance.json"),
				filepath.Join(state, "last-full-check"),
				filepath.Join(state, "flakes.jsonl"),
				filepath.Join(state, "background.jsonl"),
				filepath.Join(state, "launch.log"),
			}
			for _, p := range kept {
				sweepFile(t, now, p, 2*365*24*time.Hour)
			}
			stopFailure := filepath.Join(state, StopFailureLog)
			if err := os.WriteFile(stopFailure, []byte("{\"at\":\"2026-10-01T00:00:00Z\",\"error\":\"old\"}\n"+
				"{\"at\":\"2026-11-01T00:00:00Z\",\"error\":\"fresh\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if lines := Sweep(cfg, now, false, sweepMerged()); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
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
		// T-1090: a merged task's step logs go 3 days after its merge.
		{"T-1 merged 4 days ago, T-2 merged 2 days ago, T-3 dropped, T-4 never merged: only T-1's logs go", func(t *testing.T, cfg *config.Config, now time.Time) {
			logs := filepath.Join(StateDir(cfg.Root), "logs")
			var paths []string
			for _, id := range []string{"T-1", "T-2", "T-3", "T-4"} {
				for _, step := range []string{"after_merge", "guard", "check"} {
					p := filepath.Join(logs, id+"-"+step+".log")
					sweepFile(t, now, p, time.Hour) // its mtime is no merge time: this step reads the event log
					paths = append(paths, p)
				}
			}
			merged := sweepMerged(Merge{Task: "T-1", At: now.Add(-4 * 24 * time.Hour)},
				Merge{Task: "T-2", At: now.Add(-2 * 24 * time.Hour)})
			if lines := Sweep(cfg, now, false, merged); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			for _, p := range paths {
				if strings.HasPrefix(filepath.Base(p), "T-1-") {
					gone(t, p)
					continue
				}
				there(t, p) // a recent merge, a dropped task, a task that never merged
			}
		}},
		{"T-10 and T-100 logs, T-10 merged 4 days ago: T-100's stay (exact id match)", func(t *testing.T, cfg *config.Config, now time.Time) {
			logs := filepath.Join(StateDir(cfg.Root), "logs")
			t10, t100 := filepath.Join(logs, "T-10-check.log"), filepath.Join(logs, "T-100-check.log")
			for _, p := range []string{t10, t100} {
				sweepFile(t, now, p, time.Hour)
			}
			if lines := Sweep(cfg, now, false, sweepMerged(Merge{Task: "T-10", At: now.Add(-4 * 24 * time.Hour)})); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			gone(t, t10)
			there(t, t100)
		}},
		{"a task merged twice, last merge 2 days ago: its logs stay", func(t *testing.T, cfg *config.Config, now time.Time) {
			log := filepath.Join(StateDir(cfg.Root), "logs", "T-5-guard.log")
			sweepFile(t, now, log, time.Hour)
			merged := sweepMerged(Merge{Task: "T-5", At: now.Add(-4 * 24 * time.Hour)},
				Merge{Task: "T-5", At: now.Add(-2 * 24 * time.Hour)})
			if lines := Sweep(cfg, now, false, merged); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			there(t, log)
		}},
		{"no obor.db: no log removed, one line", func(t *testing.T, cfg *config.Config, now time.Time) {
			log := filepath.Join(StateDir(cfg.Root), "logs", "T-1-check.log")
			sweepFile(t, now, log, time.Hour)
			lines := Sweep(cfg, now, false, sweepNoStore)
			if len(lines) != 1 || !strings.Contains(lines[0], "no store") {
				t.Fatalf("Sweep = %q, want one line naming the missing store", lines)
			}
			there(t, log)
		}},
		{"nightly-check.log, release-0.32.0.log, serve.log: stay", func(t *testing.T, cfg *config.Config, now time.Time) {
			state := StateDir(cfg.Root)
			kept := []string{
				filepath.Join(state, "logs", "nightly-check.log"),
				filepath.Join(state, "logs", "release-0.32.0.log"),
				filepath.Join(state, "serve.log"),
			}
			for _, p := range kept {
				sweepFile(t, now, p, 30*24*time.Hour) // past the 14 days too: this step is not what removes them
			}
			merged := sweepMerged(Merge{Task: "T-1", At: now.Add(-4 * 24 * time.Hour)}) // the step runs
			if lines := Sweep(cfg, now, false, merged); len(lines) != 0 {
				t.Fatalf("Sweep = %q, want no lines", lines)
			}
			for _, p := range kept {
				there(t, p)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); c.test(t, sweepCfg(t), now) })
	}
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
	lines := Sweep(cfg, now, false, sweepMerged())
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
