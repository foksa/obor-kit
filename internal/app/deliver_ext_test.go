package app_test

import (
	"testing"
	"time"

	"obor/internal/app/port"
)

// retryWake is the fetch rig with T-1 dispatched to pj-l-01, running as kind with an inbox record or not, working and
// undelivered for 5 minutes: its retry.work wake goes out.
func retryWake(t *testing.T, kind string, recorded bool) *loopRig {
	t.Helper()
	r := fetchRig(t)
	if recorded {
		r.inbox.record("pj-l-01")
	}
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.setPane("w1:p1", func(p *port.Pane) { p.Agent = kind })
	r.workerIs("working")
	r.waitFor("pj-l-01 seen as "+kind, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01" && a.Agent == kind {
				return true
			}
		}
		r.push()
		return false
	})
	if len(r.promptsTo("pj-l-01")) != 0 || r.inbox.tries("pj-l-01", wake("T-1/1/work")) != 0 {
		t.Fatalf("precondition: the work job's wake went with the start, nothing sent since: %q", r.steps())
	}
	r.later(5 * time.Minute)
	return r
}

// A worker's retry wake: posted to a Claude or OpenCode session with an inbox record (none typed, no keys); typed to a
// session of either with none (the fallback), and to Codex whatever an inbox record says.
func TestWakeByKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind     string
		recorded bool
		posted   bool
	}{
		{"claude", true, true},
		{"claude", false, false},
		{"codex", true, false},
		{"opencode", true, true},
		{"opencode", false, false},
	} {
		name := c.kind
		if !c.recorded {
			name += " without a record"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := retryWake(t, c.kind, c.recorded)
				r.waitStep("1 result retry.work")
				same(t, r.result("retry.work"), `"sent"`, "retry.work result")
				w := wake("T-1/1/work")
				if c.posted {
					if n := r.inbox.count("pj-l-01", w); n != 1 {
						t.Errorf("posted %d wakes to pj-l-01, want 1", n)
					}
					eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
					if n := r.keysTo("pj-l-01"); n != 0 {
						t.Errorf("%d send_keys to pj-l-01, want none (no ctrl+u for a post)", n)
					}
					return
				}
				eq(t, r.promptsTo("pj-l-01"), []string{w}, "typed to pj-l-01")
				if n := r.inbox.count("pj-l-01", w); n != 0 {
					t.Errorf("posted %d wakes to pj-l-01, want none", n)
				}
			})
		})
	}
}

// A posted wake refused before a byte went out (a dead socket's dial) is a counted refusal, posted again on a later
// pass under the same key and only once; after a restart (replay) it is not posted again. The refusal is the next post
// alone (failOnce): how many passes run before the test looks is the loop's business, not the test's.
//
// The test waits on the refusal's own record, not on the retry's result: a pass whose herdr stream ends before
// the refused effect returns records no refusal at all (failed: not connected, so no fact), and the post of the pass
// after it goes out uncounted. The rig's pushes end serve's stream (workerIs pushes until it sees the status), so the
// test first waits for serve's stream after its last push: the pass that refuses runs on one stream, and so does the
// pass that posts.
func TestWakePostRefusedThenReplayed(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.inbox.record("pj-l-01")
		r.inbox.failOnce(false)
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		r.workerIs("working")
		r.connectedAgain() // no push after this: the pass that refuses the post runs on one stream to its end
		r.later(5 * time.Minute)
		w := wake("T-1/1/work")
		const refused = "1 fact failed{retry.work: "
		r.waitFor("the refused wake counted", func() bool { return r.countPrefix(refused) == 1 })
		if n := r.countPrefix(refused); n != 1 {
			t.Fatalf("refused posts %d, want 1: %q", n, r.steps())
		}
		r.waitStep("1 result retry.work") // the pass after the refusal posts it: wait on the record, not on the test's look
		if tried, posted := r.inbox.tries("pj-l-01", w), r.inbox.count("pj-l-01", w); tried != 2 || posted != 1 {
			t.Fatalf("tried %d, posted %d wakes, want 2 and 1 (one refusal, then the post)", tried, posted)
		}
		if r.count("1 intent retry.work") != 1 {
			t.Errorf("the post's retry wrote a second intent: %q", r.steps())
		}
		r.stop()
		r.start()
		r.passes()
		if n := r.inbox.count("pj-l-01", w); n != 1 {
			t.Errorf("posted %d wakes, want 1 (once after the refusal, not again on replay)", n)
		}
		eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
	})
}
