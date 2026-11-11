package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/config"
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

// PluginSettings is the project settings file that enables obor's plugin, relative to a checkout. A worktree has
// the files git tracks: one git does not track reaches it only as a link.
const PluginSettings = ".claude/settings.json"

// WorktreeLinks are the links every worktree of cfg's project gets: lanes.links, plus PluginSettings while the main
// checkout has it, git does not track it and lanes.links does not list it. The one choice of LinkInto's list: every
// caller (obor setup's lanes and role worktrees, obor init, the launcher's start of a role agent, the checks'
// scratch worktrees and lane tests) passes this.
func WorktreeLinks(cfg *config.Config) []string {
	return WorktreeLinksWith(cfg, Git)
}

// WorktreeLinksWith is WorktreeLinks, its git query run by git: a caller with its own git seam (obor setup's fake)
// passes that seam, so the policy stays here.
func WorktreeLinksWith(cfg *config.Config, git func(dir string, args ...string) (string, error)) []string {
	links := cfg.LaneLinks()
	if slices.Contains(links, PluginSettings) || !SettingsUntrackedWith(cfg.Root, git) {
		return links
	}
	return append(slices.Clone(links), PluginSettings)
}

// SettingsUntrackedWith: the main checkout root has PluginSettings and git does not track it; git is the query, so a
// caller with its own seam (obor setup's fake) keeps it.
func SettingsUntrackedWith(root string, git func(dir string, args ...string) (string, error)) bool {
	if _, err := os.Stat(filepath.Join(root, PluginSettings)); err != nil {
		return false
	}
	_, err := git(root, "ls-files", "--error-unmatch", "--", PluginSettings)
	return err != nil
}

// LinkInto symlinks each entry of links (WorktreeLinks: paths relative to root, the main checkout) into the worktree
// dir and returns the entries it linked. Only a missing entry is linked: whatever is at its path in dir (a file git
// tracks, the project's own file, an earlier link) stays as it is, and an entry root does not have is skipped. Every
// entry is tried: one that cannot be linked does not stop the next, and err joins the failures, each naming its
// path. The one linker of every worktree obor makes or meets: lanes and role worktrees (obor setup, the launcher's
// start of a role agent) and the checks' scratch worktrees.
func LinkInto(root, dir string, links []string) (linked []string, err error) {
	var errs []error
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
			errs = append(errs, fmt.Errorf("cannot link %s: %v", dst, err))
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			errs = append(errs, fmt.Errorf("cannot link %s: %v", dst, err))
			continue
		}
		linked = append(linked, link)
	}
	return linked, errors.Join(errs...)
}
