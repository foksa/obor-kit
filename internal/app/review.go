package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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
// the review worktree detached at the head, and the reviewer's fresh start with the brief as its first prompt (the
// path of `obor restart <reviewer> --fresh --prompt`, its brief recorded the same way: briefed with no line). Then
// fact review.started{id, head, tasks}: no line to the lead (T-650; obor status counts from its head). Any step
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

// MethodReview is the app socket's review request: Call.After the tasks it waits for, Call.Note the lead's note,
// Call.Clear ends the open request; none of them: the open request listed, else a request to start now.
const MethodReview = "review"

// ReviewTask is the task key of a project's review requests and of a failed start's needs-you.
const ReviewTask = "review"

const (
	StepReviewRequest = "review.request" // fact: a review is requested (data: ReviewData)
	StepReviewRm      = "review.rm"      // fact: obor review --rm ended it (data: ReviewData)
	StepReviewFire    = "review.fire"    // fact: a pass fires the request (data: ReviewData, Request its seq)
	StepReviewStarted = "review.started" // fact: the reviewer started on the brief (data: ReviewData)
)

// reviewDue is how many merges since the last review make obor status say a review is due (more than it).
const reviewDue = 3

// maxBriefFiles caps the files a brief lists; the rest is named by the git command that lists them.
const maxBriefFiles = 100

// ReviewData is a review fact's data: the request's tasks and note, who asked, the request a firing or start is of,
// the review's id, head and tasks once started, and the call that recorded it with its reply.
type ReviewData struct {
	After   []string `json:"after,omitempty"`
	Note    string   `json:"note,omitempty"`
	By      string   `json:"by,omitempty"`
	Request int64    `json:"request,omitempty"`
	ID      string   `json:"id,omitempty"`
	Head    string   `json:"head,omitempty"`
	Tasks   []string `json:"tasks,omitempty"`
	Call    string   `json:"call,omitempty"`
	Reply   string   `json:"reply,omitempty"`
}

// ReviewGit is the git a review's start reads and does on the project (cmd/obor implements it on the main checkout).
type ReviewGit interface {
	// Last is the subject of the newest commit on branch whose subject starts "RV-<n> closed" ("": none).
	Last(ctx context.Context, branch string) (string, error)
	// Head is branch's sha.
	Head(ctx context.Context, branch string) (string, error)
	// Merges is the first-parent merges of from..to as "<sha> <subject>" lines, oldest first (from "": all of to).
	Merges(ctx context.Context, from, to string) ([]string, error)
	// Files is the files changed from from to to (from "": every file of to).
	Files(ctx context.Context, from, to string) ([]string, error)
	// Detach puts the worktree at dir on sha, detached.
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
func (p *proj) foldReview(r Record) {
	if r.Task != ReviewTask || r.Kind != KindFact {
		return
	}
	var d ReviewData
	_ = json.Unmarshal(r.Data, &d)
	switch r.Step {
	case StepReviewRequest:
		p.folded.review = &reviewReq{seq: r.Seq, at: recordTime(r), after: d.After, note: d.Note, by: d.By}
		p.clear(ReviewTask)
	case StepReviewRm:
		p.folded.review = nil
		p.clear(ReviewTask)
	case StepReviewFire:
		p.reviewEnded(d.Request)
	case StepReviewStarted:
		p.folded.reviewed = lastReview{seq: r.Seq, id: d.ID, head: d.Head}
	default:
		return
	}
	if d.Call != "" {
		p.folded.calls[verdictStep(MethodReview, d.Call)] = d.Reply
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

// reviewSetup is what a review of the project at root needs from its config: the reviewer agent, its worktree, the
// reviews dir and the integration branch; an error names what is missing.
type reviewSetup struct {
	agent, worktree, reviews, branch string
}

func reviewConfig(root string) (reviewSetup, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return reviewSetup{}, err
	}
	rc, _ := cfg.Role("reviewer")
	s := reviewSetup{agent: rc.Agent, reviews: cfg.Paths().Reviews, branch: cfg.Data.Branches.Main}
	switch {
	case !rc.Configured || rc.Agent == "":
		return s, errors.New("no reviewer role (obor setup role reviewer)")
	case rc.Worktree == "":
		return s, errors.New("the reviewer has no worktree (roles.reviewer.worktree)")
	case s.reviews == "":
		return s, errors.New("no reviews dir (paths.reviews)")
	case s.branch == "":
		return s, errors.New("no integration branch (branches.main)")
	}
	s.worktree = filepath.Join(cfg.LanesDir(), rc.Worktree)
	return s, nil
}

// reviewCall answers the socket's review request (the lead or the owner): a call id is required and a repeated one
// gets the stored reply. --rm: REVIEW REQUEST ENDED (fact review.rm), or NO REVIEW REQUEST. No --after, no note and a
// request open: it is listed (reviewList). Else a request (fact review.request): REVIEW REQUESTED (now | after
// T-a,T-b), " (replaces the request of <age> ago)" when one was open; a task neither dispatched nor specified, a task
// of --after dropped or rejected already, no reviewer configured, or a review open (reviewOpen): refused, nothing
// recorded. A pass's claim held (reviewClaim): --rm and a request are refused.
func (a *App) reviewCall(c Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(MethodReview + ": a call id is required")
	}
	p, reply, stored := a.callProj(MethodReview, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodReview, c.Root)
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
	step := StepReviewRequest
	switch {
	case c.Clear && open == nil:
		return "NO REVIEW REQUEST", nil
	case len(c.After) == 0 && c.Note == "" && open != nil && !c.Clear:
		return a.reviewList(p), nil
	case claim != "":
		return "", fmt.Errorf("review: obor serve is %s the review request now: nothing changed (try again once it is done)", claim)
	case c.Clear:
		step, d.Reply = StepReviewRm, "REVIEW REQUEST ENDED"
	case end != "":
		return "", fmt.Errorf("review: %s already", end)
	default:
		s, err := reviewConfig(p.root)
		if err != nil {
			return "", fmt.Errorf("review: %v", err)
		}
		if id, err := a.reviewOpenNow(p, s.branch, started); err != nil {
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
	if !a.record(p, Record{Task: ReviewTask, Step: step, Kind: KindFact}, d) {
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

// reviewDueTick counts, in git, the merges on the integration branch since the previous review: the newest "RV-n
// closed" commit's reviewed sha (the previous review the brief names), or the head of a review started since that is
// not closed yet. More than reviewDue: obor status' line "review due: N merges since <RV-prev>". It counts again once
// a merge or a start folded, or reviewDueEvery passed; a git error keeps the last line and is tried on the next pass.
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

// reviewDueCount is the "review due" line from git ("": not due, or no reviewer configured).
func (a *App) reviewDueCount(ctx context.Context, p *proj, g ReviewGit, started lastReview) (string, error) {
	s, err := reviewConfig(p.root)
	if err != nil {
		return "", nil // reviews are not set up: none is due
	}
	last, err := g.Last(ctx, s.branch)
	if err != nil {
		return "", err
	}
	k, prev, from := previousReview(last)
	if n, _ := strconv.Atoi(strings.TrimPrefix(started.id, "RV-")); n >= k && started.head != "" {
		prev, from = started.id, started.head // started, not closed yet: its findings are being fixed
	}
	head, err := g.Head(ctx, s.branch)
	if err != nil {
		return "", err
	}
	merges, err := g.Merges(ctx, from, head)
	if err != nil || len(merges) <= reviewDue {
		return "", err
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
	if next, _, _ := previousReview(last); n >= next {
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
// idle. A pass claims the request (reviewClaim) before it records: fired, its fact ends it and the claim holds until
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
		if open, err = reviewOpen(ctx, fx.Review, setup.branch, started); err != nil {
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
	due := merged && open == "" && (cfgErr != nil || checked && a.reviewerIdle(p, setup.agent))
	switch {
	case end != "":
		p.rv.what = "ending"
	case due:
		p.rv.what = "starting"
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
		m := Message{To: p.out.lead(), From: "obor", Line: "[obor] review request ended: " + end, Review: q.seq}
		a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, m)
	} else if a.record(p, Record{Task: ReviewTask, Step: StepReviewFire, Kind: KindFact}, ReviewData{Request: q.seq}) {
		a.fireReview(ctx, p, fx, q, setup, cfgErr)
	}
	a.mu.Lock()
	p.rv.what = ""
	a.mu.Unlock()
	a.wake()
}

// reviewerIdle: agent has no open brief and its pane, when one runs it, is idle (none: its fresh start places it;
// two claiming it: not idle) (under mu).
func (a *App) reviewerIdle(p *proj, agent string) bool {
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

// fireReview starts the review of the fired request q: the facts, the detached worktree, the reviewer's fresh start
// on the brief, its brief recorded, fact review.started; a step failing records needs-you {<id>: <error>} instead and
// starts nothing. A fact after the launch the log refuses is logged and a needs-you {<id>: started but not recorded:
// <error>} (RV-69 #2: the review runs, serve does not know it). The lead gets no line (T-650): obor status counts the
// merges since the review's head.
func (a *App) fireReview(ctx context.Context, p *proj, fx *Loop, q *reviewReq, s reviewSetup, cfgErr error) {
	id, brief, tasks, head, err := a.prepareReview(ctx, fx.Review, q, s, cfgErr)
	if err == nil {
		_, err = fx.Agents.Launch(ctx, Launch{Agent: s.agent, Fresh: true, Prompt: brief})
	}
	if err != nil {
		reason := "next id: " + err.Error()
		if id != "" {
			reason = id + ": " + err.Error()
		}
		a.record(p, Record{Task: ReviewTask, Step: StepNeedsYou, Kind: KindFact}, FactData{Machine: Machine, Reason: reason})
		return
	}
	var lost error
	if !a.record(p, Record{Task: ReviewTask, Step: StepReviewStarted, Kind: KindFact},
		ReviewData{Request: q.seq, ID: id, Head: head, Tasks: tasks}) {
		lost = cmp.Or(p.log.Err(), errors.New("the event log refused fact "+StepReviewStarted))
	}
	if _, ok := a.recordBrief(p, s.agent, "obor", "", ""); !ok && lost == nil {
		lost = cmp.Or(p.log.Err(), errors.New("the event log refused the brief"))
	}
	if lost != nil {
		reason := id + ": started but not recorded: " + lost.Error()
		a.logf("FAIL review of %s: %s", p.name, reason)
		a.record(p, Record{Task: ReviewTask, Step: StepNeedsYou, Kind: KindFact}, FactData{Machine: Machine, Reason: reason})
	}
}

// closedRE is a review's close commit subject: "RV-<n> closed · <reviewed sha> · …".
var closedRE = regexp.MustCompile(`^RV-([0-9]+) closed(?: · ([0-9a-f]+))?`)

// previousReview is the next review's number, and the previous review's id and reviewed sha ("": none) by last, the
// newest close commit's subject ("": none yet, the next is RV-1).
func previousReview(last string) (next int, prev, sha string) {
	m := closedRE.FindStringSubmatch(last)
	if m == nil {
		return 1, "", ""
	}
	k, _ := strconv.Atoi(m[1])
	return k + 1, "RV-" + m[1], m[2]
}

// mergeRE is an obor merge's subject: "Merge T-xx: …".
var mergeRE = regexp.MustCompile(`^Merge ([^ :]+):`)

// prepareReview collects a review's facts in order (the next id, the head, the merges and files since the previous
// review's sha) and detaches the review worktree at the head: the id, the brief, the tasks and the head; an error from
// the first step that fails (id "" when the id is not known yet).
func (a *App) prepareReview(ctx context.Context, g ReviewGit, q *reviewReq, s reviewSetup, cfgErr error) (id,
	brief string, tasks []string, head string, err error) {
	last, err := g.Last(ctx, s.branch)
	if err != nil {
		return "", "", nil, "", err
	}
	n, _, prevSHA := previousReview(last)
	id = "RV-" + strconv.Itoa(n)
	if cfgErr != nil {
		return id, "", nil, "", cfgErr
	}
	if head, err = g.Head(ctx, s.branch); err != nil {
		return id, "", nil, "", err
	}
	merges, err := g.Merges(ctx, prevSHA, head)
	if err != nil {
		return id, "", nil, "", err
	}
	files, err := g.Files(ctx, prevSHA, head)
	if err != nil {
		return id, "", nil, "", err
	}
	if err = g.Detach(ctx, s.worktree, head); err != nil {
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
	return id, reviewBrief(id, s, short, last, scope, files, q.note), tasks, head, nil
}

// reviewBrief is the reviewer's first prompt: skills/lead/review-brief.md §2's template with the facts filled in and
// the lead's note appended (its out of scope and contracts). last is the previous review's close commit subject (""
// none): its file is deleted at close (RV-69 #4), so the brief names what is kept, the close commit and BACKLOG.
func reviewBrief(id string, s reviewSetup, short, last string, scope, files []string, note string) string {
	_, prev, prevSHA := previousReview(last)
	inScope := strings.Join(scope, ", ")
	if inScope == "" {
		inScope = "no merges"
	}
	listed := files
	more := ""
	if len(files) > maxBriefFiles {
		listed = files[:maxBriefFiles]
		from := prevSHA
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
	fmt.Fprintf(&b, "Review %s. %s is at %s. In scope: %s; files: %s.", id, s.branch, short, inScope, fileList)
	if prev != "" {
		fmt.Fprintf(&b, " Previous review: %s (its close commit: %s; its accepted findings are the BACKLOG lines that name %s)"+
			" — don't repeat them.", prev, last, prev)
	} else {
		b.WriteString(" Previous review: none.")
	}
	fmt.Fprintf(&b, " For every finding give severity, likelihood (does a real config or workflow hit it, or only a"+
		" constructed one?), file:line, probe/evidence, suggested fix and its cost in added code, and an Extent line:"+
		" every other entry point / branch that shares the root cause, each marked checked or unchecked. Write only"+
		" %s/%s.md in your worktree. Do not commit.", s.reviews, id)
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("\nLead's note: " + note)
	}
	return b.String()
}
