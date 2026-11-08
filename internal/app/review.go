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
// (the fold, T-1023: the next id and the merges no review covered; port.Review: the integration branch's head and the
// files since the previous review's start),
// the reviewer's role worktree detached at the head (port.Review.Detach, T-853: merges never wait for the reviewer), and
// the reviewer's fresh start with the brief as its first prompt (the same path as obor brief's fresh start, its brief
// recorded the same way: briefed with no line). Then fact review.started{id, head, tasks}: no line to the lead (T-650;
// obor status counts from its head), and the brief. Any step
// failing: no start, needs-you{RV-xx: <error>} on the key review (a new request ends it). The first --after task
// dropped or rejected ends the request with the lead's line "[obor] review request ended: T-b dropped" (fact message
// {review: the request's seq}, whose fold ends it).
//
// One review cycle is open at a time (RV-69 #3): the last review serve started (fact review.started, so a replay
// keeps it) is open until fact review.closed for it (or a later one): obor review close's call, which serve commits and
// records (reviewclose.go, T-926). While one is open, obor review refuses a new request ("RV-69 is open (not closed): close
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
// the review's id, head and tasks once started (a close's covered tasks and start are the fold's cycle, T-1023: its
// commit renders them), and the call that recorded it with its reply (a close's: CLOSED <its commit's subject>, also
// when the crash cut's read records it).
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

// reviewReq is the open review request: its fact's seq and time, its tasks, note and asker, and the open review it
// waits for once due (held: the last pass's, not folded; "": none).
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
// claim; under App.mu). reviewCall checks it and records in one App.mu section, so a change is either folded before
// a claim or refused during one. claimed is a test hook: it runs once a pass claimed, before the claim's first append
// (nil: none).
type reviewClaim struct {
	what    string
	claimed func()
}

// lastReview is the newest review the log started: its fact's seq, its id and the head it reviews (seq 0: none), and
// the seq of the review.closed fact that closed it (0: open; T-926).
type lastReview struct {
	seq      int64
	id, head string
	closed   int64
}

// open is the review's id while it is open: started and no review.closed fact for it ("": none open).
func (r lastReview) open() string {
	if r.seq == 0 || r.closed != 0 {
		return ""
	}
	return r.id
}

// foldReview folds r, a record of the project's review requests: a request opens (replacing the open one) and ends
// a failed start's needs-you; --rm and a firing end it. A start and a close (T-926) are the review fold's
// (reviewLog.fold); here a close's call is stored.
func (p *proj) foldReview(r event.Record) {
	if r.Task != event.ReviewTask || r.Kind != event.KindFact {
		return
	}
	var d ReviewData
	_ = json.Unmarshal(r.Data, &d)
	method := wire.MethodReview
	switch r.Step {
	case event.StepReviewClosed:
		method = wire.MethodReviewClose
	case event.StepReviewRequest:
		p.folded.review = &reviewReq{seq: r.Seq, at: event.RecordTime(r), after: d.After, note: d.Note, by: d.By}
		p.clear(event.ReviewTask)
	case event.StepReviewRm:
		p.folded.review = nil
		p.folded.reviewEnd = idleEnd{r.Seq, event.RecordTime(r)}     // T-868: the request's end
		p.folded.reviewIdleEnd = idleEnd{r.Seq, event.RecordTime(r)} // T-909: idle's own end
		p.clear(event.ReviewTask)
	case event.StepReviewFire:
		p.reviewEnded(d.Request)
		p.folded.reviewEnd = idleEnd{r.Seq, event.RecordTime(r)}     // T-868: the request's end
		p.folded.reviewIdleEnd = idleEnd{r.Seq, event.RecordTime(r)} // T-909: idle's own end
	default:
		return
	}
	if d.Call != "" {
		p.folded.calls[verdictStep(method, d.Call)] = d.Reply
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

// reviewConfig is ReviewConfig of the project at root.
func reviewConfig(root string) (ReviewSetup, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return ReviewSetup{}, err
	}
	return ReviewConfig(cfg)
}

// ReviewConfig is cfg's review setup (serve's review start and verify; obor review close, T-716); an error names what
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
// request open: it is listed, REVIEW REQUEST since <age> (now | after T-a,T-b[, waiting T-b])[, note: <note>]. Else a
// request (fact review.request): REVIEW REQUESTED (now | after T-a,T-b), " (replaces the request of <age> ago)" when
// one was open; a task neither dispatched nor specified, a task of --after dropped or rejected already, no reviewer
// configured, or a review open (lastReview.open): refused, nothing recorded. A pass's claim held (reviewClaim): --rm
// and a request are refused.
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
	// The reads, with no lock: the review config and the --after tasks' specs (consulted for the tasks the log does not
	// know). Then one a.mu section: a pass's claim is either made before the check (refused) or sees the fold.
	var cfgErr error
	specs := map[string]error{}
	if !c.Clear {
		_, cfgErr = reviewConfig(p.root)
		for _, task := range c.After {
			_, ok, err := specOf(p, task)
			if err == nil && !ok {
				err = fmt.Errorf("unknown task %s (no dispatch or spec in %s)", task, p.name)
			}
			specs[task] = err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	open, claim := p.folded.review, p.rv.what
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	d := ReviewData{By: by, Call: c.CallID}
	step := event.StepReviewRequest
	switch end := p.reviewOutcome(c.After); {
	case c.Clear && open == nil:
		return "NO REVIEW REQUEST", nil
	case len(c.After) == 0 && c.Note == "" && open != nil && !c.Clear:
		return "REVIEW REQUEST " + p.reviewStatus(a.opt.Now()), nil
	case claim != "":
		return "", fmt.Errorf("review: obor serve is %s the review request now: nothing changed (try again once it is done)", claim)
	case c.Clear:
		step, d.Reply = event.StepReviewRm, "REVIEW REQUEST ENDED"
	case end != "":
		return "", fmt.Errorf("review: %s already", end)
	default:
		if cfgErr != nil {
			return "", fmt.Errorf("review: %v", cfgErr)
		}
		if id := p.folded.rv.last().open(); id != "" {
			return "", fmt.Errorf("%s is open (not closed): close it first", id)
		}
		for _, task := range c.After {
			if err := specs[task]; p.folded.loop[task] == nil && err != nil {
				return "", fmt.Errorf("review: %v", err)
			}
		}
		d.After, d.Note, d.Reply = c.After, c.Note, "REVIEW REQUESTED (now)"
		if len(c.After) > 0 {
			d.Reply = "REVIEW REQUESTED (after " + strings.Join(c.After, ",") + ")"
		}
		if open != nil {
			d.Reply += " (replaces the request of " + Age(a.opt.Now().Sub(open.at)) + " ago)"
		}
	}
	if !a.recordLocked(p, event.Record{Task: event.ReviewTask, Step: step, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("review: the event log of %s refused it", p.name)
	}
	a.wake()
	return d.Reply, nil
}

// reviewStatus is the open request as obor status and obor review show it, else the "review due" line the last pass
// counted (reviewDueTick), else "" (under mu).
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
	return p.due
}

// reviewDueTick counts, on every pass, the merges in the next review's scope (reviewLog.scope: since the review before
// the last one started, the tasks no review since covered). More than reviewDue: obor status' line "review due: N
// merges since <the last review>". Reviews not set up: no line.
func (a *App) reviewDueTick(_ context.Context, p *proj, fx *Loop) {
	if fx.Workflow == nil {
		return
	}
	_, err := reviewConfig(p.root)
	a.mu.Lock()
	defer a.mu.Unlock()
	p.due = ""
	if n := len(p.folded.rv.scope()); err == nil && n > reviewDue {
		prev := cmp.Or(p.folded.rv.last().id, "the first commit (no RV started yet)")
		p.due = fmt.Sprintf("review due: %d merges since %s", n, prev)
	}
}

// reviewPass fires p's open review request when it is due, or ends it when a task of its --after ended unmerged. Due:
// every --after task merged, no review open (lastReview.open: the last start has no review.closed fact, T-926; one
// open: the request waits, held) and the reviewer idle; a publish running does not hold it (T-872: the start reads the head once, roleworktree.go). A pass claims
// the request (reviewClaim) before it records: fired, its fact ends it and the claim holds until the start returned;
// an ended line, its fold does.
func (a *App) reviewPass(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	q, started := p.folded.review, p.folded.rv.last()
	run := q != nil && p.rv.what == "" && p.merging && p.out.lead() != "" && fx.Workflow != nil && fx.Agents != nil
	ready := run && p.reviewOutcome(q.after) == "" && len(p.reviewWaits(q.after)) == 0
	a.mu.Unlock()
	if !run {
		return
	}
	setup, cfgErr := reviewConfig(p.root)
	open, checked := "", ready && cfgErr == nil
	if checked {
		open = started.open()
	}
	a.mu.Lock()
	if q != p.folded.review || p.rv.what != "" || p.folded.rv.last() != started {
		a.mu.Unlock()
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
	case due:
		p.rv.what = "starting"
	}
	claimed, hook := p.rv.what != "", p.rv.claimed
	a.mu.Unlock()
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

// fireReview starts the review of the fired request q: the facts, the reviewer's fresh start on the brief, its brief recorded, fact review.started; a step failing records needs-you {<id>: <error>} instead and
// starts nothing. A fact after the launch the log refuses is logged and a needs-you {<id>: started but not recorded:
// <error>} (RV-69 #2: the review runs, serve does not know it). The lead gets no line (T-650): obor status counts the
// merges since the review's head.
func (a *App) fireReview(ctx context.Context, p *proj, fx *Loop, q *reviewReq, s ReviewSetup, cfgErr error) {
	id, brief, tasks, head, err := a.prepareReview(ctx, p, fx.Workflow, q, s, cfgErr)
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
	}
	if lost != nil {
		reason := id + ": started but not recorded: " + lost.Error()
		a.logf("FAIL review of %s: %s", p.name, reason)
		a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Machine: Machine, Reason: reason})
	}
}

// The close commit's format lives here only (T-895): CloseCommit writes it (rendered from the fold's cycle, T-1023),
// ClosesReview (reviewclose.go) knows a review's own close commit by it, and reviewID/reviewNumber name a review's id.

// reviewID is review n's id: "RV-<n>".
func reviewID(n int) string { return "RV-" + strconv.Itoa(n) }

// reviewNumber is id's number (ok false: id is not "RV-<n>").
func reviewNumber(id string) (n int, ok bool) {
	digits, found := strings.CutPrefix(id, "RV-")
	if !found || digits == "" {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

// CloseCommit is review id's close commit: the subject "RV-<n> closed · <reviewed sha> · approved · <summary>", or
// "accepted <n>" in place of "approved" when the close carried accepted findings (T-909: the verify requested changes
// and the close carries them, so the subject does not say the review approved), and the body "Covered: <tasks>\nStart:
// <review start sha>" (T-822: the cycle's coverage, so the next review's scope takes the merges it did not cover).
func CloseCommit(id, sha, summary string, covered []string, start string, accepted int) (subject, body string) {
	verdict := "approved"
	if accepted > 0 {
		verdict = "accepted " + strconv.Itoa(accepted)
	}
	subject = id + " closed · " + strings.TrimSpace(sha) + " · " + verdict + " · " + summary
	body = "Covered: " + strings.Join(covered, ", ") + "\nStart: " + start
	return subject, body
}

// prepareReview collects a review's facts in order (from the fold: the next id, the previous review and the merges
// no review covered; then git: the head, the files since the previous review's start) and detaches the reviewer's
// role worktree at the head (port.Review.Detach, T-853): the id, the brief, the in-scope tasks and the head; an error
// from the first step that fails (id "" when the id is not known yet). The fold is read before the head: every merge
// it names is in the head (its fact follows its publish), so a review never covers a merge it does not hold.
func (a *App) prepareReview(ctx context.Context, p *proj, g port.Review, q *reviewReq, s ReviewSetup, cfgErr error) (id,
	brief string, tasks []string, head string, err error) {
	a.mu.Lock()
	l := &p.folded.rv
	id, prev, merges := l.nextID(), *l.cur(), l.scope()
	a.mu.Unlock()
	if cfgErr != nil {
		return id, "", nil, "", cfgErr
	}
	if head, err = g.Head(ctx, s.Branch); err != nil {
		return id, "", nil, "", err
	}
	files, err := g.Files(ctx, prev.head, head)
	if err != nil {
		return id, "", nil, "", err
	}
	if err = g.Detach(ctx, s.Worktree, head); err != nil {
		return id, "", nil, "", err
	}
	var scope []string
	for _, m := range merges {
		tasks = append(tasks, m.task)
		scope = append(scope, m.task+" ("+short(m.sha)+")")
	}
	last := prev.subject
	if last == "" && prev.id != "" {
		last = prev.id + " closed"
	}
	return id, reviewBrief(id, s, short(head), prev.head, prev.id, last, scope, files, q.note), tasks, head, nil
}

// copiedLine is rule 6's second question (T-884): data another type already owns, copied instead of named, and a
// client deciding from a state string serve knows.
const copiedLine = "Does it add a field or type that copies data another type already holds? Name the owner type." +
	" Does a client decide something from a state string that serve knows? Then serve ships the predicate."

// redundantLine is docs/design.md "Change rules" rule 6, in every review and verify brief (T-869): what the change
// made redundant, then (T-884) whether it copied data another type owns.
const redundantLine = "What did this make redundant? Name it or confirm it is gone. On rule-tier steps a crash or" +
	" interleaving finding counts only when a kernel guarantee breaks. " + copiedLine

// reviewBrief is the reviewer's first prompt: skills/lead/review-brief.md §2's template with the facts filled in, the
// redundancy question (redundantLine) and the lead's note appended (its out of scope and contracts). prev is the
// previous review's id ("": none) and last its close commit's subject: its file is deleted at close (RV-69 #4), so the
// brief names what is kept, the close commit and BACKLOG. from is where the brief's range starts ("": the first
// commit).
func reviewBrief(id string, s ReviewSetup, short, from, prev, last string, scope, files []string, note string) string {
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
		" each, no fix proposal. "+redundantLine+" Your worktree is at %s (check out any commit there); write only %s/%s.md in the main"+
		" checkout %s. Do not commit.", short, s.Reviews, id, s.Root)
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("\nLead's note: " + note)
	}
	return b.String()
}
