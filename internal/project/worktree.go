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

// LinkInto symlinks each entry of links (lanes.links: paths relative to root, the main checkout) into the worktree
// dir and returns the entries it linked. Only a missing entry is linked: whatever is at its path in dir (a file git
// tracks, the project's own file, an earlier link) stays as it is, and an entry root does not have is skipped. The
// one linker of every worktree obor makes or meets: lanes and role worktrees (obor setup, the launcher's start of a
// role agent) and the checks' scratch worktrees.
func LinkInto(root, dir string, links []string) (linked []string, err error) {
	for _, link := range links {
		if link == "" {
			continue
		}
		src, dst := filepath.Join(root, link), filepath.Join(dir, link)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return linked, fmt.Errorf("cannot link %s: %v", dst, err)
		}
		if err := os.Symlink(src, dst); err != nil {
			return linked, fmt.Errorf("cannot link %s: %v", dst, err)
		}
		linked = append(linked, link)
	}
	return linked, nil
}
