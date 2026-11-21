package view

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/testkit"
)

// laneFacts is what git says of one lane worktree in a test over a plain project (testkit.NewPlain, no repository).
type laneFacts struct {
	Branch string   // the branch HEAD is on ("": detached)
	Head   string   // HEAD's commit ("": headA)
	Dirty  []string // uncommitted paths, as untracked entries of git status
	Merged bool     // HEAD is in the target branch's history
}

// headA is the HEAD of a lane whose facts name none.
const headA = "0123456789abcdef0123456789abcdef01234567"

// oneLane is the lane most tests lay out: L-01 on T-01's branch.
var oneLane = map[string]laneFacts{"L-01": {Branch: "task/T-01"}}

// laneGit is the git of a lane table (LaneTable.Git, reading.git) over lanes (lane name → facts), read at each call so a
// test changes a lane between two reads (the lane test's detached checkout, then its restore): the three reads the
// table makes (rev-parse, status, merge-base) with no process. Any other read, or a dir that is no lane, fails the test
// that made it.
func laneGit(p *testkit.Project, lanes map[string]laneFacts) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, dir string, args ...string) (string, error) {
		for name, f := range lanes {
			if p.LaneDir(name) != dir {
				continue
			}
			switch args[0] {
			case "rev-parse": // --show-toplevel HEAD --symbolic-full-name HEAD
				head, ref := f.Head, "HEAD"
				if head == "" {
					head = headA
				}
				if f.Branch != "" {
					ref = "refs/heads/" + f.Branch
				}
				return dir + "\n" + head + "\n" + ref, nil
			case "status": // --porcelain --untracked-files=all
				var out []string
				for _, path := range f.Dirty {
					out = append(out, "?? "+path)
				}
				return strings.Join(out, "\n"), nil
			case "merge-base": // --is-ancestor HEAD refs/heads/<main>
				if f.Merged {
					return "", nil
				}
				return "", fmt.Errorf("git merge-base: HEAD is not in main's history")
			}
			return "", fmt.Errorf("git %s in %s: not a read of the lane table", strings.Join(args, " "), dir)
		}
		return "", fmt.Errorf("git %s in %s: no such lane worktree", strings.Join(args, " "), dir)
	}
}

// laneTable is cfg's lane table reading lanes through laneGit.
func laneTable(p *testkit.Project, cfg *config.Config, lanes map[string]laneFacts) *checks.LaneTable {
	lt := checks.NewLaneTable(context.Background(), cfg)
	lt.Git = laneGit(p, lanes)
	return lt
}
