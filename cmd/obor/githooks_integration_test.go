//go:build integration

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// kitWithHooks is a throwaway kit at dir: the checkout's githooks/ (both sets, executable), bin/obor and bin/go-commands,
// and dist/obor the binary this package builds (symlinked, as internal/setup's newKit does).
func kitWithHooks(t *testing.T, dir, bin string) {
	t.Helper()
	root := checkoutRoot(t)
	for _, glob := range []string{"githooks/*", "githooks/chain/*"} {
		files, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil || len(files) == 0 {
			t.Fatalf("precondition: %s in %s", glob, root)
		}
		for _, f := range files {
			fi, err := os.Stat(f)
			if err != nil || fi.IsDir() {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			writeExec(t, filepath.Join(dir, rel), readFile(t, f))
		}
	}
	for _, f := range []string{"bin/obor", "bin/go-commands"} {
		writeExec(t, filepath.Join(dir, f), readFile(t, filepath.Join(root, f)))
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, filepath.Join(dir, "dist", "obor")); err != nil {
		t.Fatal(err)
	}
}

// gitRun runs git -C dir args… with env added (fixture identity): exit code and stderr.
func gitRun(t *testing.T, dir string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=obor test",
		"-c", "user.email=obor-test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), errOut.String()
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, errOut.String()
}

// T-999: the githooks' contract is the real binary's, not the stubs' (internal/setup stubs whoami; TestWhoamiCanPush asks
// the classifier directly). This runs the kit's hooks over real git against the binary TestMain builds: a worker's lane
// commit is refused with the lane line, a push is refused for a role not in guards.allow_push and allowed for one in
// it.
func TestGithooksRealBinary(t *testing.T) {
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-1")
	p.Config(func(c map[string]any) { c["guards"] = map[string]any{"allow_push": []string{"lead"}} })
	kit := testkit.Dir(t)
	kitWithHooks(t, kit, build(t))
	p.Git(p.Root, "config", "--local", "core.hooksPath", filepath.Join(kit, "githooks"))
	env := []string{"HOME=" + filepath.Join(p.Base, "home"), "GIT_TERMINAL_PROMPT=0"}

	// A worker's commit in its lane: the hook asks the binary (whoami: role worker, source cwd) and refuses.
	p.Write(filepath.Join(lane, "feature.txt"), "feature\n")
	p.Git(lane, "add", "feature.txt")
	before := p.Git(lane, "rev-parse", "HEAD")
	if rc, errOut := gitRun(t, lane, env, "commit", "-q", "-m", "worker commit"); rc != 1 ||
		!strings.Contains(errOut, "obor: no commits in a lane") {
		t.Errorf("worker commit in a lane: exit %d, stderr %q; want 1 and the lane refusal", rc, errOut)
	}
	if after := p.Git(lane, "rev-parse", "HEAD"); after != before {
		t.Errorf("the refused commit moved the lane's HEAD %s -> %s", before, after)
	}

	// pre-push: a role is whoami --can-push's (MayPush); a person's shell (no OBOR_ROLE) passes without asking it
	// (T-1116). A reviewer (not in guards.allow_push) is refused and nothing reaches the remote; a shell with no role
	// and a lead (listed, not lane-bound) push.
	remote := filepath.Join(p.Base, "remote.git")
	p.Git(p.Base, "init", "-q", "--bare", remote)
	p.Git(p.Root, "remote", "add", "origin", remote)
	with := func(kv string) []string { return append(append([]string{}, env...), kv) }
	if rc, errOut := gitRun(t, p.Root, with("OBOR_ROLE=reviewer"), "push", "-q", "origin", "main"); rc != 1 ||
		!strings.Contains(errOut, "may not push") {
		t.Errorf("reviewer push: exit %d, stderr %q; want 1 and the push refusal", rc, errOut)
	}
	if got := p.Git(remote, "branch", "--list", "main"); got != "" {
		t.Errorf("the refused push reached the remote: %q", got)
	}
	if rc, errOut := gitRun(t, p.Root, env, "push", "-q", "origin", "main"); rc != 0 || errOut != "" {
		t.Errorf("a person's push (no OBOR_ROLE): exit %d, stderr %q; want 0", rc, errOut)
	}
	if got, want := p.Git(remote, "rev-parse", "main"), p.Git(p.Root, "rev-parse", "main"); got != want {
		t.Errorf("the remote's main %s, want the main checkout's %s", got, want)
	}
	p.Git(p.Root, "branch", "-f", "leadbranch", "HEAD")
	if rc, errOut := gitRun(t, p.Root, with("OBOR_ROLE=lead"), "push", "-q", "origin", "leadbranch"); rc != 0 || errOut != "" {
		t.Errorf("lead push: exit %d, stderr %q; want 0", rc, errOut)
	}
	if got, want := p.Git(remote, "rev-parse", "leadbranch"), p.Git(p.Root, "rev-parse", "leadbranch"); got != want {
		t.Errorf("the remote's leadbranch %s, want the main checkout's %s", got, want)
	}
}
