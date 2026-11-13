package checks

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"obor/internal/project"
)

// gitReader is the git Scratch reads (docs/plan/architecture-target.md §1): each method one question to git in
// dir, its answer. Scratch decides only over these answers (and the config, the lane dirs on disk), so every outcome
// of a step is a function of them; the fake in fakegit_test.go scripts them for the decision tests.
type gitReader interface {
	// Verify is rev-parse -q --verify ref: its sha, ok false (and no error) when ref does not resolve.
	Verify(dir, ref string) (sha string, ok bool, err error)
	// Rev is rev-parse rev (HEAD, refs/heads/<b>, <sha>^1): an error when it does not resolve.
	Rev(dir, rev string) (string, error)
	// Branch is the branch checked out in dir ("": detached).
	Branch(dir string) (string, error)
	// Status is status --porcelain in dir, one line per path; paths (literal) limit it.
	Status(dir string, paths ...string) ([]string, error)
	// Dirt is the uncommitted work in the worktree dir, obor's own links left out (project.LaneDirt): its paths.
	Dirt(dir string) ([]string, error)
	// Ancestor reports whether a is an ancestor of b (or b itself).
	Ancestor(dir, a, b string) (bool, error)
	// MergeBase is merge-base(a, b) in the main checkout.
	MergeBase(a, b string) (string, error)
	// Parents is sha and its parents (rev-list --parents -n 1).
	Parents(dir, sha string) ([]string, error)
	// Dates is the committer dates (unix seconds) of shas, one line each.
	Dates(dir string, shas ...string) (string, error)
	// Message is sha's committer date (unix seconds), a newline and its message.
	Message(dir, sha string) (string, error)
	// Changed is the paths from..to changes, renames as a deletion and an addition (in the main checkout).
	Changed(from, to string) ([]string, error)
	// Unmerged is the paths an open merge in dir left unmerged.
	Unmerged(dir string) ([]string, error)
	// Staged is the binary patch of dir's index against rev (renames off).
	Staged(dir, rev string) (string, error)
	// Patch is the binary patch from..to (renames off).
	Patch(dir, from, to string) (string, error)
	// Diff and Stat are the diff from..to in the main checkout and its --stat.
	Diff(from, to string) (string, error)
	Stat(from, to string) (string, error)
	// Toplevel is dir's worktree root.
	Toplevel(dir string) (string, error)
	// Refs is the refs under prefix, by name.
	Refs(prefix string) ([]string, error)
	// Log is git log in the main checkout with args (MergeEvidenceBy's).
	Log(args ...string) (string, error)
}

// gitWriter is the git Scratch writes: each method one change to the repository or a worktree.
type gitWriter interface {
	// AddAll stages every change in dir but obor's links (project.LinksIn), the ignored ones left to git; Commit
	// commits the index (an open merge included) with msg, --no-verify; date
	// ("": now) is its author and committer date; Amend amends HEAD with msg, date its committer date.
	AddAll(dir string) error
	Commit(dir, msg, date string) error
	Amend(dir, msg, date string) error
	// Merge merges input into dir's HEAD with rerere, --no-commit --no-ff: an error when it stopped (a conflict
	// among them: MERGE_HEAD stays).
	Merge(dir, input string) error
	// WriteTree writes dir's index as a tree: its sha.
	WriteTree(dir string) (string, error)
	// UpdateRef points ref at sha; DeleteRef deletes it (an absent one is no error). In the main checkout.
	UpdateRef(ref, sha string) error
	DeleteRef(ref string) error
	// FF fast-forwards the main checkout's branch to sha (merge --ff-only).
	FF(sha string) error
	// Reset is reset --hard rev ("": HEAD) in dir; Clean removes its untracked files (ignored ones and obor's links,
	// project.LinksIn, stay).
	Reset(dir, rev string) error
	Clean(dir string) error
	// Detach checks sha out detached in dir; Switch checks branch out.
	Detach(dir, sha string) error
	Switch(dir, branch string) error
	// AddWorktree adds dir as a worktree detached at rev (-f: a removed one may still be registered).
	AddWorktree(dir, rev string) error
}

// gitIO is both: what Scratch holds.
type gitIO interface {
	gitReader
	gitWriter
}

// gio is the git s works through: its fake when a test set one, else realGit over s's own calls (s's gitCtx and lock
// rule, so it is taken after the method set its ctx).
func (s Scratch) gio() gitIO {
	if s.fake != nil {
		return s.fake
	}
	return realGit{s}
}

// realGit is gitIO over real git: each method one git call through Scratch.call (gitLocked, gitCtx), the same calls
// the scratch makes elsewhere.
type realGit struct{ s Scratch }

var _ gitIO = realGit{}

func (g realGit) root() string { return g.s.Cfg.Root }

func (g realGit) Verify(dir, ref string) (string, bool, error) {
	sha, err := g.s.call(dir, "rev-parse", "-q", "--verify", ref)
	ok, err := exit1(err)
	if !ok {
		sha = ""
	}
	return sha, ok, err
}

func (g realGit) Rev(dir, rev string) (string, error) { return g.s.call(dir, "rev-parse", rev) }

func (g realGit) Branch(dir string) (string, error) { return g.s.call(dir, "branch", "--show-current") }

func (g realGit) Status(dir string, paths ...string) ([]string, error) {
	args := []string{"status", "--porcelain"}
	if len(paths) > 0 {
		args = append(append([]string{"--literal-pathspecs"}, args...), "--")
		args = append(args, paths...)
	}
	out, err := g.s.call(dir, args...)
	return lines(out), err
}

func (g realGit) Dirt(dir string) ([]string, error) { return project.LaneDirt(g.s.Cfg, dir, g.s.call) }

func (g realGit) Ancestor(dir, a, b string) (bool, error) { return g.s.ancestor(dir, a, b) }

func (g realGit) MergeBase(a, b string) (string, error) {
	return g.s.call(g.root(), "merge-base", a, b)
}

func (g realGit) Parents(dir, sha string) ([]string, error) {
	out, err := g.s.call(dir, "rev-list", "--parents", "-n", "1", sha)
	return strings.Fields(out), err
}

func (g realGit) Dates(dir string, shas ...string) (string, error) {
	return g.s.call(dir, append([]string{"show", "-s", "--format=%ct"}, shas...)...)
}

func (g realGit) Message(dir, sha string) (string, error) {
	return g.s.call(dir, "show", "-s", "--format=%ct%n%B", sha)
}

func (g realGit) Changed(from, to string) ([]string, error) {
	out, err := g.s.call(g.root(), "diff", "--no-renames", "--name-only", from, to)
	return lines(out), err
}

func (g realGit) Unmerged(dir string) ([]string, error) {
	out, err := g.s.call(dir, "diff", "--name-only", "--diff-filter=U")
	return lines(out), err
}

func (g realGit) Staged(dir, rev string) (string, error) {
	return g.s.call(dir, "diff", "--cached", "--binary", "--no-renames", rev)
}

func (g realGit) Patch(dir, from, to string) (string, error) {
	return g.s.call(dir, "diff", "--binary", "--no-renames", from, to)
}

func (g realGit) Diff(from, to string) (string, error) { return g.s.call(g.root(), "diff", from, to) }

func (g realGit) Stat(from, to string) (string, error) {
	return g.s.call(g.root(), "diff", "--stat", from, to)
}

func (g realGit) Toplevel(dir string) (string, error) {
	return g.s.call(dir, "rev-parse", "--show-toplevel")
}

func (g realGit) Refs(prefix string) ([]string, error) {
	out, err := g.s.call(g.root(), "for-each-ref", "--format=%(refname)", prefix)
	return lines(out), err
}

func (g realGit) Log(args ...string) (string, error) { return g.s.call(g.root(), args...) }

func (g realGit) AddAll(dir string) error {
	links, err := g.notIgnored(dir, project.LinksIn(g.s.Cfg, dir, g.s.call))
	if err != nil {
		return err
	}
	args := []string{"add", "-A"}
	if len(links) > 0 {
		args = append(args, "--", ".")
		for _, link := range links {
			args = append(args, ":(top,exclude,literal)"+link)
		}
	}
	_, err = g.s.call(dir, args...)
	return err
}

// notIgnored is paths in dir without the ones git ignores there: add -A never stages an ignored path, and an exclude
// pathspec that names one fails git add. One check-ignore call over them; its exit 1 (none ignored) is an answer.
// quotePath=false keeps a non-ASCII path as check-ignore names it in its output.
func (g realGit) notIgnored(dir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := append([]string{"-c", "core.quotePath=false", "check-ignore", "--"}, paths...)
	out, errOut, err := g.s.gitLocked(nil, dir, args...)
	if _, err := exit1(err); err != nil {
		return nil, gitError(dir, []string{"check-ignore"}, errOut, err)
	}
	ignored := lines(out)
	return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return slices.Contains(ignored, p) }), nil
}

func (g realGit) Commit(dir, msg, date string) error {
	var env []string
	if date != "" {
		env = []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	}
	return g.locked(env, dir, []string{"commit"}, "commit", "-q", "--no-verify", "-m", msg)
}

func (g realGit) Amend(dir, msg, date string) error {
	return g.locked([]string{"GIT_COMMITTER_DATE=" + date}, dir, []string{"commit", "--amend"},
		"commit", "-q", "--amend", "--no-verify", "-m", msg)
}

func (g realGit) Merge(dir, input string) error {
	args := append(slices.Clone(rerere), "merge", "-q", "--no-commit", "--no-ff", input)
	return g.locked(nil, dir, []string{"merge"}, args...) // named the subcommand, not a -c option
}

// locked is gitLocked whose error names the call as name (gitError).
func (g realGit) locked(env []string, dir string, name []string, args ...string) error {
	if _, errOut, err := g.s.gitLocked(env, dir, args...); err != nil {
		return gitError(dir, name, errOut, err)
	}
	return nil
}

func (g realGit) WriteTree(dir string) (string, error) { return g.s.call(dir, "write-tree") }

func (g realGit) UpdateRef(ref, sha string) error {
	_, err := g.s.call(g.root(), "update-ref", ref, sha)
	return err
}

func (g realGit) DeleteRef(ref string) error {
	_, err := g.s.call(g.root(), "update-ref", "-d", ref)
	return err
}

func (g realGit) FF(sha string) error {
	_, err := g.s.call(g.root(), "merge", "-q", "--ff-only", sha)
	return err
}

func (g realGit) Reset(dir, rev string) error {
	_, err := g.s.call(dir, nonEmpty("reset", "-q", "--hard", rev)...)
	return err
}

func (g realGit) Clean(dir string) error {
	args := []string{"clean", "-fdq"}
	for _, link := range project.LinksIn(g.s.Cfg, dir, g.s.call) {
		args = append(args, "-e", "/"+globEscape.Replace(link))
	}
	_, err := g.s.call(dir, args...)
	return err
}

// globEscape makes a path a literal git ignore pattern.
var globEscape = strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`)

func (g realGit) Detach(dir, sha string) error {
	_, err := g.s.call(dir, "checkout", "-q", "--detach", sha)
	return err
}

func (g realGit) Switch(dir, branch string) error {
	_, err := g.s.call(dir, "checkout", "-q", branch)
	return err
}

func (g realGit) AddWorktree(dir, rev string) error {
	_, err := g.s.call(g.root(), "worktree", "add", "-f", "-q", "--detach", dir, rev)
	return err
}

// ancestor reports whether a is an ancestor of b (or b itself); an error: git could not tell.
func (s Scratch) ancestor(dir, a, b string) (bool, error) {
	_, err := s.call(dir, "merge-base", "--is-ancestor", a, b)
	return exit1(err)
}

// exit1 is a yes/no git call's answer: exit 0 yes, exit 1 no, anything else an error.
func exit1(err error) (bool, error) {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// call runs git -C dir args… (a lock taken by another git waited for: gitLocked): stdout; an error names the call
// and git's last stderr line.
func (s Scratch) call(dir string, args ...string) (string, error) {
	out, errOut, err := s.gitLocked(nil, dir, args...)
	if err != nil {
		return out, gitError(dir, args, errOut, err)
	}
	return out, nil
}

// gitError wraps a failed git call's error (errors.As still finds its *exec.ExitError).
func gitError(dir string, args []string, errOut string, err error) error {
	why := ""
	if l := lines(errOut); len(l) > 0 {
		why = ": " + l[len(l)-1]
	}
	return fmt.Errorf("git %s in %s: %w%s", args[0], dir, err, why)
}
