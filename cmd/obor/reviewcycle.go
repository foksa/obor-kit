package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// The two steps of a review cycle after its start (T-716; skills/lead/review-brief.md §4–5): obor review fix links fix
// tasks, whose verify serve briefs the reviewer once they merged; obor review close records the close commit (serve
// stops the reviewer once it sees it, rolestop.go). The open review is the next review by git (the newest "RV-<n> closed" commit + 1) whose file
// <reviews>/RV-<n>.md is in the main checkout (the reviewer writes it there from its role worktree, T-853).

// reviewGitWait bounds a review command's git.
const reviewGitWait = 60 * time.Second

// openCycle is the open review of cfg (app.OpenReview): its setup, git on the main checkout, its id, the previous
// review's id and reviewed sha ("": none) and the path of its review file. No review file in the main checkout: no
// open review.
type openCycle struct {
	s       app.ReviewSetup
	g       reviewGit
	id      string
	prev    string // the previous review's id ("": none)
	prevSHA string
	file    string
}

func openReview(ctx context.Context, cfg *config.Config) (openCycle, error) {
	s, err := app.ReviewConfig(cfg)
	if err != nil {
		return openCycle{}, err
	}
	g := reviewGit{root: cfg.Root}
	o, err := app.OpenReview(ctx, g, cfg.Root, s)
	return openCycle{s: s, g: g, id: o.ID, prev: o.Prev, prevSHA: o.PrevSHA, file: o.File}, err
}

// reviewFixCmd is `obor review fix <T-a,…> [--note "<what to check>"]` (T-850): one request to obor serve
// (wire.MethodFix) linking the fix tasks to the open review; a second call adds tasks. Once every linked task merged,
// serve briefs the reviewer to verify them and the lead hears the reviewer's finished line. FAIL (exit 1): a usage
// error, serve down, or serve's refusal (no open review, a task not dispatched).
func reviewFixCmd(args []string) int {
	var tasks []string
	var note string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--note" && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "":
			i++
			note = args[i]
		case tasks == nil && a != "" && !strings.HasPrefix(a, "-"):
			tasks = strings.Split(a, ",")
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("review fix"))
			return 1
		}
	}
	if tasks == nil {
		fmt.Fprintln(out, "FAIL "+help.Usage("review fix"))
		return 1
	}
	return request(context.Background(), project.OSEnv, wire.MethodFix, func(cfg *config.Config, call *wire.Call) error {
		if err := taskIDs(cfg, tasks...); err != nil {
			return err
		}
		call.After, call.Note = tasks, note
		return nil
	})
}

// reviewCloseCmd is `obor review close "<summary>" [--accept "<line>"]… [--force]`: the open review's files deleted from the
// main checkout, each accepted finding a line in BACKLOG.md's Open section naming the review, the commit "RV-<n> closed
// · <reviewed sha> · approved · <summary>" (with --accept lines: "accepted <n>" in place of "approved", T-909) on the
// integration branch in the main checkout (BACKLOG.md with it). Serve
// stops the reviewer once its pass sees the close commit (rolestop.go; T-869: the CLI stops nothing). The reviewed
// sha, the body and the refusal read serve's log (app.ReadReviewCycle, T-856): the sha is the head the last brief
// handed the reviewer (its last done verify's, else the review start's); the body lists the cycle's covered tasks and
// its review start sha (T-822), so the next review's scope takes the merges it did not cover. Refused before any
// change: no open review, no review start for it in the log (T-869), the main checkout not on the integration branch or dirty outside BACKLOG.md
// and the review's files, a fix task linked by obor review fix neither verified nor dropped, or a verify requested or
// briefed and not done (T-850; --force, the owner's word, closes anyway).
func reviewCloseCmd(args []string) int {
	var summary string
	var accepts []string
	force := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--force" && !force:
			force = true
		case a == "--accept" && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "":
			i++
			accepts = append(accepts, strings.Join(strings.Fields(args[i]), " "))
		case summary == "" && strings.TrimSpace(a) != "" && !strings.HasPrefix(a, "-"):
			summary = strings.Join(strings.Fields(a), " ")
		default:
			fmt.Fprintln(out, "FAIL "+help.Usage("review close"))
			return 1
		}
	}
	if summary == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("review close"))
		return 1
	}
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewGitWait)
	defer cancel()
	line, err := closeReview(ctx, cfg, summary, accepts, force)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(out, line)
	return 0
}

// closeReview checks, then deletes the open review's files, appends the accepted lines and commits the close: the
// line CLOSED <subject>.
func closeReview(ctx context.Context, cfg *config.Config, summary string, accepts []string, force bool) (string, error) {
	c, err := openReview(ctx, cfg)
	if err != nil {
		return "", err
	}
	cycle := app.ReadReviewCycle(cfg.Root, c.id) // T-856: the verify records serve folds, read from its log
	// T-869: every review has a start fact; refs/obor/reviewed and the close's fallback to it are gone.
	if cycle.Reviewed == "" {
		return "", fmt.Errorf("no review start for %s in the log: nothing changed", c.id)
	}
	if len(cycle.Pending) > 0 && !force {
		return "", fmt.Errorf("%s's fixes are not verified: %s (open, merged and the verify not done yet, or a verify"+
			" requested or briefed; --force closes anyway): nothing changed", c.id, strings.Join(cycle.Pending, ", "))
	}
	root := cfg.Root
	if on, err := project.GitCtx(ctx, root, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || strings.TrimSpace(on) != c.s.Branch {
		return "", fmt.Errorf("the main checkout %s is not on %s: nothing changed", root, c.s.Branch)
	}
	files := []string{c.file}
	if verify := filepath.Join(root, c.s.Reviews, c.id+"-verify.md"); fileThere(verify) {
		files = append(files, verify)
	}
	own := map[string]bool{} // the review's files: untracked when paths.reviews is not gitignored
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		own[filepath.ToSlash(rel)] = true
	}
	st, err := project.GitCtx(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	backlogDirty := false
	for _, l := range porcelainLines(st) {
		switch {
		case l[3:] == "BACKLOG.md":
			backlogDirty = true
		case strings.HasPrefix(l, "?? ") && own[l[3:]]:
		default:
			return "", fmt.Errorf("the main checkout is dirty outside BACKLOG.md and %s's files (%s): nothing changed", c.id, l)
		}
	}
	backlog := ""
	if len(accepts) > 0 {
		b, err := os.ReadFile(filepath.Join(root, "BACKLOG.md"))
		if err != nil {
			return "", fmt.Errorf("--accept: %v", err)
		}
		if backlog, err = acceptLines(string(b), c.id, accepts); err != nil {
			return "", err
		}
	}
	// the head the reviewer last finished (its start's or a done verify's, T-857)
	sha, err := project.GitCtx(ctx, root, "rev-parse", "--short=7", "--verify", "-q", cycle.Reviewed+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("no reviewed sha (%s, the head the reviewer last finished, is no commit): nothing changed",
			cycle.Reviewed)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return "", err
		}
	}
	if backlog != "" {
		if err := os.WriteFile(filepath.Join(root, "BACKLOG.md"), []byte(backlog), 0o644); err != nil {
			return "", err
		}
		backlogDirty = true
	}
	if backlogDirty {
		if _, err := project.GitCtx(ctx, root, "add", "--", "BACKLOG.md"); err != nil {
			return "", err
		}
	}
	subject, body := app.CloseCommit(c.id, sha, summary, cycle.Covered, cycle.Start, len(accepts))
	if _, err := project.GitCtx(ctx, root, "commit", "-q", "--allow-empty", "-m", subject, "-m", body); err != nil {
		return "", err
	}
	return "CLOSED " + subject, nil
}

// porcelainLines is git status --porcelain's entry lines, untrimmed (T-725): the two status columns stay, so each
// line's path starts at column 3 (" M BACKLOG.md" is an unstaged edit); a line shorter than "XY p" is dropped.
func porcelainLines(out string) []string {
	var l []string
	for _, s := range strings.Split(out, "\n") {
		if s = strings.TrimRight(s, "\r"); len(s) > 3 {
			l = append(l, s)
		}
	}
	return l
}

// acceptLines is backlog with one line "- (<id>, accepted) <line>" per accepted finding at the top of its "## Open"
// section; no such section: an error.
func acceptLines(backlog, id string, accepts []string) (string, error) {
	ls := strings.SplitAfter(backlog, "\n")
	for i, l := range ls {
		if strings.TrimSpace(l) != "## Open" {
			continue
		}
		var add []string
		for _, a := range accepts {
			add = append(add, "- ("+id+", accepted) "+a+"\n")
		}
		if !strings.HasSuffix(l, "\n") {
			ls[i] += "\n"
		}
		return strings.Join(ls[:i+1], "") + strings.Join(add, "") + strings.Join(ls[i+1:], ""), nil
	}
	return "", errors.New("--accept: BACKLOG.md has no \"## Open\" section: nothing changed")
}

// fileThere: path is there.
func fileThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
