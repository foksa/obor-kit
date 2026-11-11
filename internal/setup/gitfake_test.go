package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"obor/internal/project"
)

// fakeGit is the gitHost of the tests that check what setup decides (the lanes it adds, the commit it makes, the
// hooks path it sets, the files it links), not what git does: no git process runs. It keeps each repository's
// branch, commits (the files they hold, as the disk had them at the commit), local config and worktrees in memory, and
// lays out on disk only what project.Find reads (a .git directory; a worktree's .git file with its gitdir under
// <root>/.git/worktrees/<name>), as testkit.NewPlain does. A test of git's own result (the integration files:
// real commits, real worktrees, real hook scripts) uses the real calls.
//
// Git answers the commands setup issues and the few a test asks (git, below); any other fails, so a new call is a
// decision about the fake, not a silent pass.
type fakeGit struct {
	mu    sync.Mutex
	repos map[string]*fakeRepo // by root
	calls []string             // every Git call: "dir: args"
}

// fakeRepo is one repository.
type fakeRepo struct {
	root      string
	branch    string
	commits   []fakeCommit // oldest first
	config    map[string]string
	worktrees []string // dirs, in the order they were added
}

// fakeCommit is a commit: its subject and the files it holds (path → content) as of that commit.
type fakeCommit struct {
	msg   string
	files map[string]string
	added []string // the paths this commit names
}

func newFakeGit() *fakeGit { return &fakeGit{repos: map[string]*fakeRepo{}} }

// repoFor is the repository dir is in: dir at or under a root, or a worktree of one.
func (f *fakeGit) repoFor(dir string) *fakeRepo {
	dir = project.Physical(dir)
	for _, r := range f.repos {
		if dir == r.root || strings.HasPrefix(dir, r.root+string(filepath.Separator)) {
			return r
		}
		for _, w := range r.worktrees {
			if dir == w || strings.HasPrefix(dir, w+string(filepath.Separator)) {
				return r
			}
		}
	}
	return nil
}

// init makes root a repository on branch with no commit: the .git directory project.Find reads.
func (f *fakeGit) init(root, branch string) error {
	root = project.Physical(root)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		return err
	}
	f.repos[root] = &fakeRepo{root: root, branch: branch, config: map[string]string{}}
	return nil
}

// seed makes root a repository on main holding files (path → content, which the caller wrote) in one commit
// "initial": the shape testkit.New gives a project.
func (f *fakeGit) seed(root string, files map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.init(root, "main"); err != nil {
		return err
	}
	r := f.repos[project.Physical(root)]
	r.commits = append(r.commits, fakeCommit{msg: "initial", files: files})
	return nil
}

// Git is project.Git over the fake's state.
func (f *fakeGit) Git(dir string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dir+": "+strings.Join(args, " "))
	fail := func(format string, a ...any) (string, error) { return "", fmt.Errorf("fakeGit: "+format, a...) }
	if len(args) == 0 {
		return fail("no command")
	}
	if args[0] == "init" { // init -q -b <branch>
		if len(args) != 4 || args[1] != "-q" || args[2] != "-b" {
			return fail("init %v", args)
		}
		return "", f.init(dir, args[3])
	}
	r := f.repoFor(dir)
	if r == nil {
		return fail("fatal: not a git repository: %s", dir)
	}
	line := strings.Join(args, " ")
	switch {
	case line == "rev-parse -q --verify HEAD", line == "rev-parse HEAD":
		if len(r.commits) == 0 {
			return fail("fatal: Needed a single revision")
		}
		return r.sha() + "\n", nil
	case line == "symbolic-ref --short HEAD", line == "branch --show-current":
		return r.branch + "\n", nil
	case args[0] == "add" && len(args) > 2 && args[1] == "--":
		return "", nil // setup adds, then commits exactly those paths (Commit)
	case args[0] == "ls-files" && len(args) == 4 && args[1] == "--error-unmatch" && args[2] == "--":
		if len(r.commits) > 0 {
			if _, ok := r.commits[len(r.commits)-1].files[args[3]]; ok {
				return args[3] + "\n", nil
			}
		}
		return fail("error: pathspec '%s' did not match any file(s) known to git", args[3])
	case line == "log --format=%s": // newest first
		var subjects []string
		for i := len(r.commits) - 1; i >= 0; i-- {
			subjects = append(subjects, r.commits[i].msg)
		}
		return strings.Join(subjects, "\n") + "\n", nil
	case line == "show --name-only --format= HEAD": // the paths the last commit named
		if len(r.commits) == 0 {
			return fail("fatal: bad revision 'HEAD'")
		}
		return strings.Join(r.commits[len(r.commits)-1].added, "\n") + "\n", nil
	case line == "worktree list":
		out := r.root + " " + r.sha() + " [" + r.branch + "]\n"
		for _, w := range r.worktrees {
			out += w + " " + r.sha() + " (detached HEAD)\n"
		}
		return out, nil
	case len(args) == 4 && args[0] == "worktree" && args[1] == "remove" && args[2] == "--force":
		return "", r.removeWorktree(args[3])
	case args[0] == "config":
		return f.config(r, slices.DeleteFunc(slices.Clone(args[1:]), func(a string) bool { return a == "--local" }))
	}
	return fail("unsupported command: git %s", line)
}

// config is git config (--local is the only scope: the fake keeps no other): --get k, --unset k, k, k v.
func (f *fakeGit) config(r *fakeRepo, args []string) (string, error) {
	switch {
	case len(args) == 2 && args[0] == "--get":
		if v, ok := r.config[args[1]]; ok {
			return v + "\n", nil
		}
		return "", errors.New("fakeGit: exit status 1") // git config --get: the key is not set
	case len(args) == 2 && args[0] == "--unset":
		if _, ok := r.config[args[1]]; !ok {
			return "", errors.New("fakeGit: exit status 5")
		}
		delete(r.config, args[1])
		return "", nil
	case len(args) == 1 && !strings.HasPrefix(args[0], "-"): // git config k reads it
		return f.config(r, []string{"--get", args[0]})
	case len(args) == 2 && !strings.HasPrefix(args[0], "-"):
		r.config[args[0]] = args[1]
		return "", nil
	}
	return "", fmt.Errorf("fakeGit: unsupported command: git config %s", strings.Join(args, " "))
}

func (r *fakeRepo) sha() string { return fmt.Sprintf("%040x", len(r.commits)) }

// removeWorktree is git worktree remove --force: the directory and its registration go.
func (r *fakeRepo) removeWorktree(dir string) error {
	dir = project.Physical(dir)
	for i, w := range r.worktrees {
		if w == dir {
			r.worktrees = append(r.worktrees[:i], r.worktrees[i+1:]...)
			os.RemoveAll(filepath.Join(r.root, ".git", "worktrees", filepath.Base(dir)))
			return os.RemoveAll(dir)
		}
	}
	return fmt.Errorf("fakeGit: %s is not a working tree", dir)
}

// Find is project.Find where a .git entry is above dir (project reads it from files, no process), else not a git
// repository (project.Find would run git to learn that).
func (f *fakeGit) Find(dir string) (*project.Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, project.ErrNotGit
	}
	for cur := project.Physical(abs); ; cur = filepath.Dir(cur) {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return project.Find(dir)
		}
		if cur == filepath.Dir(cur) {
			return nil, project.ErrNotGit
		}
	}
}

// AddWorktree is project.AddWorktree: dir becomes a detached worktree at ref, holding the files ref's commit has, laid
// out as testkit.NewPlain's worktrees are; one that exists is project's own check.
func (f *fakeGit) AddWorktree(ctx context.Context, root, dir, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := os.Stat(dir); err == nil {
		return project.AddWorktree(ctx, root, dir, ref) // no process: it stops at the existing dir
	}
	r := f.repoFor(root)
	if r == nil || len(r.commits) == 0 || ref != r.branch {
		return false, fmt.Errorf("git worktree add --detach %s %s: fatal: invalid reference", dir, ref)
	}
	dir = filepath.Clean(dir)
	gitDir := filepath.Join(r.root, ".git", "worktrees", filepath.Base(dir))
	for path, text := range map[string]string{
		filepath.Join(dir, ".git"):         "gitdir: " + gitDir + "\n",
		filepath.Join(gitDir, "commondir"): "../..\n",
		filepath.Join(gitDir, "gitdir"):    filepath.Join(dir, ".git") + "\n",
		filepath.Join(gitDir, "HEAD"):      r.sha() + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return false, err
		}
	}
	for path, text := range r.commits[len(r.commits)-1].files { // the checkout
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644); err != nil {
			return false, err
		}
	}
	r.worktrees = append(r.worktrees, dir)
	return true, nil
}

// Commit is a commit of exactly files, as the disk has them now, on top of the last one.
func (f *fakeGit) Commit(root, msg string, files []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, root+": commit "+msg+" -- "+strings.Join(files, " "))
	r := f.repoFor(root)
	if r == nil {
		return project.Failf("git add %s", strings.Join(files, " "))
	}
	c := fakeCommit{msg: msg, files: map[string]string{}, added: slicesSorted(files)}
	if n := len(r.commits); n > 0 {
		for p, t := range r.commits[n-1].files {
			c.files[p] = t
		}
	}
	for _, p := range files {
		b, err := os.ReadFile(filepath.Join(r.root, p))
		if err != nil {
			return project.Failf("git add %s", strings.Join(files, " "))
		}
		c.files[p] = string(b)
	}
	r.commits = append(r.commits, c)
	return nil
}

func slicesSorted(s []string) []string {
	s = append([]string(nil), s...)
	sort.Strings(s)
	return s
}

// ran is the Git calls whose "dir: args" line contains sub.
func (f *fakeGit) ran(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			out = append(out, c)
		}
	}
	return out
}
