package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/store"
)

func TestReady(t *testing.T) {
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
func openLog(t *testing.T, root string) (*Log, []Record) {
	t.Helper()
	db, err := store.OpenRW(DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l, recs, err := OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	return l, recs
}

func fact(t *testing.T, task, step, reason string) Record {
	t.Helper()
	r := Record{Task: task, Step: step, Kind: KindFact}
	if reason != "" {
		b, err := json.Marshal(FactData{Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		r.Data = b
	}
	return r
}

func TestLogAppendAndReplay(t *testing.T) {
	root := logRoot(t)
	l, recs := openLog(t, root)
	if len(recs) != 0 {
		t.Fatalf("new log: %v", recs)
	}
	r, err := l.Append(fact(t, "T-1", StepStalled, "idle"), logNow)
	if err != nil || r.Seq != 1 || r.TS != "2026-10-24T11:00:00Z" {
		t.Fatalf("Append = %+v, %v", r, err)
	}
	if _, err := l.Append(fact(t, "T-1", StepResumed, ""), logNow); err != nil {
		t.Fatal(err)
	}
	l, recs = openLog(t, root)
	if len(recs) != 2 || recs[0].String() != "stalled{idle}" || recs[1].String() != "resumed" || recs[1].Data != nil {
		t.Fatalf("replay = %+v", recs)
	}
	if r, err := l.Append(fact(t, "T-2", StepNeedsYou, ""), logNow); err != nil || r.Seq != 3 {
		t.Fatalf("Append after replay = %+v, %v", r, err)
	}
	if recs, err := ReadLog(root); err != nil || len(recs) != 3 || recs[2].Task != "T-2" {
		t.Errorf("ReadLog after append = %+v, %v", recs, err)
	}
}

// A failed insert is the append's error (Err, log_error) and leaves no record; the retry reuses its seq.
func TestAppendInsertFailure(t *testing.T) {
	root := logRoot(t)
	l, _ := openLog(t, root)
	if _, err := l.db.Exec(`CREATE TRIGGER full BEFORE INSERT ON events
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(fact(t, "T-1", StepStalled, "idle"), logNow); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("the failed insert appended: %v", err)
	}
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Errorf("Err = %v, want the insert's error", err)
	}
	if _, err := l.db.Exec("DROP TRIGGER full"); err != nil {
		t.Fatal(err)
	}
	if r, err := l.Append(fact(t, "T-2", StepStalled, "idle"), logNow); err != nil || r.Seq != 1 {
		t.Fatalf("retry = %+v, %v; want seq 1", r, err)
	}
	if l.Err() != nil {
		t.Errorf("Err after a good append = %v, want nil", l.Err())
	}
	if recs, err := ReadLog(root); err != nil || len(recs) != 1 || recs[0].Task != "T-2" {
		t.Errorf("log = %+v, %v; want only T-2's record", recs, err)
	}
}

func clock() time.Time { return logNow }

func TestDoRunsOnceAndReturnsStoredResult(t *testing.T) {
	root := logRoot(t)
	l, _ := openLog(t, root)
	runs := 0
	effect := func(context.Context) (any, error) {
		runs++
		return map[string]string{"sha": "abc"}, nil
	}
	in := Intent{Key: Key{"T-1", 1, "publish"}, Class: ClassGit, Data: map[string]string{"target": "main"}}
	got, err := l.Do(context.Background(), in, clock, effect)
	if err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Fatalf("Do = %s, %v, runs %d", got, err, runs)
	}
	if got, err := l.Do(context.Background(), in, clock, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("repeated Do = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}

	// After a restart the stored result still answers the key; nothing is pending.
	l, recs := openLog(t, root)
	if len(recs) != 2 || recs[0].Kind != KindIntent || recs[0].Class != ClassGit ||
		string(recs[0].Data) != `{"target":"main"}` || recs[1].Kind != KindResult {
		t.Fatalf("replay = %+v", recs)
	}
	if p := l.Pending(); len(p) != 0 {
		t.Errorf("pending after a result = %+v", p)
	}
	if got, err := l.Do(context.Background(), in, clock, effect); err != nil || string(got) != `{"sha":"abc"}` || runs != 1 {
		t.Errorf("Do after replay = %s, %v, runs %d; want the stored result and no run", got, err, runs)
	}
}

// A failed effect writes no result: the intent is pending and a retry runs the effect again.
func TestDoFailedEffectStaysPending(t *testing.T) {
	l, _ := openLog(t, logRoot(t))
	in := Intent{Key: Key{"T-1", 1, "prompt"}, Class: ClassPrompt}
	if _, err := l.Do(context.Background(), in, clock, func(context.Context) (any, error) { return nil, errors.New("herdr down") }); err == nil {
		t.Fatal("a failed effect returned no error")
	}
	if p := l.Pending(); len(p) != 1 || p[0].Step != "prompt" || p[0].Class != ClassPrompt {
		t.Errorf("pending = %+v", p)
	}
	runs := 0
	if _, err := l.Do(context.Background(), in, clock, func(context.Context) (any, error) { runs++; return "sent", nil }); err != nil || runs != 1 {
		t.Errorf("retry = %v, runs %d", err, runs)
	}
	if p := l.Pending(); len(p) != 0 {
		t.Errorf("pending after the retry = %+v", p)
	}
}

// A process killed after the effect and before its result: replay lists the intent as pending with its class.
func TestDoCrashBetweenEffectAndResult(t *testing.T) {
	if root := os.Getenv("OBOR_TEST_DO_CRASH"); root != "" {
		db, err := store.OpenRW(DBFile(root))
		if err != nil {
			os.Exit(2)
		}
		l, _, err := OpenLog(db)
		if err != nil {
			os.Exit(2)
		}
		path := filepath.Join(root, "effect")
		l.Do(context.Background(), Intent{Key: Key{"T-1", 2, "start"}, Class: ClassStart, Data: map[string]string{"pane": "w1:p1"}},
			clock, func(context.Context) (any, error) {
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
	if len(recs) != 1 || recs[0].Kind != KindIntent {
		t.Fatalf("replay = %+v; want the intent only", recs)
	}
	p := l.Pending()
	if len(p) != 1 || p[0].Task != "T-1" || p[0].Attempt != 2 || p[0].Step != "start" || p[0].Class != ClassStart ||
		string(p[0].Data) != `{"pane":"w1:p1"}` {
		t.Errorf("pending = %+v", p)
	}
}

// Dump writes the table as JSONL, one record per line in seq order, each line the record's JSON.
func TestDump(t *testing.T) {
	root := logRoot(t)
	l, _ := openLog(t, root)
	want := []Record{
		{Seq: 1, TS: "2026-10-24T11:00:00Z", Task: "T-1", Attempt: 1, Step: "gate", Kind: KindIntent, Class: ClassGit,
			Data: json.RawMessage(`{"kind":"lane","lane":"O-01"}`)},
		{Seq: 2, TS: "2026-10-24T11:02:00Z", Task: "T-1", Attempt: 1, Step: "gate", Kind: KindResult,
			Data: json.RawMessage(`{"outcome":"green"}`)},
		{Seq: 3, TS: "2026-10-24T11:03:00Z", Task: "T-2", Step: StepResumed, Kind: KindFact},
	}
	if err := insert(l.db, want...); err != nil {
		t.Fatal(err)
	}
	var dump bytes.Buffer
	if err := Dump(&dump, root); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, r := range want {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b = append(append(b, line...), '\n')
	}
	if dump.String() != string(b) {
		t.Errorf("dump =\n%s\nwant\n%s", dump.Bytes(), b)
	}
}

// Backup without a serve (no socket) copies the database directly; a project serve never loaded has nothing to back
// up; a serve that has the project loaded takes the backup itself.
func TestBackup(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "none.sock")
	day := time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC)
	if reply, err := Backup(context.Background(), sock, logRoot(t), day); reply != "" || err != nil {
		t.Errorf("backup without a database = %q, %v; want nothing", reply, err)
	}

	root := logRoot(t)
	l, _ := openLog(t, root)
	if _, err := l.Append(fact(t, "T-1", StepStalled, "idle"), logNow); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(BackupDir(root), "obor-2026-10-27.db")
	stopFailure := filepath.Join(root, ".obor", "state", "stop-failure.log")
	if err := os.WriteFile(stopFailure, []byte("{\"at\":\"2026-10-12T02:59:59Z\",\"error\":\"old\"}\n{\"at\":\"2026-10-26T03:00:00Z\",\"error\":\"fresh\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply, err := Backup(context.Background(), sock, root, day)
	if err != nil || reply != "BACKUP "+path {
		t.Fatalf("direct backup = %q, %v; want BACKUP %s", reply, err, path)
	}
	if recs, err := ReadLog(backupRoot(t, path)); err != nil || len(recs) != 1 || recs[0].Task != "T-1" {
		t.Errorf("the backup's log = %+v, %v; want T-1's record", recs, err)
	}
	if b, err := os.ReadFile(stopFailure); err != nil || string(b) != "{\"at\":\"2026-10-26T03:00:00Z\",\"error\":\"fresh\"}\n" {
		t.Errorf("stop-failure log after daily backup = %q, %v; want only the fresh entry", b, err)
	}

	h := newHarness(t)
	h.connect()
	h.tick(11 * time.Minute) // a record to back up
	if reply, err := h.a.backup(Call{Root: h.root}); err != nil || !strings.HasSuffix(reply, "obor-2026-10-24.db") {
		t.Fatalf("serve's backup = %q, %v", reply, err)
	}
	if recs, err := ReadLog(backupRoot(t, filepath.Join(BackupDir(h.root), "obor-2026-10-24.db"))); err != nil || len(recs) == 0 {
		t.Errorf("serve's backup log = %+v, %v; want its records", recs, err)
	}
	if _, err := h.a.backup(Call{Root: logRoot(t)}); err == nil {
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
	if err := os.WriteFile(DBFile(root), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
