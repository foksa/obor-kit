package setup

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"obor/internal/project"
)

// gitHost is every git call setup makes: the one seam of the package (Setup.host). The default is today's calls
// (realGit); a test that checks what setup decides, not what git does, gives the Setup a fake that records the
// worktree adds, commits and config writes and answers the reads from its own state (gitfake_test.go).
type gitHost interface {
	// Git is project.Git: git -C dir args…, its stdout, an error carrying git's stderr.
	Git(dir string, args ...string) (string, error)
	// Commit adds files (paths relative to root) and commits exactly them as msg.
	Commit(root, msg string, files []string) error
	// AddWorktree is project.AddWorktree.
	AddWorktree(ctx context.Context, root, dir, ref string) (added bool, err error)
	// Find is project.Find.
	Find(dir string) (*project.Repo, error)
}

// realGit is the seam's default: the git processes and project's readers.
type realGit struct{}

func (realGit) Git(dir string, args ...string) (string, error) { return project.Git(dir, args...) }

func (realGit) Find(dir string) (*project.Repo, error) { return project.Find(dir) }

func (realGit) AddWorktree(ctx context.Context, root, dir, ref string) (bool, error) {
	return project.AddWorktree(ctx, root, dir, ref)
}

func (g realGit) Commit(root, msg string, files []string) error {
	if _, err := g.Git(root, append([]string{"add", "--"}, files...)...); err != nil {
		return project.Failf("git add %s", strings.Join(files, " "))
	}
	args := append([]string{"-C", root, "commit", "-q", "-m", msg, "--"}, files...)
	cmd := exec.CommandContext(context.Background(), "git", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cleanup := project.Group(cmd)
	defer cleanup()
	if err := cmd.Run(); err != nil {
		return project.Failf("git commit -m %s: %s", msg, lastLine(out.String(), err))
	}
	return nil
}

// git is the Setup's seam: realGit unless a test set host.
func (s *Setup) git() gitHost {
	if s.host != nil {
		return s.host
	}
	return realGit{}
}
