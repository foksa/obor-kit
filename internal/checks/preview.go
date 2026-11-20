package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/app/port"
)

// MergePreview is the lane's work merged onto the target as it is now (the worker's merge_preview tool): the lane's
// working tree as a commit on its HEAD (gitReader.Snapshot: its changed and untracked files, the ignored ones and
// obor's links left out; a lane without uncommitted work is its HEAD), then git merge-tree of the target's head and
// that commit. Read-only for the lane and every ref: no ref, index, worktree or serve's worktree is touched, only
// loose objects are written (git's gc prunes them). A lane with no dir is a Refused error.
func (s Scratch) MergePreview(ctx context.Context, lane string) (port.Preview, error) {
	s.gitCtx = ctx
	g := s.gio()
	branch, target, err := s.target(g)
	if err != nil {
		return port.Preview{}, err
	}
	dir := s.laneDir(lane)
	if dir == "" {
		return port.Preview{}, port.Refused{Line: fmt.Sprintf("no lane %s", lane)}
	}
	work, err := g.Rev(dir, "HEAD")
	if err != nil {
		return port.Preview{}, err
	}
	dirt, err := g.Dirt(dir)
	if err != nil {
		return port.Preview{}, err
	}
	if len(dirt) > 0 {
		if work, err = g.Snapshot(dir, work); err != nil {
			return port.Preview{}, err
		}
	}
	out, clean, err := g.MergeTree(target, work)
	if err != nil {
		return port.Preview{}, err
	}
	p := port.Preview{Target: branch, TargetSHA: target}
	if !clean {
		p.Conflicts = mergeTreeConflicts(out)
	}
	return p, nil
}

// mergeTreeConflicts reads merge-tree --write-tree --name-only --messages' output of a conflicted merge: the tree's
// oid, one conflicted path per line, a blank line, then the informational messages. Each path gets the message lines
// that name it (the path as a word of the line).
func mergeTreeConflicts(out string) []port.PreviewConflict {
	head, msgs, _ := strings.Cut(out, "\n\n")
	paths := lines(head)
	if len(paths) > 0 {
		paths = paths[1:] // the tree's oid
	}
	var cs []port.PreviewConflict
	for _, path := range paths {
		c := port.PreviewConflict{Path: path}
		for _, l := range lines(msgs) {
			if namesPath(l, path) {
				c.Messages = append(c.Messages, l)
			}
		}
		cs = append(cs, c)
	}
	return cs
}

// namesPath reports whether the message line names path as a word: after a space, then the line's end, a space, or a
// period or comma before one.
func namesPath(line, path string) bool {
	for i := 0; ; {
		j := strings.Index(line[i:], " "+path)
		if j < 0 {
			return false
		}
		rest := line[i+j+1+len(path):]
		rest = strings.TrimLeft(rest, ".,")
		if rest == "" || rest[0] == ' ' {
			return true
		}
		i += j + 1
	}
}

// Snapshot builds dir's working tree as a commit on head in a temporary index (GIT_INDEX_FILE in a temp dir): read
// from head (a copy of dir's index first, for its stat info), then every change git add -A stages there (obor's links and the ignored files left out, addAllArgs),
// write-tree, commit-tree on head. The lane's own index, its refs and its worktree are not touched.
func (g realGit) Snapshot(dir, head string) (string, error) {
	tmp, err := os.MkdirTemp("", "obor-preview-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	run := func(args ...string) (string, error) {
		out, errOut, err := g.s.gitLocked(env, dir, args...)
		if err != nil {
			return "", gitError(dir, args, errOut, err)
		}
		return strings.TrimSpace(out), nil
	}
	// A copy of dir's index keeps its stat info, so add -A hashes only the files that changed; read-tree --reset then
	// sets every entry to head's (a staged change, an unmerged entry dropped), the stat info kept where they match.
	if err := g.copyIndex(dir, index); err != nil {
		return "", err
	}
	if _, err := run("read-tree", "--reset", head); err != nil {
		return "", err
	}
	add, err := g.addAllArgs(dir)
	if err != nil {
		return "", err
	}
	if _, err := run(add...); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	return run("commit-tree", tree, "-p", head, "-m", "obor merge preview")
}

// copyIndex copies dir's index file (rev-parse --git-path index) to to; a worktree without one copies nothing.
func (g realGit) copyIndex(dir, to string) error {
	path, err := g.s.call(dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o600)
}

// MergeTree is git merge-tree --write-tree --name-only --messages ours theirs in the main checkout: its output and
// whether the merge is clean (exit 0; exit 1 a conflict, anything else an error).
func (g realGit) MergeTree(ours, theirs string) (string, bool, error) {
	args := []string{"merge-tree", "--write-tree", "--name-only", "--messages", ours, theirs}
	out, errOut, err := g.s.gitLocked(nil, g.root(), args...)
	clean, err := exit1(err)
	if err != nil {
		return "", false, gitError(g.root(), args, errOut, err)
	}
	return out, clean, nil
}
