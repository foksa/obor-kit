// Package store opens a project's SQLite database. serve is the only writer: it opens the
// database with OpenRW; hooks and CLIs open it with OpenRO, read-only, so they work while serve is down or writing.
// Tables: meta (the schema version), events (the event log; internal/app's Log reads and writes it), lanes
// (each lane's dispatched task, serve's projection of the log; lanes.go), specs (each task's spec;
// specs.go) and prompts (the text of every prompt serve sent; prompts.go).
package store

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the driver: cgo-free, so CGO_ENABLED=0 builds work
)

const (
	// SchemaVersion is the schema this binary knows, the only one OpenRW opens: the database is per project, and a
	// obor that wrote it may be newer than this binary. 7: prompts; 8: the prompts table's own key; 9: the lane row's type; 10: the spec row's note; 11: the read indexes; 12: the lane row's blocked; 13: the lane row's landed; 14: serve's nightly prune of the events table (no DDL; a binary at 13 refuses a store that may be pruned, as it would fold a task's lone merged fact to nothing).
	SchemaVersion = 14

	writerBusyTimeout = 5 * time.Second        // serve waits for a reader's snapshot to end, it does not fail
	readerBusyTimeout = 500 * time.Millisecond // a hook fails fast instead of holding its caller
	openTimeout       = 5 * time.Second
	backupTimeout     = 10 * time.Minute
)

// OpenRW opens path as serve's only writer: WAL (readers never wait on the writer), synchronous=FULL (an effect is
// on disk before its result is recorded) and one connection (the driver is not safe for several goroutines on one
// connection; the app serialises its writes). A new file (and its directory) gets the schema; a store at any other
// schema version is refused.
func OpenRW(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	// WAL is a change to the file, so it is set after the schema check: a refused database stays as it was.
	db, err := open(path, fmt.Sprintf("_pragma=synchronous(FULL)&_pragma=busy_timeout(%d)", writerBusyTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: journal mode: %w", err)
	}
	return db, nil
}

// OpenRO opens path read-only for a hook or a CLI. busy_timeout is short (≤ 500 ms): a read that cannot start is the
// caller's answer, not a stall.
func OpenRO(path string) (*sql.DB, error) {
	return open(path, fmt.Sprintf("mode=ro&_pragma=busy_timeout(%d)", readerBusyTimeout.Milliseconds()))
}

// OpenAt opens the database of the project at root read-only, the handle the project's readers use: nil, nil when the
// project has no database yet. The caller closes it.
func OpenAt(root string) (*sql.DB, error) { return openROAt(root) }

// Backup writes a consistent copy of db to path (VACUUM INTO; serve's nightly backup). path must not exist.
func Backup(db *sql.DB, path string) error {
	if path == "" {
		return errors.New("store: backup without a path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("store: backup to %s: %w", path, err)
	}
	return nil
}

// BackupDaily writes db's backup for day to dir/obor-<YYYY-MM-DD>.db.gz (gzip; a backup of that day already there is
// replaced). It prunes nothing: project.Sweep keeps the newest days (obor nightly).
func BackupDaily(db *sql.DB, dir string, day time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "obor-"+day.Format("2006-01-02")+".db.gz")
	raw, tmp := path+".raw", path+".tmp"
	_ = os.Remove(raw) // a backup that died midway: VACUUM INTO needs a new file
	_ = os.Remove(tmp)
	if err := Backup(db, raw); err != nil {
		os.Remove(raw)
		return "", err
	}
	defer os.Remove(raw)
	if err := gzipFile(raw, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// gzipFile writes src's bytes to dst, gzip'd (a daily backup's copy).
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// uriPath escapes what a file: URI's path cannot hold as is.
var uriPath = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// open builds the DSN and pins the connection pool to one connection before the driver reads a pragma on it.
func open(path, params string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("store: no database path")
	}
	// The path is a URI's: escaped, or a '#' or '?' in it (a test's directory name) ends it early.
	db, err := sql.Open("sqlite", "file:"+uriPath.Replace(path)+"?"+params)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil { // sql.Open is lazy: this applies the pragmas and fails early
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return db, nil
}

// File is a project's database: <root>/.obor/state/obor.db.
func File(root string) string { return filepath.Join(root, ".obor", "state", "obor.db") }

// schema is every table this binary knows. events is the event log (docs/state-model.md "The log"): one row per
// record, seq its order, data the record's JSON (NULL: none). lanes is serve's lane projection (lanes.go); specs the
// tasks' specs (specs.go); prompts the text of the prompts serve sent, by the seq of the fact it belongs to and its
// own id (prompts.go). Every statement is IF NOT EXISTS: a new database gets the tables and indexes, and a store at
// SchemaVersion is opened with its own as they are.
var schema = []string{
	"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
	`CREATE TABLE IF NOT EXISTS events (seq INTEGER PRIMARY KEY, ts TEXT NOT NULL, task TEXT NOT NULL,
		attempt INTEGER NOT NULL, step TEXT NOT NULL, kind TEXT NOT NULL, class TEXT NOT NULL, data TEXT)`,
	`CREATE TABLE IF NOT EXISTS lanes (lane TEXT PRIMARY KEY, task TEXT NOT NULL, branch TEXT NOT NULL,
		agent TEXT NOT NULL, base TEXT NOT NULL, head TEXT NOT NULL, dispatched_at INTEGER NOT NULL,
		finished INTEGER NOT NULL, type TEXT NOT NULL DEFAULT '', blocked INTEGER NOT NULL DEFAULT 0,
		landed INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS specs (task TEXT PRIMARY KEY, text TEXT NOT NULL, rev INTEGER NOT NULL,
		set_by TEXT NOT NULL, set_at TEXT NOT NULL, note TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS prompts (id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER NOT NULL,
		agent TEXT NOT NULL, kind TEXT NOT NULL, job TEXT NOT NULL, task TEXT NOT NULL, ts TEXT NOT NULL,
		rev INTEGER NOT NULL DEFAULT 0, text TEXT NOT NULL)`,
	// The read indexes: a task's timeline and session facts filter by task and order by seq, the blocked
	// report reads one task's (and msg's); a job's prompt lookup filters by job and takes its newest first (id is the
	// rowid, so the index's entries are ordered by it). A query plan on a copy of this project's store uses both;
	// the other readers' columns (kind, step, a task's prompts) get one only when a plan shows it pays for its writes.
	"CREATE INDEX IF NOT EXISTS events_task_seq ON events(task, seq)",
	"CREATE INDEX IF NOT EXISTS prompts_job ON prompts(job)",
}

// versionErr is a binary's refusal of a database at schema got, the binary knowing schema binary (nil: it opens
// it): newer than the binary (a store an older obor must not fold: 14's pruned log), or older than the binary.
func versionErr(got, binary int) error {
	switch {
	case got > binary:
		return fmt.Errorf("store: schema version %d is newer than this obor (%d): upgrade obor", got, binary)
	case got == binary:
		return nil
	case got < 9: // schemas 2–8 were migrated by v0.24.0, 9–13 by v0.40.7
		return fmt.Errorf("store: schema version %d is older than this obor (%d): upgrade through v0.24.0 first, "+
			"then open it once with obor v0.40.7, which migrates it", got, binary)
	}
	return fmt.Errorf("store: schema version %d is older than this obor (%d): open it once with obor v0.40.7 "+
		"first, which migrates it", got, binary)
}

// initSchema checks an existing database's schema version before it changes anything (a refused database is left
// exactly as it was: a version other than SchemaVersion), then creates the tables and indexes and records the
// version on a new database, all in one transaction. A store at SchemaVersion keeps its version and rows.
func initSchema(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback()
	var hasMeta int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'").
		Scan(&hasMeta); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	known, found := "", false
	if hasMeta > 0 {
		err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'schema_version'").Scan(&known)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: read schema version: %w", err)
		}
		found = err == nil
	}
	if found {
		got, err := strconv.Atoi(known)
		if err != nil {
			return fmt.Errorf("store: schema version %q is not a number", known)
		}
		if err := versionErr(got, SchemaVersion); err != nil {
			return err
		}
	}
	for _, ddl := range schema {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("store: create schema: %w", err)
		}
	}
	if !found {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('schema_version', ?)",
			strconv.Itoa(SchemaVersion)); err != nil {
			return fmt.Errorf("store: write schema version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}
