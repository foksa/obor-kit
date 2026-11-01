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
// fact review.started{id, head, tasks} and the lead's line "[obor] review RV-xx started at <sha7> (T-a, T-b)". Any
// step failing: no start, needs-you{RV-xx: <error>} on the key review (a new request ends it). The first --after task
// dropped or rejected ends the request with the lead's line "[obor] review request ended: T-b dropped" (fact message
// {review: the request's seq}, whose fold ends it).

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

// reviewReq is the open review request: its fact's seq and time, its tasks, note and asker, and whether a pass is
// firing it now (reviewPass' claim, not folded).
type reviewReq struct {
	seq    int64
	at     time.Time
	after  []string
	note   string
	by     string
	firing bool
}

// lastReview is the newest review the log started: its fact's seq and its id ("" seq 0: none).
type lastReview struct {
	seq int64
	id  string
}

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
		p.folded.reviewed = lastReview{seq: r.Seq, id: d.ID}
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
// of --after dropped or rejected already, or no reviewer configured: refused, nothing recorded.
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
	a.mu.Lock()
	open := p.folded.review
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
	case c.Clear:
		step, d.Reply = StepReviewRm, "REVIEW REQUEST ENDED"
	case len(c.After) == 0 && c.Note == "" && open != nil:
		return a.reviewList(p), nil
	case end != "":
		return "", fmt.Errorf("review: %s already", end)
	default:
		if _, err := reviewConfig(p.root); err != nil {
			return "", fmt.Errorf("review: %v", err)
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

// reviewStatus is the open request as obor status and obor review show it, else "review due: N merges since <the last
// review>" when more than reviewDue merged since, else "" (under mu).
func (p *proj) reviewStatus(now time.Time) string {
	if q := p.folded.review; q != nil {
		s := "since " + Age(now.Sub(q.at)) + " (now"
		if len(q.after) > 0 {
			s = "since " + Age(now.Sub(q.at)) + " (after " + strings.Join(q.after, ",")
			if w := p.reviewWaits(q.after); len(w) > 0 {
				s += ", waiting " + strings.Join(w, ",")
			}
		}
		s += ")"
		if q.note != "" {
			s += ", note: " + strings.Join(strings.Fields(q.note), " ")
		}
		return s
	}
	n := 0
	for _, mt := range p.folded.loop {
		if mt.state == stMerged && mt.mergedSeq > p.folded.reviewed.seq {
			n++
		}
	}
	if n <= reviewDue {
		return ""
	}
	prev := "serve's log began (no review started by obor review)"
	if p.folded.reviewed.id != "" {
		prev = p.folded.reviewed.id
	}
	return fmt.Sprintf("review due: %d merges since %s", n, prev)
}

// reviewPass fires p's open review request when it is due, or ends it when a task of its --after ended unmerged. A
// pass claims the request under mu before it records (fired: its fact ends it; an ended line: its fold does).
func (a *App) reviewPass(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	q := p.folded.review
	run := q != nil && !q.firing && p.merging && p.out.lead() != "" && fx.Review != nil && fx.Agents != nil
	a.mu.Unlock()
	if !run {
		return
	}
	setup, cfgErr := reviewConfig(p.root)
	a.mu.Lock()
	if q != p.folded.review || q.firing {
		a.mu.Unlock()
		return
	}
	end := p.reviewOutcome(q.after)
	due := end == "" && len(p.reviewWaits(q.after)) == 0 && (cfgErr != nil || a.reviewerIdle(p, setup.agent))
	if end != "" || due {
		q.firing = true
	}
	a.mu.Unlock()
	switch {
	case end != "":
		m := Message{To: p.out.lead(), From: "obor", Line: "[obor] review request ended: " + end, Review: q.seq}
		a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, m)
	case due:
		if a.record(p, Record{Task: ReviewTask, Step: StepReviewFire, Kind: KindFact}, ReviewData{Request: q.seq}) {
			a.fireReview(ctx, p, fx, q, setup, cfgErr)
		}
	default:
		return
	}
	a.mu.Lock()
	q.firing = false
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
// on the brief, its brief recorded, fact review.started and the lead's line; a step failing records needs-you
// {<id>: <error>} instead and starts nothing.
func (a *App) fireReview(ctx context.Context, p *proj, fx *Loop, q *reviewReq, s reviewSetup, cfgErr error) {
	id, line, brief, tasks, head, err := a.prepareReview(ctx, fx.Review, q, s, cfgErr)
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
	a.record(p, Record{Task: ReviewTask, Step: StepReviewStarted, Kind: KindFact},
		ReviewData{Request: q.seq, ID: id, Head: head, Tasks: tasks})
	a.recordBrief(p, s.agent, "obor", "", "")
	a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, Message{To: p.out.lead(), From: "obor", Line: line})
}

// closedRE is a review's close commit subject: "RV-<n> closed · <reviewed sha> · …".
var closedRE = regexp.MustCompile(`^RV-([0-9]+) closed(?: · ([0-9a-f]+))?`)

// mergeRE is an obor merge's subject: "Merge T-xx: …".
var mergeRE = regexp.MustCompile(`^Merge ([^ :]+):`)

// prepareReview collects a review's facts in order (the next id, the head, the merges and files since the previous
// review's sha) and detaches the review worktree at the head: the id, the lead's line, the brief, the tasks and the
// head; an error from the first step that fails (id "" when the id is not known yet).
func (a *App) prepareReview(ctx context.Context, g ReviewGit, q *reviewReq, s reviewSetup, cfgErr error) (id, line,
	brief string, tasks []string, head string, err error) {
	last, err := g.Last(ctx, s.branch)
	if err != nil {
		return "", "", "", nil, "", err
	}
	n, prev, prevSHA := 1, "", ""
	if m := closedRE.FindStringSubmatch(last); m != nil {
		k, _ := strconv.Atoi(m[1])
		n, prev, prevSHA = k+1, "RV-"+m[1], m[2]
	}
	id = "RV-" + strconv.Itoa(n)
	if cfgErr != nil {
		return id, "", "", nil, "", cfgErr
	}
	if head, err = g.Head(ctx, s.branch); err != nil {
		return id, "", "", nil, "", err
	}
	merges, err := g.Merges(ctx, prevSHA, head)
	if err != nil {
		return id, "", "", nil, "", err
	}
	files, err := g.Files(ctx, prevSHA, head)
	if err != nil {
		return id, "", "", nil, "", err
	}
	if err = g.Detach(ctx, s.worktree, head); err != nil {
		return id, "", "", nil, "", err
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
	line = "[obor] review " + id + " started at " + short + " (" + strings.Join(tasks, ", ") + ")"
	if len(tasks) == 0 {
		line = "[obor] review " + id + " started at " + short + " (no merges since " + cmp.Or(prev, "the start") + ")"
	}
	return id, line, reviewBrief(id, s, short, prev, prevSHA, scope, files, q.note), tasks, head, nil
}

// reviewBrief is the reviewer's first prompt: skills/lead/review-brief.md §2's template with the facts filled in and
// the lead's note appended (its out of scope and contracts).
func reviewBrief(id string, s reviewSetup, short, prev, prevSHA string, scope, files []string, note string) string {
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
		fmt.Fprintf(&b, " Previous review: %s (git show <its removal's parent>:%s/%s.md, or the file while open) — don't repeat its findings.",
			prev, s.reviews, prev)
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
