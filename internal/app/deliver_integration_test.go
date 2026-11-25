//go:build integration

package app_test

import (
	"strings"
	"testing"

	"obor/internal/app/wire"
)

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
