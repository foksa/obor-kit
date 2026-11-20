package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// obor prune: the task branches of finished tasks and the worktrees of lanes and roles the config no longer names go,
// by hand (obor prune) and, for the branches of tasks merged pruneNightly ago, in the nightly. Serve decides, from its
// fold, never from git's ancestry (the gate merges a candidate commit, so a merged branch is not an ancestor of the
// target); the Pruner does the git. A branch goes only when its task is merged (or landed outside the app); with
// Unmerged also when the task was dropped over pruneUnmerged ago. Never a branch outside task/, a task serve's fold
// does not know or still holds open, or a branch checked out in a worktree. A structured reply names every task
// branch with its task's state and, apart from it, whether prune deletes it. A worktree goes only when it sits directly
// under lanes.dir, is a lane beyond lanes.count or the worktree of a role the config does not run, holds no task, and
// git finds it clean; a registered worktree whose dir is gone is git worktree prune's. Every deletion is a line with its
// tip sha and, once, fact pruned.

const (
	pruneNightly  = 7 * 24 * time.Hour  // the nightly takes the branches of tasks merged longer ago than this
	pruneUnmerged = 14 * 24 * time.Hour // obor prune --unmerged takes the branches of tasks dropped longer ago than this
)

// laneName is the name of a lane's worktree: config.LaneName's L-01, L-100.
var laneName = regexp.MustCompile(`^L-\d+$`)

// pruneCall answers the socket's prune request: the owner's or the lead's (the nightly's call is the owner's).
func (a *App) pruneCall(ctx context.Context, c wire.PruneReq) (string, error) {
	if c.Role != "" && c.Role != "lead" {
		return "", fmt.Errorf("prune: the owner or the lead prunes, not %s (role %s)", Dash(c.Agent), c.Role)
	}
	if c.Nightly && (c.Unmerged || c.DryRun) {
		return "", errors.New("prune: the nightly's call takes neither --unmerged nor --dry-run")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	a.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodPrune, c.Root)
	}
	fx, err := a.loopOf(p)
	if err != nil {
		return "", fmt.Errorf("prune: %v", err)
	}
	if fx.Prune == nil {
		return "", fmt.Errorf("prune: no git pruner for %s", p.name)
	}
	cfg, err := config.Load(p.root)
	if err != nil {
		return "", fmt.Errorf("prune: %v", err)
	}
	return a.prune(ctx, p, fx.Prune, cfg, c)
}

// prune runs c on p through g. Its reply is a line per deletion (or per kept worktree) and the summary last; the
// nightly's is the summary only (its line in the nightly's result), the lines going to serve's log; c.Structured: the
// reply is a wire.PruneReply's JSON, every task branch with its task's state and whether this call deletes it.
func (a *App) prune(ctx context.Context, p *proj, g port.Pruner, cfg *config.Config, c wire.PruneReq) (string, error) {
	now := a.opt.Now()
	var lines []string
	var did event.Pruned
	var oldest time.Time
	reply := wire.PruneReply{Branches: []wire.PruneBranch{}}
	branches, err := g.Branches(ctx)
	if err != nil {
		return "", fmt.Errorf("prune: %v", err)
	}
	checked, err := g.CheckedOut(ctx)
	if err != nil {
		return "", fmt.Errorf("prune: %v", err)
	}
	for _, b := range branches {
		task := cfg.TaskFromBranch(b.Name)
		if task == "" || !strings.HasPrefix(b.Name, "task/") || cfg.TaskBranch(task) != b.Name {
			continue
		}
		state, ended := a.taskEnd(p, task)
		ref := wire.PruneBranch{Name: b.Name, SHA: b.SHA, State: state}
		if !ended.IsZero() {
			ref.Ended = ended.UTC().Format(time.RFC3339)
		}
		if checked[b.Name] || !prunable(state, ended, c, now) {
			reply.Branches = append(reply.Branches, ref)
			continue
		}
		if c.DryRun {
			lines = append(lines, fmt.Sprintf("would delete %s (was %s)", b.Name, short(b.SHA)))
		} else if err := g.DeleteBranch(ctx, b); err != nil {
			lines = append(lines, fmt.Sprintf("kept %s: %v", b.Name, err))
			reply.Branches = append(reply.Branches, ref)
			continue
		} else {
			lines = append(lines, fmt.Sprintf("deleted %s (was %s)", b.Name, short(b.SHA)))
		}
		did.Branches = append(did.Branches, event.PrunedRef{Name: b.Name, SHA: b.SHA})
		ref.Deletable = true
		reply.Branches = append(reply.Branches, ref)
		if oldest.IsZero() || ended.Before(oldest) {
			oldest = ended
		}
	}
	if !c.Nightly {
		lines = append(lines, a.pruneTrees(ctx, p, g, cfg, c, &did)...)
	}
	did.Unmerged, did.Nightly = c.Unmerged, c.Nightly
	if !c.DryRun && len(did.Branches)+len(did.Worktrees) > 0 {
		a.record(p, event.Record{Task: event.PruneTask, Step: event.StepPruned, Kind: event.KindFact}, did)
	}
	if c.Structured {
		b, err := json.Marshal(reply)
		return string(b), err
	}
	summary := pruneSummary(c, len(did.Branches), len(did.Worktrees), oldest)
	if c.Nightly {
		for _, l := range lines {
			a.logf("prune of %s: %s", p.name, l)
		}
		return summary, nil
	}
	if !c.DryRun {
		for _, l := range lines {
			if strings.HasPrefix(l, "deleted ") || strings.HasPrefix(l, "removed ") || strings.HasPrefix(l, "pruned ") {
				a.logf("prune of %s: %s", p.name, l)
			}
		}
	}
	return strings.Join(append(lines, summary), "\n"), nil
}

// pruneSummary is the last line of a prune's reply: the nightly's `prune: N branches (oldest merge <date>)`, a hand
// run's `PRUNE N branches, M worktrees` (`PRUNE would delete …` for a dry run).
func pruneSummary(c wire.PruneReq, branches, trees int, oldest time.Time) string {
	if c.Nightly {
		if branches == 0 {
			return "prune: 0 branches"
		}
		return fmt.Sprintf("prune: %d branches (oldest merge %s)", branches, oldest.Local().Format("2006-01-02"))
	}
	what := "deleted"
	if c.DryRun {
		what = "would delete"
	}
	return fmt.Sprintf("PRUNE %s %d branches, %d worktrees", what, branches, trees)
}

// taskEnd is task's state as a wire.PruneBranch reports it and, for a task that ended, when — the fold's read, under mu.
func (a *App) taskEnd(p *proj, task string) (string, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	mt := p.folded.loop[task]
	switch {
	case mt == nil:
		return wire.PruneUnknown, time.Time{}
	case mt.endedAt.IsZero():
		return wire.PruneOpen, time.Time{}
	case mt.state == stMerged:
		return wire.PruneMerged, mt.endedAt
	case mt.state == stDropped:
		return wire.PruneEnded, mt.endedAt
	}
	return wire.PruneOpen, time.Time{}
}

// prunable: the branch of a task in state, ended at ended, may go under c (it is not checked out: the caller's).
func prunable(state string, ended time.Time, c wire.PruneReq, now time.Time) bool {
	age := now.Sub(ended)
	switch state {
	case wire.PruneMerged:
		return !c.Nightly || age > pruneNightly
	case wire.PruneEnded:
		return c.Unmerged && !c.Nightly && age > pruneUnmerged
	}
	return false
}

// pruneTrees is the worktree part of a hand run: its lines; the removed ones are added to did.
func (a *App) pruneTrees(ctx context.Context, p *proj, g port.Pruner, cfg *config.Config, c wire.PruneReq, did *event.Pruned) []string {
	trees, err := g.Trees(ctx)
	if err != nil {
		return []string{fmt.Sprintf("kept every worktree: %v", err)}
	}
	var lines []string
	lanes := project.Physical(cfg.LanesDir())
	var gone []string
	goneSHA := map[string]string{}
	for _, t := range trees {
		if project.Physical(filepath.Dir(t.Dir)) != lanes {
			continue // not directly under lanes.dir: never touched
		}
		name := filepath.Base(t.Dir)
		if t.Gone {
			gone = append(gone, t.Dir)
			goneSHA[t.Dir] = t.SHA
			continue
		}
		if !orphan(cfg, name) {
			continue
		}
		a.mu.Lock()
		task := p.laneHolds(name)
		a.mu.Unlock()
		if task != "" {
			lines = append(lines, fmt.Sprintf("kept worktree %s: it holds task %s", t.Dir, task))
			continue
		}
		dirt, err := g.Dirt(ctx, t.Dir)
		if err == nil && dirt == "" && !c.DryRun {
			err = g.RemoveTree(ctx, t.Dir)
		}
		switch {
		case err != nil:
			lines = append(lines, fmt.Sprintf("kept worktree %s: %v", t.Dir, err))
		case dirt != "":
			lines = append(lines, fmt.Sprintf("kept worktree %s: %s", t.Dir, dirt))
		default:
			lines = append(lines, pruneLine(c, "remove", "worktree "+t.Dir, short(t.SHA)))
			did.Worktrees = append(did.Worktrees, event.PrunedRef{Name: t.Dir, SHA: t.SHA})
		}
	}
	failed := map[string]error{}
	if len(gone) > 0 && !c.DryRun {
		failed = g.PruneTrees(ctx, gone)
	}
	for _, dir := range gone {
		if err := failed[dir]; err != nil {
			lines = append(lines, fmt.Sprintf("kept worktree %s: %v", dir, err))
			continue
		}
		lines = append(lines, pruneLine(c, "prune", "worktree "+dir+" (its dir is gone)", short(goneSHA[dir])))
		did.Worktrees = append(did.Worktrees, event.PrunedRef{Name: dir, SHA: goneSHA[dir]})
	}
	return lines
}

// pruneLine is one deletion's line: `<verb>d what (was sha)`, `would <verb> …` for a dry run.
func pruneLine(c wire.PruneReq, verb, what, sha string) string {
	line := verb + "d " + what
	if verb == "remove" {
		line = "removed " + what
	}
	if c.DryRun {
		line = "would " + verb + " " + what
	}
	if sha != "" {
		line += " (was " + sha + ")"
	}
	return line
}

// orphan: the worktree named name under lanes.dir is one obor names and the config no longer does: a lane beyond
// lanes.count, or the worktree of a role the config does not run. The merge scratch, the nightly's and anything else
// is nobody's to remove.
func orphan(cfg *config.Config, name string) bool {
	if laneName.MatchString(name) {
		return !cfg.HasLane(name)
	}
	for _, dir := range cfg.RoleWorktrees() {
		if filepath.Base(dir) == name {
			rc, ok := cfg.Role(name)
			return !ok || rc.Role.Optional && !rc.Configured
		}
	}
	return false
}
