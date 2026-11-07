package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// T-975: `make build` writes OUT (dist/obor by default), so the promotion's after_merge can build the kit in its own
// worktree and still land the binary serve follows in the main checkout's dist/obor. The kit's own
// commands.after_merge.cmd is run as written (bash -c, as internal/run does), with and without OBOR_ROOT.

// outKit makes dir a kit copy for `make build`: the tree's Makefile, VERSION 1.2.3 and a fake go at dir/bin/go that
// logs its argv and writes the -o path, so a test can see both the argument and what the build left behind.
func outKit(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeExec(t, filepath.Join(dir, "bin", "go"), `#!/bin/bash
echo "$*" >> "`+filepath.Join(dir, "go.log")+`"
out=
prev=
for a; do
  [ "$prev" != -o ] || out=$a
  prev=$a
done
[ -n "$out" ] || { echo "fake go: build without -o" >&2; exit 1; }
mkdir -p "$(dirname "$out")"
echo binary > "$out"
`)
}

// runBuild runs `make -s -C dir build [extra...]` on a kit copy (outKit) and returns the fake go's logged argv.
func runBuild(t *testing.T, dir string, extra ...string) string {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-s", "-C", dir, "build"}, extra...)...)
	cmd.Env = append(plainMakeEnv(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make build: %v\n%s", err, out)
	}
	return strings.TrimSpace(readFile(t, filepath.Join(dir, "go.log")))
}

// TestMakeBuildOut: make build writes dist/obor by default and OUT's file when given (its directories created: a
// promotion's worktree has no dist/ under the root it builds into).
func TestMakeBuildOut(t *testing.T) {
	t.Parallel()
	t.Run("default: dist/obor", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		outKit(t, dir)
		build := runBuild(t, dir)
		if !strings.Contains(build, "-o dist/obor ") {
			t.Errorf("the default build is %q, want -o dist/obor", build)
		}
		if got := readFile(t, filepath.Join(dir, "dist", "obor")); got != "binary\n" {
			t.Errorf("make build left %q at dist/obor, want the built binary", got)
		}
	})
	t.Run("OUT: the binary lands there, not in dist/", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		outKit(t, dir)
		out := filepath.Join(t.TempDir(), "elsewhere", "obor")
		build := runBuild(t, dir, "OUT="+out)
		if !strings.Contains(build, "-o "+out+" ") {
			t.Errorf("build OUT=%s is %q, want -o %s", out, build, out)
		}
		if got := readFile(t, out); got != "binary\n" {
			t.Errorf("make build OUT=%s left %q there, want the built binary", out, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "dist", "obor")); !os.IsNotExist(err) {
			t.Errorf("make build OUT=%s also wrote dist/obor (%v)", out, err)
		}
	})
}

// afterMergeCmd is the tree's commands.after_merge.cmd (T-975: `make build OUT=${OBOR_ROOT:-.}/dist/obor`).
func afterMergeCmd(t *testing.T) string {
	t.Helper()
	var cfg struct {
		Commands struct {
			AfterMerge struct {
				Cmd string `json:"cmd"`
			} `json:"after_merge"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(treeFile(t, ".obor/config.json"), &cfg); err != nil {
		t.Fatalf(".obor/config.json: %v", err)
	}
	if cfg.Commands.AfterMerge.Cmd == "" {
		t.Fatal("precondition: .obor/config.json sets commands.after_merge.cmd")
	}
	return cfg.Commands.AfterMerge.Cmd
}

// runAfterMerge runs the kit's commands.after_merge.cmd in dir (the checkout the merge landed in) with dir's fake go
// first on PATH and OBOR_ROOT set only when root is not empty; it returns the output.
func runAfterMerge(t *testing.T, dir, root string) string {
	t.Helper()
	var env []string
	for _, kv := range plainMakeEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if name != "OBOR_ROOT" { // the caller sets it, or must not inherit one
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if root != "" {
		env = append(env, "OBOR_ROOT="+root)
	}
	cmd := exec.Command("bash", "-c", afterMergeCmd(t))
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commands.after_merge in %s (OBOR_ROOT=%q): %v\n%s", dir, root, err, out)
	}
	return string(out)
}

// TestAfterMergeBuildsIntoRoot: today's per-publish after_merge (no OBOR_ROOT) builds in the main checkout; the
// promotion's (OBOR_ROOT set, make run in the promotion worktree) still leaves the binary in the root's dist/obor.
func TestAfterMergeBuildsIntoRoot(t *testing.T) {
	t.Parallel()
	t.Run("no OBOR_ROOT: the main checkout", func(t *testing.T) {
		t.Parallel()
		root := testkit.Dir(t)
		outKit(t, root)
		runAfterMerge(t, root, "")
		if got := readFile(t, filepath.Join(root, "dist", "obor")); got != "binary\n" {
			t.Errorf("after_merge without OBOR_ROOT left %q at dist/obor, want the built binary", got)
		}
	})
	t.Run("OBOR_ROOT set: the root's dist/obor, from the worktree", func(t *testing.T) {
		t.Parallel()
		worktree := testkit.Dir(t) // the promotion's own worktree: the build runs here
		outKit(t, worktree)
		root := t.TempDir() // the main checkout: no dist/ until the build writes it
		runAfterMerge(t, worktree, root)
		if got := readFile(t, filepath.Join(root, "dist", "obor")); got != "binary\n" {
			t.Errorf("after_merge with OBOR_ROOT=%s left %q at the root's dist/obor, want the built binary", root, got)
		}
		if _, err := os.Stat(filepath.Join(worktree, "dist", "obor")); !os.IsNotExist(err) {
			t.Errorf("after_merge with OBOR_ROOT also wrote the worktree's dist/obor (%v)", err)
		}
	})
}
