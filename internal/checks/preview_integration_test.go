//go:build integration

package checks

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// MergePreview on real git: the target moved on with conflicting edits to a file the lane changed (uncommitted) and to
// the lane's untracked new file, and added the path of a file the lane's .gitignore ignores. The preview names the two
// conflicts and not the ignored one (left out of the snapshot), and leaves the lane's status, its index file, every
// ref and serve's worktree as they were.
func TestMergePreviewRealGit(t *testing.T) {
	t.Parallel()
	y := newSx(t)
	y.Write(filepath.Join(y.lane, "shared.txt"), "lane\n")        // tracked, changed in the lane
	y.Write(filepath.Join(y.lane, ".obor/state/x.txt"), "lane\n") // ignored in the lane
	// feature.txt is the lane's untracked new file (the fixture's).
	y.Write("shared.txt", "main\n")
	y.Write("feature.txt", "main\n")
	y.Write(".obor/state/x.txt", "main\n")
	y.Git(y.Root, "add", "-f", "shared.txt", "feature.txt", ".obor/state/x.txt")
	y.Git(y.Root, "commit", "-q", "-m", "main edit")

	status := y.Git(y.lane, "status", "--porcelain", "--untracked-files=all")
	if want := "M shared.txt\n?? feature.txt"; status != want { // the output trimmed
		t.Fatalf("precondition: the lane's status %q, want %q", status, want)
	}
	index := y.Git(y.lane, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(index) {
		index = filepath.Join(y.lane, index)
	}
	readIndex := func() []byte {
		b, err := os.ReadFile(index)
		if err != nil {
			t.Fatalf("the lane's index: %v", err)
		}
		return b
	}
	before := readIndex()
	refs := y.Git(y.Root, "for-each-ref", "--format=%(refname) %(objectname)")
	laneHead := y.head(y.lane)

	p, err := y.s.MergePreview(ctx, "L-01")
	if err != nil {
		t.Fatalf("MergePreview: %v", err)
	}
	var paths []string
	for _, c := range p.Conflicts {
		paths = append(paths, c.Path)
		if !slices.ContainsFunc(c.Messages, func(m string) bool {
			return m == "CONFLICT (add/add): Merge conflict in "+c.Path ||
				m == "CONFLICT (content): Merge conflict in "+c.Path
		}) {
			t.Errorf("%s: messages %q; want git's CONFLICT line for it", c.Path, c.Messages)
		}
	}
	if want := []string{"feature.txt", "shared.txt"}; !slices.Equal(paths, want) {
		t.Errorf("conflicts %q; want %q (the changed and the untracked file, not the ignored one)", paths, want)
	}
	if p.Target != "main" || p.TargetSHA != y.head(y.Root) {
		t.Errorf("target %s %s; want main at %s", p.Target, p.TargetSHA, y.head(y.Root))
	}

	if !bytes.Equal(readIndex(), before) {
		t.Error("the lane's index file changed")
	}
	if got := y.Git(y.Root, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
		t.Errorf("refs after the preview:\n%s\nwant unchanged:\n%s", got, refs)
	}
	if got := y.head(y.lane); got != laneHead {
		t.Errorf("the lane's HEAD %s; want unchanged %s", got, laneHead)
	}
	if got := y.Git(y.lane, "status", "--porcelain", "--untracked-files=all"); got != status {
		t.Errorf("the lane's status %q after the preview; want unchanged %q", got, status)
	}
	if _, err := os.Stat(y.serve); !os.IsNotExist(err) {
		t.Errorf("serve's worktree %s: %v; want it not made by a preview", y.serve, err)
	}

	// The lane's work committed and the target merged into it: clean.
	y.Write(filepath.Join(y.lane, "shared.txt"), "main\n")
	y.Write(filepath.Join(y.lane, "feature.txt"), "main\n")
	if p, err := y.s.MergePreview(ctx, "L-01"); err != nil || len(p.Conflicts) != 0 {
		t.Errorf("the lane matching the target: %+v, %v; want clean", p, err)
	}
}
