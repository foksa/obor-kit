package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
)

// The two steps of a review cycle after its start (T-716; skills/lead/review-brief.md §4–5): obor review verify asks serve to brief
// the open review's reviewer to re-run its probes on the fixes, obor review close records the close commit and stops
// the reviewer. The open review is the next review by git (the newest "RV-<n> closed" commit + 1) whose file
// <reviews>/RV-<n>.md is in the main checkout (the reviewer writes it there from its role worktree, T-853).

const (
	reviewVerifyUsage = `FAIL usage: obor review verify <T-a>[,<T-b>…] [--findings <n>[,<m>…]] [--note "<text>"]`
	reviewCloseUsage  = `FAIL usage: obor review close "<summary>" [--accept "<line>"]… [--force]`
	reviewFixUsage    = `FAIL usage: obor review fix <T-a>[,<T-b>…] [--note "<what to check>"]`
)

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

// reviewVerifyCmd is `obor review verify <T-a,…> [--findings 1,3] [--note "…"]`: one request to obor serve
// (wire.MethodVerify, T-730). Serve, once no publish runs and none can start, reads the integration branch's head,
// finds each fix task's merge since the previous review, marks the head reviewed (app.ReviewedRef) and briefs the open
// review's reviewer with review-brief.md §4's text: the fix tasks with their merge shas, the findings to re-run
// (default: every finding), the verify file. FAIL (exit 1): a usage error, serve down, or serve's refusal (no open
// review, a task with no merge since the previous review, git refusing, a publish still running).
func reviewVerifyCmd(args []string) int {
	var tasks, findings []string
	var note string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case (a == "--findings" || a == "--note") && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "":
			i++
			if a == "--note" {
				note = args[i]
			} else {
				findings = strings.Split(args[i], ",")
			}
		case tasks == nil && a != "" && !strings.HasPrefix(a, "-"):
			tasks = strings.Split(a, ",")
		default:
			fmt.Fprintln(out, reviewVerifyUsage)
			return 1
		}
	}
	if tasks == nil {
		fmt.Fprintln(out, reviewVerifyUsage)
		return 1
	}
	for _, f := range findings {
		if n, err := strconv.Atoi(f); err != nil || n < 1 {
			fmt.Fprintf(out, "FAIL --findings: %q is no finding number\n", f)
			return 1
		}
	}
	return request(context.Background(), project.OSEnv, wire.MethodVerify, func(cfg *config.Config, call *wire.Call) error {
		if err := taskIDs(cfg, tasks...); err != nil {
			return err
		}
		call.After, call.Findings, call.Note = tasks, findings, note
		return nil
	})
}

// reviewFixCmd is `obor review fix <T-a,…> [--note "<what to check>"]` (T-850): one request to obor serve
// (wire.MethodFix) linking the fix tasks to the open review; a second call adds tasks. Once every linked task merged,
// serve runs what obor review verify runs and the lead hears the reviewer's finished line. FAIL (exit 1): a usage
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
			fmt.Fprintln(out, reviewFixUsage)
			return 1
		}
	}
	if tasks == nil {
		fmt.Fprintln(out, reviewFixUsage)
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
// · <reviewed sha> · approved · <summary>" on the integration branch in the main checkout (BACKLOG.md with it), then
// the reviewer stopped (obor restart --stop). The reviewed sha is app.ReviewedRef: the last verify's head, else the
// review's. The body lists the cycle's covered tasks and its review start sha (app.ReviewCovered; T-822), so the next
// review's scope takes the merges it did not cover. Refused before any change: no open review, no reviewed sha, the
// main checkout not on the integration branch or dirty outside BACKLOG.md and the review's files, or a fix task linked
// by obor review fix neither verified nor dropped (T-850; --force, the owner's word, closes anyway).
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
			fmt.Fprintln(out, reviewCloseUsage)
			return 1
		}
	}
	if summary == "" {
		fmt.Fprintln(out, reviewCloseUsage)
		return 1
	}
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewGitWait)
	defer cancel()
	c, line, err := closeReview(ctx, cfg, summary, accepts, force)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(out, line)
	l := &launch.Launcher{Cfg: cfg, Env: project.OSEnv, H: herdrSocket(project.OSEnv), Out: out}
	stopped, err := l.Stop(context.Background(), c.s.Agent, "", false)
	return launchResult(out, stopped, err)
}

// closeReview checks, then deletes the open review's files, appends the accepted lines and commits the close: the
// review and the line CLOSED <subject>.
func closeReview(ctx context.Context, cfg *config.Config, summary string, accepts []string, force bool) (openCycle, string, error) {
	c, err := openReview(ctx, cfg)
	if err != nil {
		return c, "", err
	}
	if pending := app.ReviewFixPending(cfg.Root, c.id); len(pending) > 0 && !force {
		return c, "", fmt.Errorf("%s's fixes are not verified: %s (open, or merged and the verify not run yet; --force"+
			" closes anyway): nothing changed", c.id, strings.Join(pending, ", "))
	}
	root := cfg.Root
	if on, err := gitIn(ctx, root, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || strings.TrimSpace(on) != c.s.Branch {
		return c, "", fmt.Errorf("the main checkout %s is not on %s: nothing changed", root, c.s.Branch)
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
	st, err := gitIn(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return c, "", err
	}
	backlogDirty := false
	for _, l := range porcelainLines(st) {
		switch {
		case l[3:] == "BACKLOG.md":
			backlogDirty = true
		case strings.HasPrefix(l, "?? ") && own[l[3:]]:
		default:
			return c, "", fmt.Errorf("the main checkout is dirty outside BACKLOG.md and %s's files (%s): nothing changed", c.id, l)
		}
	}
	backlog := ""
	if len(accepts) > 0 {
		b, err := os.ReadFile(filepath.Join(root, "BACKLOG.md"))
		if err != nil {
			return c, "", fmt.Errorf("--accept: %v", err)
		}
		if backlog, err = acceptLines(string(b), c.id, accepts); err != nil {
			return c, "", err
		}
	}
	sha, err := gitIn(ctx, root, "rev-parse", "--short=7", "--verify", "-q", app.ReviewedRef)
	if err != nil {
		return c, "", fmt.Errorf("no reviewed sha (%s, set by the review's start and obor review verify; by hand: git"+
			" update-ref %s <sha>): nothing changed", app.ReviewedRef, app.ReviewedRef)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return c, "", err
		}
	}
	if backlog != "" {
		if err := os.WriteFile(filepath.Join(root, "BACKLOG.md"), []byte(backlog), 0o644); err != nil {
			return c, "", err
		}
		backlogDirty = true
	}
	if backlogDirty {
		if _, err := gitIn(ctx, root, "add", "--", "BACKLOG.md"); err != nil {
			return c, "", err
		}
	}
	subject := c.id + " closed · " + strings.TrimSpace(sha) + " · approved · " + summary
	args := []string{"commit", "-q", "--allow-empty", "-m", subject}
	// T-822: the body lists the cycle's covered tasks and where its review started, so the next review's scope takes
	// the merges this cycle did not cover. No start recorded (a review started by an older obor): the old format.
	if covered, start, ok := app.ReviewCovered(root); ok {
		args = append(args, "-m", "Covered: "+strings.Join(covered, ", ")+"\nStart: "+start)
	}
	if _, err := gitIn(ctx, root, args...); err != nil {
		return c, "", err
	}
	return c, "CLOSED " + subject, nil
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
