package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newPath is a database path in the test's own directory; nothing creates the file.
func newPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "obor.db")
}

// pragma reads one PRAGMA value off db.
func pragma(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

// TestOpenRWAndRO: the writer is WAL, synchronous=FULL, one connection, a busy_timeout, and the schema version
// recorded; the reader is read-only with a short busy_timeout, and a write is refused.
func TestOpenRWAndRO(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	rw, err := OpenRW(path)
	if err != nil {
		t.Fatalf("OpenRW: %v", err)
	}
	defer rw.Close()
	for _, c := range []struct{ name, want string }{
		{"journal_mode", "wal"},
		{"synchronous", "2"}, // FULL
		{"busy_timeout", "5000"},
	} {
		if got := pragma(t, rw, c.name); got != c.want {
			t.Errorf("writer PRAGMA %s = %q, want %q", c.name, got, c.want)
		}
	}
	if got := rw.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
	var version string
	if err := rw.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if want := strconv.Itoa(SchemaVersion); version != want {
		t.Errorf("schema_version = %q, want %q", version, want)
	}

	ro, err := OpenRO(path)
	if err != nil {
		t.Fatalf("OpenRO: %v", err)
	}
	defer ro.Close()
	if got := pragma(t, ro, "busy_timeout"); got != "500" {
		t.Errorf("reader busy_timeout = %q, want %q", got, "500")
	}
	if got := pragma(t, ro, "journal_mode"); got != "wal" {
		t.Errorf("reader journal_mode = %q, want %q (the writer chose it)", got, "wal")
	}
	if _, err := ro.Exec("CREATE TABLE written_by_a_reader (x INTEGER)"); err == nil {
		t.Fatal("a read-only connection created a table")
	} else if !strings.Contains(err.Error(), "readonly") {
		t.Errorf("write error = %v, want a read-only refusal", err)
	}
	var n int
	if err := rw.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'written_by_a_reader'").Scan(&n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if n != 0 {
		t.Errorf("the reader's refused write left a table behind (%d)", n)
	}
}

// TestOpenRWRefusesNewerSchema: a database a newer obor wrote is refused with one line, not used.
func TestOpenRWRefusesNewerSchema(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, version, want string
	}{
		{"newer", strconv.Itoa(SchemaVersion + 1), "newer than this obor"},
		{"older", "0", "older than this obor"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := newPath(t)
			db, err := OpenRW(path)
			if err != nil {
				t.Fatalf("OpenRW: %v", err)
			}
			if _, err := db.Exec("UPDATE meta SET value = ? WHERE key = 'schema_version'", c.version); err != nil {
				t.Fatalf("set schema_version: %v", err)
			}
			db.Close()

			db, err = OpenRW(path)
			if err == nil {
				db.Close()
				t.Fatalf("OpenRW accepted schema version %s", c.version)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("refusal is not one line: %q", err)
			}
		})
	}
}

// TestOpenROOnMissingFile: a reader does not create the database it cannot find.
func TestOpenROOnMissingFile(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	if _, err := OpenRO(path); err == nil {
		t.Fatal("OpenRO opened a database that does not exist")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("OpenRO created %s (%v), want no file", path, err)
	}
}

// TestTwoReadersWhileWriterWrites: WAL lets readers hold a snapshot while serve commits; the writer does not block
// and each reader sees exactly its own snapshot.
func TestTwoReadersWhileWriterWrites(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	rw, err := OpenRW(path)
	if err != nil {
		t.Fatalf("OpenRW: %v", err)
	}
	defer rw.Close()
	if _, err := rw.Exec("CREATE TABLE probe (x INTEGER)"); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	if _, err := rw.Exec("INSERT INTO probe VALUES (1)"); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	ro1, err := OpenRO(path)
	if err != nil {
		t.Fatalf("OpenRO (reader 1): %v", err)
	}
	defer ro1.Close()
	ro2, err := OpenRO(path)
	if err != nil {
		t.Fatalf("OpenRO (reader 2): %v", err)
	}
	defer ro2.Close()

	// reader 1 holds its snapshot open for the whole write
	tx, err := ro1.Begin()
	if err != nil {
		t.Fatalf("reader 1 begin: %v", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM probe").Scan(&n); err != nil {
		t.Fatalf("reader 1 select: %v", err)
	}
	if n != 1 {
		t.Fatalf("precondition: reader 1 sees %d rows, want 1", n)
	}

	// the writer commits while both readers are open
	done := make(chan error, 1)
	go func() {
		_, err := rw.Exec("INSERT INTO probe VALUES (2)")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writer while readers are open: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocked on a reader's snapshot")
	}

	if err := tx.QueryRow("SELECT count(*) FROM probe").Scan(&n); err != nil {
		t.Fatalf("reader 1 select in its snapshot: %v", err)
	}
	if n != 1 {
		t.Errorf("reader 1's snapshot grew to %d rows, want 1", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("reader 1 commit: %v", err)
	}
	for name, ro := range map[string]*sql.DB{"reader 1": ro1, "reader 2": ro2} {
		if err := ro.QueryRow("SELECT count(*) FROM probe").Scan(&n); err != nil {
			t.Fatalf("%s select after the write: %v", name, err)
		}
		if n != 2 {
			t.Errorf("%s sees %d rows after the commit, want 2", name, n)
		}
	}
}

// TestOpenPaths: the DSN is built from the path verbatim — a project directory may have a space, and a '#', '?' or
// '%' is part of the file name, not the URI's fragment or query.
func TestOpenPaths(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, dir string }{
		{"a space", filepath.Join("My Project", "obor state")},
		{"URI punctuation", "a#b?c%41d"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), c.dir, "obor.db")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			db, err := OpenRW(path)
			if err != nil {
				t.Fatalf("OpenRW(%q): %v", path, err)
			}
			db.Close()
			if _, err := os.Stat(path); err != nil {
				t.Errorf("the database is not at %s: %v", path, err)
			}
			ro, err := OpenRO(path)
			if err != nil {
				t.Fatalf("OpenRO: %v", err)
			}
			ro.Close()
		})
	}
}

// TestBackupDailyKeepsSeven: one backup per day, a second the same day replaces it, and only the newest keep stay. A
// backup is a readable copy: `VACUUM INTO` leaves the source alone and the copy opens read-only with its rows.
func TestBackupDailyKeepsSeven(t *testing.T) {
	t.Parallel()
	db, err := OpenRW(newPath(t))
	if err != nil {
		t.Fatalf("OpenRW: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE probe (x INTEGER)"); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.Exec("INSERT INTO probe VALUES (?)", i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	dir := filepath.Join(t.TempDir(), "backup")
	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for i := range 9 {
		path, err := BackupDaily(db, dir, day.AddDate(0, 0, i), 7)
		if err != nil {
			t.Fatalf("BackupDaily day %d: %v", i, err)
		}
		if want := filepath.Join(dir, day.AddDate(0, 0, i).Format("obor-2006-01-02.db")); path != want {
			t.Errorf("path = %s, want %s", path, want)
		}
	}
	if _, err := BackupDaily(db, dir, day.AddDate(0, 0, 8), 7); err != nil { // the same day again
		t.Fatalf("BackupDaily again: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"obor-2026-10-03.db", "obor-2026-10-04.db", "obor-2026-10-05.db", "obor-2026-10-06.db",
		"obor-2026-10-07.db", "obor-2026-10-08.db", "obor-2026-10-09.db"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("backups = %v, want the newest 7 %v", names, want)
	}
	ro, err := OpenRO(filepath.Join(dir, "obor-2026-10-09.db"))
	if err != nil {
		t.Fatalf("OpenRO(the copy): %v", err)
	}
	defer ro.Close()
	var rows int
	if err := ro.QueryRow("SELECT count(*) FROM probe").Scan(&rows); err != nil {
		t.Fatalf("select on the copy: %v", err)
	}
	if rows != 3 {
		t.Errorf("copy has %d rows, want 3", rows)
	}
	if err := ro.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(new(string)); err != nil {
		t.Errorf("copy has no schema version: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM probe").Scan(&rows); err != nil || rows != 3 {
		t.Errorf("source after backup: %d rows, %v; want 3", rows, err)
	}
}

// TestOpenRWRefusalChangesNothing: a database OpenRW refuses (newer, older or non-numeric schema version) is left
// exactly as it was: no table added, journal mode and bytes kept.
func TestOpenRWRefusalChangesNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, version, want string }{
		{"newer", "99", "newer than this obor"},
		{"older", "1", "older than this obor"},
		{"non-numeric", "abc", "is not a number"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := newPath(t)
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
				"INSERT INTO meta (key, value) VALUES ('schema_version', '" + c.version + "')"} {
				if _, err := raw.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			raw.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			db, err := OpenRW(path)
			if err == nil {
				db.Close()
				t.Fatalf("OpenRW accepted schema version %q", c.version)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want %q", err, c.want)
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil || string(after) != string(before) {
				t.Errorf("the refused file changed (read error %v)", rerr)
			}
			ro, err := OpenRO(path)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			var tables int
			if err := ro.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil || tables != 1 {
				t.Errorf("tables = %d, %v; want only meta", tables, err)
			}
			if mode := pragma(t, ro, "journal_mode"); mode == "wal" {
				t.Errorf("journal_mode = %s, want it unchanged (delete)", mode)
			}
		})
	}
}

// TestOpenRWRefusesPreV012 (T-519): a store from before v0.12 (schema 5 with its queue table, and an older one) is
// refused with one line naming its schema and v0.12.0, and the file is left byte for byte as it was.
func TestOpenRWRefusesPreV012(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"5", "2"} {
		t.Run("schema "+version, func(t *testing.T) {
			t.Parallel()
			path := newPath(t)
			raw, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
				"INSERT INTO meta (key, value) VALUES ('schema_version', '" + version + "')",
				`CREATE TABLE events (seq INTEGER PRIMARY KEY, ts TEXT NOT NULL, task TEXT NOT NULL,
					attempt INTEGER NOT NULL, step TEXT NOT NULL, kind TEXT NOT NULL, class TEXT NOT NULL, data TEXT)`,
				"INSERT INTO events VALUES (1, '2026-10-24T11:00:00Z', 'T-1', 1, 'stalled', 'fact', '', NULL)",
				"CREATE TABLE queue (id TEXT PRIMARY KEY, todo TEXT NOT NULL)",
			} {
				if _, err := raw.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			raw.Close()
			before, err := os.ReadFile(path)
			if err != nil || len(before) == 0 {
				t.Fatalf("precondition: the schema-%s file (%d bytes, %v)", version, len(before), err)
			}

			db, err := OpenRW(path)
			if err == nil {
				db.Close()
				t.Fatalf("OpenRW accepted schema version %s", version)
			}
			want := "schema version " + version + " is older than this obor (" + strconv.Itoa(SchemaVersion) +
				"): upgrade through v0.12.0 first"
			if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "\n") {
				t.Errorf("error = %q, want one line with %q", err, want)
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil || string(after) != string(before) {
				t.Errorf("the refused file changed (read error %v)", rerr)
			}
			for _, side := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(path + side); !os.IsNotExist(err) {
					t.Errorf("the refusal left %s%s (%v)", filepath.Base(path), side, err)
				}
			}
		})
	}
}
