package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// obor review verify (T-716; skills/lead/review-brief.md §4) is a request to serve (T-730): serve reads the
// integration branch's head and marks it reviewed at its admission point (mergehold.go), so a publish in flight never
// moves the main checkout under the verify and none starts before its brief is recorded. The CLI only asks.

// verifyGitWait bounds a verify's git once admitted.
const verifyGitWait = 30 * time.Second

// OpenCycle is the open review by git: its id (the newest "RV-<n> closed" commit + 1), the previous review's id,
// reviewed sha and coverage start ("" when it has neither: no close commit yet), and its review file in the main
// checkout.
type OpenCycle struct {
	ID, Prev, PrevSHA, From, File string
}

// OpenReview is the open review of the project at root (obor review verify and close): no review file
// <reviews>/RV-<n>.md in the main checkout (T-718: the reviewer writes it there) is no open review, an error.
func OpenReview(ctx context.Context, g ReviewGit, root string, s ReviewSetup) (OpenCycle, error) {
	last, err := g.Last(ctx, s.Branch)
	if err != nil {
		return OpenCycle{}, err
	}
	n, prev, prevSHA := PreviousReview(last.Subject)
	from, _ := reviewScope(last)
	c := OpenCycle{ID: "RV-" + strconv.Itoa(n), Prev: prev, PrevSHA: prevSHA, From: from}
	c.File = filepath.Join(root, s.Reviews, c.ID+".md")
	if _, err := os.Stat(c.File); err != nil {
		return c, fmt.Errorf("no open review: no %s/%s.md in %s (%s is not written yet, or the last review is closed)",
			s.Reviews, c.ID, root, c.ID)
	}
	return c, nil
}

// verifyCall answers the socket's review_verify request (the lead or the owner): the open review's reviewer briefed
// (recordBrief: its fact is a brief's, so a repeated call id gets the brief's stored reply) with review-brief.md §4's
// text for the fix tasks (Call.After), the findings (Call.Findings; none: every finding) and the lead's note. Once
// admitted, serve reads the head and each task's merge in the next review's scope and marks the head reviewed
// (ReviewedRef); the tasks are recorded as covered by the cycle (T-822). Refused, nothing marked or recorded: no
// reviewer setup, no open review, a task with no merge since the previous review, git failing, a publish still running
// after admitWait.
func (a *App) verifyCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodVerify + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodBrief, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodVerify, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("review verify: the lead or the owner asks for a verify, not %s (role %s)", Dash(c.Agent), c.Role)
	case len(c.After) == 0:
		return "", errors.New("review verify: the fix tasks are required")
	}
	s, err := reviewConfig(p.root)
	if err != nil {
		return "", err
	}
	fx, err := a.opt.Loop(p.root)
	if err == nil && fx.Review == nil {
		err = errors.New("the merge loop has no review git")
	}
	if err != nil {
		return "", fmt.Errorf("review verify: %v", err)
	}
	wait, cancel := context.WithTimeout(context.Background(), admitWait)
	release, err := a.admit(wait, p)
	cancel()
	if err != nil {
		return "", fmt.Errorf("review verify: %v", err)
	}
	defer release() // after the brief is recorded: its open brief holds the merge from then on
	ctx, cancel := context.WithTimeout(context.Background(), verifyGitWait)
	defer cancel()
	text, err := verifyText(ctx, fx.Review, p.root, s, c.After, c.Findings, c.Note)
	if err != nil {
		return "", err
	}
	from := cmp.Or(c.Agent, "owner")
	reply, ok := a.recordBrief(p, s.Agent, from, text, c.CallID, "", false)
	if !ok {
		return "", fmt.Errorf("review verify: the event log of %s refused it", p.name)
	}
	// T-822: the verified tasks join the cycle's covered set (obor review close lists it in the close commit's body).
	a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewVerify, Kind: event.KindFact}, ReviewData{Tasks: c.After})
	a.wake()
	return reply, nil
}

// verifyText finds the open review and each task's merge in the next review's scope (T-822: a task merged after the
// review started, which it does not cover, is allowed) and marks the integration branch's head reviewed: the verify
// brief.
func verifyText(ctx context.Context, g ReviewGit, root string, s ReviewSetup, tasks, findings []string, note string) (string, error) {
	c, err := OpenReview(ctx, g, root, s)
	if err != nil {
		return "", err
	}
	head, err := g.Head(ctx, s.Branch)
	if err != nil {
		return "", err
	}
	merges, err := g.Merges(ctx, c.From, head)
	if err != nil {
		return "", err
	}
	fixed := make([]string, 0, len(tasks))
	for _, task := range tasks {
		sha := ""
		for _, m := range merges { // oldest first: the last merge of the task wins
			if h, subject, _ := strings.Cut(m, " "); strings.HasPrefix(subject, "Merge "+task+":") {
				sha = h[:min(len(h), 7)]
			}
		}
		if sha == "" {
			return "", fmt.Errorf("%s has no merge on %s since %s", task, s.Branch, cmp.Or(c.Prev, "the first commit"))
		}
		fixed = append(fixed, task+" ("+sha+")")
	}
	if err := g.Mark(ctx, head); err != nil {
		return "", err
	}
	return verifyBrief(c.ID, s, head[:min(len(head), 7)], fixed, findings, note), nil
}

// verifyBrief is review-brief.md §4's text: the fixes, the head, the findings to re-run (none: every finding), the
// verify file, the lead's note last.
func verifyBrief(id string, s ReviewSetup, short string, fixed, findings []string, note string) string {
	which := "every finding"
	if len(findings) > 0 {
		which = "#" + strings.Join(findings, ", #")
	}
	text := fmt.Sprintf("The %s findings were fixed in %s; %s is at %s. Re-run your probes for %s and report fixed /"+
		" incomplete / regressed per finding. Write %s/%s-verify.md. Do not commit.",
		id, strings.Join(fixed, ", "), s.Branch, short, which, s.Reviews, id)
	if note = strings.TrimSpace(note); note != "" {
		text += "\nLead's note: " + note
	}
	return text
}
