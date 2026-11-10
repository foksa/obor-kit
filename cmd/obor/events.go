package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/store"
)

// eventsCmd is `obor events [--follow] [--after <seq>] [--json]`: the project's event log after the cursor
// <seq> (0: from the first record), one line per record in seq order, each carrying its seq so an integration keeps
// its own cursor. --json prints each as a JSON line (wire.FeedRecord: the record and its task's spec Ref:); else "<seq>
// <ts> <task> <kind> <step>[ ref <ref>][ — <line>]". Serve up: its feed (wire.Feed); serve down: the store, the same
// records (design rule 7). --follow keeps on: serve's stream, and across serve's restart (or while it is down) the
// store's records then serve again, from the last seq printed; it ends on INT or TERM (exit 0). FAIL: serve's
// refusal or an unreadable store (exit 1); a usage error exit 2.
func eventsCmd(args []string) int {
	follow, asJSON, after, ok := eventsArgs(args)
	if !ok {
		errf("%s", help.Usage("events"))
		return 2
	}
	cfg, _, rc := loadReader()
	if rc != 0 {
		return rc
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	each := func(r wire.FeedRecord) error {
		if err := printFeedRecord(r, asJSON); err != nil {
			return err
		}
		return out.Flush() // a follower's reader sees each line as it comes
	}
	for {
		seq, err := wire.Feed(ctx, wire.SocketPath(cfg.Root), cfg.Root, after, follow, each)
		after = seq
		switch {
		case err == nil || ctx.Err() != nil:
			return 0
		case wire.Absent(err) || follow && wire.FeedAgain(err):
			if after, err = feedFromStore(cfg.Root, after, each); err != nil {
				fmt.Fprintf(out, "FAIL events: %v\n", err)
				return 1
			}
			if !follow {
				return 0
			}
		default:
			fmt.Fprintf(out, "FAIL %v\n", err)
			return 1
		}
		select { // serve down or restarting: ask it again
		case <-ctx.Done():
			return 0
		case <-time.After(feedRetry):
		}
	}
}

// feedRetry is how long obor events --follow waits before it asks a serve that is not there again.
const feedRetry = time.Second

// eventsArgs is obor events' arguments: --follow, --json and --after <seq> (a non-negative integer), each at most once,
// in any order.
func eventsArgs(args []string) (follow, asJSON bool, after int64, ok bool) {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if seen[a] {
			return false, false, 0, false
		}
		seen[a] = true
		switch a {
		case "--follow":
			follow = true
		case "--json":
			asJSON = true
		case "--after":
			if i+1 == len(args) {
				return false, false, 0, false
			}
			i++
			n, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || n < 0 {
				return false, false, 0, false
			}
			after = n
		default:
			return false, false, 0, false
		}
	}
	return follow, asJSON, after, true
}

// feedFromStore hands each record of root's store after seq to each, in seq order (serve down: the feed's down path,
// wire.FeedSince as serve reads it); the last seq handed on. No store yet: none.
func feedFromStore(root string, seq int64, each func(wire.FeedRecord) error) (int64, error) {
	db, err := store.OpenAt(root)
	if err != nil || db == nil {
		return seq, err
	}
	defer db.Close()
	for {
		recs, err := wire.FeedSince(db, seq, wire.FeedPage)
		if err != nil {
			return seq, err
		}
		for _, r := range recs {
			if err := each(r); err != nil {
				return seq, err
			}
			seq = r.Seq
		}
		if len(recs) < wire.FeedPage {
			return seq, nil
		}
	}
}

// printFeedRecord prints r as obor events shows it: its JSON line, or the text line (app.Describe's wording of it when
// it has one).
func printFeedRecord(r wire.FeedRecord, asJSON bool) error {
	if asJSON {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", b)
		return err
	}
	line := fmt.Sprintf("%d %s %s %s %s", r.Seq, r.TS, r.Task, r.Kind, r.Step)
	if r.Ref != "" {
		line += " ref " + r.Ref
	}
	if d := app.Describe(r.Record); d.Line != "" {
		line += " — " + d.Line
	}
	_, err := fmt.Fprintln(out, line)
	return err
}
