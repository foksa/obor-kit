package testkit

// The setup cache. A project New makes is pristine while only Config, Lane and Commit have changed it: its tree is
// then the tree its key names, the calls so far (Config by the bytes it writes, Lane by name and branch, Commit by dir
// and subject) and the git they run. The first test of a binary to make a call from a key runs it with git and keeps a
// copy of the result; every later test that makes the same call from the same key gets that copy, in process. The first
// other write or git call through the project ends its pristine run, and so does a tree that no longer is its key's
// (a file the test wrote itself): from there every call runs its git.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// node is the tree one key names: <base>/proj and <base>/proj-wt, its worktrees pointing at base. The template is the
// node of the empty key; the others live under the template's base, so Run removes them with it.
type node struct {
	once   sync.Once
	base   string
	files  tree
	ok     bool         // built: a call that failed its test leaves the node unusable, and the next test runs its git
	copies atomic.Int32 // projects that took this tree by copy
}

// nodes are the built trees by key; nodeSeq names their dirs.
var (
	nodes   sync.Map // key → *node
	nodeSeq atomic.Int64
)

// nodeFor is key's node, made empty on first use.
func nodeFor(key string) *node {
	n, _ := nodes.LoadOrStore(key, &node{})
	return n.(*node)
}

// stat is what the cache compares of a path: its type, and for a file its permissions, size and mtime (a copy keeps
// them, so a file the test wrote differs), for a symlink its target.
type stat struct {
	mode  fs.FileMode
	size  int64
	mtime time.Time
	link  string
}

// tree is the stats of a project's <base>/proj and <base>/proj-wt, by path relative to base.
type tree map[string]stat

// projectDirs are the dirs under a project's base the cache keeps: the main checkout and the lanes.
var projectDirs = []string{"proj", "proj-wt"}

// scan is the tree at base.
func scan(base string) (tree, error) {
	t := tree{}
	for _, top := range projectDirs {
		err := filepath.WalkDir(filepath.Join(base, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			s := stat{mode: info.Mode().Type() | info.Mode().Perm()}
			switch {
			case info.Mode().IsRegular():
				s.size, s.mtime = info.Size(), info.ModTime()
			case info.Mode()&fs.ModeSymlink != 0:
				if s.link, err = os.Readlink(path); err != nil {
					return err
				}
			case !info.IsDir():
				return fmt.Errorf("scan %s: not a dir, file or symlink", path)
			}
			if info.IsDir() {
				s.mode = fs.ModeDir // a dir's permissions and times change with its entries
			}
			t[rel] = s
			return nil
		})
		if err != nil && !(top != "proj" && errors.Is(err, fs.ErrNotExist)) {
			return nil, err
		}
	}
	return t, nil
}

// pointer reports whether rel is a file RepairWorktrees writes (a worktree's .git, its admin dir's gitdir): a copy's
// differs from its source's by design.
func pointer(rel string) bool {
	for _, pat := range []string{"proj-wt/*/.git", "proj/.git/worktrees/*/gitdir"} {
		if ok, _ := filepath.Match(pat, filepath.ToSlash(rel)); ok {
			return true
		}
	}
	return false
}

// same reports whether have is the tree want names, the worktree pointers aside.
func same(have, want tree) bool {
	if len(have) != len(want) {
		return false
	}
	for rel, w := range want {
		h, ok := have[rel]
		if !ok || h.mode != w.mode || (!pointer(rel) && (h.size != w.size || !h.mtime.Equal(w.mtime) || h.link != w.link)) {
			return false
		}
	}
	return true
}

// cached makes the setup call step (empty: not a setup call) on p: while p is pristine, by copy of the tree its key
// plus step names (built here, by call, when no test has made it yet); otherwise by call.
func (p *Project) cached(step string, call func()) {
	p.t.Helper()
	from, ok := p.pristineTree(step)
	p.pristine = false
	if !ok {
		call()
		return
	}
	key := p.key + "\x1e" + step + "\x1f" + gitContext()
	n := nodeFor(key)
	built := false
	n.once.Do(func() {
		built = true
		call() // a failure stops the test here: n stays unbuilt
		dir := filepath.Join(tmpl.base, fmt.Sprintf("k%d", nodeSeq.Add(1)))
		if err := keepTree(p.Base, dir); err != nil {
			p.t.Logf("testkit: keep the setup tree: %v", err)
			return
		}
		files, err := scan(dir)
		if err != nil {
			p.t.Logf("testkit: keep the setup tree: %v", err)
			return
		}
		n.base, n.files, n.ok = dir, files, true
	})
	switch {
	case built:
	case !n.ok:
		call()
		return
	default:
		if err := syncTree(n.base, p.Base, n.files, from); err != nil {
			p.t.Fatalf("testkit: copy the setup tree: %v", err)
		}
		n.copies.Add(1)
	}
	if n.ok {
		p.key, p.pristine = key, true
	}
}

// pristineTree is p's tree when step is a setup call and p is pristine: still the tree its key names.
func (p *Project) pristineTree(step string) (tree, bool) {
	if !p.pristine || step == "" {
		return nil, false
	}
	n := nodeFor(p.key)
	if p.key == "" {
		n.once.Do(func() {
			files, err := scan(tmpl.base)
			n.base, n.files, n.ok = tmpl.base, files, err == nil
		})
	}
	if !n.ok {
		return nil, false
	}
	have, err := scan(p.Base)
	if err != nil || !same(have, n.files) {
		return nil, false
	}
	return have, true
}

// gitContext is what decides a setup call's result besides the calls before it: the git that runs (a test's fake git
// first on PATH is a different git) and the git environment.
func gitContext() string {
	git, err := exec.LookPath("git")
	if err != nil {
		git = "?" + err.Error()
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	if _, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); !ok {
		env = append(env, "HOME="+os.Getenv("HOME"), "XDG_CONFIG_HOME="+os.Getenv("XDG_CONFIG_HOME"))
	}
	sort.Strings(env)
	return git + "\x1f" + strings.Join(env, "\x1f")
}

// keepTree copies the project tree at base (proj, proj-wt) to dir and points its worktrees at dir.
func keepTree(base, dir string) error {
	for _, top := range projectDirs {
		src := filepath.Join(base, top)
		if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) && top != "proj" {
			continue
		}
		if err := copyTree(src, filepath.Join(dir, top)); err != nil {
			return err
		}
	}
	return repairWorktrees(dir)
}

// syncTree makes the project tree at dst (whose tree is have) the one at src (whose tree is want): it writes what
// differs, removes what src does not have, keeps the rest (dirs keep their inode: a test's cwd stays valid), then
// points the worktrees at dst.
func syncTree(src, dst string, want, have tree) error {
	rels := slices.Sorted(func(yield func(string) bool) {
		for rel := range want {
			if !yield(rel) {
				return
			}
		}
	})
	for _, rel := range rels {
		w, to := want[rel], filepath.Join(dst, rel)
		h, ok := have[rel]
		if ok && h.mode.Type() != w.mode.Type() {
			if err := os.RemoveAll(to); err != nil {
				return err
			}
			ok = false
		}
		switch {
		case w.mode.IsDir():
			if !ok {
				if err := os.Mkdir(to, 0o755); err != nil {
					return err
				}
			}
		case ok && h.mode == w.mode && h.size == w.size && h.mtime.Equal(w.mtime) && h.link == w.link:
		default:
			if err := os.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if w.mode&fs.ModeSymlink != 0 {
				if err := os.Symlink(w.link, to); err != nil {
					return err
				}
				continue
			}
			if err := copyFile(filepath.Join(src, rel), to, w.mode.Perm(), w.mtime); err != nil {
				return err
			}
		}
	}
	gone := []string{}
	for rel := range have {
		if _, ok := want[rel]; !ok {
			gone = append(gone, rel)
		}
	}
	sort.Strings(gone)
	for _, rel := range gone {
		if err := os.RemoveAll(filepath.Join(dst, rel)); err != nil {
			return err
		}
	}
	return repairWorktrees(dst)
}

// RepairWorktrees points every copied worktree under base/proj-wt at base: its .git file at the repo's copied admin
// dir, and that admin dir's gitdir at the worktree (what `git worktree repair` writes, with no git process). For a
// test that copies a built project tree itself.
func RepairWorktrees(t testing.TB, base string) {
	t.Helper()
	if err := repairWorktrees(base); err != nil {
		t.Fatalf("repair worktrees: %v", err)
	}
}

// repairWorktrees is RepairWorktrees; a base with no proj-wt has none to repair.
func repairWorktrees(base string) error {
	entries, err := os.ReadDir(filepath.Join(base, "proj-wt"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		wt := filepath.Join(base, "proj-wt", e.Name())
		dotgit := filepath.Join(wt, ".git")
		b, err := os.ReadFile(dotgit)
		if err != nil {
			continue // not a worktree
		}
		old, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return fmt.Errorf("%s is not a gitdir file: %q", dotgit, b)
		}
		admin := filepath.Join(base, "proj", ".git", "worktrees", filepath.Base(old))
		if err := os.WriteFile(dotgit, []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(dotgit+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}
