package app

import (
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"obor/internal/store"
)

// Two passes logging a brief's seen work at once (the merge loop's and the watchdog's) record its fact worked once:
// the brief is claimed under mu while its fact is recorded (T-615 attempt 3; the gate saw two).
func TestLogWorkedTwoPassesAtOnceRecordOneFact(t *testing.T) {
	t.Parallel()
	root := logRoot(t)
	db, err := store.OpenRW(DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := OpenLog(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	a := &App{opt: Options{Now: func() time.Time { return logNow }, Out: io.Discard}}
	p := &proj{name: "proj", log: log, folded: newFolded(), out: newOutbox("pj-lead", "")}
	const rounds = 40
	for i := range rounds {
		agent := "pj-r" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		b := &brief{seq: int64(1000 + i), worked: true}
		p.out.briefs[agent] = b
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); a.logWorked(p) }()
		}
		wg.Wait()
		if !b.logged || b.logging {
			t.Fatalf("round %d: brief logged %v, logging %v; want its fact folded and no claim left", i, b.logged, b.logging)
		}
	}
	db.Close()

	db, err = store.OpenRW(DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, recs, err := OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	per := map[int64]int{}
	for _, r := range recs {
		if r.Task == MsgTask && r.Step == StepWorked {
			var d workedData
			_ = json.Unmarshal(r.Data, &d)
			per[d.Brief]++
		}
	}
	if len(per) != rounds {
		t.Fatalf("worked facts for %d briefs, want %d: %v", len(per), rounds, per)
	}
	for seq, n := range per {
		if n != 1 {
			t.Errorf("brief %d: %d worked facts, want 1", seq, n)
		}
	}
}
