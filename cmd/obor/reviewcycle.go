package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
)

// The two steps of a review cycle after its start (T-716; skills/lead/review-brief.md §4–5): obor review verify briefs
// the open review's reviewer to re-run its probes on the fixes, obor review close records the close commit and stops
// the reviewer. The open review is the next review by git (the newest "RV-<n> closed" commit + 1) whose file
// <reviews>/RV-<n>.md is in the review worktree.

const (
	reviewVerifyUsage = `FAIL usage: obor review verify <T-a>[,<T-b>…] [--findings <n>[,<m>…]] [--note "<text>"]`
	reviewCloseUsage  = `FAIL usage: obor review close "<summary>" [--accept "<line>"]…`
)

// reviewGitWait bounds a review command's git.
const reviewGitWait = 60 * time.Second

// openCycle is the open review of cfg: its setup, git on the main checkout, its id, the previous review's reviewed sha
// ("": none) and the path of its review file. No review file in the review worktree: no open review.
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
	last, err := g.Last(ctx, s.Branch)
	if err != nil {
		return openCycle{}, err
	}
	n, prev, prevSHA := app.PreviousReview(last)
	c := openCycle{s: s, g: g, id: "RV-" + strconv.Itoa(n), prev: prev, prevSHA: prevSHA}
	c.file = filepath.Join(s.Worktree, s.Reviews, c.id+".md")
	if _, err := os.Stat(c.file); err != nil {
		return c, fmt.Errorf("no open review: no %s/%s.md in %s (%s is not written yet, or the last review is closed)",
			s.Reviews, c.id, s.Worktree, c.id)
	}
	return c, nil
}

// reviewVerifyCmd is `obor review verify <T-a,…> [--findings 1,3] [--note "…"]`: the review worktree detached at the
// integration branch's head, then the open review's reviewer briefed in its running session (obor brief) with
// review-brief.md §4's text: the fix tasks with their merge shas, the findings to re-run (default: every finding), the
// verify file. FAIL (exit 1): no open review, a task with no merge since the previous review, git or serve refusing.
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
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 1
	}
	if err := taskIDs(cfg, tasks...); err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewGitWait)
	defer cancel()
	agent, text, err := verifyReview(ctx, cfg, tasks, findings, note)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	reply, err := brief(ctx, cfg, agent, text, "", project.OSEnv)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(out, reply)
	return 0
}

// verifyReview finds the open review and each task's merge since the previous review, detaches the review worktree at
// the integration branch's head: the reviewer and the verify brief.
func verifyReview(ctx context.Context, cfg *config.Config, tasks, findings []string, note string) (agent, text string,
	err error) {
	c, err := openReview(ctx, cfg)
	if err != nil {
		return "", "", err
	}
	head, err := c.g.Head(ctx, c.s.Branch)
	if err != nil {
		return "", "", err
	}
	merges, err := c.g.Merges(ctx, c.prevSHA, head)
	if err != nil {
		return "", "", err
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
			since := cmp.Or(c.prev, "the first commit")
			return "", "", fmt.Errorf("%s has no merge on %s since %s", task, c.s.Branch, since)
		}
		fixed = append(fixed, task+" ("+sha+")")
	}
	if err := c.g.Detach(ctx, c.s.Worktree, head); err != nil {
		return "", "", err
	}
	return c.s.Agent, verifyBrief(c.id, c.s, head[:min(len(head), 7)], fixed, findings, note), nil
}

// verifyBrief is review-brief.md §4's text: the fixes, the head, the findings to re-run (none: every finding), the
// verify file, the lead's note last.
func verifyBrief(id string, s app.ReviewSetup, short string, fixed, findings []string, note string) string {
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

// reviewCloseCmd is `obor review close "<summary>" [--accept "<line>"]…`: the open review's files deleted from the
// review worktree (it is clean then), each accepted finding a line in BACKLOG.md's Open section naming the review, the
// commit "RV-<n> closed · <reviewed sha> · approved · <summary>" on the integration branch in the main checkout
// (BACKLOG.md with it), then the reviewer stopped (obor restart --stop). The reviewed sha is the review worktree's
// head: the last verify's, else the review's. Refused before any change: no open review, the main checkout not on the
// integration branch or dirty outside BACKLOG.md, the review worktree dirty outside the review's files.
func reviewCloseCmd(args []string) int {
	var summary string
	var accepts []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
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
	c, line, err := closeReview(ctx, cfg, summary, accepts)
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
func closeReview(ctx context.Context, cfg *config.Config, summary string, accepts []string) (openCycle, string, error) {
	c, err := openReview(ctx, cfg)
	if err != nil {
		return c, "", err
	}
	root, wt := cfg.Root, c.s.Worktree
	if on, err := gitIn(ctx, root, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || strings.TrimSpace(on) != c.s.Branch {
		return c, "", fmt.Errorf("the main checkout %s is not on %s: nothing changed", root, c.s.Branch)
	}
	st, err := gitIn(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return c, "", err
	}
	backlogDirty := false
	for _, l := range lines(st) {
		if len(l) > 3 && l[3:] == "BACKLOG.md" {
			backlogDirty = true
			continue
		}
		return c, "", fmt.Errorf("the main checkout is dirty outside BACKLOG.md (%s): nothing changed", l)
	}
	files := []string{c.file}
	if verify := filepath.Join(wt, c.s.Reviews, c.id+"-verify.md"); fileThere(verify) {
		files = append(files, verify)
	}
	own := map[string]bool{}
	for _, f := range files {
		rel, _ := filepath.Rel(wt, f)
		own[filepath.ToSlash(rel)] = true
	}
	if st, err = gitIn(ctx, wt, "status", "--porcelain", "--untracked-files=all"); err != nil {
		return c, "", err
	}
	for _, l := range lines(st) {
		if !strings.HasPrefix(l, "?? ") || !own[l[3:]] {
			return c, "", fmt.Errorf("the review worktree %s has more than %s's files (%s): nothing changed", wt, c.id, l)
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
	sha, err := gitIn(ctx, wt, "rev-parse", "--short=7", "HEAD")
	if err != nil {
		return c, "", err
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return c, "", err
		}
	}
	if st, err = gitIn(ctx, wt, "status", "--porcelain", "--untracked-files=all"); err != nil || strings.TrimSpace(st) != "" {
		return c, "", errors.Join(fmt.Errorf("the review worktree %s is not clean after deleting %s's files: %s", wt, c.id,
			strings.TrimSpace(st)), err)
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
	if _, err := gitIn(ctx, root, "commit", "-q", "--allow-empty", "-m", subject); err != nil {
		return c, "", err
	}
	return c, "CLOSED " + subject, nil
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
