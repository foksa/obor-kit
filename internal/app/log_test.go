package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/store"
)

func TestReady(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{"idle": true, "done": true, "working": false, "blocked": false, "unknown": false, "": false} {
		if Ready(status) != want {
			t.Errorf("Ready(%q) = %v, want %v", status, !want, want)
		}
	}
}

var logNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("x", 3600))

// logRoot is a project root with an empty state dir: no database yet.
func logRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// openLog opens root's database as serve does and replays it; closed when the test ends.
func openLog(t *testing.T, root string) (*event.Log, []event.Record) {
	t.Helper()
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l, recs, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	return l, recs
}

func fact(t *testing.T, task, step, reason string) event.Record {
	t.Helper()
	r := event.Record{Task: task, Step: step, Kind: event.KindFact}
	if reason != "" {
		b, err := json.Marshal(event.FactData{Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		r.Data = b
	}
	return r
}

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

func clock() time.Time { return logNow }

// logDo is one effect under its key, the shape serve uses (App.do): Begin, the effect outside the log, Finish; a key
// with a result gets it and runs nothing, a failed effect writes no result (the intent stays pending).
func logDo(l *event.Log, in event.Intent, effect func(context.Context) (any, error)) (json.RawMessage, error) {
	r, done, _, err := l.Begin(in, clock())
	if err != nil {
		return nil, err
	}
	if done {
		return r.Data, nil
	}
	v, err := effect(context.Background())
	if err != nil {
		l.Finish(in.Key, nil, false, clock())
		return nil, err
	}
	res, err := l.Finish(in.Key, v, true, clock())
	return res.Data, err
}

func TestDoRunsOnceAndReturnsStoredResult(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	l, _ := openLog(t, root)
	runs := 0
	effect := func(context.Context) (any, error) {
		runs++
		return map[string]string{"sha": "abc"}, nil
	}
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: "publish"}, Class: event.ClassGit, Data: map[string]string{"target": "main"}}
	got, err := logDo(l, in, effect)
	if err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Fatalf("Do = %s, %v, runs %d", got, err, runs)
	}
	if got, err := logDo(l, in, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("repeated Do = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}

	// After a restart the stored result still answers the key; nothing is pending.
	l, recs := openLog(t, root)
	if len(recs) != 2 || recs[0].Kind != event.KindIntent || recs[0].Class != event.ClassGit ||
		string(recs[0].Data) != `{"target":"main"}` || recs[1].Kind != event.KindResult {
		t.Fatalf("replay = %+v", recs)
	}
	if p := l.Pending(); len(p) != 0 {
		t.Errorf("pending after a result = %+v", p)
	}
	if got, err := logDo(l, in, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("Do after replay = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}
}

// A failed effect writes no result: the intent is pending and a retry runs the effect again.
func TestDoFailedEffectStaysPending(t *testing.T) {
	t.Parallel()
	l, _ := openLog(t, logRoot(t))
	in := event.Intent{Key: event.Key{Task: "T-1", Attempt: 1, Step: "prompt"}, Class: event.ClassPrompt}
	if _, err := logDo(l, in, func(context.Context) (any, error) { return nil, errors.New("herdr down") }); err == nil {
		t.Fatal("a failed effect returned no error")
	}
	if p := l.Pending(); len(p) != 1 || p[0].Step != "prompt" || p[0].Class != event.ClassPrompt {
		t.Errorf("pending = %+v", p)
	}
	runs := 0
	if _, err := logDo(l, in, func(context.Context) (any, error) { runs++; return "sent", nil }); err != nil || runs != 1 {
		t.Errorf("retry = %v, runs %d", err, runs)
	}
	if p := l.Pending(); len(p) != 0 {
		t.Errorf("pending after the retry = %+v", p)
	}
}

// A process killed after the effect and before its result: replay lists the intent as pending with its class.
func TestDoCrashBetweenEffectAndResult(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("OBOR_TEST_DO_CRASH"); root != "" {
		db, err := store.OpenRW(event.DBFile(root))
		if err != nil {
			os.Exit(2)
		}
		l, _, err := event.OpenLog(db)
		if err != nil {
			os.Exit(2)
		}
		path := filepath.Join(root, "effect")
		logDo(l, event.Intent{Key: event.Key{Task: "T-1", Attempt: 2, Step: "start"}, Class: event.ClassStart, Data: map[string]string{"pane": "w1:p1"}},
			func(context.Context) (any, error) {
				os.WriteFile(path, nil, 0o644)
				os.Exit(3) // killed after the effect, before the result
				return nil, nil
			})
		os.Exit(4)
	}
	root := logRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDoCrashBetweenEffectAndResult$")
	cmd.Env = append(os.Environ(), "OBOR_TEST_DO_CRASH="+root)
	var ee *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("child = %v, want exit 3 inside the effect", err)
	}
	if _, err := os.Stat(filepath.Join(root, "effect")); err != nil {
		t.Fatalf("precondition: the effect ran: %v", err)
	}
	l, recs := openLog(t, root)
	if len(recs) != 1 || recs[0].Kind != event.KindIntent {
		t.Fatalf("replay = %+v; want the intent only", recs)
	}
	p := l.Pending()
	if len(p) != 1 || p[0].Task != "T-1" || p[0].Attempt != 2 || p[0].Step != "start" || p[0].Class != event.ClassStart ||
		string(p[0].Data) != `{"pane":"w1:p1"}` {
		t.Errorf("pending = %+v", p)
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
	path := filepath.Join(wire.BackupDir(root), "obor-2026-10-27.db")
	stopFailure := filepath.Join(root, ".obor", "state", "stop-failure.log")
	if err := os.WriteFile(stopFailure, []byte("{\"at\":\"2026-10-12T02:59:59Z\",\"error\":\"old\"}\n{\"at\":\"2026-10-26T03:00:00Z\",\"error\":\"fresh\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply, err := wire.Backup(ctx, sock, root, day)
	if err != nil || reply != "BACKUP "+path {
		t.Fatalf("direct backup = %q, %v; want BACKUP %s", reply, err, path)
	}
	if recs, err := event.ReadLog(backupRoot(t, path)); err != nil || len(recs) != 1 || recs[0].Task != "T-1" {
		t.Errorf("the backup's log = %+v, %v; want T-1's record", recs, err)
	}
	if b, err := os.ReadFile(stopFailure); err != nil || string(b) != "{\"at\":\"2026-10-26T03:00:00Z\",\"error\":\"fresh\"}\n" {
		t.Errorf("stop-failure log after daily backup = %q, %v; want only the fresh entry", b, err)
	}

	h := newHarness(t)
	h.connect()
	h.tick(11 * time.Minute) // a record to back up
	// T-665: the nightly backup prunes the prompts older than 14 days, keeps the rest.
	for _, r := range []store.PromptRow{
		{Seq: 1, Agent: "pj-l-01", Kind: store.PromptJob, Job: "T-1/1/work", Task: "T-1", TS: "2026-10-10T12:00:00Z", Text: "old"},
		{Seq: 2, Agent: "pj-l-01", Kind: store.PromptJob, Job: "T-1/2/work", Task: "T-1", TS: "2026-10-24T12:00:00Z", Text: "fresh"},
	} {
		if err := store.WritePrompt(h.a.p.log.DB(), r); err != nil {
			t.Fatal(err)
		}
	}
	if reply,
		err := h.a.backup(wire.BackupReq{Envelope: wire.Envelope{Root: h.root}}); err != nil || !strings.HasSuffix(reply, "obor-2026-10-24.db") {
		t.Fatalf("serve's backup = %q, %v", reply, err)
	}
	if rows, err := store.ReadPrompts(h.a.p.log.DB(), store.PromptQuery{Task: "T-1"}, 10); err != nil || len(rows) != 1 ||
		rows[0].Text != "fresh" {
		t.Errorf("prompts after the nightly backup: %+v, %v; want only the fresh one (14 days kept)", rows, err)
	}
	if recs, err := event.ReadLog(backupRoot(t, filepath.Join(wire.BackupDir(h.root), "obor-2026-10-24.db"))); err != nil || len(recs) == 0 {
		t.Errorf("serve's backup log = %+v, %v; want its records", recs, err)
	}
	if _, err := h.a.backup(wire.BackupReq{Envelope: wire.Envelope{Root: logRoot(t)}}); err == nil {
		t.Error("serve backed up a project it has not loaded")
	}
}

// backupRoot is a project root whose database is a copy of the backup at path (ReadLog reads a root's).
func backupRoot(t *testing.T, path string) string {
	t.Helper()
	root := logRoot(t)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(event.DBFile(root), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
