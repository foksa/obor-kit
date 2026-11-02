package app

import (
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/store"
)

// T-638: obor review records a request; a merge-loop pass starts the review once its --after tasks merged and the
// reviewer is idle.

// reviewGitFake is a ReviewGit with fixed answers; detachErr fails the checkout. detached is every Detach "<dir>@<sha>".
type reviewGitFake struct {
	last      string
	merges    []string
	from      []string // every Merges call's from
	files     []string
	detachErr error
	detached  []string
}

func (g *reviewGitFake) Last(context.Context, string) (string, error) { return g.last, nil }
func (g *reviewGitFake) Head(context.Context, string) (string, error) {
	return "abcdef0123456789abcdef0123456789abcdef01", nil
}
func (g *reviewGitFake) Merges(_ context.Context, from, _ string) ([]string, error) {
	g.from = append(g.from, from)
	return g.merges, nil
}
func (g *reviewGitFake) Files(context.Context, string, string) ([]string, error) { return g.files, nil }
func (g *reviewGitFake) Detach(_ context.Context, dir, sha string) error {
	if g.detachErr != nil {
		return g.detachErr
	}
	g.detached = append(g.detached, dir+"@"+sha)
	return nil
}

// agentsSpy is an Agents that records every Launch and starts nothing.
type agentsSpy struct{ launches []port.Launch }

func (s *agentsSpy) Launch(_ context.Context, l port.Launch) (port.Started, error) {
	s.launches = append(s.launches, l)
	return port.Started{Line: "RESTARTED " + l.Agent}, nil
}
func (s *agentsSpy) Stop(context.Context, string, string, bool) (string, error) { return "", nil }

// reviewRig is a merging project with a reviewer pj-rev (idle, worktree review), paths.reviews and git and agent fakes.
type reviewRig struct {
	t      *testing.T
	a      *App
	p      *proj
	now    time.Time
	git    *reviewGitFake
	agents *agentsSpy
}

func newReviewRig(t *testing.T) *reviewRig {
	t.Helper()
	root := lineRoot(t)
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":{},"reviewer":{}},"paths":{"reviews":"reviews"}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &reviewRig{t: t, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), git: &reviewGitFake{
		last:   "RV-4 closed · 1111111 · approved · T-9",
		merges: []string{"aaaaaaa1 Merge T-a: one", "bbbbbbb2 Merge T-b: two"},
		files:  []string{"internal/app/x.go", "docs/y.md"}}, agents: &agentsSpy{}}
	r.a, r.p = lineApp(t, root, &r.now)
	r.wire()
	return r
}

// wire gives obor review's call the rig's git (its open-review check reads the merge loop's).
func (r *reviewRig) wire() {
	r.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Review: r.git, Agents: r.agents}, nil }
}

// reload is a restart: the log replayed.
func (r *reviewRig) reload() {
	r.a, r.p = lineReload(r.t, r.a, r.p, &r.now)
	r.wire()
}

// started is RV-5 fired and started on request r1 (the reviewer's pane then works and is idle again: its brief ends).
func (r *reviewRig) started() {
	r.t.Helper()
	r.mustCall("r1", nil, "", false)
	r.pass()
	if len(r.agents.launches) != 1 || r.p.folded.reviewed.id != "RV-5" {
		r.t.Fatalf("precondition: RV-5 not started: %+v", r.agents.launches)
	}
	lineStatus(r.a, "w:rev", "working", r.now)
	lineStatus(r.a, "w:rev", "idle", r.now)
	r.a.deliver(context.Background(), r.p, &Loop{Inbox: &inboxSpy{}}) // records the finished line
	if r.p.out.brief("pj-rev") != nil {
		r.t.Fatal("precondition: the reviewer's brief is still open")
	}
}

// refuseStep makes the event log refuse every fact of step (nil: none refused).
func (r *reviewRig) refuseStep(step string) {
	r.p.log.Refuse = func(rec event.Record) error {
		if rec.Step == step {
			return errors.New("disk full")
		}
		return nil
	}
}

// pass is one review pass.
func (r *reviewRig) pass() {
	r.a.reviewPass(context.Background(), r.p, &Loop{Review: r.git, Agents: r.agents})
}

// tick is one "review due" count.
func (r *reviewRig) tick() {
	r.a.reviewDueTick(context.Background(), r.p, &Loop{Review: r.git, Agents: r.agents})
}

// call is an obor review request from the lead.
func (r *reviewRig) call(id string, after []string, note string, rm bool) (string, error) {
	return r.a.reviewCall(wire.Call{Root: r.p.root, CallID: id, Role: "lead", Agent: "pj-lead", After: after, Note: note, Clear: rm})
}

func (r *reviewRig) mustCall(id string, after []string, note string, rm bool) string {
	r.t.Helper()
	reply, err := r.call(id, after, note, rm)
	if err != nil {
		r.t.Fatal(err)
	}
	return reply
}

// submitted opens task (attempt 1 on a lane); merged and dropped end it.
func (r *reviewRig) submitted(task string) {
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact}, submitData{Lane: "O-01"})
}
func (r *reviewRig) merged(task string) {
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepMerged, Kind: event.KindFact}, event.MergedData{Head: "cccccccdddd"})
}
func (r *reviewRig) dropped(task string) {
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepDropped, Kind: event.KindFact}, event.FactData{Reason: "superseded"})
}

// leadLines is the lines to the lead recorded so far.
func (r *reviewRig) leadLines() []string {
	var out []string
	for _, q := range r.p.queuedLines() {
		if q.msg.To == "pj-lead" {
			out = append(out, q.msg.Line)
		}
	}
	return out
}

// facts is the log's review facts by step.
func (r *reviewRig) facts(step string) int {
	recs, err := event.ReadLog(r.p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == event.ReviewTask && rec.Kind == event.KindFact && rec.Step == step {
			n++
		}
	}
	return n
}

// needs is the review key's needs-you reasons in force.
func (r *reviewRig) needs() []string {
	var out []string
	for _, f := range r.p.folded.active[event.ReviewTask] {
		if f.Step == event.StepNeedsYou {
			out = append(out, f.Fact().Reason)
		}
	}
	return out
}

func TestReviewRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, r *reviewRig)
	}{
		{"fires after the last --after merges", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.submitted("T-b")
			if got := r.mustCall("r1", []string{"T-a", "T-b"}, "skip docs", false); got != "REVIEW REQUESTED (after T-a,T-b)" {
				t.Fatalf("reply %q", got)
			}
			r.merged("T-a")
			r.pass()
			if len(r.agents.launches) != 0 || r.p.folded.review == nil {
				t.Fatalf("started with T-b open: %+v", r.agents.launches)
			}
			r.merged("T-b")
			r.pass()
			if len(r.agents.launches) != 1 {
				t.Fatalf("launches %+v, want one", r.agents.launches)
			}
			l := r.agents.launches[0]
			if l.Agent != "pj-rev" || !l.Fresh || !strings.HasPrefix(l.Prompt, "Review RV-5. main is at abcdef0. In scope: T-a (aaaaaaa), T-b (bbbbbbb); files: internal/app/x.go, docs/y.md.") ||
				!strings.Contains(l.Prompt, "Previous review: RV-4 (") || !strings.Contains(l.Prompt, "Write only reviews/RV-5.md") ||
				!strings.HasSuffix(l.Prompt, "\nLead's note: skip docs") ||
				!strings.Contains(l.Prompt, " Previous review: RV-4 (its close commit: RV-4 closed · 1111111 · approved · T-9; its accepted"+
					" findings are the BACKLOG lines that name RV-4) — don't repeat them. ") || strings.Contains(l.Prompt, ".md, or the file") {
				t.Errorf("launch %+v", l)
			}
			if len(r.git.detached) != 1 || !strings.HasSuffix(r.git.detached[0], "/review@abcdef0123456789abcdef0123456789abcdef01") {
				t.Errorf("detached %q, want the review worktree at the head", r.git.detached)
			}
			if lines := r.leadLines(); len(lines) != 0 {
				t.Errorf("lead lines %q, want none on a start (T-650)", lines)
			}
			if r.p.out.brief("pj-rev") == nil || r.p.folded.review != nil || r.facts(event.StepReviewStarted) != 1 {
				t.Errorf("brief %v, request %v, started facts %d", r.p.out.brief("pj-rev"), r.p.folded.review, r.facts(event.StepReviewStarted))
			}
			rows, err := store.ReadPrompts(r.p.log.DB(), store.PromptQuery{Agent: "pj-rev"}, 10)
			if err != nil || len(rows) != 1 || rows[0].Kind != store.PromptStart || rows[0].Text != l.Prompt ||
				rows[0].Task != "" {
				t.Errorf("the review start's prompt row: %+v %v; want its first prompt (T-665)", rows, err)
			}
		}},
		{"waits while the reviewer is busy", func(t *testing.T, r *reviewRig) {
			lineStatus(r.a, "w:rev", "working", r.now)
			r.mustCall("r1", nil, "", false)
			if r.p.folded.review == nil {
				t.Fatal("precondition: no request open")
			}
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(event.StepReviewFire) != 0 {
				t.Fatalf("fired while pj-rev works: %+v", r.agents.launches)
			}
			lineStatus(r.a, "w:rev", "idle", r.now)
			r.pass()
			if len(r.agents.launches) != 1 {
				t.Errorf("launches %+v once idle, want one", r.agents.launches)
			}
		}},
		{"a dropped --after ends it with the line", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.submitted("T-b")
			r.mustCall("r1", []string{"T-a", "T-b"}, "", false)
			r.merged("T-a")
			r.dropped("T-b")
			r.pass()
			r.pass()
			if lines := r.leadLines(); len(lines) != 1 || lines[0] != "[obor] review request ended: T-b dropped" {
				t.Errorf("lead lines %q", lines)
			}
			if r.p.folded.review != nil || len(r.agents.launches) != 0 {
				t.Errorf("request %v, launches %+v; want ended, none", r.p.folded.review, r.agents.launches)
			}
			r.reload()
			r.pass()
			if lines := r.leadLines(); len(lines) != 1 || r.p.folded.review != nil {
				t.Errorf("after a replay: lines %q, request %v", lines, r.p.folded.review)
			}
		}},
		{"replay after the firing fact starts nothing", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "", false)
			seq := r.p.folded.review.seq
			// A crash right after the firing fact: nothing else of the start ran.
			r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewFire, Kind: event.KindFact}, ReviewData{Request: seq})
			r.reload()
			if r.facts(event.StepReviewFire) != 1 {
				t.Fatal("precondition: no firing fact")
			}
			r.pass()
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(event.StepReviewFire) != 1 || r.p.folded.review != nil {
				t.Errorf("launches %+v, fire facts %d, request %v; want none started", r.agents.launches,
					r.facts(event.StepReviewFire), r.p.folded.review)
			}
		}},
		{"second call replaces", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.submitted("T-b")
			r.mustCall("r1", []string{"T-a"}, "", false)
			r.now = r.now.Add(5 * time.Minute)
			if got := r.mustCall("r2", []string{"T-b"}, "", false); got != "REVIEW REQUESTED (after T-b) (replaces the request of 5m ago)" {
				t.Errorf("reply %q", got)
			}
			if got := r.mustCall("r3", nil, "", false); got != "REVIEW REQUEST since 0s (after T-b, waiting T-b)" {
				t.Errorf("the list %q", got)
			}
			r.merged("T-a")
			r.pass()
			if len(r.agents.launches) != 0 {
				t.Errorf("fired on the replaced request's task: %+v", r.agents.launches)
			}
		}},
		{"--rm", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.mustCall("r1", []string{"T-a"}, "", false)
			if got := r.mustCall("r2", nil, "", true); got != "REVIEW REQUEST ENDED" {
				t.Errorf("--rm %q", got)
			}
			if got := r.mustCall("r3", nil, "", true); got != "NO REVIEW REQUEST" {
				t.Errorf("--rm again %q", got)
			}
			r.merged("T-a")
			r.pass()
			if len(r.agents.launches) != 0 || r.p.folded.review != nil || r.facts(event.StepReviewRm) != 1 {
				t.Errorf("launches %+v, request %v", r.agents.launches, r.p.folded.review)
			}
		}},
		{"a request while a started RV has no close commit is refused", func(t *testing.T, r *reviewRig) {
			r.started()
			if _, err := r.call("r2", nil, "again", false); err == nil || err.Error() != "RV-5 is open (not closed): close it first" {
				t.Errorf("request with RV-5 open: %v", err)
			}
			if _, err := r.call("r3", []string{}, "", false); err == nil || !strings.Contains(err.Error(), "RV-5 is open") {
				t.Errorf("a request to start now with RV-5 open: %v", err)
			}
			if r.facts(event.StepReviewRequest) != 1 || r.p.folded.review != nil {
				t.Errorf("request facts %d, request %v; want only r1, none open", r.facts(event.StepReviewRequest), r.p.folded.review)
			}
			r.git.last = "RV-5 closed · abcdef0 · approved"
			if got := r.mustCall("r4", nil, "", false); got != "REVIEW REQUESTED (now)" {
				t.Errorf("after RV-5 closed: %q", got)
			}
		}},
		{"the open check survives a replay", func(t *testing.T, r *reviewRig) {
			r.started()
			r.reload()
			if r.p.folded.reviewed.id != "RV-5" {
				t.Fatalf("precondition: the replay lost RV-5's start: %+v", r.p.folded.reviewed)
			}
			if _, err := r.call("r2", nil, "", false); err == nil || err.Error() != "RV-5 is open (not closed): close it first" {
				t.Errorf("request after a replay with RV-5 open: %v", err)
			}
		}},
		{"a queued request waits while RV-5 is open, then fires after its close commit", func(t *testing.T, r *reviewRig) {
			r.started()
			// A request already in the log (one obor review recorded before RV-5's start, or an older serve's).
			r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewRequest, Kind: event.KindFact}, ReviewData{By: "pj-lead", Note: "next"})
			if r.p.folded.review == nil || !r.a.reviewerIdle(r.p, "pj-rev") {
				t.Fatal("precondition: no request open, or the reviewer is not idle")
			}
			r.pass()
			if len(r.agents.launches) != 1 || r.facts(event.StepReviewFire) != 1 {
				t.Fatalf("fired with RV-5 open: launches %d, fire facts %d", len(r.agents.launches), r.facts(event.StepReviewFire))
			}
			if got := r.a.Status().Project.Review; got != "review request since 0s (now, waiting RV-5 to close), note: next" {
				t.Errorf("status %q", got)
			}
			if q := r.a.Status().Project.ReviewRequest; q == nil || q.Held != "RV-5" || q.Note != "next" || q.By != "pj-lead" ||
				q.Since == 0 || len(q.After) != 0 { // T-664: the request as data
				t.Errorf("status review_request %+v; want held by RV-5, note next, by pj-lead, its time", q)
			}
			r.git.last = "RV-5 closed · abcdef0 · approved · T-7"
			r.pass()
			if len(r.agents.launches) != 2 || !strings.HasPrefix(r.agents.launches[1].Prompt, "Review RV-6. ") ||
				!strings.HasSuffix(r.agents.launches[1].Prompt, "\nLead's note: next") {
				t.Errorf("after RV-5 closed: launches %+v", r.agents.launches)
			}
		}},
		{"replace and --rm during a claim are refused, the old request never fires after them", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "old", false)
			old := r.p.folded.review.seq
			var errs []error
			r.p.rv.claimed = func() {
				_, err := r.call("r2", nil, "new", false)
				errs = append(errs, err)
				_, err = r.call("r3", nil, "", true)
				errs = append(errs, err)
			}
			r.refuseStep(event.StepReviewFire) // the claim ends without a firing: the old request stays open
			r.pass()
			want := "review: obor serve is starting the review request now: nothing changed (try again once it is done)"
			if len(errs) != 2 || errs[0] == nil || errs[0].Error() != want || errs[1] == nil || errs[1].Error() != want {
				t.Fatalf("replace and --rm during the claim: %v", errs)
			}
			if r.p.folded.review == nil || r.p.folded.review.seq != old || r.facts(event.StepReviewRequest) != 1 || r.facts(event.StepReviewRm) != 0 {
				t.Fatalf("the claim's request changed: %+v", r.p.folded.review)
			}
			r.p.rv.claimed, r.p.log.Refuse = nil, nil
			if got := r.mustCall("r4", nil, "new", false); !strings.HasPrefix(got, "REVIEW REQUESTED (now) (replaces") {
				t.Errorf("replace after the claim: %q", got)
			}
			r.pass()
			if len(r.agents.launches) != 1 || !strings.HasSuffix(r.agents.launches[0].Prompt, "\nLead's note: new") {
				t.Errorf("launches %+v, want the new request's only", r.agents.launches)
			}
			r.pass()
			if len(r.agents.launches) != 1 || r.facts(event.StepReviewFire) != 1 {
				t.Errorf("the old request fired after the replace: launches %d, fire facts %d", len(r.agents.launches), r.facts(event.StepReviewFire))
			}
		}},
		{"--rm after a refused claim ends the request: nothing fires", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "", false)
			var rmErr error
			r.p.rv.claimed = func() { _, rmErr = r.call("r2", nil, "", true) }
			r.refuseStep(event.StepReviewFire)
			r.pass()
			if rmErr == nil || !strings.Contains(rmErr.Error(), "is starting the review request now") {
				t.Fatalf("--rm during the claim: %v", rmErr)
			}
			r.p.rv.claimed, r.p.log.Refuse = nil, nil
			if got := r.mustCall("r3", nil, "", true); got != "REVIEW REQUEST ENDED" {
				t.Errorf("--rm after the claim: %q", got)
			}
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(event.StepReviewFire) != 0 {
				t.Errorf("fired after --rm: launches %d, fire facts %d", len(r.agents.launches), r.facts(event.StepReviewFire))
			}
		}},
		{"a replace during the ended line's claim is refused", func(t *testing.T, r *reviewRig) {
			r.submitted("T-a")
			r.mustCall("r1", []string{"T-a"}, "", false)
			r.dropped("T-a")
			var err error
			r.p.rv.claimed = func() { _, err = r.call("r2", nil, "replace", false) }
			r.pass()
			if err == nil || !strings.Contains(err.Error(), "is ending the review request now") {
				t.Errorf("replace during the ended line's claim: %v", err)
			}
			if lines := r.leadLines(); len(lines) != 1 || r.p.folded.review != nil || r.facts(event.StepReviewRequest) != 1 {
				t.Errorf("lines %q, request %v, request facts %d", lines, r.p.folded.review, r.facts(event.StepReviewRequest))
			}
		}},
		{"a failing append after the launch is a needs-you", func(t *testing.T, r *reviewRig) {
			r.mustCall("r1", nil, "", false)
			r.refuseStep(event.StepReviewStarted)
			r.pass()
			if len(r.agents.launches) != 1 || r.facts(event.StepReviewStarted) != 0 {
				t.Fatalf("precondition: launches %d, started facts %d; want launched, not recorded", len(r.agents.launches), r.facts(event.StepReviewStarted))
			}
			if n := r.needs(); len(n) != 1 || n[0] != "RV-5: started but not recorded: disk full" {
				t.Errorf("needs-you %q", n)
			}
		}},
		{"no previous RV is RV-1", func(t *testing.T, r *reviewRig) {
			r.git.last = ""
			r.mustCall("r1", nil, "", false)
			r.pass()
			if len(r.agents.launches) != 1 || !strings.HasPrefix(r.agents.launches[0].Prompt, "Review RV-1. ") ||
				!strings.Contains(r.agents.launches[0].Prompt, "Previous review: none.") {
				t.Errorf("launches %+v", r.agents.launches)
			}
		}},
		{"a failing checkout is a needs-you, no restart", func(t *testing.T, r *reviewRig) {
			r.git.detachErr = errors.New("git checkout: your local changes would be overwritten")
			r.mustCall("r1", nil, "", false)
			r.pass()
			if len(r.agents.launches) != 0 {
				t.Errorf("launched %+v", r.agents.launches)
			}
			if n := r.needs(); len(n) != 1 || n[0] != "RV-5: git checkout: your local changes would be overwritten" {
				t.Errorf("needs-you %q", n)
			}
			if r.p.folded.review != nil || r.facts(event.StepReviewStarted) != 0 {
				t.Errorf("request %v, started %d; want ended, none", r.p.folded.review, r.facts(event.StepReviewStarted))
			}
			r.git.detachErr = nil
			r.mustCall("r2", nil, "", false)
			if n := r.needs(); len(n) != 0 {
				t.Errorf("needs-you %q after a new request, want ended", n)
			}
		}},
		{"status says review due at 4 unreviewed merges, not at 3", func(t *testing.T, r *reviewRig) {
			// The count is git's: the first-parent merges since the previous review (RV-4 closed · 1111111), read on a
			// pass once a merge folded; the log's merges only say when to read it again.
			merge := func(task string, merges int) {
				r.submitted(task)
				r.merged(task)
				r.git.merges = nil
				for i := range merges {
					r.git.merges = append(r.git.merges, fmt.Sprintf("%07d Merge T-%d: x", i, i))
				}
				r.tick()
			}
			merge("T-1", 3)
			if got := r.a.Status().Project.Review; got != "" || len(r.git.from) != 1 || r.git.from[0] != "1111111" {
				t.Errorf("at 3 merges: %q (counted from %q; want since 1111111)", got, r.git.from)
			}
			merge("T-2", 4)
			if got := r.a.Status().Project.Review; got != "review due: 4 merges since RV-4" {
				t.Errorf("at 4 merges: %q", got)
			}
			r.mustCall("r1", nil, "", false)
			r.pass()
			merge("T-3", 2) // RV-5 started, not closed: counted from its head
			if got, from := r.a.Status().Project.Review, r.git.from[len(r.git.from)-1]; got != "" ||
				from != "abcdef0123456789abcdef0123456789abcdef01" {
				t.Errorf("after RV-5 started: %q (counted from %q)", got, from)
			}
			merge("T-4", 4)
			if got := r.a.Status().Project.Review; got != "review due: 4 merges since RV-5" {
				t.Errorf("4 merges after RV-5: %q", got)
			}
			r.git.last = "RV-5 closed · 2222222 · approved"
			merge("T-5", 1) // RV-5 closed: counted from its reviewed sha
			if got, from := r.a.Status().Project.Review, r.git.from[len(r.git.from)-1]; got != "" || from != "2222222" {
				t.Errorf("after RV-5 closed: %q (counted from %q)", got, from)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, newReviewRig(t))
		})
	}
}
