//go:build integration

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

// The git hooks (githooks/*: bash, run by git through core.hooksPath; design "Git hooks layer") driven by real git
// commit / push / merge. The kit is a throwaway copy of this checkout's bin/, VERSION and githooks/ with a fresh build
// as dist/obor, so the hooks' `bin/obor whoami` runs this code.

// pushRefusal: the pre-push hook's refusal names its way out (T-793; T-1116: OBOR_ALLOW_PUSH is gone).
func pushRefusal(errOut string) bool {
	return strings.Contains(errOut, "rules.may_push")
}

// remote adds a bare origin and returns its path.
func (x *hx) remote() string {
	x.t.Helper()
	remote := filepath.Join(x.Base, "remote.git")
	x.Git(x.Base, "init", "-q", "--bare", remote)
	x.Git(x.Root, "remote", "add", "origin", remote)
	return remote
}

// hx is a project (prefix pj, lane L-01 on task/T-05) with the hooks of kit installed by `obor setup githooks`, and a
// stub binary (refuse); a case sets its own with binary.
type hx struct {
	*fx
	t    testing.TB
	lane string
	kit  string
}

// refuse is the default stub dist/obor: whoami answers like a worker in a lane (a lane commit refuses with the lane
// line) and whoami --can-push refuses (a push refuses). The real binary's answers are cmd/obor's TestWhoamiCanPush.
const refuse = "#!/bin/bash\n[ \"$2\" = --can-push ] && exit 1\necho 'role: worker'\necho 'source: cwd'\n"

func newHx(t *testing.T) *hx {
	t.Helper()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-05")
	p.Config()
	h := herdrtest.New()
	x := &hx{fx: &fx{Project: p, h: h, srv: herdrtest.Serve(t, h)}, t: t, lane: lane, kit: newKit(t, "")}
	x.fx.kit = x.kit
	x.binary(refuse)
	x.ok(t, "githooks")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != x.hooks() {
		t.Fatalf("precondition: hooks installed: %q", got)
	}
	return x
}

// newKit is a kit in a temp dir: bin/*, VERSION and the two githooks sets of this checkout, bin as dist/obor ("":
// none).
func newKit(t *testing.T, bin string) string {
	t.Helper()
	k := testkit.Dir(t)
	for _, glob := range []string{"bin/*", "VERSION", "githooks/*", "githooks/chain/*"} {
		files, _ := filepath.Glob(filepath.Join(kit, glob))
		if len(files) == 0 {
			t.Fatalf("precondition: %s in %s", glob, kit)
		}
		for _, f := range files {
			if fi, err := os.Stat(f); err != nil || fi.IsDir() {
				continue
			}
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

// hooks is the kit's small githooks set (pre-commit, pre-push and the runner); the project's own hooksPath is a dir of
// the main checkout, recorded for it.
func (x *hx) hooks() string { return filepath.Join(x.kit, "githooks") }

// chainDir is the kit's full hook set: what core.hooksPath points at when there is something to chain (T-623).
func (x *hx) chainDir() string { return filepath.Join(x.kit, "githooks", "chain") }

// chain makes dir (relative to the main checkout) hold hook with script, executable, and records it as obor.hooksChain.
func (x *hx) chain(dir, hook, script string) {
	x.t.Helper()
	os.MkdirAll(filepath.Join(x.Root, dir), 0o755)
	os.Chmod(x.Write(filepath.Join(dir, hook), script), 0o755)
	x.Git(x.Root, "config", "obor.hooksChain", dir)
}

// gitEnv is the environment of a git command: HOME under Base, the fake herdr first on PATH, plus kv ("VAR=value").
func (x *hx) gitEnv(kv ...string) []string {
	path := filepath.Join(x.Base, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	return append(append(os.Environ(), "HOME="+filepath.Join(x.Base, "home"), "PATH="+path,
		"OBOR_PRE_COMMIT_WHOAMI_TIMEOUT=30"), kv...)
}

// envWith is env with kv set, replacing a key it already has: a duplicate reaches the hook and getenv reads the first,
// so the last value would otherwise not win.
func envWith(env []string, kv ...string) []string {
	for _, e := range kv {
		k, _, _ := strings.Cut(e, "=")
		var out []string
		for _, old := range env {
			if !strings.HasPrefix(old, k+"=") {
				out = append(out, old)
			}
		}
		env = append(out, e)
	}
	return env
}

// chainHook runs the chain set's hook name directly with dir as git's cwd (the file git runs when core.hooksPath points
// at the chain set, for a hook whose result no git command here can exercise): its exit code.
func (x *hx) chainHook(env []string, dir, name string) int {
	x.t.Helper()
	cmd := exec.Command(filepath.Join(x.chainDir(), name))
	cmd.Dir, cmd.Env = dir, env
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		x.t.Fatalf("hook %s: %v", name, err)
	}
	return 0
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
	return envWith(append(os.Environ(), "HOME="+home, "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH")),
		"OBOR_PRE_COMMIT_WHOAMI_TIMEOUT=30"), curlLog
}

// binary makes script the kit's dist/obor.
func (x *hx) binary(script string) {
	path := filepath.Join(x.kit, "dist", "obor")
	os.Remove(path)
	x.Write(path, script)
	os.Chmod(path, 0o755)
}

// bash tests/githooks.test.sh case_setup_githooks_sets_refuses_removes is
// TestGithooksSetsRefusesRemoves. The binary is a stub (hx.binary) at each case, never a build: the real binary's
// whoami --can-push answers are cmd/obor's TestWhoamiCanPush.
func TestGithooks(t *testing.T) {
	t.Parallel()
	const lane = "obor: no commits in a lane"
	const unknown = "obor: cannot tell whether this is a lane"
	for _, c := range []struct {
		name string
		test func(t *testing.T, x *hx)
	}{
		// T-1116: the push block is for agents. A shell with no OBOR_ROLE (the owner's) pushes, even though the stub
		// binary refuses --can-push: the hook answers this one itself, without asking (and so without a build).
		{"pre-push: no OBOR_ROLE (a person's shell) pushes; a role the binary refuses is refused", func(t *testing.T, x *hx) {
			remote := x.remote()
			rc, errOut := x.git(x.gitEnv("OBOR_ROLE=reviewer"), x.Root, "push", "-q", "origin", "main")
			if rc != 1 || !pushRefusal(errOut) {
				t.Errorf("a reviewer's push: exit %d\n%s", rc, errOut)
			}
			if got := x.Git(remote, "branch", "--list", "main"); got != "" {
				t.Errorf("the refused push reached the remote: %q", got)
			}
			if rc, errOut := x.git(x.gitEnv(), x.Root, "push", "-q", "origin", "main"); rc != 0 {
				t.Errorf("no OBOR_ROLE: exit %d\n%s", rc, errOut)
			}
			if got, want := x.Git(remote, "rev-parse", "main"), x.head(x.Root); got != want {
				t.Errorf("the remote's main is %s, want %s", got, want)
			}
		}},
		// T-793: the hook asks the binary (whoami --can-push) and obeys its answer for a role: a listed role passes, a
		// role not listed and a lane-bound one refuse. T-1116: no OBOR_ROLE is not asked (the stub would refuse it).
		// The decision itself (rules.may_push, the lane row) is the real binary's, in cmd/obor's TestWhoamiCanPush.
		{"pre-push obeys whoami --can-push: a listed role passes, a role not listed and a lane-bound one refuse", func(t *testing.T, x *hx) {
			x.Config(func(c map[string]any) { c["rules"] = map[string]any{"may_push": []string{"lead", "worker"}} })
			x.binary("#!/bin/bash\n[ \"$OBOR_ROLE\" = lead ] && exit 0\nexit 1\n")
			remote := x.remote()
			pushes := func(dir, branch string, env []string, rc int, what string) {
				t.Helper()
				got, errOut := x.git(env, dir, "push", "-q", "origin", branch)
				if got != rc {
					t.Errorf("%s: exit %d, want %d\n%s", what, got, rc, errOut)
				}
				if (rc != 0) != pushRefusal(errOut) {
					t.Errorf("%s: refusal message %v, want %v\n%s", what, pushRefusal(errOut), rc != 0, errOut)
				}
				if has := x.Git(remote, "branch", "--list", branch) != ""; has != (rc == 0) {
					t.Errorf("%s: pushed %v, want %v", what, has, rc == 0)
				}
			}
			pushes(x.Root, "main", x.gitEnv("OBOR_ROLE=reviewer"), 1, "role not listed")
			x.Git(x.lane, "branch", "-f", "lanebranch", "HEAD")
			pushes(x.lane, "lanebranch", x.gitEnv("OBOR_ROLE=worker"), 1, "lane-bound role (the binary answers no)")
			pushes(x.Root, "main", x.gitEnv("OBOR_ROLE=lead"), 0, "lead listed in rules.may_push")
			x.Git(x.Root, "branch", "-f", "ownerbranch", "HEAD")
			pushes(x.Root, "ownerbranch", x.gitEnv(), 0, "no OBOR_ROLE (the binary would refuse it)")
		}},
		{"pre-push refuses without a usable binary (none, stale, hanging)", func(t *testing.T, x *hx) {
			x.Config(func(c map[string]any) { c["rules"] = map[string]any{"may_push": []string{"lead"}} })
			remote := x.remote()
			env, _ := x.cold(t)
			env = append(env, "OBOR_ROLE=lead")
			refused := func(env []string, what string) {
				t.Helper()
				rc, errOut := x.git(env, x.Root, "push", "-q", "origin", "main")
				if rc != 1 || !pushRefusal(errOut) {
					t.Errorf("%s: exit %d\n%s", what, rc, errOut)
				}
				if got := x.Git(remote, "branch", "--list", "main"); got != "" {
					t.Errorf("%s: nothing pushed: %q", what, got)
				}
			}
			refused(env, "no binary")
			x.binary("#!/bin/bash\necho usage >&2\nexit 2\n")
			refused(env, "stale binary (no --can-push)")
			x.binary("#!/bin/bash\nexec sleep 30\n")
			start := time.Now()
			refused(envWith(env, "OBOR_PRE_COMMIT_WHOAMI_TIMEOUT=1"), "hanging binary")
			if d := time.Since(start); d > hangBound {
				t.Errorf("hanging binary: push returned after %v, want within the watchdog bound", d)
			}
			// Precondition for the pass above: the same stub that answers 0 lets the push through.
			x.binary("#!/bin/bash\nexit 0\n")
			if rc, errOut := x.git(env, x.Root, "push", "-q", "origin", "main"); rc != 0 {
				t.Errorf("binary answering yes: exit %d\n%s", rc, errOut)
			}
		}},
		// T-1116 replaced OBOR_ALLOW_PUSH=1: a person's shell (no OBOR_ROLE) pushes with no binary at all, since the
		// hook needs no classifier for it; a role with no binary still fails closed (the case above).
		{"pre-push: no OBOR_ROLE passes without a binary", func(t *testing.T, x *hx) {
			remote := x.remote()
			env, _ := x.cold(t)
			if rc, errOut := x.git(env, x.Root, "push", "-q", "origin", "main"); rc != 0 {
				t.Errorf("no OBOR_ROLE without a binary: exit %d\n%s", rc, errOut)
			}
			if got, want := x.Git(remote, "rev-parse", "main"), x.head(x.Root); got != want {
				t.Errorf("the remote's main is %s, want %s", got, want)
			}
		}},
		// T-709: a stopped project's git hooks stand down (obor stop wrote .obor/state/stopped): a lane commit and a
		// push go through, the project's own hooks still chained.
		{"a stopped project's hooks only chain", func(t *testing.T, x *hx) {
			x.commits(t, x.lane, x.gitEnv(), 1, lane, "precondition: lane commit refused while running")
			x.Write(".obor/state/stopped", "at: now\nwhy: test\n")
			x.commits(t, x.lane, x.gitEnv(), 0, "", "lane commit in a stopped project")
			remote := filepath.Join(x.Base, "remote.git")
			x.Git(x.Base, "init", "-q", "--bare", remote)
			x.Git(x.Root, "remote", "add", "origin", remote)
			if rc, errOut := x.git(x.gitEnv(), x.Root, "push", "-q", "origin", "main"); rc != 0 {
				t.Errorf("push in a stopped project: exit %d\n%s", rc, errOut)
			}
		}},
		// bash case_pre_commit_refuses_in_lane_allows_with_obor_merge_and_in_main
		{"pre-commit refuses in a lane, allows with OBOR_MERGE=1 and in main", func(t *testing.T, x *hx) {
			x.commits(t, x.lane, x.gitEnv(), 1, lane, "lane commit")
			x.commits(t, x.lane, x.gitEnv("OBOR_MERGE=1"), 0, "", "lane commit with OBOR_MERGE=1")
			x.commits(t, x.Root, x.gitEnv(), 0, "", "main commit")
		}},
		// T-762: the owner's drive agent (role driver) commits on its branch in its lane; a worker and any other role
		// stay refused, and the main checkout and OBOR_MERGE=1 are as before. The classifier is a stub here; whoami
		// derives driver/worker from OBOR_ROLE and the cwd in cmd/obor's tests.
		{"pre-commit allows a drive agent's commit in its lane, refuses a worker's", func(t *testing.T, x *hx) {
			x.binary("#!/bin/bash\ncase \"${OBOR_ROLE:-}\" in\n" +
				"  driver) echo 'role: driver'; echo 'source: env+cwd';;\n" +
				"  *) echo 'role: worker'; echo 'source: cwd';;\n" +
				"esac\n")
			x.commits(t, x.lane, x.gitEnv("OBOR_ROLE=driver"), 0, "", "drive agent commit in its lane")
			x.commits(t, x.lane, x.gitEnv("OBOR_ROLE=worker"), 1, lane, "worker commit in its lane")
			x.commits(t, x.lane, x.gitEnv(), 1, lane, "no OBOR_ROLE in a lane (cwd: worker)")
			x.commits(t, x.Root, x.gitEnv("OBOR_ROLE=driver"), 0, "", "main commit with OBOR_ROLE=driver")
			x.commits(t, x.lane, x.gitEnv("OBOR_ROLE=driver", "OBOR_MERGE=1"), 0, "", "drive agent commit with OBOR_MERGE=1")
		}},
		// T-762: only the role whoami names is allowed: an unknown role in a lane, or a whoami without one, refuses.
		{"pre-commit refuses an unknown role in a lane", func(t *testing.T, x *hx) {
			env, _ := x.cold(t)
			x.binary("#!/bin/bash\necho 'role: bogus'\necho 'source: env+cwd'\n")
			x.commits(t, x.lane, env, 1, lane, "unknown role in a lane")
			x.binary("#!/bin/bash\necho 'role: driver'\n")
			x.commits(t, x.lane, env, 1, unknown, "a driver line without a source line")
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
			rc, errOut := x.commit(x.lane, envWith(env, "OBOR_PRE_COMMIT_WHOAMI_TIMEOUT=1")...)
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
		// bash case_pre_commit_without_a_binary_allows_main_and_obor_merge
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
		{"no .obor/config.json: hooks chain only", func(t *testing.T, x *hx) {
			x.Git(x.Root, "rm", "-q", ".obor/config.json")
			x.Git(x.Root, "commit", "-q", "-m", "no config")
			if x.Exists(".obor/config.json") {
				t.Fatal("precondition: no config")
			}
			calls := filepath.Join(x.Base, "own.calls")
			x.chain(".githooks", "pre-commit", fmt.Sprintf("#!/bin/sh\necho ran >> %q\n", calls))
			x.commits(t, x.lane, x.gitEnv(), 0, "", "lane commit without a config")
			if got := x.Read(calls); got != "ran\n" {
				t.Errorf("a lane commit without a config chains the project's pre-commit: %q", got)
			}
		}},
		// T-620: the project's own hooksPath (recorded by `obor setup githooks`) is chained by every hook, and a
		// chained failure is the hook's result. The lane runs it from the main checkout, not from the lane.
		{"a chained pre-commit's failure blocks the commit, before obor's rule", func(t *testing.T, x *hx) {
			calls := filepath.Join(x.Base, "own.calls")
			own := x.Write(".githooks/pre-commit", fmt.Sprintf("#!/bin/sh\necho chained >> %q\nexit 3\n", calls))
			os.Chmod(own, 0o755)
			dotgit := x.Write(".git/hooks/pre-commit", fmt.Sprintf("#!/bin/sh\necho dotgit >> %q\n", calls))
			os.Chmod(dotgit, 0o755)
			x.Git(x.Root, "config", "core.hooksPath", ".githooks")
			has(t, x.ok(t, "githooks"), "chaining .githooks", "setup records the project's own hooksPath")
			before := x.head(x.lane)
			rc, errOut := x.commit(x.lane, x.gitEnv("OBOR_MERGE=1")...)
			if rc == 0 {
				t.Errorf("a failing chained pre-commit blocks the commit (OBOR_MERGE=1 skips only obor's rule)\n%s", errOut)
			}
			if x.head(x.lane) != before {
				t.Error("HEAD must not move")
			}
			if got := x.Read(calls); got != "chained\n" {
				t.Errorf("the chained hook runs once, .git/hooks not at all: %q", got)
			}
		}},
		// T-620: git aborts `git gc --auto` on a non-zero pre-auto-gc ("Exiting with non-zero status from this script
		// causes the git gc --auto to abort"), so a project's vote must reach it through the chain.
		{"a chained pre-auto-gc's veto is the hook's result", func(t *testing.T, x *hx) {
			calls := filepath.Join(x.Base, "own.calls")
			x.chain(".githooks", "pre-auto-gc", fmt.Sprintf("#!/bin/sh\necho ran >> %q\nexit 1\n", calls))
			if rc := x.chainHook(x.gitEnv(), x.Root, "pre-auto-gc"); rc != 1 {
				t.Errorf("a chained veto: exit %d, want 1", rc)
			}
			if got := x.Read(calls); got != "ran\n" {
				t.Errorf("the chained pre-auto-gc ran once: %q", got)
			}
			x.chain(".githooks", "pre-auto-gc", fmt.Sprintf("#!/bin/sh\necho again >> %q\n", calls))
			if rc := x.chainHook(x.gitEnv(), x.Root, "pre-auto-gc"); rc != 0 {
				t.Errorf("a passing chained pre-auto-gc: exit %d, want 0", rc)
			}
			if got := x.Read(calls); got != "ran\nagain\n" {
				t.Errorf("the chained pre-auto-gc ran again: %q", got)
			}
		}},
		{"a chained post-merge runs; its failure is not the merge's", func(t *testing.T, x *hx) {
			calls := filepath.Join(x.Base, "own.calls")
			x.chain(".githooks", "post-merge", fmt.Sprintf("#!/bin/sh\necho post-merge >> %q\nexit 3\n", calls))
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+x.chainDir(), "a chain moves hooksPath to the full set")
			x.Git(x.Root, "checkout", "-q", "-b", "side")
			x.Write(filepath.Join(x.Root, "side.txt"), "side\n")
			x.Git(x.Root, "add", "side.txt")
			x.Git(x.Root, "commit", "-q", "-m", "side")
			x.Git(x.Root, "checkout", "-q", "main")
			if rc, errOut := x.git(x.gitEnv(), x.Root, "merge", "--no-ff", "-m", "merge side", "side"); rc != 0 {
				t.Fatalf("a chained post-merge's exit does not fail the merge: exit %d\n%s", rc, errOut)
			}
			if got := x.Read(calls); got != "post-merge\n" {
				t.Errorf("the chained post-merge ran once: %q", got)
			}
		}},
		{"pre-push stdin reaches the chained hook", func(t *testing.T, x *hx) {
			remote := filepath.Join(x.Base, "remote.git")
			x.Git(x.Base, "init", "-q", "--bare", remote)
			x.Git(x.Root, "remote", "add", "origin", remote)
			in := filepath.Join(x.Base, "chain-stdin")
			x.chain(".githooks", "pre-push", fmt.Sprintf("#!/bin/sh\ncat > %q\n", in))
			if rc, errOut := x.git(x.gitEnv(), x.Root, "push", "-q", "origin", "main"); rc != 0 {
				t.Fatalf("push from a person's shell: exit %d\n%s", rc, errOut)
			}
			if got := x.Read(in); !strings.Contains(got, "refs/heads/main") {
				t.Errorf("stdin reached the chained pre-push: %q", got)
			}
			if x.Git(remote, "rev-parse", "main") != x.head(x.Root) {
				t.Error("pushed")
			}
		}},
		// bash case_pre_push_refuses_after_a_passing_chain
		{"obor's pre-push rule still refuses after a passing chained hook", func(t *testing.T, x *hx) {
			remote := filepath.Join(x.Base, "remote.git")
			x.Git(x.Base, "init", "-q", "--bare", remote)
			x.Git(x.Root, "remote", "add", "origin", remote)
			calls := filepath.Join(x.Base, "own.calls")
			x.chain(".githooks", "pre-push", fmt.Sprintf("#!/bin/sh\necho passed >> %q\n", calls))
			rc, errOut := x.git(x.gitEnv("OBOR_ROLE=reviewer"), x.Root, "push", "-q", "origin", "main")
			if rc != 1 || !pushRefusal(errOut) {
				t.Errorf("push refused after the chain passed: exit %d\n%s", rc, errOut)
			}
			if got := x.Read(calls); got != "passed\n" {
				t.Errorf("the chained hook ran first: %q", got)
			}
			if got := x.Git(remote, "branch", "--list", "main"); got != "" {
				t.Errorf("nothing pushed: %q", got)
			}
		}},
		// bash case_no_chain_no_dotgit_hooks_only_obors_rule
		{"no chain and no .git/hooks: only obor's rule", func(t *testing.T, x *hx) {
			if got, _ := gitIn(x.Root, "config", "--get", "obor.hooksChain"); got != "" {
				t.Fatalf("precondition: no chain: %q", got)
			}
			if _, err := os.Stat(filepath.Join(x.Root, ".git/hooks/pre-commit")); err == nil {
				t.Fatal("precondition: no .git/hooks/pre-commit")
			}
			x.commits(t, x.lane, x.gitEnv(), 1, lane, "lane commit")
			x.commits(t, x.Root, x.gitEnv(), 0, "", "main commit")
		}},
		// T-623: the small set ships pre-commit and pre-push; every other hook git runs lives in chain/, which git
		// runs only when core.hooksPath points there. Each chain file sources ../_hook (one runner, one level up).
		{"pre-commit and pre-push in githooks/, every hook in githooks/chain/", func(t *testing.T, x *hx) {
			entries, _ := os.ReadDir(filepath.Join(kit, "githooks"))
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
				if fi, err := e.Info(); err == nil && !e.IsDir() && fi.Mode()&0o111 == 0 {
					t.Errorf("%s is not executable", e.Name())
				}
			}
			if got := strings.Join(names, " "); got != "_hook chain pre-commit pre-push" {
				t.Errorf("githooks/:\n got %q\nwant %q", got, "_hook chain pre-commit pre-push")
			}
			entries, _ = os.ReadDir(filepath.Join(kit, "githooks", "chain"))
			names = nil
			for _, e := range entries {
				names = append(names, e.Name())
				if fi, err := e.Info(); err == nil && fi.Mode()&0o111 == 0 {
					t.Errorf("chain/%s is not executable", e.Name())
				}
				if b, err := os.ReadFile(filepath.Join(kit, "githooks", "chain", e.Name())); err != nil ||
					!strings.Contains(string(b), `. "$(dirname "$0")/../_hook"`) {
					t.Errorf("chain/%s does not source ../_hook", e.Name())
				}
			}
			want := "applypatch-msg commit-msg post-applypatch post-checkout post-commit post-index-change " +
				"post-merge post-rewrite pre-applypatch pre-auto-gc pre-commit pre-merge-commit pre-push pre-rebase " +
				"prepare-commit-msg push-to-checkout reference-transaction"
			if got := strings.Join(names, " "); got != want {
				t.Errorf("chain/:\n got %q\nwant %q", got, want)
			}
		}},
		// T-623 #5: with a chain recorded, a hook the chain lacks runs nothing: .git/hooks is not a fallback (git
		// never runs it once core.hooksPath is set), so a failing .git/hooks/pre-commit cannot block the commit.
		{"with a chain, a hook it lacks runs nothing (.git/hooks is no fallback)", func(t *testing.T, x *hx) {
			calls := filepath.Join(x.Base, "dotgit.calls")
			own := x.Write(".git/hooks/pre-commit", fmt.Sprintf("#!/bin/sh\necho dotgit >> %q\nexit 3\n", calls))
			os.Chmod(own, 0o755)
			x.Git(x.Root, "config", "obor.hooksChain", ".githooks")
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+x.chainDir(), "a chain moves hooksPath to the full set")
			x.commits(t, x.Root, x.gitEnv(), 0, "", "main commit with a chain that lacks pre-commit")
			if _, err := os.Stat(calls); err == nil {
				t.Error(".git/hooks/pre-commit ran: a recorded chain is the only project hook")
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.test(t, newHx(t))
		})
	}
}

// tests/githooks.test.sh case_setup_githooks_sets_chains_removes (T-620)
func TestGithooksSetsChainsRemoves(t *testing.T) {
	t.Parallel()
	x := newGitFx(t)
	hooks := filepath.Join(kit, "githooks")
	chain := filepath.Join(hooks, "chain")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+hooks+"\n" {
		t.Errorf("set: %q", out)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != hooks {
		t.Errorf("hooksPath %q", got)
	}
	if got, err := gitIn(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != "" || err == nil {
		t.Errorf("no chain for a repo without its own hooks: %q (%v)", got, err)
	}
	has(t, x.ok(t, "githooks"), "OK githooks: core.hooksPath already", "rerun")
	x.ok(t, "githooks", "--remove")
	if got, _ := gitIn(x.Root, "config", "--local", "--get", "core.hooksPath"); got != "" {
		t.Errorf("removed: %q", got)
	}
	// The project's own hooksPath is chained, not refused (T-620): recorded, then obor's full set (T-623).
	x.Git(x.Root, "config", "core.hooksPath", ".githooks")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+chain+", chaining .githooks\n" {
		t.Errorf("own hooksPath: %q", out)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != ".githooks" {
		t.Errorf("obor.hooksChain %q", got)
	}
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != chain {
		t.Errorf("chained hooksPath %q", got)
	}
	has(t, x.ok(t, "githooks"), "OK githooks: core.hooksPath already", "rerun chained")
	x.ok(t, "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != ".githooks" {
		t.Errorf("restored hooksPath %q", got)
	}
	if got, err := gitIn(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != "" || err == nil {
		t.Errorf("chain unset: %q (%v)", got, err)
	}
	// An absolute chainsPath is recorded as it is.
	x.Git(x.Root, "config", "core.hooksPath", "/elsewhere")
	if out := x.ok(t, "githooks"); out != "OK githooks: core.hooksPath = "+chain+", chaining /elsewhere\n" {
		t.Errorf("absolute hooksPath: %q", out)
	}
	x.ok(t, "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != "/elsewhere" {
		t.Errorf("absolute restored: %q", got)
	}
	// Not obor's: --remove leaves it alone.
	x.failing(t, "FAIL core.hooksPath is /elsewhere (not obor's "+hooks+"): left alone", "githooks", "--remove")
	if got := x.Git(x.Root, "config", "--get", "core.hooksPath"); got != "/elsewhere" {
		t.Errorf("someone else's hooksPath kept: %q", got)
	}
}

// T-623: core.hooksPath is githooks/ (pre-commit, pre-push and the runner) unless there is something to chain: a
// recorded obor.hooksChain, or a hook in .git/hooks the small set lacks. Then it is githooks/chain/ (every hook git
// runs). A rerun moves between the two as that changes, and --remove works from either.
// Slow: seven cases, each three obor setup githooks runs whose work is git config (the code under test runs git: no
// fake), ≈1.8 s together at 2 cores (T-1009 measured).
func TestGithooksPicksTheHooksDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(kit, "githooks")
	chain := filepath.Join(dir, "chain")
	hook := func(x *fx, name string, mode os.FileMode) {
		os.MkdirAll(filepath.Join(x.Root, ".git", "hooks"), 0o755)
		os.Chmod(x.Write(filepath.Join(".git", "hooks", name), "#!/bin/sh\n"), mode)
	}
	for _, c := range []struct {
		name    string
		setup   func(t *testing.T, x *fx)
		want    string
		removed string // core.hooksPath after --remove ("" unset)
	}{
		{"no chain, no .git/hooks hook: githooks/", func(*testing.T, *fx) {}, dir, ""},
		{"a chain recorded: chain/", func(t *testing.T, x *fx) {
			x.Git(x.Root, "config", "obor.hooksChain", ".githooks")
		}, chain, ".githooks"},
		{"a .git/hooks/post-merge: chain/", func(t *testing.T, x *fx) { hook(x, "post-merge", 0o755) }, chain, ""},
		{"only .git/hooks/pre-commit: githooks/", func(t *testing.T, x *fx) { hook(x, "pre-commit", 0o755) }, dir, ""},
		{"a .git/hooks/post-merge.sample: githooks/", func(t *testing.T, x *fx) {
			hook(x, "post-merge.sample", 0o755)
		}, dir, ""},
		{"a non-executable .git/hooks/post-merge: githooks/", func(t *testing.T, x *fx) {
			hook(x, "post-merge", 0o644)
		}, dir, ""},
		{"the chain removed: back to githooks/", func(t *testing.T, x *fx) {
			x.Git(x.Root, "config", "core.hooksPath", ".githooks")
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+chain, "a chain moves to the full set")
			x.Git(x.Root, "config", "--local", "--unset", "obor.hooksChain")
		}, dir, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newGitFx(t)
			c.setup(t, x)
			has(t, x.ok(t, "githooks"), "core.hooksPath = "+c.want, "hooksPath")
			if got := x.Git(x.Root, "config", "--local", "--get", "core.hooksPath"); got != c.want {
				t.Errorf("core.hooksPath = %q, want %q", got, c.want)
			}
			has(t, x.ok(t, "githooks"), "already "+c.want, "rerun")
			x.ok(t, "githooks", "--remove")
			if got, _ := gitIn(x.Root, "config", "--local", "--get", "core.hooksPath"); got != c.removed {
				t.Errorf("--remove: core.hooksPath = %q, want %q", got, c.removed)
			}
		})
	}
}

// A githooks path of another obor kit (a checkout whose plugin.json names obor, or one that is gone) is re-pointed at
// this kit's; anyone else's hooks dir is the project's own: chained, not refused (T-620), so obor's full set runs
// (T-623).
func TestGithooksRepointsAnotherOborKitsHooksAndChainsOthers(t *testing.T) {
	x := newGitFx(t)
	mine := filepath.Join(kit, "githooks")
	chain := filepath.Join(mine, "chain")
	// an obor kit's githooks (its plugin.json names obor): re-pointed
	old := filepath.Join(x.Base, "obor-kit-stable")
	x.Write(old+"/.claude-plugin/plugin.json", `{"name": "obor"}`)
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks")
	out := x.ok(t, "githooks")
	has(t, out, "core.hooksPath = "+mine, "output")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("core.hooksPath = %s, want %s", got, mine)
	}
	// an obor kit's chain set is re-pointed too (T-623)
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks/chain")
	has(t, x.ok(t, "githooks"), "core.hooksPath = "+mine, "a kit's chain set re-pointed")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("a kit's chain set: core.hooksPath = %s, want %s", got, mine)
	}
	// a kit that is gone, named like one: re-pointed
	x.Git(x.Root, "config", "--local", "core.hooksPath", "/nowhere/obor-kit/githooks")
	x.ok(t, "githooks")
	// anyone else's hooks dir is the project's own: chained, not refused
	for _, other := range []string{"/nowhere/husky/githooks", "/usr/local/hooks", x.Base + "/githooks"} {
		x.Git(x.Root, "config", "--local", "core.hooksPath", other)
		has(t, x.ok(t, "githooks"), "chaining "+other, "chained hooks "+other)
		if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != other {
			t.Errorf("obor.hooksChain = %s, want %s", got, other)
		}
		if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != chain {
			t.Errorf("core.hooksPath %s changed to %s, want %s", other, got, chain)
		}
	}
}
