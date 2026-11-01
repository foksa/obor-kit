package store

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPromptsWriteReadPrune (T-665): a row per fact, read newest first by agent, by task and by job id with a limit,
// read-only once the writer is closed (serve down), and pruned by the ts before the cutoff; a query naming no one of
// an agent, a task or a job, and a row with no seq, agent or kind, are refused.
func TestPromptsWriteReadPrune(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if rows, err := ReadPromptsAt(root, PromptQuery{Agent: "pj-o1"}, 10); err != nil || len(rows) != 0 {
		t.Fatalf("no database: %v %v; want none", rows, err)
	}
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, r := range []PromptRow{
		{Seq: 1, Agent: "pj-o1", Kind: PromptJob, Job: "T-1/1/work", Task: "T-1", TS: at.Format(time.RFC3339), Text: "work one"},
		{Seq: 2, Agent: "pj-o1", Kind: PromptJob, Job: "T-1/1/fix", Task: "T-1", TS: at.Add(time.Hour).Format(time.RFC3339),
			Text: "fix one"},
		{Seq: 3, Agent: "pj-int", Kind: PromptJob, Job: "T-1/1/review", Task: "T-1",
			TS: at.Add(2 * time.Hour).Format(time.RFC3339), Text: "review one"},
		{Seq: 4, Agent: "pj-rev", Kind: PromptBrief, TS: at.Add(3 * time.Hour).Format(time.RFC3339), Text: "[obor lead] brief"},
		{Seq: 5, Agent: "pj-rev", Kind: PromptStart, TS: at.Add(4 * time.Hour).Format(time.RFC3339), Text: "Review RV-1."},
		{Seq: 6, Agent: "pj-o1", Kind: PromptJob, Job: "T-2/1/work", Task: "T-2",
			TS: at.Add(40 * 24 * time.Hour).Format(time.RFC3339), Text: "work two"},
	} {
		if err := WritePrompt(db, r); err != nil {
			t.Fatalf("WritePrompt %+v: %v", r, err)
		}
	}
	if err := WritePrompt(db, PromptRow{Seq: 1, Agent: "pj-o1", Kind: PromptJob, Text: "again"}); err == nil {
		t.Error("a second row for one fact's seq stored")
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
		{"by agent", PromptQuery{Agent: "pj-o1"}, 10, "6 2 1"},
		{"by agent, limited", PromptQuery{Agent: "pj-o1"}, 2, "6 2"},
		{"by task", PromptQuery{Task: "T-1"}, 10, "3 2 1"},
		{"by job", PromptQuery{Job: "T-1/1/fix"}, 10, "2"},
		{"a brief's agent", PromptQuery{Agent: "pj-rev"}, 10, "5 4"},
		{"nothing", PromptQuery{Agent: "pj-nobody"}, 10, ""},
		{"no limit", PromptQuery{Agent: "pj-o1"}, 0, ""},
	} {
		rows, err := ReadPromptsAt(root, c.q, c.limit)
		if err != nil || seqs(rows) != c.want {
			t.Errorf("%s: %q %v; want %q", c.name, seqs(rows), err, c.want)
		}
	}
	rows, err := ReadPromptsAt(root, PromptQuery{Agent: "pj-rev"}, 10)
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
	if err != nil || n != 5 {
		t.Fatalf("PrunePrompts: %d %v; want 5", n, err)
	}
	rows, err = ReadPrompts(db, PromptQuery{Agent: "pj-o1"}, 10)
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
	if err := WritePrompt(db, PromptRow{Seq: 1, Agent: "pj-o1", Kind: PromptJob, TS: "2026-11-01T10:00:00Z",
		Text: "work"}); err != nil {
		t.Errorf("WritePrompt after the migration: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&n); err != nil || n != 1 {
		t.Errorf("events after the migration: %d %v; want the one record", n, err)
	}
}
