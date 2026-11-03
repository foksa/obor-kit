package store

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPromptsWriteReadPrune (T-665, T-670): a row per prompt, read newest first by agent, by task and by job id with a
// limit (a job's rows, one per distinct text its fetch returned, newest first), read-only once the writer is closed
// (serve down), and pruned by the ts before the cutoff; a query naming no one of an agent, a task or a job, and a row
// with no seq, agent or kind, are refused.
func TestPromptsWriteReadPrune(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if rows, err := ReadPromptsAt(root, PromptQuery{Agent: "pj-l1"}, 10); err != nil || len(rows) != 0 {
		t.Fatalf("no database: %v %v; want none", err, rows)
	}
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, r := range []PromptRow{
		{Seq: 1, Agent: "pj-l1", Kind: PromptJob, Job: "T-1/1/work", Task: "T-1", TS: at.Format(time.RFC3339), Rev: 1,
			Text: "work one"},
		{Seq: 2, Agent: "pj-l1", Kind: PromptJob, Job: "T-1/1/fix", Task: "T-1", TS: at.Add(time.Hour).Format(time.RFC3339),
			Text: "fix one"},
		{Seq: 3, Agent: "pj-int", Kind: PromptJob, Job: "T-1/1/review", Task: "T-1",
			TS: at.Add(2 * time.Hour).Format(time.RFC3339), Text: "review one"},
		{Seq: 4, Agent: "pj-rev", Kind: PromptBrief, TS: at.Add(3 * time.Hour).Format(time.RFC3339), Text: "[obor lead] brief"},
		{Seq: 5, Agent: "pj-rev", Kind: PromptStart, TS: at.Add(4 * time.Hour).Format(time.RFC3339), Text: "Review RV-1."},
		{Seq: 6, Agent: "pj-l1", Kind: PromptJob, Job: "T-2/1/work", Task: "T-2",
			TS: at.Add(40 * 24 * time.Hour).Format(time.RFC3339), Text: "work two"},
		// The same job fetched again, its text changed (T-670): a row of its own, under the same delivery fact.
		{Seq: 1, Agent: "pj-l1", Kind: PromptJob, Job: "T-1/1/work", Task: "T-1", TS: at.Add(5 * time.Hour).Format(time.RFC3339),
			Rev: 2, Text: "work one, spec rev 2"},
	} {
		if err := WritePrompt(db, r); err != nil {
			t.Fatalf("WritePrompt %+v: %v", r, err)
		}
	}
	for _, c := range []struct {
		name string
		row  PromptRow
	}{
		{"no seq", PromptRow{Agent: "a", Kind: PromptJob}},
		{"no agent", PromptRow{Seq: 7, Kind: PromptJob}},
		{"no kind", PromptRow{Seq: 7, Agent: "a"}},
		{"another kind", PromptRow{Seq: 7, Agent: "a", Kind: "line"}},
	} {
		if err := WritePrompt(db, c.row); err == nil {
			t.Errorf("%s: stored", c.name)
		}
	}
	db.Close() // serve down: the readers open the file read-only

	seqs := func(rows []PromptRow) string {
		var out []string
		for _, r := range rows {
			out = append(out, strconv.FormatInt(r.Seq, 10))
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name  string
		q     PromptQuery
		limit int
		want  string
	}{
		{"by agent", PromptQuery{Agent: "pj-l1"}, 10, "1 6 2 1"},
		{"by agent, limited", PromptQuery{Agent: "pj-l1"}, 2, "1 6"},
		{"by task", PromptQuery{Task: "T-1"}, 10, "1 3 2 1"},
		{"by job", PromptQuery{Job: "T-1/1/fix"}, 10, "2"},
		{"a job fetched twice", PromptQuery{Job: "T-1/1/work"}, 10, "1 1"},
		{"a brief's agent", PromptQuery{Agent: "pj-rev"}, 10, "5 4"},
		{"nothing", PromptQuery{Agent: "pj-nobody"}, 10, ""},
		{"no limit", PromptQuery{Agent: "pj-l1"}, 0, ""},
	} {
		rows, err := ReadPromptsAt(root, c.q, c.limit)
		if err != nil || seqs(rows) != c.want {
			t.Errorf("%s: %q %v; want %q", c.name, seqs(rows), err, c.want)
		}
	}
	rows, err := ReadPromptsAt(root, PromptQuery{Job: "T-1/1/work"}, 10)
	if err != nil || len(rows) != 2 || rows[0].Text != "work one, spec rev 2" || rows[0].Rev != 2 ||
		rows[1].Text != "work one" || rows[1].Rev != 1 || rows[0].TS != at.Add(5*time.Hour).Format(time.RFC3339) {
		t.Errorf("the job's rows: %+v %v; want its newest text first", rows, err)
	}
	rows, err = ReadPromptsAt(root, PromptQuery{Agent: "pj-rev"}, 10)
	if err != nil || len(rows) != 2 || rows[0].Kind != PromptStart || rows[0].Text != "Review RV-1." || rows[0].Task != "" ||
		rows[1].Kind != PromptBrief || rows[1].TS != at.Add(3*time.Hour).Format(time.RFC3339) {
		t.Errorf("pj-rev's rows: %+v %v", rows, err)
	}

	db, err = OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := PrunePrompts(db, at.Add(14*24*time.Hour))
	if err != nil || n != 6 {
		t.Fatalf("PrunePrompts: %d %v; want 6", n, err)
	}
	rows, err = ReadPrompts(db, PromptQuery{Agent: "pj-l1"}, 10)
	if err != nil || seqs(rows) != "6" {
		t.Errorf("after the prune: %q %v; want 6", seqs(rows), err)
	}
	if n, err := PrunePrompts(db, at.Add(14*24*time.Hour)); err != nil || n != 0 {
		t.Errorf("a second prune: %d %v; want 0", n, err)
	}
}

// TestPromptsQueryRefused: a query that names no one of an agent, a task or a job (or more than one) is an error.
func TestPromptsQueryRefused(t *testing.T) {
	t.Parallel()
	db, err := OpenRW(newPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []PromptQuery{{}, {Agent: "a", Job: "j"}, {Task: "t", Job: "j"}, {Agent: "a", Task: "t"}} {
		if _, err := ReadPrompts(db, q, 10); err == nil {
			t.Errorf("ReadPrompts(%+v): no error", q)
		}
	}
}

// TestOpenRWMigratesTheOldSchema (T-665): a database at the version before prompts (6) opens, gets the prompts table
// and the new version, and keeps its events.
func TestOpenRWMigratesTheOldSchema(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	db, err := OpenRW(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TABLE prompts",
		"UPDATE meta SET value = '6' WHERE key = 'schema_version'",
		"INSERT INTO events VALUES (1, '2026-10-24T11:00:00Z', 'T-1', 1, 'stalled', 'fact', '', NULL)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	db, err = OpenRW(path)
	if err != nil {
		t.Fatalf("OpenRW on schema 6: %v", err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if want := strconv.Itoa(SchemaVersion); version != want {
		t.Errorf("schema_version = %q, want %q after the migration", version, want)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'prompts'").Scan(&n); err != nil || n != 1 {
		t.Errorf("prompts table after the migration: %d %v; want 1", n, err)
	}
	if err := WritePrompt(db, PromptRow{Seq: 1, Agent: "pj-l1", Kind: PromptJob, TS: "2026-11-01T10:00:00Z",
		Text: "work"}); err != nil {
		t.Errorf("WritePrompt after the migration: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&n); err != nil || n != 1 {
		t.Errorf("events after the migration: %d %v; want the one record", n, err)
	}
}

// TestOpenRWMigratesTheOldPromptsKey (T-670): a version-7 database holds the prompts table keyed by its fact's seq; it
// opens at the new version with the table keyed by its own id, its rows kept (rev none) and a second row for one job's
// fact accepted.
func TestOpenRWMigratesTheOldPromptsKey(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	db, err := OpenRW(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TABLE prompts",
		`CREATE TABLE prompts (seq INTEGER PRIMARY KEY, agent TEXT NOT NULL, kind TEXT NOT NULL, job TEXT NOT NULL,
			task TEXT NOT NULL, ts TEXT NOT NULL, text TEXT NOT NULL)`,
		"INSERT INTO prompts VALUES (1, 'pj-l1', 'job', 'T-1/1/work', 'T-1', '2026-10-24T11:00:00Z', 'work one')",
		"INSERT INTO prompts VALUES (2, 'pj-int', 'job', 'T-1/1/review', 'T-1', '2026-10-24T11:01:00Z', 'review one')",
		"UPDATE meta SET value = '7' WHERE key = 'schema_version'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	db, err = OpenRW(path)
	if err != nil {
		t.Fatalf("OpenRW on schema 7: %v", err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if want := strconv.Itoa(SchemaVersion); version != want {
		t.Errorf("schema_version = %q, want %q after the migration", version, want)
	}
	rows, err := ReadPrompts(db, PromptQuery{Task: "T-1"}, 10)
	if err != nil || len(rows) != 2 || rows[0].Seq != 2 || rows[0].Text != "review one" || rows[0].Rev != 0 ||
		rows[1].Seq != 1 || rows[1].Text != "work one" || rows[1].Agent != "pj-l1" ||
		rows[1].TS != "2026-10-24T11:00:00Z" {
		t.Fatalf("the migrated rows: %+v %v; want the old two, newest first", rows, err)
	}
	if err := WritePrompt(db, PromptRow{Seq: 1, Agent: "pj-l1", Kind: PromptJob, Job: "T-1/1/work", Task: "T-1",
		TS: "2026-10-25T11:00:00Z", Rev: 2, Text: "work one, spec rev 2"}); err != nil {
		t.Fatalf("a second row for one fact's job after the migration: %v", err)
	}
	rows, err = ReadPrompts(db, PromptQuery{Job: "T-1/1/work"}, 10)
	if err != nil || len(rows) != 2 || rows[0].Text != "work one, spec rev 2" || rows[1].Text != "work one" {
		t.Errorf("the job's rows after a second write: %+v %v", rows, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'prompts_v7'").Scan(&n); err != nil || n != 0 {
		t.Errorf("the old table after the migration: %d %v; want none", n, err)
	}
}
