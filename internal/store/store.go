// Package store opens a project's SQLite database. serve is the only writer: it opens the
// database with OpenRW; hooks and CLIs open it with OpenRO, read-only, so they work while serve is down or writing.
// Tables: meta (the schema version), events (the event log, T-435; internal/app's Log reads and writes it), lanes
// (each lane's dispatched task, serve's projection of the log, T-439; lanes.go) and specs (each task's spec, T-440;
// specs.go).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the driver: cgo-free, so CGO_ENABLED=0 builds work
)

const (
	// SchemaVersion is the schema this binary knows. OpenRW refuses a newer one: the database is per project, and a
	// obor that wrote it may be newer than this binary.
	SchemaVersion = 6
	// upgradeFrom is the oldest schema OpenRW opens: a store from before v0.12 (schema 2–5) is refused, and upgrades
	// through v0.12.0 first.
	upgradeFrom = 6

	writerBusyTimeout = 5 * time.Second        // serve waits for a reader's snapshot to end, it does not fail
	readerBusyTimeout = 500 * time.Millisecond // a hook fails fast instead of holding its caller
	openTimeout       = 5 * time.Second
	backupTimeout     = 10 * time.Minute
)

// OpenRW opens path as serve's only writer: WAL (readers never wait on the writer), synchronous=FULL (an effect is
// on disk before its result is recorded) and one connection (the driver is not safe for several goroutines on one
// connection; the app serialises its writes). A new file (and its directory) gets the schema; a newer schema is
// refused.
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

// BackupDaily writes db's backup for day to dir/obor-<YYYY-MM-DD>.db (a backup of that day already there is
// replaced) and removes all but the newest keep of them (obor nightly).
func BackupDaily(db *sql.DB, dir string, day time.Time, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "obor-"+day.Format("2006-01-02")+".db")
	tmp := path + ".tmp"
	_ = os.Remove(tmp) // a backup that died midway: VACUUM INTO needs a new file
	if err := Backup(db, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	old, err := filepath.Glob(filepath.Join(dir, "obor-????-??-??.db"))
	if err != nil {
		return "", err
	}
	sort.Strings(old) // by date: the name's only variable part
	for len(old) > keep {
		if err := os.Remove(old[0]); err != nil {
			return "", err
		}
		old = old[1:]
	}
	return path, nil
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
// tasks' specs (specs.go).
var schema = []string{
	"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
	`CREATE TABLE IF NOT EXISTS events (seq INTEGER PRIMARY KEY, ts TEXT NOT NULL, task TEXT NOT NULL,
		attempt INTEGER NOT NULL, step TEXT NOT NULL, kind TEXT NOT NULL, class TEXT NOT NULL, data TEXT)`,
	`CREATE TABLE IF NOT EXISTS lanes (lane TEXT PRIMARY KEY, task TEXT NOT NULL, branch TEXT NOT NULL,
		agent TEXT NOT NULL, base TEXT NOT NULL, head TEXT NOT NULL, dispatched_at INTEGER NOT NULL,
		finished INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS specs (task TEXT PRIMARY KEY, text TEXT NOT NULL, rev INTEGER NOT NULL,
		set_by TEXT NOT NULL, set_at TEXT NOT NULL)`,
}

// initSchema checks an existing database's schema version before it changes anything (a refused database is left
// exactly as it was: a version older than upgradeFrom or newer than SchemaVersion), then creates the tables and
// records the version on a new database, all in one transaction.
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
		switch {
		case err != nil:
			return fmt.Errorf("store: schema version %q is not a number", known)
		case got > SchemaVersion:
			return fmt.Errorf("store: schema version %d is newer than this obor (%d): upgrade obor", got, SchemaVersion)
		case got < upgradeFrom:
			return fmt.Errorf("store: schema version %d is older than this obor (%d): upgrade through v0.12.0 first",
				got, SchemaVersion)
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
