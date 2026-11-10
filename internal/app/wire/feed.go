package wire

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"net"
	"time"

	"obor/internal/app/event"
	"obor/internal/store"
	"obor/internal/version"
)

// The event feed (plan §2c "Integrations"): an integration reads the project's event log from a cursor of its
// own, the seq of the last record it handled. A record is handed on once per cursor, in seq order, with nothing
// skipped: serve's MethodEvents streams it, and while serve is down obor events reads the store (FeedSince, the one
// read of both). A restart of the reader repeats nothing it had and misses nothing after it.

// FeedRecord is one record of the feed: the log's record as serve wrote it (its seq the cursor) and Ref, its task's
// spec's Ref: line (an external tracker's id; "": the task has no stored spec or no Ref:).
type FeedRecord struct {
	event.Record
	Ref string `json:"ref,omitempty"`
}

// FeedEnd is the reply line that ends a feed without Follow: every record after the cursor was sent.
const FeedEnd = "END"

// FeedPage is how many records one read of the feed takes.
const FeedPage = 500

// FeedSince is db's records after seq, at most limit, in seq order, each with its task's spec ref (read once per task
// per call: a later rev's Ref is the next call's).
func FeedSince(db *sql.DB, seq int64, limit int) ([]FeedRecord, error) {
	recs, err := event.Since(db, seq, limit)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	out := make([]FeedRecord, 0, len(recs))
	for _, r := range recs {
		ref, known := refs[r.Task]
		if !known {
			row, ok, err := store.ReadSpec(db, r.Task)
			if err != nil {
				return nil, err
			}
			if ok {
				ref = row.Spec.Ref
			}
			refs[r.Task] = ref
		}
		out = append(out, FeedRecord{Record: r, Ref: ref})
	}
	return out, nil
}

// feedDial bounds the feed's dial: a socket that is there answers at once.
const feedDial = 5 * time.Second

// Feed asks the serve listening at path for root's feed after seq and hands each record to each, in order (each's
// error ends the feed with it). It returns the seq of the last record handed on (seq: none) and why it ended: nil once
// a feed without follow has sent every record; with follow only an error ends it (ctx, serve gone: io.EOF), and the
// caller asks again from the seq returned. A dial that finds no serve is Absent.
func Feed(ctx context.Context, path, root string, seq int64, follow bool, each func(FeedRecord) error) (int64, error) {
	dctx, cancel := context.WithTimeout(ctx, feedDial)
	var d net.Dialer
	c, err := d.DialContext(dctx, "unix", path)
	cancel()
	if err != nil {
		return seq, fmt.Errorf("obor serve is not running (%w)", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	call, _ := json.Marshal(EventsReq{Envelope: Envelope{Root: root, Version: version.Version}, Seq: seq,
		Follow: follow})
	b, _ := json.Marshal(Request{Method: MethodEvents, Call: call}) // strings and numbers: cannot fail
	if _, err := c.Write(append(b, '\n')); err != nil {
		return seq, err
	}
	rd := bufio.NewReader(c)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			if ctx.Err() != nil {
				return seq, ctx.Err()
			}
			return seq, err
		}
		var ans Answer
		if err := json.Unmarshal(line, &ans); err != nil {
			return seq, fmt.Errorf("events: unreadable line from serve: %w", err)
		}
		switch {
		case ans.Error != "":
			return seq, errors.New(ans.Error)
		case ans.Record != nil:
			if ans.Record.Seq <= seq { // never twice per cursor
				continue
			}
			if err := each(*ans.Record); err != nil {
				return seq, err
			}
			seq = ans.Record.Seq
		case ans.Reply == FeedEnd && !follow:
			return seq, nil
		default:
			return seq, fmt.Errorf("events: unexpected line from serve: %s", line)
		}
	}
}

// FeedAgain: err ended a feed in a way a follower asks again after (no serve listening: down or restarting; the
// connection closed or reset under it). Serve's refusal is no such error.
func FeedAgain(err error) bool { return absent(err) || lostReply(err) }
