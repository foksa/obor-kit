//go:build release

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

// after_merge runs only per publish in the main checkout (T-1076 deleted the promotion), so the kit's own
// commands.after_merge.cmd — plain `make build` (T-1079) — is run as written (bash -c, as internal/run does) and must
// leave the binary serve follows in this checkout's dist/obor.

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

// TestMakeBuildOut: make build writes dist/obor by default and OUT's file when given (its directories created: another
// worktree has no dist/ under the root it builds into), and the OpenCode kind's binary beside it, from
// kinds/opencode (T-1046: obor runs a kit kind from its own executable's directory).
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
		if !strings.Contains(build, "-o dist/obor-kind-opencode ./kinds/opencode") {
			t.Errorf("the default build is %q, want the kind built as dist/obor-kind-opencode", build)
		}
		if got := readFile(t, filepath.Join(dir, "dist", "obor-kind-opencode")); got != "binary\n" {
			t.Errorf("make build left %q at dist/obor-kind-opencode, want the built kind", got)
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
		if got := readFile(t, filepath.Join(filepath.Dir(out), "obor-kind-opencode")); got != "binary\n" {
			t.Errorf("make build OUT=%s left %q beside it, want the built kind", out, got)
		}
	})
}

// afterMergeCmd is the tree's after_merge command (T-1079: `make build`): merge.after.cmd in config shape 2,
// commands.after_merge.cmd in shape 1 (T-1124: serve rewrites the kit's file, not the task that lands the shape).
func afterMergeCmd(t *testing.T) string {
	t.Helper()
	type command struct {
		Cmd string `json:"cmd"`
	}
	var cfg struct {
		Merge struct {
			After command `json:"after"`
		} `json:"merge"`
		Commands struct {
			AfterMerge command `json:"after_merge"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(treeFile(t, ".obor/config.json"), &cfg); err != nil {
		t.Fatalf(".obor/config.json: %v", err)
	}
	cmd := cfg.Merge.After.Cmd
	if cmd == "" {
		cmd = cfg.Commands.AfterMerge.Cmd
	}
	if cmd == "" {
		t.Fatal("precondition: .obor/config.json sets merge.after.cmd (shape 1: commands.after_merge.cmd)")
	}
	return cmd
}

// runAfterMerge runs the kit's commands.after_merge.cmd in dir (the main checkout the merge landed in) with dir's fake
// go first on PATH; it returns the output.
func runAfterMerge(t *testing.T, dir string) string {
	t.Helper()
	env := append(plainMakeEnv(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command("bash", "-c", afterMergeCmd(t))
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commands.after_merge in %s: %v\n%s", dir, err, out)
	}
	return string(out)
}

// TestAfterMergeBuildsIntoRoot: the per-publish after_merge builds in the main checkout and leaves the binary serve
// follows at its dist/obor.
func TestAfterMergeBuildsIntoRoot(t *testing.T) {
	t.Parallel()
	root := testkit.Dir(t)
	outKit(t, root)
	runAfterMerge(t, root)
	if got := readFile(t, filepath.Join(root, "dist", "obor")); got != "binary\n" {
		t.Errorf("after_merge left %q at dist/obor, want the built binary", got)
	}
}
