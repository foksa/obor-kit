package app_test

import (
	"encoding/json"
	"obor/internal/app/wire"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
)

// T-499 (P5-4): serve's lines and job wakes reach a running agent as its kind takes them (agents.Kind.Deliver): a
// Claude session gets them posted to its inbox (nothing typed into its pane, no ctrl+u), Codex and OpenCode panes get
// them typed as before. A Claude session with no inbox record: a line stays queued, a job wake is typed (the fallback).

// keysTo is the agent.send_keys requests to agent.
func (r *loopRig) keysTo(agent string) int {
	n := 0
	for _, req := range r.srv.Requests() {
		var p struct{ Target string }
		_ = json.Unmarshal(req.Params, &p)
		if req.Method == "agent.send_keys" && p.Target == agent {
			n++
		}
	}
	return n
}

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
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind = kind })
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

// A worker's retry wake: posted to a Claude session with an inbox record (none typed, no keys); typed to a Claude
// session with none (the fallback), and to Codex and OpenCode whatever an inbox record says.
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
		{"opencode", true, false},
	} {
		name := c.kind
		if !c.recorded {
			name += " without a record"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
	}
}

// A posted wake refused before a byte went out (a dead socket's dial) is a counted refusal, posted again on a later
// pass under the same key and only once; after a restart (replay) it is not posted again. The refusal is the next post
// alone (failOnce): how many passes run before the test looks is the loop's business, not the test's (T-565).
//
// The test waits on the refusal's own record, not on the retry's result (T-980): a pass whose herdr stream ends before
// the refused effect returns records no refusal at all (failed: not connected, so no fact), and the post of the pass
// after it goes out uncounted. The rig's pushes end serve's stream (workerIs pushes until it sees the status), so the
// test first waits for serve's stream after its last push: the pass that refuses runs on one stream, and so does the
// pass that posts.
func TestWakePostRefusedThenReplayed(t *testing.T) {
	t.Parallel()
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
}

// A posted wake that broke after a byte went out is sent (with the error): never posted again under its key, not after
// a restart either.
func TestWakePostPartialNotRepeated(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.inbox.record("pj-l-01")
	r.inbox.fail(true)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerIs("working")
	r.later(5 * time.Minute)
	r.waitStep("1 result retry.work")
	same(t, r.result("retry.work"), `"sent: broken pipe"`, "retry.work result")
	r.inbox.ok()
	r.stop()
	r.start()
	r.passes()
	if n := r.inbox.tries("pj-l-01", wake("T-1/1/work")); n != 1 {
		t.Errorf("tried %d posts of the wake, want 1", n)
	}
	eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
}

// A brief to a Claude role agent is posted to its session (none typed); to Codex and OpenCode it is typed (none
// posted); a Claude session with no inbox record gets it typed (T-746: as its job wakes are; before, it waited
// untried), nothing posted, nothing left waiting; its work and idle end it as before.
func TestBriefByKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind     string
		recorded bool
	}{
		{"claude", true},
		{"claude", false},
		{"codex", true},
		{"opencode", true},
	} {
		name := c.kind
		if !c.recorded {
			name += " without a record"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := lineRig(t, "idle", "idle")
			if !c.recorded {
				r.inbox.unrecord("pj-rev")
			}
			r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Kind = c.kind })
			r.start()
			const line = "[obor pj-lead] verify"
			if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "verify"}); err != nil {
				t.Fatal(err)
			}
			r.waitFor("the brief delivered", func() bool {
				return r.inbox.count("pj-rev", line)+r.typedTo("pj-rev", line) == 1
			})
			r.passes()
			posted, typed := r.inbox.tries("pj-rev", line), r.typedTo("pj-rev", line)
			post := c.kind == "claude" && c.recorded
			if post && (posted != 1 || typed != 0) || !post && (posted != 0 || typed != 1) {
				t.Errorf("%s: posted %d, typed %d", name, posted, typed)
			}
			if c.kind == "claude" && !c.recorded {
				if w := r.waiting(); len(w) != 0 {
					t.Errorf("waiting %+v, want none: the fallback typed it", w)
				}
			}
			r.agentIs("w1:p6", "pj-rev", "working")
			r.agentIs("w1:p6", "pj-rev", "idle")
			r.waitFor("the finished line posted", func() bool {
				return r.inbox.count("pj-lead", "[obor] pj-rev finished (briefed 0s ago)") == 1
			})
		})
	}
}
