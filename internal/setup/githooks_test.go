package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// hangBound is headroom for a loaded merge machine; only the injected watchdog bound is timed below.
const hangBound = 15 * time.Second

// The git hooks (githooks/pre-commit, githooks/pre-push: bash, run by git through core.hooksPath; design "Git hooks
// layer (v2.3)") driven by real git commit / git push. The kit is a throwaway copy of this checkout's bin/, VERSION
// and githooks/ with a fresh build as dist/obor, so the hooks' `bin/obor whoami` runs this code.

// hx is a project (prefix pj, lane O-01 on task/T-05) with the hooks of kit installed by `obor setup githooks`.
type hx struct {
	*fx
	t    testing.TB
	lane string
	kit  string
}

func newHx(t *testing.T, bin string) *hx {
	t.Helper()
	p := testkit.New(t)
	lane := p.Lane("O-01", "task/T-05")
	p.Config()
	h := herdrtest.New()
	x := &hx{fx: &fx{Project: p, h: h, srv: herdrtest.Serve(t, h)}, t: t, lane: lane, kit: newKit(t, bin)}
	x.fx.kit = x.kit
	x.ok(t, "githooks")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != x.hooks() {
		t.Fatalf("precondition: hooks installed: %q", got)
	}
	return x
}

// newKit is a kit in a temp dir: bin/*, VERSION and githooks/* of this checkout, bin as dist/obor ("": none).
func newKit(t *testing.T, bin string) string {
	t.Helper()
	k := testkit.Dir(t)
	for _, glob := range []string{"bin/*", "VERSION", "githooks/*"} {
		files, _ := filepath.Glob(filepath.Join(kit, glob))
		if len(files) == 0 {
			t.Fatalf("precondition: %s in %s", glob, kit)
		}
		for _, f := range files {
			rel, _ := filepath.Rel(kit, f)
			copyFile(t, f, filepath.Join(k, rel))
		}
	}
	os.MkdirAll(filepath.Join(k, "dist"), 0o755)
	if bin != "" {
		if err := os.Symlink(bin, filepath.Join(k, "dist", "obor")); err != nil {
			t.Fatal(err)
		}
	}
	return k
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(from)
	os.MkdirAll(filepath.Dir(to), 0o755)
	if err := os.WriteFile(to, b, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func (x *hx) hooks() string { return filepath.Join(x.kit, "githooks") }

// gitEnv is the environment of a git command: HOME under Base, the fake herdr first on PATH, plus kv ("VAR=value").
func (x *hx) gitEnv(kv ...string) []string {
	path := filepath.Join(x.Base, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	return append(append(os.Environ(), "HOME="+filepath.Join(x.Base, "home"), "PATH="+path,
		"OBOR_PRE_COMMIT_WHOAMI_TIMEOUT=30"), kv...)
}

// git runs git -C dir args… with env (fixture identity): its exit code and stderr.
func (x *hx) git(env []string, dir string, args ...string) (int, string) {
	x.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=obor test",
		"-c", "user.email=obor-test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = env
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), errOut.String()
	} else if err != nil {
		x.t.Fatalf("git %v: %v", args, err)
	}
	return 0, errOut.String()
}

// commit changes file.txt in dir and commits it: git's exit code and stderr.
func (x *hx) commit(dir string, env ...string) (int, string) {
	x.t.Helper()
	x.Write(filepath.Join(dir, "file.txt"), x.Read(filepath.Join(dir, "file.txt"))+"x\n")
	x.Git(dir, "add", "file.txt")
	return x.git(env, dir, "commit", "-q", "-m", "change in "+filepath.Base(dir))
}

func (x *hx) head(dir string) string { return x.Git(dir, "rev-parse", "HEAD") }

// commits wants commit in dir with env to exit rc (and stderr to hold want when not ""), HEAD moved iff rc is 0.
func (x *hx) commits(t *testing.T, dir string, env []string, rc int, want, what string) {
	t.Helper()
	before := x.head(dir)
	got, errOut := x.commit(dir, env...)
	if got != rc {
		t.Errorf("%s: exit %d, want %d\n%s", what, got, rc, errOut)
	}
	if want != "" && !strings.Contains(errOut, want) {
		t.Errorf("%s: no %q in stderr\n%s", what, want, errOut)
	}
	if moved := x.head(dir) != before; moved != (rc == 0) {
		t.Errorf("%s: HEAD moved %v, want %v", what, moved, rc == 0)
	}
}

// cold is kit_without_binary: a fresh kit without dist/obor (stub: dist/obor is that script) as core.hooksPath, a curl
// stub first on PATH that logs a call; returns the environment of a cold commit (no OBOR_*, empty HOME) and the log.
func (x *hx) cold(t *testing.T) (env []string, curlLog string) {
	t.Helper()
	x.kit = newKit(t, "")
	x.Git(x.Root, "config", "core.hooksPath", x.hooks())
	stubs := filepath.Join(x.Base, "stubs")
	curlLog = filepath.Join(x.Base, "curl.log")
	x.Write(filepath.Join(stubs, "curl"), fmt.Sprintf("#!/bin/bash\necho curl-called >> %q\nexit 22\n", curlLog))
	os.Chmod(filepath.Join(stubs, "curl"), 0o755)
	home := filepath.Join(x.Base, "cold-home")
	os.MkdirAll(home, 0o755)
	return append(os.Environ(), "HOME="+home, "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH")), curlLog
}

// binary makes script the kit's dist/obor.
func (x *hx) binary(script string) {
	path := filepath.Join(x.kit, "dist", "obor")
	os.Remove(path)
	x.Write(path, script)
	os.Chmod(path, 0o755)
}

// bash tests/githooks.test.sh case_setup_githooks_sets_refuses_removes is
// TestGithooksSetsRefusesRemoves.
func TestGithooks(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(testkit.Dir(t), "obor")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/obor").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	const lane = "obor: no commits in a lane"
	const unknown = "obor: cannot tell whether this is a lane"
	for _, c := range []struct {
		name string
		test func(t *testing.T, x *hx)
	}{
		// bash case_pre_push_refuses_allow_push_allows
		{"pre-push refuses, OBOR_ALLOW_PUSH=1 allows", func(t *testing.T, x *hx) {
			remote := filepath.Join(x.Base, "remote.git")
			x.Git(x.Base, "init", "-q", "--bare", remote)
			x.Git(x.Root, "remote", "add", "origin", remote)
			rc, errOut := x.git(x.gitEnv(), x.Root, "push", "-q", "origin", "main")
			if rc != 1 || !strings.Contains(errOut, "obor: the user pushes; set OBOR_ALLOW_PUSH=1 to push by hand") {
				t.Errorf("push refused: exit %d\n%s", rc, errOut)
			}
			if got := x.Git(remote, "branch", "--list", "main"); got != "" {
				t.Errorf("nothing pushed: %q", got)
			}
			if rc, errOut := x.git(x.gitEnv("OBOR_ALLOW_PUSH=1"), x.Root, "push", "-q", "origin", "main"); rc != 0 {
				t.Errorf("OBOR_ALLOW_PUSH=1: exit %d\n%s", rc, errOut)
			}
			if x.Git(remote, "rev-parse", "main") != x.head(x.Root) {
				t.Error("pushed")
			}
		}},
		// bash case_pre_commit_refuses_in_lane_allows_with_herd_merge_and_in_main
		{"pre-commit refuses in a lane, allows with OBOR_MERGE=1 and in main", func(t *testing.T, x *hx) {
			x.commits(t, x.lane, x.gitEnv(), 1, lane, "lane commit")
			x.commits(t, x.lane, x.gitEnv("OBOR_MERGE=1"), 0, "", "lane commit with OBOR_MERGE=1")
			x.commits(t, x.Root, x.gitEnv(), 0, "", "main commit")
		}},
		// bash case_pre_commit_fails_closed_in_lane_without_a_usable_binary
		{"pre-commit fails closed in a lane without a usable binary (none, stale, hanging); never fetches", func(t *testing.T, x *hx) {
			env, curlLog := x.cold(t)
			if _, err := os.Stat(filepath.Join(x.kit, "dist", "obor")); err == nil {
				t.Fatal("precondition: no dist/obor")
			}
			x.commits(t, x.lane, env, 1, unknown, "no binary")
			x.commits(t, x.lane, env, 1, "make -C "+x.kit+" build", "no binary: the repair hint")
			if _, err := os.Stat(curlLog); err == nil {
				t.Error("the hook never fetches")
			}
			x.binary("#!/bin/bash\necho usage >&2\nexit 2\n")
			x.commits(t, x.lane, env, 1, unknown, "stale binary (no whoami)")
			// exec sleep: the binary's own pid is the one the watchdog kills, nothing is left behind.
			x.binary("#!/bin/bash\nexec sleep 30\n")
			before := x.head(x.lane)
			start := time.Now()
			rc, errOut := x.commit(x.lane, append(env, "OBOR_PRE_COMMIT_WHOAMI_TIMEOUT=1")...)
			if rc != 1 || !strings.Contains(errOut, unknown) {
				t.Errorf("hanging binary (refused by watchdog): exit %d\n%s", rc, errOut)
			}
			if strings.Contains(errOut, "setpgid") {
				t.Errorf("hanging binary: no job-control noise on git's stderr\n%s", errOut)
			}
			if d := time.Since(start); d > hangBound {
				t.Errorf("hanging binary: commit returned after %v, want within the watchdog bound", d)
			}
			if x.head(x.lane) != before {
				t.Error("hanging binary: HEAD must not move")
			}
		}},
		// bash case_pre_commit_allows_only_known_non_lane_sources
		{"pre-commit allows only the known non-lane sources (env, none); empty and unknown are refused in a lane", func(t *testing.T, x *hx) {
			env, _ := x.cold(t)
			for _, s := range []struct{ source, want string }{
				{"cwd", lane}, {"env+cwd", lane}, {"", unknown}, {"bogus", unknown}, {"env", ""}, {"none", ""},
			} {
				x.binary(fmt.Sprintf("#!/bin/bash\necho 'source: %s'\n", s.source))
				rc := 1
				if s.want == "" {
					rc = 0
				}
				x.commits(t, x.lane, env, rc, s.want, "source: "+s.source)
			}
		}},
		// bash case_pre_commit_without_a_binary_allows_main_and_herd_merge
		{"pre-commit without a binary still allows main and OBOR_MERGE=1", func(t *testing.T, x *hx) {
			env, _ := x.cold(t)
			x.commits(t, x.Root, env, 0, "", "main commit without a binary")
			x.commits(t, x.lane, append(env, "OBOR_MERGE=1"), 0, "", "OBOR_MERGE=1 lane commit without a binary")
		}},
		// bash case_pre_commit_chains_to_failing_project_hook_refused
		{"pre-commit chains to a failing project pre-commit: refused", func(t *testing.T, x *hx) {
			calls := filepath.Join(x.Base, "own-hook.calls")
			own := x.Write(".git/hooks/pre-commit", fmt.Sprintf("#!/bin/sh\necho ran >> %q\nexit 3\n", calls))
			os.Chmod(own, 0o755)
			x.commits(t, x.Root, x.gitEnv(), 1, "", "main commit")
			if got := x.Read(calls); got != "ran\n" {
				t.Errorf("project's own hook ran once: %q", got)
			}
		}},
		// bash case_no_config_is_a_noop
		{"no .obor/config.json: hooks are no-ops", func(t *testing.T, x *hx) {
			x.Git(x.Root, "rm", "-q", ".obor/config.json")
			x.Git(x.Root, "commit", "-q", "-m", "no config")
			if x.Exists(".obor/config.json") {
				t.Fatal("precondition: no config")
			}
			x.commits(t, x.lane, x.gitEnv(), 0, "", "lane commit without a config")
		}},
		// bash case_only_pre_commit_and_pre_push_ship
		{"only pre-commit and pre-push ship", func(t *testing.T, x *hx) {
			entries, _ := os.ReadDir(filepath.Join(kit, "githooks"))
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if got := strings.Join(names, " "); got != "pre-commit pre-push" {
				t.Errorf("only the two hooks (no _common.sh, post-merge, post-commit, _post.sh): %q", got)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.test(t, newHx(t, bin))
		})
	}
}
