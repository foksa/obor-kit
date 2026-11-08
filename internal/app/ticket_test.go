package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// The sample integration (T-1039, docs/agents/integrations.md): a tracker, a process that runs as the custom role
// tracker (OBOR_ROLE=tracker, roles.tracker.verbs [spec_set, dispatch]), takes a ticket of a fake tracker into obor
// and marks it done once its task merged. It is what an integration script does with obor spec set, obor dispatch and
// obor events --follow --after <cursor> --json, through the same wire calls those commands make.

// ticket is one ticket of the fake tracker.
type ticket struct {
	ID, Title, Task string
	Done            bool
}

// tracker is the sample integration: its identity (the derived name and role a CLI sends), its fake tracker's
// tickets and its own cursor, the seq of the last feed record it handled.
type tracker struct {
	sock, root string
	mu         sync.Mutex
	tickets    map[string]*ticket // by Ref
	cursor     int64
	handled    map[int64]int // feed seq → how often handled (once per cursor)
}

func (tr *tracker) call(method string, c wire.Call) (string, error) {
	c.Root, c.Agent, c.Role, c.CallID = tr.root, "pj-tracker", "tracker", wire.NewCallID()
	return wire.Verdict(context.Background(), tr.sock, method, c)
}

// take is a ticket into obor: its spec (with a Ref: line naming the ticket) and its dispatch.
func (tr *tracker) take(tk *ticket) error {
	spec := fmt.Sprintf("# %s — %s\n\nDifficulty: medium\nRef: %s\n", tk.Task, tk.Title, tk.ID)
	if _, err := tr.call(wire.MethodSpecSet, wire.Call{Task: tk.Task, Spec: spec}); err != nil {
		return err
	}
	tr.mu.Lock()
	tr.tickets[tk.ID] = tk
	tr.mu.Unlock()
	_, err := tr.call(wire.MethodDispatch, wire.Call{Task: tk.Task, Lane: "L-01", Types: []string{"sonnet"}})
	return err
}

// follow reads the feed from its cursor until ctx ends, asking again from the cursor when the feed ends under it (a
// serve restart): a task's merged fact marks its ticket (by the record's Ref) done.
func (tr *tracker) follow(ctx context.Context) {
	for ctx.Err() == nil {
		tr.mu.Lock()
		from := tr.cursor
		tr.mu.Unlock()
		_, err := wire.Feed(ctx, tr.sock, tr.root, from, true, func(f wire.FeedRecord) error {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.handled[f.Seq]++
			if tk := tr.tickets[f.Ref]; tk != nil && f.Kind == event.KindFact && f.Step == event.StepMerged && f.Task == tk.Task {
				tk.Done = true
			}
			tr.cursor = f.Seq
			return nil
		})
		if err != nil && !wire.FeedAgain(err) && !errors.Is(err, context.Canceled) {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// T-1039 e2e on fakes (no agents, no git): the tracker, a custom role with verbs spec_set and dispatch, takes
// TICKET-1 (spec_set with Ref: TICKET-1, then dispatch), follows the feed from its cursor, and on T-1's merged record
// (carrying ref TICKET-1) marks the ticket done; it handled each record once. Without a verb serve refuses it.
func TestSampleIntegrationTicketToMerged(t *testing.T) {
	t.Parallel()
	r := verbsRig(t)
	tr := &tracker{sock: r.sock, root: r.p.Root, tickets: map[string]*ticket{}, handled: map[int64]int{}}
	start := feed(t, r, 0) // the tracker's cursor: where it starts reading, the log's end now
	if len(start) > 0 {
		tr.cursor = start[len(start)-1].Seq
	}
	// Precondition: the tracker's verbs are spec_set and dispatch only: watch is refused.
	if _, err := tr.call(wire.MethodWatch, wire.Call{Task: "T-1"}); err == nil {
		t.Fatal("precondition: the tracker's watch was admitted, want it refused (no verb)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	followed := make(chan struct{})
	go func() { tr.follow(ctx); close(followed) }()

	tk := &ticket{ID: "TICKET-1", Title: "Fix the widget", Task: "T-1"}
	if err := tr.take(tk); err != nil {
		t.Fatalf("take TICKET-1: %v", err)
	}
	if spec, ok, err := store.ReadSpecAt(r.p.Root, "T-1"); err != nil || !ok || spec.SetBy != "pj-tracker" {
		t.Errorf("T-1's spec %+v, %v; want set by pj-tracker", spec, ok)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	tr.mu.Lock()
	done := tk.Done
	tr.mu.Unlock()
	if done {
		t.Fatal("TICKET-1 done before T-1 merged")
	}
	r.runToMerge("T-1", nil)
	merged, _ := r.record("T-1", event.KindFact, event.StepMerged)
	r.waitFor("TICKET-1 marked done", func() bool {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return tk.Done && tr.cursor >= merged.Seq
	})
	cancel()
	<-followed
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for seq, n := range tr.handled {
		if n != 1 || seq <= seqOrZero(start) {
			t.Errorf("record %d handled %d times (cursor start %d); want each record after the cursor once", seq, n, seqOrZero(start))
		}
	}
	if tr.handled[merged.Seq] != 1 {
		t.Errorf("the merged record %d was not handled once: %d", merged.Seq, tr.handled[merged.Seq])
	}
	// A restart of the tracker from its cursor repeats nothing: the feed after it has no merged record of T-1.
	for _, f := range feed(t, r, tr.cursor) {
		if f.Task == "T-1" && f.Step == event.StepMerged {
			t.Errorf("the feed after the cursor %d repeats T-1's merge at %d", tr.cursor, f.Seq)
		}
	}
}

func seqOrZero(fs []wire.FeedRecord) int64 {
	if len(fs) == 0 {
		return 0
	}
	return fs[len(fs)-1].Seq
}
