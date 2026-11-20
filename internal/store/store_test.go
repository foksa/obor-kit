package store

import (
	"compress/gzip"
	"database/sql"
	"io"
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

// TestOpenRWChecksTheSchemaVersion: only schema 14 opens. 8, 9 and 13 are refused with the release that migrates
// them and the file is left byte for byte as it was; 15 is refused as newer; a version that is not a number is
// refused; 14 opens.
func TestOpenRWChecksTheSchemaVersion(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, version, want string
	}{
		{"schema 8", "8", "schema version 8 is older than this obor (14): upgrade through v0.24.0 first, " +
			"then open it once with obor v0.40.7, which migrates it"},
		{"schema 9", "9", "schema version 9 is older than this obor (14): open it once with obor v0.40.7 " +
			"first, which migrates it"},
		{"schema 13", "13", "schema version 13 is older than this obor (14): open it once with obor v0.40.7 " +
			"first, which migrates it"},
		{"newer", "15", "schema version 15 is newer than this obor (14): upgrade obor"},
		{"non-numeric", "abc", `schema version "abc" is not a number`},
		{"current", "14", ""},
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
			if err != nil || len(before) == 0 {
				t.Fatalf("precondition: the schema-%q file (%d bytes, %v)", c.version, len(before), err)
			}

			db, err := OpenRW(path)
			if c.want == "" {
				if err != nil {
					t.Fatalf("OpenRW on schema %s: %v", c.version, err)
				}
				defer db.Close()
				var version string
				if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil ||
					version != c.version {
					t.Errorf("schema_version = %q, %v; want %s", version, err, c.version)
				}
				return
			}
			if err == nil {
				db.Close()
				t.Fatalf("OpenRW accepted schema version %q", c.version)
			}
			if !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "\n") {
				t.Errorf("error = %q, want one line with %q", err, c.want)
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

// TestBackupDailyGzip: a day's backup is one gzip'd file (a second the same day replaces it), and its gunzip'd copy
// opens read-only with the source's rows. `VACUUM INTO` leaves the source alone.
func TestBackupDailyGzip(t *testing.T) {
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
	path, err := BackupDaily(db, dir, day)
	if err != nil {
		t.Fatalf("BackupDaily: %v", err)
	}
	if want := filepath.Join(dir, day.Format("obor-2006-01-02.db.gz")); path != want {
		t.Errorf("path = %s, want %s", path, want)
	}
	copyPath := gunzip(t, path)
	ro, err := OpenRO(copyPath)
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
	if _, err := BackupDaily(db, dir, day); err != nil { // the same day again
		t.Fatalf("BackupDaily again: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("backups = %v, want only %s", names, filepath.Base(path))
	}
}

// gunzip writes path's gunzip'd bytes to a fresh file and returns its path.
func gunzip(t *testing.T, path string) string {
	t.Helper()
	gz, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	zr, err := gzip.NewReader(gz)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	dst := filepath.Join(t.TempDir(), "obor.db")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return dst
}

// The read indexes: a new store has them, and the timeline, session-fact and prompt queries plan with them
// instead of scanning.
func TestReadIndexesAreThePlan(t *testing.T) {
	t.Parallel()
	db, err := OpenRW(newPath(t))
	if err != nil {
		t.Fatalf("OpenRW: %v", err)
	}
	defer db.Close()
	for _, c := range []struct{ index, query string }{
		{"events_task_seq",
			"SELECT data FROM events WHERE task = 'session' AND kind = 'fact' AND step = 'session' ORDER BY seq"},
		{"prompts_job",
			"SELECT seq, agent, kind, job, task, ts, rev, text FROM prompts WHERE job = 'T-1/1/work' ORDER BY id DESC LIMIT 1"},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + c.query)
		if err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		used := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatalf("query plan of %s: %v", c.query, err)
			}
			if strings.Contains(detail, c.index) {
				used = true
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("query plan of %s: %v", c.query, err)
		}
		if !used {
			t.Errorf("%s is not the plan of: %s", c.index, c.query)
		}
	}
}

// SchemaVersion is 14, the pruned-log schema: a binary at 13 refuses a store at 14 (an older fold reads a lone
// merged fact as nothing), and this binary refuses a store at 13 with the release that migrates it.
func TestSchema14(t *testing.T) {
	t.Parallel()
	if SchemaVersion != 14 {
		t.Fatalf("SchemaVersion %d, want 14", SchemaVersion)
	}
	if err := versionErr(14, 13); err == nil || !strings.Contains(err.Error(), "schema version 14 is newer than this obor (13)") {
		t.Errorf("a binary at 13 on a store at 14: %v; want refused", err)
	}
	if err := versionErr(13, 14); err == nil || !strings.Contains(err.Error(), "obor v0.40.7") {
		t.Errorf("this binary on a store at 13: %v; want refused with the release that migrates it", err)
	}
}
