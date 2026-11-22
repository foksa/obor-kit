//go:build integration

package app_test

import (
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// A post refused before a byte went out (a dead socket's dial) is fact failed and posted again on a later
// pass, once; a post that began and then failed is "sent: <error>" and never posted again.
func TestSendPostRefusedAndPartial(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.inbox.fail(false)
	r.start()
	if _, err := r.line("s1", &wire.SendReq{Text: "one", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the refused post's failed fact", func() bool {
		for _, rec := range r.records() { // deliver.<the message's seq>
			s := rec.String()
			if rec.Task == event.MsgTask && strings.HasPrefix(s, "failed{deliver.") && strings.HasSuffix(s, ": dial unix: connection refused}") {
				return true
			}
		}
		return false
	})
	if w := r.waiting(); len(w) != 1 {
		t.Fatalf("a refused post: waiting %+v, want still queued", w)
	}
	r.inbox.fail(true)
	r.inbox.ok()
	r.waitFor("the retry posted", func() bool { return len(r.waiting()) == 0 })
	if got := r.lineResults(); len(got) != 1 || got[0] != "sent" {
		t.Errorf("delivery results %q, want one sent", got)
	}

	r.inbox.fail(true)
	if _, err := r.line("s2", &wire.SendReq{Text: "two", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the partial post's result", func() bool { return len(r.lineResults()) == 2 })
	r.passes()
	r.passes()
	if got := r.lineResults(); got[1] != "sent: broken pipe" {
		t.Errorf("a post that began then failed: %q, want sent: broken pipe", got[1])
	}
	if n := r.inbox.tries("pj-lead", sentLine("other", "two")); n != 1 {
		t.Errorf("the partial post tried %d times, want once", n)
	}
	if w := r.waiting(); len(w) != 0 {
		t.Errorf("waiting %+v, want none", w)
	}
}

// An OpenCode lead's line whose post is refused (a dead socket) waits for its inbox while the record is live (the
// hook's ask is refused), and goes to its hooks once the record names no socket that is there: held for them (obor
// status says so), handed once by the hook's ask, never posted after. Real time, no bubble: a post refused on every
// try is tried again at every pass.
func TestLeadLineRefusedPostFallsToHooks(t *testing.T) {
	t.Parallel()
	r := leadKindRig(t, "deepseek", "opencode")
	r.inbox.record("pj-lead")
	r.inbox.fail(false) // refused before a byte went out
	r.start()
	line := sentLine("other", "hello")
	if _, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the post refused", func() bool { return r.inbox.tries("pj-lead", line) >= 1 })
	r.passes()
	if w, res := r.waiting(), r.lineResults(); len(w) != 1 || !strings.Contains(w[0].Held, "refused") || len(res) != 0 {
		t.Fatalf("precondition: waiting %+v, results %q; want the refused line queued for its inbox, undelivered", w, res)
	}
	if _, err := r.leadLines("h0"); err == nil {
		t.Fatal("the hook's ask while the record is live: handed; want refused (the inbox posts it again)")
	}
	r.inbox.unrecord("pj-lead")
	r.passes()
	why := noRecordWhy
	if w := r.waiting(); len(w) != 1 || w[0].Held != why {
		t.Fatalf("waiting %+v; want the line held for the hooks", w)
	}
	tries := r.inbox.tries("pj-lead", line)
	got, err := r.leadLines("h1")
	if err != nil || got != line {
		t.Fatalf("the hook's ask: %q, %v; want %q", got, err, line)
	}
	r.inbox.ok()
	r.inbox.record("pj-lead")
	r.passes()
	// One delivery: the refused post's open intent, its result written by the hand-over; nothing posted after it.
	if res := r.lineResults(); len(res) != 1 || res[0] != "sent" || len(r.waiting()) != 0 ||
		r.inbox.tries("pj-lead", line) != tries || r.inbox.count("pj-lead", line) != 0 {
		t.Errorf("results %q, waiting %+v, tries %d (was %d), posted %d; want one delivery, no post after", res,
			r.waiting(), r.inbox.tries("pj-lead", line), tries, r.inbox.count("pj-lead", line))
	}
}
