package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"obor/internal/app/port"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"obor/internal/config"
	"obor/internal/testkit"
)

// gitScratch is the fake app.Scratch the loop tests drive: the real adapter's steps over the fixture's
// git in a few lines (the real adapter is checks'; T-341). Its worktree is <Base>/proj-wt/merge; commands.lane_setup
// and commands.compile run there, commands.check in the lane (Test, T-961) and commands.after_merge in the main checkout,
// each with sh. fail makes a method
// return an error instead of acting (the failure rule's error cells).
type gitScratch struct {
	p       *testkit.Project
	lanes   *laneTable
	environ []string

	submits, gates, tests, publishes, afterMerges atomic.Int32

	mu           sync.Mutex
	fail         map[string]func(ctx context.Context) error // by method: Submit, Gate, Publish, AfterMerge, Unpin
	onAfterMerge func()                                     // runs before each AfterMerge
	onTest       func(ctx context.Context, lane string)     // runs in each Test with the candidate checked out in lane
	specs        []string                                   // each gate's GateInput.Spec, in order
}

// dir is the fake's worktree.
func (s *gitScratch) dir() string { return filepath.Join(s.p.Base, "proj-wt", "merge") }

// failing is the error the armed hook of method gives (nil: none, or no hook).
func (s *gitScratch) failing(ctx context.Context, method string) error {
	s.mu.Lock()
	f := s.fail[method]
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(ctx)
}

// arm makes method fail with fn (nil: heals it).
func (s *gitScratch) arm(method string, fn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail == nil {
		s.fail = map[string]func(context.Context) error{}
	}
	s.fail[method] = fn
}

func (s *gitScratch) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = s.environ
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// sh runs commands.<name> in dir: its rc (-1: not configured) and its output.
func (s *gitScratch) sh(ctx context.Context, dir, name string) (int, string, error) {
	cfg, err := config.Load(s.p.Root)
	if err != nil {
		return 0, "", err
	}
	c, ok := cfg.Command(name)
	if !ok {
		return -1, "", nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", c.Cmd)
	cmd.Dir, cmd.Env = dir, s.environ
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && ctx.Err() == nil:
		return ee.ExitCode(), strings.TrimSpace(string(out)), nil
	case err != nil:
		return 0, "", err
	}
	return 0, strings.TrimSpace(string(out)), nil
}

func (s *gitScratch) Submit(ctx context.Context, task, id, lane, summary, _ string) (string, error) {
	s.submits.Add(1)
	if err := s.failing(ctx, "Submit"); err != nil {
		return "", err
	}
	pin := "refs/obor/submitted/" + task + "/" + id
	if sha, err := s.git(ctx, s.p.Root, "rev-parse", "-q", "--verify", pin); err == nil {
		return sha, nil
	}
	dir := ""
	s.lanes.mu.Lock()
	for _, l := range s.lanes.lanes {
		if l.Name == lane {
			dir = l.Dir
		}
	}
	s.lanes.mu.Unlock()
	if dir == "" {
		return "", port.Refused{Line: "FAIL " + task + ": no lane " + lane}
	}
	if dirty, err := s.git(ctx, dir, "status", "--porcelain"); err != nil {
		return "", err
	} else if dirty != "" {
		if _, err := s.git(ctx, dir, "add", "-A"); err != nil {
			return "", err
		}
		if _, err := s.git(ctx, dir, "commit", "-qm", task+": "+summary); err != nil {
			return "", err
		}
	}
	sha, err := s.git(ctx, dir, "rev-parse", "HEAD")
	if err == nil {
		_, err = s.git(ctx, s.p.Root, "update-ref", pin, sha)
	}
	return sha, err
}

func (s *gitScratch) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	s.gates.Add(1)
	if err := s.failing(ctx, "Gate"); err != nil {
		return port.GateOutcome{}, err
	}
	s.mu.Lock()
	s.specs = append(s.specs, in.Spec)
	s.mu.Unlock()
	dir := s.dir()
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if _, err := s.git(ctx, s.p.Root, "worktree", "add", "-q", "--detach", dir, "main"); err != nil {
			return port.GateOutcome{}, err
		}
	}
	target, err := s.git(ctx, s.p.Root, "rev-parse", "main")
	if err != nil {
		return port.GateOutcome{}, err
	}
	reset := target
	if in.Kind == port.InputResolved {
		reset = in.Input
	}
	if _, err := s.git(ctx, dir, "reset", "-q", "--hard", reset); err != nil {
		return port.GateOutcome{}, err
	}
	if _, err := s.git(ctx, dir, "clean", "-qfd"); err != nil {
		return port.GateOutcome{}, err
	}
	if in.Kind != port.InputResolved {
		if _, err := s.git(ctx, dir, "merge", "-q", "--no-ff", "--no-commit", in.Input); err != nil {
			files, _ := s.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
			if files == "" {
				return port.GateOutcome{}, err
			}
			return port.GateOutcome{Kind: port.OutcomeConflict, Files: strings.Fields(files), Dir: dir}, nil
		}
		if _, err := s.git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err != nil {
			return port.GateOutcome{Kind: port.OutcomeFail, Line: "FAIL " + in.Task + ": nothing to merge"}, nil
		}
	}
	if rc, _, err := s.sh(ctx, dir, "lane_setup"); err != nil {
		return port.GateOutcome{}, err
	} else if rc > 0 {
		return port.GateOutcome{Kind: port.OutcomeFail, Line: fmt.Sprintf("FAIL lane_setup (rc %d)", rc)}, nil
	}
	checks := "" // T-961: the slot compiles (commands.compile), the lane test runs commands.check
	if rc, out, err := s.sh(ctx, dir, "compile"); err != nil {
		return port.GateOutcome{}, err
	} else if rc > 0 {
		return port.GateOutcome{Kind: port.OutcomeRed, Line: fmt.Sprintf("RED compile (rc %d): %s", rc, out)}, nil
	} else if rc == 0 {
		checks = "compile rc=0"
	}
	sha, submitted := in.Input, in.Input
	if in.Kind == port.InputResolved {
		if target, err = s.git(ctx, dir, "rev-parse", sha+"^1"); err != nil {
			return port.GateOutcome{}, err
		}
		if submitted, err = s.git(ctx, dir, "rev-parse", sha+"^2"); err != nil {
			return port.GateOutcome{}, err
		}
	} else {
		msg := "Merge " + in.Task + ": " + in.Title
		if in.Title == "" {
			msg = "Merge " + in.Task + ": " + in.Summary
		}
		if in.Spec != "" { // as checks.Scratch does: the stored spec into the merge's message (T-521)
			msg += fmt.Sprintf("\n\nSpec rev %d:\n\n%s", in.SpecRev, in.Spec)
		}
		if _, err := s.git(ctx, dir, "commit", "-qm", msg); err != nil {
			return port.GateOutcome{}, err
		}
		if sha, err = s.git(ctx, dir, "rev-parse", "HEAD"); err != nil {
			return port.GateOutcome{}, err
		}
	}
	if _, err := s.git(ctx, s.p.Root, "update-ref", "refs/obor/candidate/"+in.Task, sha); err != nil {
		return port.GateOutcome{}, err
	}
	// The task's own diff (T-961): merge-base(S, target)..S, which a move keeps.
	base, err := s.git(ctx, s.p.Root, "merge-base", submitted, target)
	if err != nil {
		return port.GateOutcome{}, err
	}
	diff, err := s.git(ctx, s.p.Root, "diff", base, submitted)
	if err != nil {
		return port.GateOutcome{}, err
	}
	stat, _ := s.git(ctx, s.p.Root, "diff", "--stat", base, submitted)
	sum := sha256.Sum256([]byte(diff))
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: port.Candidate{Task: in.Task, Lane: in.Lane, SHA: sha, Target: target,
		Base: base, Patch: hex.EncodeToString(sum[:]), Checks: checks, Stat: stat}}, nil
}

// Test is the lane test (T-961), as checks.Scratch's: a dirty lane is an error (the lane untouched); the candidate
// checked out detached in the lane, commands.check run there, the lane's branch restored.
func (s *gitScratch) Test(ctx context.Context, in port.TestInput) (port.TestOutcome, error) {
	s.tests.Add(1)
	if err := s.failing(ctx, "Test"); err != nil {
		return port.TestOutcome{}, err
	}
	dir := ""
	s.lanes.mu.Lock()
	for _, l := range s.lanes.lanes {
		if l.Name == in.Lane {
			dir = l.Dir
		}
	}
	s.lanes.mu.Unlock()
	if dir == "" {
		return port.TestOutcome{Kind: port.OutcomeFail, Line: "FAIL no lane " + in.Lane}, nil
	}
	if dirty, err := s.git(ctx, dir, "status", "--porcelain"); err != nil {
		return port.TestOutcome{}, err
	} else if dirty != "" {
		return port.TestOutcome{}, errors.New("lane " + in.Lane + " dirty: its test runs once it is clean")
	}
	branch, err := s.git(ctx, dir, "branch", "--show-current")
	if err != nil {
		return port.TestOutcome{}, err
	}
	if branch == "" {
		branch = in.Branch
	}
	if _, err := s.git(ctx, dir, "checkout", "-q", "--detach", in.SHA); err != nil {
		return port.TestOutcome{}, err
	}
	s.mu.Lock()
	during := s.onTest
	s.mu.Unlock()
	if during != nil {
		during(ctx, dir)
	}
	rc, out, err := s.sh(ctx, dir, "check")
	if _, rerr := s.git(context.WithoutCancel(ctx), dir, "checkout", "-q", branch); rerr != nil && err == nil {
		err = rerr
	}
	switch {
	case err != nil:
		return port.TestOutcome{}, err
	case rc > 0:
		return port.TestOutcome{Kind: port.OutcomeRed, Line: fmt.Sprintf("RED check (rc %d): %s", rc, out)}, nil
	}
	checks := "no check"
	if rc == 0 {
		checks = "rc=0"
	}
	tree, _ := s.git(ctx, s.p.Root, "rev-parse", in.SHA+"^{tree}")
	return port.TestOutcome{Kind: port.OutcomeGreen, Line: "GREEN " + in.Task + " (" + checks + ")", Checks: checks, Tree: tree}, nil
}

func (s *gitScratch) Resolved(ctx context.Context, task, input string, _ *port.Mismatch) (string, error) {
	dir := s.dir()
	if _, err := s.git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		return "", port.Refused{Line: "the merge in " + dir + " is not committed"}
	}
	head, err := s.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := s.git(ctx, s.p.Root, "merge-base", "--is-ancestor", head, "main"); err == nil {
		return "", port.Refused{Line: dir + " holds no merge"}
	}
	if second, err := s.git(ctx, dir, "rev-parse", "HEAD^2"); err != nil || second != input {
		return "", port.Refused{Line: dir + " holds no merge of " + input}
	}
	_, err = s.git(ctx, s.p.Root, "update-ref", "refs/obor/resolved/"+task, head)
	return head, err
}

func (s *gitScratch) Publish(ctx context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.publishes.Add(1)
	if err := s.failing(ctx, "Publish"); err != nil {
		return port.PublishOutcome{}, err
	}
	if _, err := s.git(ctx, s.p.Root, "merge-base", "--is-ancestor", c.SHA, "main"); err == nil {
		return port.PublishOutcome{Kind: port.PublishAlready}, s.Unpin(ctx, c.Task)
	}
	head, err := s.git(ctx, s.p.Root, "rev-parse", "main")
	if err != nil {
		return port.PublishOutcome{}, err
	}
	if head != c.Target {
		return port.PublishOutcome{Kind: port.PublishMoved, Head: head}, nil
	}
	files, err := s.git(ctx, s.p.Root, "diff", "--name-only", c.Target, c.SHA)
	if err != nil {
		return port.PublishOutcome{}, err
	}
	dirty, err := s.git(ctx, s.p.Root, append([]string{"status", "--porcelain", "--"}, strings.Fields(files)...)...)
	if err != nil {
		return port.PublishOutcome{}, err
	}
	if dirty != "" {
		var held []string
		for _, l := range strings.Split(dirty, "\n") {
			held = append(held, strings.TrimSpace(l[2:]))
		}
		return port.PublishOutcome{Kind: port.PublishHold, Files: held}, nil
	}
	if _, err := s.git(ctx, s.p.Root, "merge", "-q", "--ff-only", c.SHA); err != nil {
		return port.PublishOutcome{}, err
	}
	return port.PublishOutcome{Kind: port.PublishMerged}, s.Unpin(ctx, c.Task)
}

func (s *gitScratch) AfterMerge(ctx context.Context, c port.Candidate) (string, error) {
	s.afterMerges.Add(1)
	s.mu.Lock()
	before := s.onAfterMerge
	s.mu.Unlock()
	if before != nil {
		before()
	}
	if err := s.failing(ctx, "AfterMerge"); err != nil {
		return "", err
	}
	line := "ran"
	if rc, out, err := s.sh(ctx, s.p.Root, "after_merge"); err != nil {
		return "", err
	} else if rc > 0 {
		line = "WARN after_merge failed: " + out
	} else if rc < 0 {
		line = "skipped"
	}
	return line, nil
}

func (s *gitScratch) Landed(ctx context.Context, task string) (bool, error) {
	out, err := s.git(ctx, s.p.Root, "log", "--first-parent", "--format=%s", "main")
	if err != nil {
		return false, err
	}
	for _, subject := range strings.Split(out, "\n") {
		if strings.HasPrefix(subject, "Merge "+task+":") {
			return true, nil
		}
	}
	return false, nil
}

func (s *gitScratch) Unpin(ctx context.Context, task string) error {
	if err := s.failing(ctx, "Unpin"); err != nil {
		return err
	}
	submitted, err := s.git(ctx, s.p.Root, "for-each-ref", "--format=%(refname)", "refs/obor/submitted/"+task+"/")
	if err != nil {
		return err
	}
	for _, ref := range append(strings.Fields(submitted), "refs/obor/candidate/"+task, "refs/obor/resolved/"+task) {
		if _, err := s.git(ctx, s.p.Root, "rev-parse", "-q", "--verify", ref); err != nil {
			continue
		}
		if _, err := s.git(ctx, s.p.Root, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}
