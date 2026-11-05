package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"obor/internal/config"
)

// A review request (T-638; docs/state-model.md row 52): `obor review [--after T-a,T-b] [--note "<text>"]` (the lead or
// the owner) records fact review.request{after, note, by} on the task key review; one is open at a time, a second
// call replaces it, `obor review --rm` ends it (fact review.rm). A merge-loop pass fires the open request once every
// --after task merged and the reviewer role agent is idle (no open brief, its pane idle or not running): fact
// review.fire{request: its seq} first (its fold ends the request, so a replay never fires it again), then the facts
// from git (ReviewGit: the next id, the integration branch's head, the merges and files since the previous review),
// the reviewer's role worktree detached at the head (ReviewGit.Detach, T-853: merges never wait for the reviewer), and
// the reviewer's fresh start with the brief as its first prompt (the same path as obor brief's fresh start, its brief
// recorded the same way: briefed with no line). Then fact review.started{id, head, tasks}: no line to the lead (T-650;
// obor status counts from its head), and the brief; once it is recorded the head is marked reviewed (ReviewGit.Mark,
// T-856: a mirror of the records, a failed mark is logged). Any step
// failing: no start, needs-you{RV-xx: <error>} on the key review (a new request ends it). The first --after task
// dropped or rejected ends the request with the lead's line "[obor] review request ended: T-b dropped" (fact message
// {review: the request's seq}, whose fold ends it).
//
// One review cycle is open at a time (RV-69 #3): the last review serve started (fact review.started, so a replay
// keeps it) is open until an "RV-<n> closed" commit for it (or a later one) is on the integration branch, read in git
// at the call and at firing. While one is open, obor review refuses a new request ("RV-69 is open (not closed): close
// it first"), and a request that comes due waits (obor status: "waiting RV-69 to close"): a fresh start would replace
// the session its verify needs. A request a pass claimed (RV-69 #1: to fire it, or for its ended line) cannot change
// until the claim ends: obor review's replace and --rm are refused while it holds (reviewClaim). A crash between the
// reviewer's launch and fact review.started is accepted (RV-69 #2): the launch is not exactly-once; a failing append
// after the launch is a needs-you "<RV-xx>: started but not recorded: <error>".

// reviewDue is how many merges since the last review make obor status say a review is due (more than it).
const reviewDue = 3

// maxBriefFiles caps the files a brief lists; the rest is named by the git command that lists them.
const maxBriefFiles = 100

// ReviewData is a review fact's data: the request's tasks and note, who asked, the request a firing or start is of,
// the review's id, head and tasks once started, a verify request's findings and fix round (T-856), and the call that
// recorded it with its reply.
type ReviewData struct {
	After    []string `json:"after,omitempty"`
	Note     string   `json:"note,omitempty"`
	By       string   `json:"by,omitempty"`
	Request  int64    `json:"request,omitempty"`
	ID       string   `json:"id,omitempty"`
	Head     string   `json:"head,omitempty"`
	Tasks    []string `json:"tasks,omitempty"`
	Findings []string `json:"findings,omitempty"`
	Round    int64    `json:"round,omitempty"`
	Call     string   `json:"call,omitempty"`
	Reply    string   `json:"reply,omitempty"`
}

// ReviewClose is the newest close commit on the integration branch: its subject ("RV-<n> closed · <sha> · approved ·
// <summary>") and its body ("": a close commit from before T-822, which records neither the tasks its cycle covered
// nor the sha its review started at).
type ReviewClose struct {
	Subject, Body string
}

// ReviewGit is the git a review's start reads and does on the project (cmd/obor implements it on the main checkout).
type ReviewGit interface {
	// Last is the newest commit on branch whose subject starts "RV-<n> closed" ({}, subject "", when none).
	Last(ctx context.Context, branch string) (ReviewClose, error)
	// Head is branch's sha.
	Head(ctx context.Context, branch string) (string, error)
	// Merges is the first-parent merges of from..to as "<sha> <subject>" lines, oldest first (from "": all of to).
	Merges(ctx context.Context, from, to string) ([]string, error)
	// Files is the files changed from from to to (from "": every file of to).
	Files(ctx context.Context, from, to string) ([]string, error)
	// Mark records sha as the open review's reviewed head (git ref ReviewedRef): obor review close names it.
	Mark(ctx context.Context, sha string) error
	// Detach puts the role worktree at dir on sha, detached, adding it when it is missing (T-853).
	Detach(ctx context.Context, dir, sha string) error
}

// reviewReq is the open review request: its fact's seq and time, its tasks, note and asker, and the open review it
// waits for once due (held: the last pass's git read, not folded; "": none).
type reviewReq struct {
	seq   int64
	at    time.Time
	after []string
	note  string
	by    string
	held  string
}

// reviewClaim is a pass's claim of the open review request (live). what is "starting" from the claim until the
// reviewer's start returned (its fire fact appended and folded first), "ending" until its ended line is ("": no
// claim; under App.mu). mu orders a claim against obor review's changes: reviewCall holds it from its check through
// its fact's append and fold, reviewPass while it claims, so a change is either folded before a claim or refused
// during one. claimed is a test hook: it runs once a pass claimed, before the claim's first append (nil: none).
type reviewClaim struct {
	mu      sync.Mutex
	what    string
	claimed func()
}

// lastReview is the newest review the log started: its fact's seq, its id and the head it reviews (seq 0: none).
type lastReview struct {
	seq      int64
	id, head string
}

// reviewDueLine is obor status' "review due" line ("": none) as a pass last counted it in git, and what it was
// counted on: the merges folded and the last review started (key), and when.
type reviewDueLine struct {
	key  string
	at   time.Time
	line string
}

// reviewDueEvery is how often the "review due" count is read again when nothing it was counted on changed (a review's
// close is a commit serve does not record).
const reviewDueEvery = 3 * time.Minute

// foldReview folds r, a record of the project's review requests: a request opens (replacing the open one) and ends
// a failed start's needs-you; --rm and a firing end it; a start is the last review.
func (p *proj) foldReview(r event.Record) {
	if r.Task != event.ReviewTask || r.Kind != event.KindFact {
		return
	}
	var d ReviewData
	_ = json.Unmarshal(r.Data, &d)
	switch r.Step {
	case event.StepReviewRequest:
		p.folded.review = &reviewReq{seq: r.Seq, at: event.RecordTime(r), after: d.After, note: d.Note, by: d.By}
		p.clear(event.ReviewTask)
	case event.StepReviewRm:
		p.folded.review = nil
		p.folded.reviewEnd = idleEnd{r.Seq, event.RecordTime(r)} // T-868: the request's end
		p.clear(event.ReviewTask)
	case event.StepReviewFire:
		p.reviewEnded(d.Request)
		p.folded.reviewEnd = idleEnd{r.Seq, event.RecordTime(r)} // T-868: the request's end
	case event.StepReviewStarted:
		p.folded.reviewed = lastReview{seq: r.Seq, id: d.ID, head: d.Head}
	case event.StepReviewFix, event.StepReviewVerifyRequest: // their records are reviewLog's (reviewverify.go): only the call here
		if d.Call != "" {
			method := wire.MethodFix
			if r.Step == event.StepReviewVerifyRequest {
				method = wire.MethodVerify
			}
			p.folded.calls[verdictStep(method, d.Call)] = d.Reply
		}
		return
	default:
		return
	}
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodReview, d.Call)] = d.Reply
	}
}

// reviewEnded ends the review request whose fact is seq (a firing's fold, or its ended line's).
func (p *proj) reviewEnded(seq int64) {
	if q := p.folded.review; q != nil && q.seq == seq {
		p.folded.review = nil
	}
}

// reviewOutcome is how the request's tasks ended before they merged, as its line names it: "T-b dropped" or "T-b
// rejected" for the first such task; "" while none has (under mu).
func (p *proj) reviewOutcome(after []string) string {
	for _, task := range after {
		switch mt := p.folded.loop[task]; {
		case mt == nil:
		case mt.state == stDropped:
			return task + " dropped"
		case mt.state == stRejected:
			return task + " rejected"
		}
	}
	return ""
}

// reviewWaits is the tasks of after not merged yet (none for no tasks; under mu).
func (p *proj) reviewWaits(after []string) []string {
	if len(after) == 0 {
		return nil
	}
	return p.waiting(strings.Join(after, ","))
}

// ReviewSetup is what a review of a project needs from its config: the reviewer agent, its role worktree (absolute,
// T-853: serve moves it to the reviewed head), the main checkout (Root), the reviews dir in it (T-718) and the
// integration branch.
type ReviewSetup struct {
	Agent, Worktree, Root, Reviews, Branch string
}

// ReviewedRef is the git ref of the open review's reviewed head (T-718: it was the review worktree's HEAD): written
// once a review's start or a verify is briefed, after the reviewer's worktree moved there (T-856). It mirrors the log:
// obor review close reads the briefed head from the log (ReviewCycle.Reviewed) and this ref only for a review an older
// obor started. The cycle's covered tasks are not here (T-822): the close commit's body lists them (ReviewCycle).
const ReviewedRef = "refs/obor/reviewed"

// reviewConfig is ReviewConfig of the project at root.
func reviewConfig(root string) (ReviewSetup, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return ReviewSetup{}, err
	}
	return ReviewConfig(cfg)
}

// ReviewConfig is cfg's review setup (serve's review start; obor review verify and close, T-716); an error names what
// is missing.
func ReviewConfig(cfg *config.Config) (ReviewSetup, error) {
	rc, _ := cfg.Role("reviewer")
	s := ReviewSetup{Agent: rc.Agent, Worktree: cfg.RoleWorktree(rc.Role.Name), Root: cfg.Root,
		Reviews: cfg.Paths().Reviews, Branch: cfg.Data.Branches.Main}
	switch {
	case !rc.Configured || rc.Agent == "":
		return s, errors.New("no reviewer role (obor setup role reviewer)")
	case s.Reviews == "":
		return s, errors.New("no reviews dir (paths.reviews)")
	case s.Branch == "":
		return s, errors.New("no integration branch (branches.main)")
	}
	return s, nil
}

// reviewCall answers the socket's review request (the lead or the owner): a call id is required and a repeated one
// gets the stored reply. --rm: REVIEW REQUEST ENDED (fact review.rm), or NO REVIEW REQUEST. No --after, no note and a
// request open: it is listed (reviewList). Else a request (fact review.request): REVIEW REQUESTED (now | after
// T-a,T-b), " (replaces the request of <age> ago)" when one was open; a task neither dispatched nor specified, a task
// of --after dropped or rejected already, no reviewer configured, or a review open (reviewOpen): refused, nothing
// recorded. A pass's claim held (reviewClaim): --rm and a request are refused.
func (a *App) reviewCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodReview + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodReview, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodReview, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("review: the lead or the owner requests a review, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	p.rv.mu.Lock() // no pass claims the request from this check to this call's fact's fold
	defer p.rv.mu.Unlock()
	a.mu.Lock()
	open, claim, started := p.folded.review, p.rv.what, p.folded.reviewed
	var since time.Duration
	if open != nil {
		since = a.opt.Now().Sub(open.at)
	}
	end := p.reviewOutcome(c.After)
	var unknown []string
	for _, task := range c.After {
		if p.folded.loop[task] == nil {
			unknown = append(unknown, task)
		}
	}
	a.mu.Unlock()
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	d := ReviewData{By: by, Call: c.CallID}
	step := event.StepReviewRequest
	switch {
	case c.Clear && open == nil:
		return "NO REVIEW REQUEST", nil
	case len(c.After) == 0 && c.Note == "" && open != nil && !c.Clear:
		return a.reviewList(p), nil
	case claim != "":
		return "", fmt.Errorf("review: obor serve is %s the review request now: nothing changed (try again once it is done)", claim)
	case c.Clear:
		step, d.Reply = event.StepReviewRm, "REVIEW REQUEST ENDED"
	case end != "":
		return "", fmt.Errorf("review: %s already", end)
	default:
		s, err := reviewConfig(p.root)
		if err != nil {
			return "", fmt.Errorf("review: %v", err)
		}
		if id, err := a.reviewOpenNow(p, s.Branch, started); err != nil {
			return "", fmt.Errorf("review: %v", err)
		} else if id != "" {
			return "", fmt.Errorf("%s is open (not closed): close it first", id)
		}
		for _, task := range unknown {
			_, ok, err := specOf(p, task)
			if err != nil {
				return "", fmt.Errorf("review: %v", err)
			}
			if !ok {
				return "", fmt.Errorf("review: unknown task %s (no dispatch or spec in %s)", task, p.name)
			}
		}
		d.After, d.Note, d.Reply = c.After, c.Note, "REVIEW REQUESTED (now)"
		if len(c.After) > 0 {
			d.Reply = "REVIEW REQUESTED (after " + strings.Join(c.After, ",") + ")"
		}
		if open != nil {
			d.Reply += " (replaces the request of " + Age(since) + " ago)"
		}
	}
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: step, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("review: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// reviewList is p's open request as obor review lists it: REVIEW REQUEST since <age> (now | after T-a,T-b[, waiting
// T-b])[, note: <note>].
func (a *App) reviewList(p *proj) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return "REVIEW REQUEST " + p.reviewStatus(a.opt.Now())
}

// reviewStatus is the open request as obor status and obor review show it, else the "review due" line the last pass
// counted in git (reviewDueTick), else "" (under mu).
func (p *proj) reviewStatus(now time.Time) string {
	if q := p.folded.review; q != nil {
		s := "since " + Age(now.Sub(q.at)) + " (now"
		if len(q.after) > 0 {
			s = "since " + Age(now.Sub(q.at)) + " (after " + strings.Join(q.after, ",")
			if w := p.reviewWaits(q.after); len(w) > 0 {
				s += ", waiting " + strings.Join(w, ",")
			}
		}
		if q.held != "" {
			s += ", waiting " + q.held + " to close"
		}
		s += ")"
		if q.note != "" {
			s += ", note: " + strings.Join(strings.Fields(q.note), " ")
		}
		return s
	}
	return p.due.line
}

// reviewDueTick counts, in git, the merges in the next review's scope (reviewScope): since the previous review's review
// start sha, without the tasks its cycle covered. The previous review is the newest "RV-n closed" commit (or, one
// started and not closed yet, its head). More than reviewDue: obor status' line "review due: N merges since <RV-prev>".
// It counts again once a merge or a start folded, or reviewDueEvery passed; a git error keeps the last line and is tried
// on the next pass.
func (a *App) reviewDueTick(ctx context.Context, p *proj, fx *Loop) {
	if fx.Review == nil {
		return
	}
	a.mu.Lock()
	n := 0
	for _, mt := range p.folded.loop {
		if mt.state == stMerged {
			n++
		}
	}
	started := p.folded.reviewed
	key := fmt.Sprintf("%d %d", n, started.seq)
	now := a.opt.Now()
	fresh := key == p.due.key && now.Sub(p.due.at) < reviewDueEvery
	a.mu.Unlock()
	if fresh {
		return
	}
	line, err := a.reviewDueCount(ctx, p, fx.Review, started)
	if err != nil {
		a.logf("review due of %s: %v", p.name, err)
		return
	}
	a.mu.Lock()
	p.due = reviewDueLine{key: key, at: now, line: line}
	a.mu.Unlock()
}

// reviewDueCount is the "review due" line from git ("": not due, or no reviewer configured): the merges in the next
// review's scope (reviewScope: since the previous close's review start sha, the cycle's covered tasks left out).
func (a *App) reviewDueCount(ctx context.Context, p *proj, g ReviewGit, started lastReview) (string, error) {
	s, err := reviewConfig(p.root)
	if err != nil {
		return "", nil // reviews are not set up: none is due
	}
	last, err := g.Last(ctx, s.Branch)
	if err != nil {
		return "", err
	}
	k, prev, _ := PreviousReview(last.Subject)
	from, covered := reviewScope(last)
	if n, _ := strconv.Atoi(strings.TrimPrefix(started.id, "RV-")); n >= k && started.head != "" {
		prev, from, covered = started.id, started.head, nil // started, not closed yet: its findings are being fixed
	}
	head, err := g.Head(ctx, s.Branch)
	if err != nil {
		return "", err
	}
	merges, err := g.Merges(ctx, from, head)
	if err != nil {
		return "", err
	}
	if merges = uncovered(merges, covered); len(merges) <= reviewDue {
		return "", nil
	}
	return fmt.Sprintf("review due: %d merges since %s", len(merges), cmp.Or(prev, "the first commit (no RV closed yet)")), nil
}

// reviewOpen is the id of started, the last review serve started, while no "RV-<n> closed" commit on branch closes it
// or a later one ("": none open, or none started).
func reviewOpen(ctx context.Context, g ReviewGit, branch string, started lastReview) (string, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(started.id, "RV-"))
	if started.seq == 0 || err != nil {
		return "", nil
	}
	last, err := g.Last(ctx, branch)
	if err != nil {
		return "", err
	}
	if next, _, _ := PreviousReview(last.Subject); n >= next {
		return started.id, nil
	}
	return "", nil
}

// reviewOpenNow is reviewOpen for obor review's call, on the merge loop's git (none: no review is started, none open).
func (a *App) reviewOpenNow(p *proj, branch string, started lastReview) (string, error) {
	fx, err := a.opt.Loop(p.root)
	if err != nil || fx.Review == nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return reviewOpen(ctx, fx.Review, branch, started)
}

// reviewPass fires p's open review request when it is due, or ends it when a task of its --after ended unmerged. Due:
// every --after task merged, no review open in git (reviewOpen; one open: the request waits, held) and the reviewer
// idle; a publish running, it waits for the next pass, and a start claimed is admitted (roleworktree.go, T-730) until
// fireReview returned. A pass claims the request (reviewClaim) before it records: fired, its fact ends it and the claim holds until
// the start returned; an ended line, its fold does.
func (a *App) reviewPass(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	q, started := p.folded.review, p.folded.reviewed
	run := q != nil && p.rv.what == "" && p.merging && p.out.lead() != "" && fx.Review != nil && fx.Agents != nil
	ready := run && p.reviewOutcome(q.after) == "" && len(p.reviewWaits(q.after)) == 0
	a.mu.Unlock()
	if !run {
		return
	}
	setup, cfgErr := reviewConfig(p.root)
	open, checked := "", ready && cfgErr == nil
	if checked {
		var err error
		if open, err = reviewOpen(ctx, fx.Review, setup.Branch, started); err != nil {
			a.logf("review of %s: is the last review closed: %v", p.name, err)
			return
		}
	}
	p.rv.mu.Lock()
	a.mu.Lock()
	if q != p.folded.review || p.rv.what != "" || p.folded.reviewed != started {
		a.mu.Unlock()
		p.rv.mu.Unlock()
		return
	}
	end := p.reviewOutcome(q.after)
	merged := end == "" && len(p.reviewWaits(q.after)) == 0
	q.held = ""
	if merged && open != "" {
		q.held = open
	}
	due := merged && open == "" && (cfgErr != nil || checked && a.roleIdle(p, setup.Agent))
	switch {
	case end != "":
		p.rv.what = "ending"
	case due && p.publishing(): // the head it reads is the published one: the publish's end wakes the next pass (T-730)
	case due:
		p.rv.what = "starting"
		p.admits++ // admitted (roleworktree.go): no publish starts until the reviewer's brief is recorded
	}
	claimed, hook := p.rv.what != "", p.rv.claimed
	a.mu.Unlock()
	p.rv.mu.Unlock()
	if !claimed {
		return
	}
	if hook != nil {
		hook()
	}
	if end != "" {
		m := event.Message{To: p.out.lead(), From: "obor", Line: "[obor] review request ended: " + end, Review: q.seq}
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
	} else if a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewFire, Kind: event.KindFact}, ReviewData{Request: q.seq}) {
		a.fireReview(ctx, p, fx, q, setup, cfgErr)
	}
	a.mu.Lock()
	if p.rv.what == "starting" {
		p.admits--
	}
	p.rv.what = ""
	a.mu.Unlock()
	a.wake()
}

// roleIdle: the role agent has no open brief and its pane, when one runs it, is idle (none: a fresh start places it;
// two claiming it: not idle): the reviewer's for a review's start, the deep dive's for its worktree's move at a brief
// (briefMove) (under mu).
func (a *App) roleIdle(p *proj, agent string) bool {
	if p.out.brief(agent) != nil {
		return false
	}
	switch w := a.whereIs(p, agent); w.State {
	case Identified:
		return Ready(w.Pane.Status)
	case Ambiguous:
		return false
	}
	return true
}

// fireReview starts the review of the fired request q: the facts, the reviewed head marked, the reviewer's fresh start
// on the brief, its brief recorded, fact review.started; a step failing records needs-you {<id>: <error>} instead and
// starts nothing. A fact after the launch the log refuses is logged and a needs-you {<id>: started but not recorded:
// <error>} (RV-69 #2: the review runs, serve does not know it). The lead gets no line (T-650): obor status counts the
// merges since the review's head.
func (a *App) fireReview(ctx context.Context, p *proj, fx *Loop, q *reviewReq, s ReviewSetup, cfgErr error) {
	id, brief, tasks, head, err := a.prepareReview(ctx, fx.Review, q, s, cfgErr)
	if err == nil {
		_, err = fx.Agents.Launch(ctx, port.Launch{Agent: s.Agent, Fresh: true, Prompt: brief})
	}
	if err != nil {
		reason := "next id: " + err.Error()
		if id != "" {
			reason = id + ": " + err.Error()
		}
		if cutByServe(ctx, err) { // serve is stopping: the review's start was cut, not refused (T-660)
			a.shutDown(event.ReviewTask + " " + event.StepNeedsYou + "{" + reason + "}")
			return
		}
		a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Machine: Machine, Reason: reason})
		return
	}
	var lost error
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{Request: q.seq, ID: id, Head: head, Tasks: tasks}) {
		lost = cmp.Or(p.log.Err(), errors.New("the event log refused fact "+event.StepReviewStarted))
	}
	if _, ok := a.recordBrief(p, s.Agent, "obor", "", "", brief, false, nil); !ok {
		lost = cmp.Or(lost, p.log.Err(), errors.New("the event log refused the brief"))
	} else if err := fx.Review.Mark(ctx, head); err != nil { // T-856: once briefed; a mirror, close reads the log
		a.logf("review of %s: %s not moved to %s: %v", p.name, ReviewedRef, head, err)
	}
	if lost != nil {
		reason := id + ": started but not recorded: " + lost.Error()
		a.logf("FAIL review of %s: %s", p.name, reason)
		a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Machine: Machine, Reason: reason})
	}
}

// closedRE is a review's close commit subject: "RV-<n> closed · <reviewed sha> · …".
var closedRE = regexp.MustCompile(`^RV-([0-9]+) closed(?: · ([0-9a-f]+))?`)

// PreviousReview is the next review's number, and the previous review's id and reviewed sha ("": none) by last, the
// newest close commit's subject ("": none yet, the next is RV-1).
func PreviousReview(last string) (next int, prev, sha string) {
	m := closedRE.FindStringSubmatch(last)
	if m == nil {
		return 1, "", ""
	}
	k, _ := strconv.Atoi(m[1])
	return k + 1, "RV-" + m[1], m[2]
}

// mergeRE is an obor merge's subject: "Merge T-xx: …".
var mergeRE = regexp.MustCompile(`^Merge ([^ :]+):`)

// closeCovered reads a close commit's body (T-822): the tasks its cycle covered and the sha its review started at. Both
// are empty on an old close commit (no "Covered:" line): its scope is the sha range, as before.
func closeCovered(body string) (covered []string, start string) {
	for _, l := range strings.Split(body, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), ":")
		switch k {
		case "Covered":
			for _, t := range strings.Split(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					covered = append(covered, t)
				}
			}
		case "Start":
			start = strings.TrimSpace(v)
		}
	}
	return covered, start
}

// reviewScope is where the next review's range starts, and the tasks a closed cycle already covered: the previous
// close's review start sha and Covered list (T-822), else its reviewed sha and no task (an old close commit: the sha
// range).
func reviewScope(last ReviewClose) (from string, covered []string) {
	_, _, sha := PreviousReview(last.Subject)
	covered, start := closeCovered(last.Body)
	return cmp.Or(start, sha), covered
}

// uncovered is merges' "<sha> <subject>" lines whose task no covered id names (one whose subject names none: kept).
func uncovered(merges, covered []string) []string {
	if len(covered) == 0 {
		return merges
	}
	var out []string
	for _, m := range merges {
		_, subject, _ := strings.Cut(m, " ")
		if t := mergeRE.FindStringSubmatch(subject); t != nil && slices.Contains(covered, t[1]) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// prepareReview collects a review's facts in order (the next id, the head, the merges since the previous review's
// review start sha that no closed cycle covered and the files of that range) and detaches the reviewer's role worktree at
// the head (ReviewGit.Detach, T-853; fireReview marks it reviewed once the brief is recorded): the id, the brief, the in-scope tasks and
// the head; an error from the first step that fails (id "" when the id is not known yet).
func (a *App) prepareReview(ctx context.Context, g ReviewGit, q *reviewReq, s ReviewSetup, cfgErr error) (id,
	brief string, tasks []string, head string, err error) {
	last, err := g.Last(ctx, s.Branch)
	if err != nil {
		return "", "", nil, "", err
	}
	n, _, _ := PreviousReview(last.Subject)
	from, covered := reviewScope(last)
	id = "RV-" + strconv.Itoa(n)
	if cfgErr != nil {
		return id, "", nil, "", cfgErr
	}
	if head, err = g.Head(ctx, s.Branch); err != nil {
		return id, "", nil, "", err
	}
	merges, err := g.Merges(ctx, from, head)
	if err != nil {
		return id, "", nil, "", err
	}
	merges = uncovered(merges, covered)
	files, err := g.Files(ctx, from, head)
	if err != nil {
		return id, "", nil, "", err
	}
	if err = g.Detach(ctx, s.Worktree, head); err != nil {
		return id, "", nil, "", err
	}
	short := head[:min(len(head), 7)]
	var scope []string
	for _, m := range merges {
		sha, subject, _ := strings.Cut(m, " ")
		sha = sha[:min(len(sha), 7)]
		if t := mergeRE.FindStringSubmatch(subject); t != nil {
			tasks = append(tasks, t[1])
			scope = append(scope, t[1]+" ("+sha+")")
		} else {
			scope = append(scope, subject+" ("+sha+")")
		}
	}
	return id, reviewBrief(id, s, short, from, last.Subject, scope, files, q.note), tasks, head, nil
}

// reviewBrief is the reviewer's first prompt: skills/lead/review-brief.md §2's template with the facts filled in and
// the lead's note appended (its out of scope and contracts). last is the previous review's close commit subject (""
// none): its file is deleted at close (RV-69 #4), so the brief names what is kept, the close commit and BACKLOG. from
// is where the brief's range starts ("": the first commit).
func reviewBrief(id string, s ReviewSetup, short, from, last string, scope, files []string, note string) string {
	_, prev, _ := PreviousReview(last)
	inScope := strings.Join(scope, ", ")
	if inScope == "" {
		inScope = "no merges"
	}
	listed := files
	more := ""
	if len(files) > maxBriefFiles {
		listed = files[:maxBriefFiles]
		if from == "" {
			more = fmt.Sprintf(" and %d more (git ls-tree -r --name-only %s)", len(files)-maxBriefFiles, short)
		} else {
			more = fmt.Sprintf(" and %d more (git diff --name-only %s..%s)", len(files)-maxBriefFiles,
				from[:min(len(from), 7)], short)
		}
	}
	fileList := strings.Join(listed, ", ") + more
	if len(files) == 0 {
		fileList = "none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Review %s. %s is at %s. In scope: %s; files: %s.", id, s.Branch, short, inScope, fileList)
	if prev != "" {
		fmt.Fprintf(&b, " Previous review: %s (its close commit: %s; its accepted findings are the BACKLOG lines that name %s)"+
			" — don't repeat them.", prev, last, prev)
	} else {
		b.WriteString(" Previous review: none.")
	}
	fmt.Fprintf(&b, " Report only findings a real config or workflow hits; for each give severity, likelihood (the real"+
		" config or workflow that hits it), file:line, probe/evidence, suggested fix and its cost in added code, and an"+
		" Extent line: every other entry point / branch that shares the root cause, each marked checked or unchecked."+
		" A finding only a constructed case hits goes in a short \"Not reported (constructed)\" list instead: one line"+
		" each, no fix proposal. Your worktree is at %s (check out any commit there); write only %s/%s.md in the main"+
		" checkout %s. Do not commit.", short, s.Reviews, id, s.Root)
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("\nLead's note: " + note)
	}
	return b.String()
}
