package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AddWorktree makes dir a worktree of the repo whose main checkout is root, added detached at ref when dir is missing
// (git worktree add -f: a worktree whose directory was removed is still registered); added false when dir already is
// one. A dir there that is no worktree of root is an error, nothing changed. The one adder of the role worktrees and
// lanes (obor setup, serve's review moves, the launcher's start of a role agent).
func AddWorktree(ctx context.Context, root, dir, ref string) (added bool, err error) {
	if _, err := os.Stat(dir); err == nil {
		if r, err := Find(dir); err != nil || r.Root != Physical(root) || r.Top != Physical(dir) {
			return false, fmt.Errorf("%s exists and is not a worktree of %s", dir, root)
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "-f", "-q", "--detach", dir, ref)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git worktree add --detach %s %s: %s", dir, ref, strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return true, nil
}
