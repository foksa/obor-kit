package app

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

func TestLogAppendAndReplay(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, recs := openLog(t, root)
	if len(recs) != 0 {
		t.Fatalf("new log: %v", recs)
	}
	r, err := l.Append(fact(t, "T-1", event.StepStalled, "idle"), logNow)
	if err != nil || r.Seq != 1 || r.TS != "2026-10-24T11:00:00Z" {
		t.Fatalf("Append = %+v, %v", r, err)
	}
	if _, err := l.Append(fact(t, "T-1", event.StepResumed, ""), logNow); err != nil {
		t.Fatal(err)
	}
	l, recs = openLog(t, root)
	if len(recs) != 2 || recs[0].String() != "stalled{idle}" || recs[1].String() != "resumed" || recs[1].Data != nil {
		t.Fatalf("replay = %+v", recs)
	}
	if r, err := l.Append(fact(t, "T-2", event.StepNeedsYou, ""), logNow); err != nil || r.Seq != 3 {
		t.Fatalf("Append after replay = %+v, %v", r, err)
	}
	if recs, err := event.ReadLog(root); err != nil || len(recs) != 3 || recs[2].Task != "T-2" {
		t.Errorf("ReadLog after append = %+v, %v", recs, err)
	}
}

// A failed insert is the append's error (Err, log_error) and leaves no record; the retry reuses its seq.
func TestAppendInsertFailure(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	if _, err := l.DB().Exec(`CREATE TRIGGER full BEFORE INSERT ON events
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(fact(t, "T-1", event.StepStalled, "idle"), logNow); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("the failed insert appended: %v", err)
	}
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Errorf("Err = %v, want the insert's error", err)
	}
	if _, err := l.DB().Exec("DROP TRIGGER full"); err != nil {
		t.Fatal(err)
	}
	if r, err := l.Append(fact(t, "T-2", event.StepStalled, "idle"), logNow); err != nil || r.Seq != 1 {
		t.Fatalf("retry = %+v, %v; want seq 1", r, err)
	}
	if l.Err() != nil {
		t.Errorf("Err after a good append = %v, want nil", l.Err())
	}
	if recs, err := event.ReadLog(root); err != nil || len(recs) != 1 || recs[0].Task != "T-2" {
		t.Errorf("log = %+v, %v; want only T-2's record", recs, err)
	}
}

// Backup without a serve (no socket) copies the database directly; a project serve never loaded has nothing to back
// up; a serve that has the project loaded takes the backup itself.
func TestBackup(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "none.sock")
	day := time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC)
	// No serve listens on sock: the calls end at once (T-685), not at the deadline table's 15 s.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if reply, err := wire.Backup(ctx, sock, logRoot(t), day); reply != "" || err != nil {
		t.Errorf("backup without a database = %q, %v; want nothing", reply, err)
	}

	root := logRoot(t)
	l, _ := openLog(t, root)
	if _, err := l.Append(fact(t, "T-1", event.StepStalled, "idle"), logNow); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wire.BackupDir(root), "obor-2026-10-27.db.gz")
	reply, err := wire.Backup(ctx, sock, root, day)
	if err != nil || reply != "BACKUP "+path {
		t.Fatalf("direct backup = %q, %v; want BACKUP %s", reply, err, path)
	}
	if recs, err := event.ReadLog(backupRoot(t, path)); err != nil || len(recs) != 1 || recs[0].Task != "T-1" {
		t.Errorf("the backup's log = %+v, %v; want T-1's record", recs, err)
	}

	h := newHarness(t)
	h.connect()
	h.tick(11 * time.Minute) // a record to back up
	// T-665, T-1092: the nightly backup prunes the prompts older than 3 days, keeps the rest.
	for _, r := range []store.PromptRow{
		{Seq: 1, Agent: "pj-l-01", Kind: store.PromptJob, Job: "T-1/1/work", Task: "T-1", TS: "2026-10-10T12:00:00Z", Text: "old"},
		// 4 days old: kept under the old 14 days, pruned under the 3 this test holds (T-1092).
		{Seq: 3, Agent: "pj-l-01", Kind: store.PromptJob, Job: "T-1/3/work", Task: "T-1", TS: "2026-10-20T12:00:00Z", Text: "stale"},
		{Seq: 2, Agent: "pj-l-01", Kind: store.PromptJob, Job: "T-1/2/work", Task: "T-1", TS: "2026-10-22T12:00:00Z", Text: "fresh"},
	} {
		if err := store.WritePrompt(h.a.p.log.DB(), r); err != nil {
			t.Fatal(err)
		}
	}
	if reply,
		err := h.a.backup(wire.BackupReq{Envelope: wire.Envelope{Root: h.root}}); err != nil || !strings.HasSuffix(reply, "obor-2026-10-24.db.gz") {
		t.Fatalf("serve's backup = %q, %v", reply, err)
	}
	if rows, err := store.ReadPrompts(h.a.p.log.DB(), store.PromptQuery{Task: "T-1"}, 10); err != nil || len(rows) != 1 ||
		rows[0].Text != "fresh" {
		t.Errorf("prompts after the nightly backup: %+v, %v; want only the fresh one (3 days kept)", rows, err)
	}
	if recs, err := event.ReadLog(backupRoot(t, filepath.Join(wire.BackupDir(h.root), "obor-2026-10-24.db.gz"))); err != nil || len(recs) == 0 {
		t.Errorf("serve's backup log = %+v, %v; want its records", recs, err)
	}
	if _, err := h.a.backup(wire.BackupReq{Envelope: wire.Envelope{Root: logRoot(t)}}); err == nil {
		t.Error("serve backed up a project it has not loaded")
	}
}

// backupRoot is a project root whose database is the gunzip'd backup at path (ReadLog reads a root's).
func backupRoot(t *testing.T, path string) string {
	t.Helper()
	root := logRoot(t)
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
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(event.DBFile(root), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
