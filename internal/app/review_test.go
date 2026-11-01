package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// T-638: obor review records a request; a merge-loop pass starts the review once its --after tasks merged and the
// reviewer is idle.

// reviewGitFake is a ReviewGit with fixed answers; detachErr fails the checkout. detached is every Detach "<dir>@<sha>".
type reviewGitFake struct {
	last      string
	merges    []string
	files     []string
	detachErr error
	detached  []string
}

func (g *reviewGitFake) Last(context.Context, string) (string, error) { return g.last, nil }
func (g *reviewGitFake) Head(context.Context, string) (string, error) {
	return "abcdef0123456789abcdef0123456789abcdef01", nil
}
func (g *reviewGitFake) Merges(context.Context, string, string) ([]string, error) {
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
type agentsSpy struct{ launches []Launch }

func (s *agentsSpy) Launch(_ context.Context, l Launch) (Started, error) {
	s.launches = append(s.launches, l)
	return Started{Line: "RESTARTED " + l.Agent}, nil
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
	return r
}

// reload is a restart: the log replayed.
func (r *reviewRig) reload() { r.a, r.p = lineReload(r.t, r.a, r.p, &r.now) }

// pass is one review pass.
func (r *reviewRig) pass() {
	r.a.reviewPass(context.Background(), r.p, &Loop{Review: r.git, Agents: r.agents})
}

// call is an obor review request from the lead.
func (r *reviewRig) call(id string, after []string, note string, rm bool) (string, error) {
	return r.a.reviewCall(Call{Root: r.p.root, CallID: id, Role: "lead", Agent: "pj-lead", After: after, Note: note, Clear: rm})
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
	r.a.record(r.p, Record{Task: task, Attempt: 1, Step: StepSubmitted, Kind: KindFact}, submitData{Lane: "O-01"})
}
func (r *reviewRig) merged(task string) {
	r.a.record(r.p, Record{Task: task, Attempt: 1, Step: StepMerged, Kind: KindFact}, mergedData{Head: "cccccccdddd"})
}
func (r *reviewRig) dropped(task string) {
	r.a.record(r.p, Record{Task: task, Attempt: 1, Step: StepDropped, Kind: KindFact}, FactData{Reason: "superseded"})
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
	recs, err := ReadLog(r.p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == ReviewTask && rec.Kind == KindFact && rec.Step == step {
			n++
		}
	}
	return n
}

// needs is the review key's needs-you reasons in force.
func (r *reviewRig) needs() []string {
	var out []string
	for _, f := range r.p.folded.active[ReviewTask] {
		if f.Step == StepNeedsYou {
			out = append(out, f.Fact().Reason)
		}
	}
	return out
}

func TestReviewRequest(t *testing.T) {
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
				!strings.HasSuffix(l.Prompt, "\nLead's note: skip docs") {
				t.Errorf("launch %+v", l)
			}
			if len(r.git.detached) != 1 || !strings.HasSuffix(r.git.detached[0], "/review@abcdef0123456789abcdef0123456789abcdef01") {
				t.Errorf("detached %q, want the review worktree at the head", r.git.detached)
			}
			if lines := r.leadLines(); len(lines) != 1 || lines[0] != "[obor] review RV-5 started at abcdef0 (T-a, T-b)" {
				t.Errorf("lead lines %q", lines)
			}
			if r.p.out.brief("pj-rev") == nil || r.p.folded.review != nil || r.facts(StepReviewStarted) != 1 {
				t.Errorf("brief %v, request %v, started facts %d", r.p.out.brief("pj-rev"), r.p.folded.review, r.facts(StepReviewStarted))
			}
		}},
		{"waits while the reviewer is busy", func(t *testing.T, r *reviewRig) {
			lineStatus(r.a, "w:rev", "working", r.now)
			r.mustCall("r1", nil, "", false)
			if r.p.folded.review == nil {
				t.Fatal("precondition: no request open")
			}
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(StepReviewFire) != 0 {
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
			r.a.record(r.p, Record{Task: ReviewTask, Step: StepReviewFire, Kind: KindFact}, ReviewData{Request: seq})
			r.reload()
			if r.facts(StepReviewFire) != 1 {
				t.Fatal("precondition: no firing fact")
			}
			r.pass()
			r.pass()
			if len(r.agents.launches) != 0 || r.facts(StepReviewFire) != 1 || r.p.folded.review != nil {
				t.Errorf("launches %+v, fire facts %d, request %v; want none started", r.agents.launches,
					r.facts(StepReviewFire), r.p.folded.review)
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
			if len(r.agents.launches) != 0 || r.p.folded.review != nil || r.facts(StepReviewRm) != 1 {
				t.Errorf("launches %+v, request %v", r.agents.launches, r.p.folded.review)
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
			if r.p.folded.review != nil || r.facts(StepReviewStarted) != 0 {
				t.Errorf("request %v, started %d; want ended, none", r.p.folded.review, r.facts(StepReviewStarted))
			}
			r.git.detachErr = nil
			r.mustCall("r2", nil, "", false)
			if n := r.needs(); len(n) != 0 {
				t.Errorf("needs-you %q after a new request, want ended", n)
			}
		}},
		{"status says review due at 4 unreviewed merges, not at 3", func(t *testing.T, r *reviewRig) {
			for _, task := range []string{"T-1", "T-2", "T-3"} {
				r.submitted(task)
				r.merged(task)
			}
			if got := r.a.Status().Project.Review; got != "" {
				t.Errorf("at 3 merges: %q", got)
			}
			r.submitted("T-4")
			r.merged("T-4")
			if got := r.a.Status().Project.Review; !strings.HasPrefix(got, "review due: 4 merges since ") {
				t.Errorf("at 4 merges: %q", got)
			}
			r.mustCall("r1", nil, "", false)
			r.pass()
			if got := r.a.Status().Project.Review; got != "" {
				t.Errorf("after RV-5 started: %q", got)
			}
			for _, task := range []string{"T-5", "T-6", "T-7", "T-8"} {
				r.submitted(task)
				r.merged(task)
			}
			if got := r.a.Status().Project.Review; got != "review due: 4 merges since RV-5" {
				t.Errorf("4 merges after RV-5: %q", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, newReviewRig(t)) })
	}
}
