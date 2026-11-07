package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"obor/internal/app/event"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/spec"
)

// The promotion (T-974; docs/state-model.md rows 50, 51; owner 11-07: one staging branch promoted by serve). With
// branches.promote set, tasks merge into branches.main (the target, e.g. staging) and serve moves the promoted branch
// (e.g. main) only to R, the reviewed head of the last closed review (T-978: the start's head, or its last finished
// verify's when it verified fixes; reviews not set up: the last GREEN run's sha), once the full suite passed at R.
// Each pass:
//
//   - Trigger: no promotion running, no self-restart waiting (the drain), the target's head H is not the promoted
//     branch's, and the weight merged since (first-parent merges of promoted..H, each its spec's Difficulty level's
//     merge.weights, no spec or no line: medium) is at least merge.promote_weight, or at least one merge with the loop
//     idle. Due, it requests a review when a first-parent merge since R is in H (fact review.request, by obor, its
//     note naming the batch) unless a review or a request is open, and, unless a run was at H already, records
//     intent promote.<seq>{sha H, since, merges, weight} (G) and runs it outside the scratch token, one at a time:
//     Promote.Run, the full suite at H in its own worktree (RED: the failing tests rerun once). A GREEN there is
//     promoted only when the review closes at exactly H.
//   - R (a review closed): the suite at R (intent promote.<seq>{sha R, …}), unless a run at R has a result already
//     (a GREEN at R is reused, a RED one waits for the next close); the trigger's weight is not asked.
//   - RED: needs-you{promotion RED <sha7>: <line>; merges since <since7>: T-a, …} (the nightly's shape), ended by the
//     next GREEN promotion (fact promoted). A run that ends neither is a counted failure, tried again promoteRetry
//     after it; the tries-th is a needs-you.
//   - GREEN at R: the promoted branch is fast-forwarded to R (Promote.FF), then fact promoted{sha R}. A GREEN at any
//     other sha is never promoted; it waits for a close at its sha (obor status says why). Commits after R (the close
//     commit, the lead's docs) are the next review's.
//   - After fact promoted: intent after_promote.<seq>{sha, since} (G): commands.after_merge in the promotion worktree at
//     the promoted sha with OBOR_ROOT set (row 51); the per-publish after_merge is skipped while promote is set.
//
// A restart mid-run runs the pending intent again at its recorded sha (the promoted branch there already: GREEN without
// a second run, Promote.Run's own check); a crash between the fast-forward and fact promoted finds the branch at R and
// records the fact; one between a RED result and its needs-you settles it from the result on the next pass.

// promoteRetry is how long after a failed promotion step its next try waits (a git lock, the worktree held).
const promoteRetry = 10 * time.Minute

// promoteRedPrefix starts the reason of a RED promotion's needs-you.
const promoteRedPrefix = "promotion RED "

// Promote is a project's promotion (branches.promote set; Loop.Promote nil: off, today's flow).
type Promote struct {
	Branch string                 // branches.promote: the promoted branch (main)
	Target string                 // branches.main: the branch tasks merge into (staging)
	Weight int                    // merge.promote_weight: the merged weight that triggers a promotion
	Weigh  func(level string) int // merge.weights: a difficulty level's weight
	// Run runs the full suite at sha in the promotion worktree: GREEN or RED (the promoted branch at sha already: GREEN,
	// nothing run); an error: it ended neither (tried again).
	Run func(ctx context.Context, sha string) (port.PromoteOut, error)
	// FF fast-forwards the promoted branch to sha (it must be an ancestor of sha; at sha already: nothing to do).
	FF func(ctx context.Context, sha string) (string, error)
	// After runs commands.after_merge in the promotion worktree at sha, OBOR_ROOT set; since is the sha promoted before
	// ("": none), for its when paths. ran | skipped | WARN …; an error: tried again.
	After func(ctx context.Context, sha, since string) (string, error)
}

// PromoteOut is port.PromoteOut, the promotion run's one result type (T-978), by its name in package app (the
// readers' describe.go decodes a result with it).
type PromoteOut = port.PromoteOut

// promoteIn is a promotion's intent: the target's head it checks, the promoted sha it follows, the merges between
// them and their weight.
type promoteIn struct {
	SHA    string   `json:"sha"`
	Since  string   `json:"since,omitempty"`
	Merges []string `json:"merges,omitempty"`
	Weight int      `json:"weight"`
}

// promotedData is fact promoted's data and after_promote's intent: the sha the promoted branch was moved to, and the
// sha it was at before (after_promote's).
type promotedData struct {
	SHA   string `json:"sha"`
	Since string `json:"since,omitempty"`
}

// promoteState is the promotion's fold and its live state: the last run's step, intent and result (and whether a RED
// one waits for its needs-you), every run's outcome by sha, the last promoted sha, the last after_promote, the counted
// failures per step, and, live, whether a step runs and what the last pass counted (obor status).
type promoteState struct {
	seq       int // the last promote.<seq>
	step      string
	in        promoteIn
	done      bool
	out       *port.PromoteOut
	unsettled bool // out is RED and no needs-you or resumed fact followed it
	// ran is each sha's run outcome: GREEN, RED, or failed (its step failed tries times); none: not run there.
	ran       map[string]string
	promoted  string // the last fact promoted's sha
	since     string // the sha promoted before it
	afterSeq  int
	afterStep string
	afterSHA  string
	afterDone bool
	fails     map[string]int
	failedAt  time.Time
	failedWhy string
	running   bool // a promotion step runs in its own goroutine (under mu)
	// The last pass's count (live, obor status): the promoted branch, its sha, the merges since, why a GREEN run waits.
	branch string
	head   string
	merges int
	wait   string
}

// promoteSeq is the <seq> of step "<base>.<seq>" (0: another step).
func promoteSeq(step, base string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(step, base+"."))
	if err != nil || !strings.HasPrefix(step, base+".") {
		return 0
	}
	return n
}

// foldPromote folds r, a record of the promotion.
func (p *proj) foldPromote(r event.Record) {
	if r.Task != event.PromoteTask {
		return
	}
	s := &p.prom
	run, after := promoteSeq(r.Step, event.StepPromote), promoteSeq(r.Step, event.StepAfterPromote)
	switch {
	case r.Kind == event.KindIntent && run > 0:
		s.seq, s.step, s.done, s.in = run, r.Step, false, promoteIn{}
		_ = json.Unmarshal(r.Data, &s.in)
	case r.Kind == event.KindResult && run > 0 && r.Step == s.step:
		s.done = true
		var out port.PromoteOut
		if json.Unmarshal(r.Data, &out) == nil && out.Verdict != "" {
			s.out, s.unsettled = &out, out.Verdict == "RED"
			s.setRan(out.SHA, out.Verdict)
		}
	case r.Kind == event.KindIntent && after > 0:
		var d promotedData
		_ = json.Unmarshal(r.Data, &d)
		s.afterSeq, s.afterStep, s.afterSHA, s.afterDone = after, r.Step, d.SHA, false
	case r.Kind == event.KindResult && after > 0 && r.Step == s.afterStep:
		s.afterDone = true
	case r.Kind == event.KindFact && r.Step == event.StepPromoted:
		var d promotedData
		_ = json.Unmarshal(r.Data, &d)
		s.promoted, s.since = d.SHA, d.Since
	case r.Kind == event.KindFact && r.Step == event.StepFailed:
		if s.fails == nil {
			s.fails = map[string]int{}
		}
		key := r.Fact().Step
		s.fails[key]++
		s.failedAt, s.failedWhy = event.RecordTime(r), r.Fact().Reason
		if key == s.step && !s.done && s.spent(key) {
			s.setRan(s.in.SHA, "failed")
		}
	case r.Kind == event.KindFact && (r.Step == event.StepNeedsYou || r.Step == event.StepResumed):
		s.unsettled = false
	}
}

// setRan records sha's run outcome.
func (s *promoteState) setRan(sha, outcome string) {
	if s.ran == nil {
		s.ran = map[string]string{}
	}
	s.ran[sha] = outcome
}

// spent: step failed tries times (its needs-you is out); it is not tried again.
func (s promoteState) spent(step string) bool { return s.fails[step] >= tries }

// waitRetry: step's last try failed less than promoteRetry ago.
func (s promoteState) waitRetry(step string, now time.Time) bool {
	return s.fails[step] > 0 && now.Sub(s.failedAt) < promoteRetry
}

// loopIdle: no merge-loop step runs and no task is between its submit and its merge (under mu): the promotion's idle
// trigger.
func (p *proj) loopIdle() bool {
	if p.running.Task != "" || len(p.tests) > 0 {
		return false
	}
	for _, mt := range p.folded.loop {
		switch mt.state {
		case stSubmitted, stCandidate, stGreen, stAwaiting, stConflict, stResolving, stResolved, stAccepted, stMoved,
			stPublished:
			return false
		}
	}
	return true
}

// promotion is the pass's promotion step (see the top of this file): settle, a pending run, a GREEN run's
// fast-forward, after_promote, then the trigger. Off (no Loop.Promote, or no review git): nothing.
func (a *App) promotion(ctx context.Context, p *proj, fx *Loop) {
	pr := fx.Promote
	if pr == nil || fx.Review == nil {
		return
	}
	a.mu.Lock()
	st := p.prom
	st.ran = maps.Clone(p.prom.ran) // the pass reads its own copy, the fold writes the map under mu
	idle, restarting := p.loopIdle(), !p.restarting.IsZero()
	a.mu.Unlock()
	if st.running {
		return // the step settles its own outcome
	}
	if st.unsettled && st.out != nil { // a crash between a RED result and its needs-you
		a.promoteRed(p, *st.out, st.in)
	}
	now := a.opt.Now()
	if st.step != "" && !st.done && !st.spent(st.step) { // a run cut by a restart: again at its recorded H
		if !st.waitRetry(st.step, now) && !restarting {
			a.goPromote(ctx, p, func(ctx context.Context) { a.runPromote(ctx, p, pr, st.step, st.in) })
		}
		return
	}
	head, err := fx.Review.Head(ctx, pr.Target)
	if err == nil {
		var at string
		if at, err = fx.Review.Head(ctx, pr.Branch); err == nil {
			a.promoteHeads(ctx, p, fx, pr, st, head, at, idle, restarting, now)
			return
		}
	}
	a.logf("promotion of %s: %v", p.name, err)
}

// promoteHeads is promotion's part once it read the target's head and the promoted branch's sha (at).
func (a *App) promoteHeads(ctx context.Context, p *proj, fx *Loop, pr *Promote, st promoteState, head, at string,
	idle, restarting bool, now time.Time) {
	wait := ""
	defer func() {
		a.mu.Lock()
		p.prom.branch, p.prom.head, p.prom.wait = pr.Branch, at, wait
		a.mu.Unlock()
	}()
	r, reviews := a.promoteCandidate(p, st)
	// R not promoted yet (at R already: a crash after the fast-forward when its GREEN is recorded, the fact is what is
	// missing): its GREEN fast-forwards the promoted branch; no run at R yet: the suite at R; RED or failed there: it
	// waits for the next close.
	if r != "" && r != st.promoted && (r != at || st.ran[r] == "GREEN") {
		switch ffKey := "ff " + short(r); {
		case st.ran[r] == "GREEN":
			if !st.spent(ffKey) && !st.waitRetry(ffKey, now) {
				since := at
				a.goPromote(ctx, p, func(ctx context.Context) { a.promoteFF(ctx, p, pr, r, since, ffKey) })
				return
			}
		case st.ran[r] == "" && !restarting:
			in, err := a.promoteBatch(ctx, p, fx, pr, at, r)
			if err != nil {
				a.logf("promotion of %s: merges since %s: %v", p.name, short(at), err)
				return
			}
			step := event.StepPromote + "." + strconv.Itoa(st.seq+1)
			a.goPromote(ctx, p, func(ctx context.Context) { a.runPromote(ctx, p, pr, step, in) })
			return
		}
	}
	if o := st.out; reviews && o != nil && o.Verdict == "GREEN" && o.SHA != r && o.SHA != st.promoted {
		wait = "GREEN " + short(o.SHA) + " waits: " + a.reviewWait(p)
	}
	// after_promote of the last promoted sha, once (G: a cut one runs again).
	if st.promoted != "" && (st.afterSHA != st.promoted || !st.afterDone) {
		step := st.afterStep
		if st.afterSHA != st.promoted || step == "" {
			step = event.StepAfterPromote + "." + strconv.Itoa(st.afterSeq+1)
		}
		if !st.spent(step) && !st.waitRetry(step, now) {
			d := promotedData{SHA: st.promoted, Since: st.since}
			a.goPromote(ctx, p, func(ctx context.Context) { a.afterPromote(ctx, p, pr, step, d) })
			return
		}
	}
	if head == at {
		a.setMerges(p, 0)
		return
	}
	in, err := a.promoteBatch(ctx, p, fx, pr, at, head)
	if err != nil {
		a.logf("promotion of %s: merges since %s: %v", p.name, short(at), err)
		return
	}
	a.setMerges(p, len(in.Merges))
	if restarting || len(in.Merges) == 0 || in.Weight < pr.Weight && !idle {
		return
	}
	if reviews {
		a.promoteReview(ctx, p, fx, in, r)
	}
	if st.ran[head] != "" {
		return // run at this head already: a RED one waits for a new merge, a GREEN one for a close at it
	}
	step := event.StepPromote + "." + strconv.Itoa(st.seq+1)
	a.goPromote(ctx, p, func(ctx context.Context) { a.runPromote(ctx, p, pr, step, in) })
}

// promoteCandidate is the one sha the promotion may move the promoted branch to (RV-136, T-978), and whether reviews
// are set up: R, the reviewed head of the last closed review (the fold's closedHead: its start's head, or its last
// finished verify's; "": none closed in the log yet); reviews not set up, the last run's sha when it is GREEN (nothing
// to wait for).
func (a *App) promoteCandidate(p *proj, st promoteState) (string, bool) {
	if _, err := reviewConfig(p.root); err != nil {
		if o := st.out; o != nil && o.Verdict == "GREEN" {
			return o.SHA, false
		}
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.folded.closedHead, true
}

// reviewWait is why a GREEN run at another sha than R waits, for obor status: the open review, the review request,
// or no review closed at it.
func (a *App) reviewWait(p *proj) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case p.folded.reviewed.open() != "":
		return p.folded.reviewed.open() + " open (not closed)"
	case p.folded.review != nil:
		return "the review request"
	}
	return "no review closed at it"
}

// setMerges keeps the merges since the promoted sha for obor status.
func (a *App) setMerges(p *proj, n int) {
	a.mu.Lock()
	p.prom.merges = n
	a.mu.Unlock()
}

// promoteBatch is the promotion intent for head: the tasks merged since at (first-parent merges, oldest first) and
// their weight (each its spec's Difficulty level's weight; no spec or no line: medium's).
func (a *App) promoteBatch(ctx context.Context, p *proj, fx *Loop, pr *Promote, at, head string) (promoteIn, error) {
	merges, err := fx.Review.Merges(ctx, at, head)
	if err != nil {
		return promoteIn{}, err
	}
	in := promoteIn{SHA: head, Since: at}
	for _, m := range merges {
		_, subject, _ := strings.Cut(m, " ")
		task := parseMergeSubject(subject)
		if task == "" {
			continue
		}
		level := "medium"
		row, ok, err := specOf(p, task)
		if err != nil {
			return promoteIn{}, err
		}
		if l, known := spec.Difficulty(row.Text); ok && known {
			level = l
		}
		in.Merges = append(in.Merges, task)
		in.Weight += pr.Weigh(level)
	}
	return in, nil
}

// promoteReview requests the review of in's batch (fact review.request by obor, its note naming the batch) when a
// first-parent merge since r (the last closed review's reviewed head; "": none, every merge) is in in.SHA and no
// review, request or claim is open.
func (a *App) promoteReview(ctx context.Context, p *proj, fx *Loop, in promoteIn, r string) {
	a.mu.Lock()
	busy := p.folded.review != nil || p.folded.reviewed.open() != "" || p.rv.what != ""
	a.mu.Unlock()
	if busy || r == in.SHA {
		return
	}
	merges, err := fx.Review.Merges(ctx, r, in.SHA)
	if err != nil {
		a.logf("promotion of %s: merges since %s: %v", p.name, short(r), err)
		return
	}
	if len(merges) == 0 {
		return
	}
	note := fmt.Sprintf("promotion batch %s..%s: %s", short(in.Since), short(in.SHA), strings.Join(in.Merges, ", "))
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.folded.review != nil || p.folded.reviewed.open() != "" || p.rv.what != "" {
		return
	}
	if a.recordLocked(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewRequest, Kind: event.KindFact},
		ReviewData{By: "obor", Note: note}) {
		a.wake()
	}
}

// goPromote runs fn, a promotion step, in its own goroutine (one at a time: p.prom.running), outside the scratch.
func (a *App) goPromote(ctx context.Context, p *proj, fn func(context.Context)) {
	a.mu.Lock()
	if p.prom.running {
		a.mu.Unlock()
		return
	}
	p.prom.running = true
	a.mu.Unlock()
	a.effects.Add(1)
	go func() {
		defer a.effects.Done()
		fn(ctx)
		a.mu.Lock()
		p.prom.running = false
		a.mu.Unlock()
		a.wake()
	}()
}

// runPromote runs the full suite at in.SHA under step's intent, then settles a RED result (its needs-you) or counts
// the failure; a GREEN one is the next pass's fast-forward.
func (a *App) runPromote(ctx context.Context, p *proj, pr *Promote, step string, in promoteIn) {
	key := event.Intent{Key: event.Key{Task: event.PromoteTask, Step: step}, Class: event.ClassGit, Data: in}
	raw, err := a.do(ctx, p, key, func(ctx context.Context) (any, error) {
		out, err := pr.Run(ctx, in.SHA)
		if err == nil && out.Verdict != "GREEN" && out.Verdict != "RED" {
			err = errors.New("no GREEN or RED: " + out.Line)
		}
		return out, err
	})
	if err != nil {
		a.promoteFailed(ctx, p, step, err)
		return
	}
	var out port.PromoteOut
	if json.Unmarshal(raw, &out) == nil && out.Verdict == "RED" {
		a.promoteRed(p, out, in)
	}
}

// promoteFF fast-forwards the promoted branch to sha (since: where it was), then records fact promoted{sha}, which
// ends a RED promotion's needs-you; a failure is counted under key.
func (a *App) promoteFF(ctx context.Context, p *proj, pr *Promote, sha, since, key string) {
	if _, err := pr.FF(ctx, sha); err != nil {
		a.promoteFailed(ctx, p, key, err)
		return
	}
	if a.record(p, event.Record{Task: event.PromoteTask, Step: event.StepPromoted, Kind: event.KindFact},
		promotedData{SHA: sha, Since: since}) {
		a.settleNeeds(p, event.PromoteTask, "")
	}
}

// afterPromote runs commands.after_merge for the promoted d.SHA under step's intent (row 51).
func (a *App) afterPromote(ctx context.Context, p *proj, pr *Promote, step string, d promotedData) {
	in := event.Intent{Key: event.Key{Task: event.PromoteTask, Step: step}, Class: event.ClassGit, Data: d}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) { return pr.After(ctx, d.SHA, d.Since) }); err != nil {
		a.promoteFailed(ctx, p, step, err)
	}
}

// promoteFailed counts err, the failure of the promotion step key (fact failed); the tries-th is the project's
// needs-you{promotion failed: <key>: <err>}. A cut by serve's stop is no failure (T-660).
func (a *App) promoteFailed(ctx context.Context, p *proj, key string, err error) {
	if cutByServe(ctx, err) {
		a.shutDown(event.PromoteTask + " " + event.StepFailed + "{" + key + ": " + err.Error() + "}")
		return
	}
	if !a.record(p, event.Record{Task: event.PromoteTask, Step: event.StepFailed, Kind: event.KindFact},
		event.FactData{Machine: Machine, Reason: key + ": " + err.Error(), Step: key}) {
		return
	}
	a.mu.Lock()
	tell := p.prom.fails[key] >= tries
	a.mu.Unlock()
	if tell {
		a.settleNeeds(p, event.PromoteTask, "promotion"+failedSuffix+": "+key+": "+oneLine(err.Error()))
	}
}

// promoteRed is a RED run's needs-you{promotion RED <sha7>: <line>; merges since <since7>: T-a, …}: one line, the
// nightly's shape; the next GREEN promotion ends it (fact promoted).
func (a *App) promoteRed(p *proj, out port.PromoteOut, in promoteIn) {
	since := "the first commit"
	if in.Since != "" {
		since = short(in.Since)
	}
	merges := strings.Join(in.Merges, ", ")
	if merges == "" {
		merges = "none"
	}
	reason := fmt.Sprintf("%s%s: %s; merges since %s: %s", promoteRedPrefix, short(out.SHA), oneLine(out.Line), since,
		merges)
	if !a.settleNeeds(p, event.PromoteTask, reason) {
		return
	}
	a.mu.Lock()
	p.prom.unsettled = false
	a.mu.Unlock()
}
