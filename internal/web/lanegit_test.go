package web

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/testkit"
)

// laneGit is the git of a lane table over a plain project (testkit.NewPlain, no repository): what git says of each lane
// worktree in lanes (lane name → its branch, "" detached) without a process. HEAD is one commit, no lane is dirty and
// HEAD is in main's history; any other read fails the test that made an unplanned one.
func laneGit(p *testkit.Project, lanes map[string]string) func(context.Context, string, ...string) (string, error) {
	byDir := map[string]string{}
	for name, branch := range lanes {
		byDir[p.LaneDir(name)] = branch
	}
	return func(_ context.Context, dir string, args ...string) (string, error) {
		branch, ok := byDir[dir]
		if !ok {
			return "", fmt.Errorf("git %s in %s: no such lane worktree", strings.Join(args, " "), dir)
		}
		switch args[0] {
		case "rev-parse": // --show-toplevel HEAD --symbolic-full-name HEAD
			ref := "HEAD"
			if branch != "" {
				ref = "refs/heads/" + branch
			}
			return dir + "\n0123456789abcdef0123456789abcdef01234567\n" + ref, nil
		case "status", "merge-base":
			return "", nil
		}
		return "", fmt.Errorf("git %s in %s: unplanned", strings.Join(args, " "), dir)
	}
}

// laneTable is cfg's lane table reading lanes through laneGit.
func laneTable(p *testkit.Project, cfg *config.Config, lanes map[string]string) *checks.LaneTable {
	lt := checks.NewLaneTable(context.Background(), cfg)
	lt.Git = laneGit(p, lanes)
	return lt
}

// oneLane is the lane every web test with a lane has: L-01 on T-01's branch.
var oneLane = map[string]string{"L-01": "task/T-01"}
