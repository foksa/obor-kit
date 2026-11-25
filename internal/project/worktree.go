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
	cmd.Env = GitEnv(nil)
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

// WorktreeLinks are the links the worktree dir of cfg's project gets: lanes.links, plus PluginSettings when it is not
// listed there, the main checkout has the file and dir's checkout (its HEAD) lacks that path, or dir lacks it (LinkInto
// then asks HEAD): a file git does not track reaches a worktree only as a link, and so does a tracked one a worktree
// sits behind (a role worktree made before the commit that tracked it). A worktree at a commit that has the file gets
// it from git, and LinkInto never links a path HEAD tracks. The one choice of
// LinkInto's list: every caller (obor setup's lanes and role worktrees, obor init, the launcher's start of a role
// agent, the checks' scratch worktrees and lane tests) passes this.
func WorktreeLinks(cfg *config.Config, dir string) []string {
	return WorktreeLinksWith(cfg, dir, Git)
}

// WorktreeLinksWith is WorktreeLinks, its git queries run by git: a caller with its own git seam (obor setup's fake)
// passes that seam, so the policy stays here. The main checkout is asked first; dir only when the main checkout
// tracks the file and dir has that path. A dir that lacks the path gets it in the list: LinkInto asks dir's HEAD for
// every missing entry anyway and never links one HEAD tracks, so the list's answer for that path is LinkInto's, one
// query fewer (the set's other readers, LaneDirt and LinksIn, look only at paths dir has).
func WorktreeLinksWith(cfg *config.Config, dir string, git func(dir string, args ...string) (string, error)) []string {
	links := cfg.LaneLinks()
	if slices.Contains(links, PluginSettings) {
		return links
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, PluginSettings)); err != nil {
		return links
	}
	if _, err := os.Lstat(filepath.Join(dir, PluginSettings)); err == nil && TrackedIn(cfg.Root, PluginSettings, git) {
		if out, err := git(dir, "ls-tree", "--name-only", "HEAD", "--", PluginSettings); err == nil && strings.TrimSpace(out) != "" {
			return links
		}
	}
	return append(slices.Clone(links), PluginSettings)
}

// SettingsUntrackedWith: the main checkout root has PluginSettings and git does not track it; git is the query, so a
// caller with its own seam (obor setup's fake) keeps it.
func SettingsUntrackedWith(root string, git func(dir string, args ...string) (string, error)) bool {
	if _, err := os.Stat(filepath.Join(root, PluginSettings)); err != nil {
		return false
	}
	return !TrackedIn(root, PluginSettings, git)
}

// TrackedIn: git tracks path (a file, or a directory with tracked files) in the checkout root; git is the query.
func TrackedIn(root, path string, git func(dir string, args ...string) (string, error)) bool {
	_, err := git(root, "ls-files", "--error-unmatch", "--", path)
	return err == nil
}

// LinkInto symlinks each entry of links (WorktreeLinks: paths relative to root, the main checkout) into the worktree
// dir and returns the entries it linked. Only a missing entry is linked: whatever is at its path in dir (a file git
// tracks, the project's own file, an earlier link) stays as it is, and an entry root does not have is skipped. An
// entry whose path the worktree's HEAD tracks is never linked (a directory entry: tracked when HEAD has anything under
// it): a missing tracked file stays missing, git's view of it is a deletion (a worker's uncommitted one, a merge's
// still open). git asks that once, in dir, only when an entry is missing (trackedAtHead: git ls-tree --name-only HEAD
// -- <missing…>); a worktree without a HEAD links every missing entry. Every entry is tried: one that cannot be
// linked does not stop the next, and err joins the failures, each naming its path. The one linker of every worktree
// obor makes or meets: lanes and role worktrees (obor setup, obor init, the launcher's start of a role agent), the
// checks' scratch worktrees and ReconcileLinks. Nothing is written to git's ignore files: a lane's own steps tell a
// link from the lane's work (LaneDirt, LinksIn).
func LinkInto(root, dir string, links []string, git func(dir string, args ...string) (string, error)) (linked []string, err error) {
	var missing []string
	for _, link := range links {
		if link == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, link)); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, link)); err != nil {
			continue
		}
		missing = append(missing, link)
	}
	tracked := trackedAtHead(dir, missing, git)
	var errs []error
	for _, link := range missing {
		if tracked[link] {
			continue
		}
		src, dst := filepath.Join(root, link), filepath.Join(dir, link)
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

// trackedAtHead is the entries of paths (relative to the worktree dir) that dir's HEAD tracks: a file HEAD has, or a
// directory HEAD has anything under. One git ls-tree --name-only HEAD -- <paths…> in dir; none for no paths. A query
// that fails (a worktree without a HEAD) tracks nothing.
func trackedAtHead(dir string, paths []string, git func(dir string, args ...string) (string, error)) map[string]bool {
	if len(paths) == 0 {
		return nil
	}
	out, err := git(dir, append([]string{"ls-tree", "--name-only", "HEAD", "--"}, paths...)...)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	tracked := map[string]bool{}
	for _, path := range paths {
		p := strings.Trim(filepath.ToSlash(path), "/")
		for _, name := range names {
			if name == p || strings.HasPrefix(name, p+"/") {
				tracked[path] = true
				break
			}
		}
	}
	return tracked
}

// WithLinks runs change (a checkout, a merge) in the worktree dir with obor's links kept right around it: first each
// link of dir whose path target tracks is removed (git refuses to overwrite an untracked file, and a link is one:
// removeTrackedLinks), then change runs, then the worktree's links are reconciled with its link policy
// (ReconcileLinks: the path a tracked file took stays that file, the link the target does not track comes back, and a
// path the worktree's HEAD tracks is never linked, so a path a merge drops is linked once the merge is committed). say
// gets a line per link removed ("removed link <path>: <target7> tracks it") and per link made ("linked <path>").
// change leaves the tree as it was when it fails (a merge that stopped is aborted before it returns), so a failed
// change reconciles the links from the policy and gets back git's error as change gave it; so does a failure of the
// removal. A link that cannot be made is the error of a change that succeeded. git runs in dir (the removal's status,
// ls-tree and rev-parse; the link set's queries). The one link handling of the worktrees obor changes with git: a
// checkout (CheckoutWorktree), the lane merge with its commit, and the abort of a merge a cut left open (Lane.Sync).
func WithLinks(cfg *config.Config, dir, target string, git func(dir string, args ...string) (string, error), say func(line string), change func() error) error {
	err := removeTrackedLinks(cfg.Root, dir, target, git, say)
	if err == nil {
		err = change()
	}
	if err != nil {
		_ = ReconcileLinks(cfg, dir, git, say) // best effort: the change's own error is the one reported
		return err
	}
	return ReconcileLinks(cfg, dir, git, say)
}

// ReconcileLinks makes every entry of the worktree dir's link set (WorktreeLinksWith) that the worktree lacks, from
// the policy alone, and says "linked <path>" for each: a path a tracked file or an ordinary file holds stays as it is
// (LinkInto never overwrites one), and a path the worktree's HEAD tracks is never linked (LinkInto: a missing tracked
// file is a deletion, the worker's own or an open merge's, and stays missing). It keeps no memory of an earlier call's
// removals, so a worktree a crash left without a link its policy wants gets it back. err is the linking's own failure. The one restore of obor's links:
// after a change that did not take place (WithLinks) and in a lane the sync finds without them (Lane.Sync).
func ReconcileLinks(cfg *config.Config, dir string, git func(dir string, args ...string) (string, error), say func(line string)) error {
	linked, err := LinkInto(cfg.Root, dir, WorktreeLinksWith(cfg, dir, git), git)
	for _, path := range linked {
		say("linked " + path)
	}
	return err
}

// CheckoutWorktree runs checkout, the caller's git checkout of target in the worktree dir (its arguments stay the
// caller's: -B, --detach, a branch), with obor's links kept right around it (WithLinks); a failed checkout puts the
// links back from the policy (ReconcileLinks) and returns git's error as checkout gave it. The one checkout of every
// worktree obor makes (lane.prepare, a lane's drop, a lane test's candidate and its restore, the role worktrees'
// moves, the scratch's).
func CheckoutWorktree(cfg *config.Config, dir, target string, git func(dir string, args ...string) (string, error), say func(line string), checkout func() error) error {
	return WithLinks(cfg, dir, target, git, say, checkout)
}

// removeTrackedLinks removes each untracked entry of dir that is obor's link (isLink: a symlink to root's file of the
// same path) whose path target tracks, and says "removed link <path>: <target7> tracks it" for it. err is the
// removal's own failure; a link removed before it is the caller's to reconcile. Nothing else is touched: a regular
// file at such a path is the worktree's and the change fails on it, as git refuses it. git runs in dir (status,
// ls-tree, and rev-parse for the line's short sha).
func removeTrackedLinks(root, dir, target string, git func(dir string, args ...string) (string, error), say func(line string)) error {
	out, err := git(dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	short := ""
	for _, entry := range strings.Split(out, "\x00") {
		path, ok := strings.CutPrefix(entry, "?? ")
		if !ok || !isLink(root, dir, path) {
			continue
		}
		tracked, err := git(dir, "ls-tree", "--name-only", target, "--", path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(tracked) == "" {
			continue
		}
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
			return fmt.Errorf("cannot remove link %s: %v", filepath.Join(dir, path), err)
		}
		if short == "" {
			short = target
			if sha, err := git(dir, "rev-parse", "--verify", "--short=7", target+"^{commit}"); err == nil && strings.TrimSpace(sha) != "" {
				short = strings.TrimSpace(sha)
			}
		}
		say("removed link " + path + ": " + short + " tracks it")
	}
	return nil
}

// LaneDirt is the uncommitted work in the worktree dir (a lane): the path of each entry git status --porcelain
// --untracked-files=all names (every untracked path, never the directory git collapses them into), but an untracked
// entry that is obor's link (isLink: a symlink to the main checkout's file of the same path, which is in obor's link
// set, WorktreeLinksWith). A regular file at a link path is the lane's, and so is a tracked change there. git runs the
// status in dir and, only when an untracked entry is such a symlink, the link set's query in the main checkout. nil:
// the lane is clean. The one lane-dirt check: the lane table, a lane test, a submission and a lane drop ask it.
func LaneDirt(cfg *config.Config, dir string, git func(dir string, args ...string) (string, error)) ([]string, error) {
	out, err := git(dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths, links []string
	read := false
	for _, line := range strings.Split(out, "\n") {
		if len(line) <= 3 {
			continue
		}
		path := line[3:]
		if strings.HasPrefix(line, "?? ") && isLink(cfg.Root, dir, path) {
			if !read {
				links, read = linkSet(cfg, dir, git), true
			}
			if slices.Contains(links, path) {
				continue
			}
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// LinksIn are the paths of obor's link set (WorktreeLinksWith, each without a trailing slash) that the worktree dir
// has as obor's link (isLink): what a lane's cleanup (git clean) and a submission (git add) leave alone.
func LinksIn(cfg *config.Config, dir string, git func(dir string, args ...string) (string, error)) []string {
	var in []string
	for _, link := range linkSet(cfg, dir, git) {
		if isLink(cfg.Root, dir, link) {
			in = append(in, link)
		}
	}
	return in
}

// linkSet is the paths of WorktreeLinksWith for dir as git status names them: slash-separated, no trailing slash (a linked
// directory is a symlink, a file to git).
func linkSet(cfg *config.Config, dir string, git func(dir string, args ...string) (string, error)) []string {
	var set []string
	for _, link := range WorktreeLinksWith(cfg, dir, git) {
		if link = strings.Trim(filepath.ToSlash(link), "/"); link != "" {
			set = append(set, link)
		}
	}
	return set
}

// isLink: path in the worktree dir is a symlink to root's (the main checkout's) file of the same path.
func isLink(root, dir, path string) bool {
	dst := filepath.Join(dir, filepath.FromSlash(path))
	if fi, err := os.Lstat(dst); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	got, err := os.Stat(dst)
	if err != nil {
		return false
	}
	want, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
	return err == nil && os.SameFile(got, want)
}
