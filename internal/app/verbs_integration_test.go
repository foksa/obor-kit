//go:build integration

package app_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/store"
)

// The feed: --after N returns N+1 … in order, each once, the same records serve's store holds (the down path reads
// them with the same FeedSince); a cursor past the end is no record and no error; a follow gets a record appended
// after it started, and only that one.
func TestEventFeed(t *testing.T) {
	t.Parallel()
	r := verbsRig(t)
	r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\nRef: TICKET-7\n")
	r.setSpec("s2", "# T-1 — Work again\n\nDifficulty: medium\nRef: TICKET-7\n")
	r.setSpec("s2b", "# T-1 — Work once more\n\nDifficulty: medium\nRef: TICKET-7\n")
	all := feed(t, r, 0)
	if len(all) < 3 {
		t.Fatalf("precondition: %d records in the log, want at least 3", len(all))
	}
	for i, rec := range all {
		if rec.Seq != int64(i+1) {
			t.Fatalf("feed from 0: record %d has seq %d, want %d (in order, none skipped)", i, rec.Seq, i+1)
		}
	}
	n := all[len(all)/2].Seq
	after := feed(t, r, n)
	if len(after) != len(all)-int(n) || after[0].Seq != n+1 || !slices.EqualFunc(after, all[n:], sameRecord) {
		t.Errorf("feed --after %d: %d records from seq %d, want the %d from %d", n, len(after), seqOf(after), len(all)-int(n), n+1)
	}
	if got := feed(t, r, all[len(all)-1].Seq+100); len(got) != 0 {
		t.Errorf("a cursor past the end: %d records, want none", len(got))
	}
	// The store read (obor events with serve down) is the same records.
	db, err := store.OpenAt(r.p.Root)
	if err != nil || db == nil {
		t.Fatalf("store: %v", err)
	}
	defer db.Close()
	fromStore, err := wire.FeedSince(db, n, wire.FeedPage)
	if err != nil || !slices.EqualFunc(fromStore, after, sameRecord) {
		t.Errorf("store read after %d: %d records, %v; want serve's %d", n, len(fromStore), err, len(after))
	}
	// A task's records carry its spec's Ref: line.
	if i := slices.IndexFunc(all, func(f wire.FeedRecord) bool { return f.Task == "T-1" }); i < 0 || all[i].Ref != "TICKET-7" {
		t.Errorf("T-1's records: %+v; want ref TICKET-7", all)
	}

	// Follow: started at the end, it gets the record appended after it, and nothing it had.
	last := all[len(all)-1].Seq
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan wire.FeedRecord, 16)
	ended := make(chan error, 1)
	go func() {
		_, err := wire.Feed(ctx, r.sock, r.p.Root, last, true, func(f wire.FeedRecord) error { got <- f; return nil })
		ended <- err
	}()
	select {
	case f := <-got:
		t.Fatalf("follow before any append: got seq %d, want nothing", f.Seq)
	case <-time.After(200 * time.Millisecond):
	}
	r.setSpec("s3", "# T-1 — Work a third time\n\nDifficulty: medium\nRef: TICKET-7\n")
	select {
	case f := <-got:
		if f.Seq != last+1 || f.Task != "T-1" || f.Ref != "TICKET-7" {
			t.Errorf("follow: got %+v; want seq %d of T-1, ref TICKET-7", f, last+1)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow: the record appended after it started never came")
	}
	cancel()
	if err := <-ended; err == nil {
		t.Error("follow ended without an error; want the context's")
	}
}

func sameRecord(a, b wire.FeedRecord) bool {
	return a.Seq == b.Seq && a.Task == b.Task && a.Step == b.Step && a.Kind == b.Kind && string(a.Data) == string(b.Data) &&
		a.Ref == b.Ref
}

func seqOf(fs []wire.FeedRecord) int64 {
	if len(fs) == 0 {
		return 0
	}
	return fs[0].Seq
}

// feed is the served feed after seq without follow (fatal on an error).
func feed(t *testing.T, r *loopRig, seq int64) []wire.FeedRecord {
	t.Helper()
	var out []wire.FeedRecord
	if inBubble(r.t) {
		r.t.Fatal("the event feed needs the unix socket: this test stays outside a bubble")
	}
	last, err := wire.Feed(context.Background(), r.sock, r.p.Root, seq, false, func(f wire.FeedRecord) error {
		out = append(out, f)
		return nil
	})
	if err != nil {
		t.Fatalf("feed after %d: %v", seq, err)
	}
	if len(out) > 0 && last != out[len(out)-1].Seq || len(out) == 0 && last != seq {
		t.Errorf("feed after %d: cursor %d, want the last record's seq", seq, last)
	}
	return out
}
