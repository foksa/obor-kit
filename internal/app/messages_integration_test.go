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
