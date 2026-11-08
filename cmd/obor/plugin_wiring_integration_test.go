//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// bash plugin.test.sh case_pre_tool_use_missing_binary_denies: no Go binary (no dist/obor, nothing in ~/.obor/bin) in a
// obor project → every Bash / Edit / Write / NotebookEdit denied with the hint, except the repair command; other tools
// and a cwd outside an obor project allowed.
func TestPreToolUseMissingBinaryDenies(t *testing.T) {
	t.Parallel()
	k := newGitNoBinKit(t)
	if k.p.Exists("kit/dist/obor") || k.p.Exists(filepath.Join(k.home, ".obor/bin")) {
		t.Fatal("precondition: the kit has no dev binary, HOME no installed one")
	}
	k.deniesAll(t, "missing binary")
	other := testkit.Dir(t)
	k.p.Git(other, "-c", "init.defaultBranch=main", "init", "-q")
	read, _ := json.Marshal(map[string]any{"tool_name": "Read", "cwd": k.lane, "tool_input": map[string]any{"file_path": "x"}})
	for _, c := range []struct {
		name, event string
		rc          int
		denied      bool
	}{
		{"make -C <kit> build allowed", bashEvent(k.lane, "make -C "+k.dir+" build"), 0, false},
		{"make -C <kit> build, blanks around, allowed", bashEvent(k.lane, "  make -C "+k.dir+" build "), 0, false},
		{"precondition: the kit is in an obor project", bashEvent(k.dir, "ls"), 2, true},
		{"make build in the kit allowed", bashEvent(k.dir, "make build"), 0, false},
		{"make build outside the kit denied", bashEvent(k.lane, "make build"), 2, true},
		{"make -C <kit> build plus more denied", bashEvent(k.lane, "make -C "+k.dir+" build && git commit -m x"), 2, true},
		{"make -C <another dir> build denied", bashEvent(k.lane, "make -C "+k.p.Base+" build"), 2, true},
		{"other tools allowed", string(read), 0, false},
		{"outside an obor project: Bash allowed", bashEvent(other, "git commit -m x"), 0, false},
		{"outside an obor project: Edit allowed", fileEvent(other, "Edit", filepath.Join(other, "x")), 0, false},
	} {
		want := ""
		if c.denied {
			want = k.hint
		}
		if rc, _, stderr, _ := k.hook(t, "pre-tool-use", c.event); rc != c.rc || stderr != want {
			t.Errorf("%s: exit %d, %q; want %d, %q", c.name, rc, stderr, c.rc, want)
		}
	}
}

// bash plugin.test.sh case_pre_tool_use_stale_binary_denies: a stale dist/obor (no `has`: the T-193 outage) or a
// binary whose `has hook pre-tool-use` exits 1 is not run: the hint denies; a binary that has it runs.
func TestPreToolUseStaleBinaryDenies(t *testing.T) {
	t.Parallel()
	k := newGitNoBinKit(t)
	dist := filepath.Join(k.dir, "dist/obor")
	k.stub(t, dist, "stale")
	if rc, _, _, _ := shimRun(t, k.dir, "", nil, dist, "hook", "noop"); rc != 0 {
		t.Fatalf("precondition: the stale binary runs hook noop (exit %d)", rc)
	}
	if rc, _, stderr, _ := shimRun(t, k.dir, "", nil, dist, "has", "hook", "pre-tool-use"); rc != 2 ||
		!strings.Contains(stderr, "usage: obor hook noop") {
		t.Fatalf("precondition: the stale binary has no has: exit %d, %q", rc, stderr)
	}
	k.deniesAll(t, "stale dist/obor")
	event := bashEvent(k.lane, "ls")
	for _, c := range []struct {
		name, release, dist, stderr string
	}{
		{"release whose has → 1: hint, binary not run", "no", "stale", k.hint},
		{"release with has → Go guard", "deny", "stale", "obor: go stub"},
		{"dist/obor with has → Go guard", "", "deny", "obor: go stub"},
	} {
		// A rewrite keeps the release file's inode: only its first write here is a fresh file (T-1006: macOS charges
		// the first exec of a new file, not of a rewritten one). The last case needs it absent.
		if c.release == "" {
			os.Remove(k.release(t))
		} else {
			k.stub(t, k.release(t), c.release)
		}
		k.stub(t, dist, c.dist)
		if rc, _, stderr, _ := k.hook(t, "pre-tool-use", event); rc != 2 || stderr != c.stderr {
			t.Errorf("%s: exit %d, %q; want 2, %q", c.name, rc, stderr, c.stderr)
		}
	}
}

// bash plugin.test.sh case_pre_tool_use_hanging_binary_denies (RV-22 #1, T-201): a dist/obor whose `has` never returns
// → denied with the hint within the watchdog's bound (OBOR_HOOK_TIMEOUT, 1 s here), the binary and its child reaped;
// the repair command allowed; the binary rebuilt runs.
func TestPreToolUseHangingBinaryDenies(t *testing.T) {
	t.Parallel()
	k := newGitNoBinKit(t)
	dist := filepath.Join(k.dir, "dist/obor")
	event := bashEvent(k.lane, "git commit -m x")
	k.stub(t, dist, "hang")
	// 0.5 s: ample for the stub to start and write its pid before the watchdog's kill; the repair call below need not
	// let the stub start at all, so 0.1 s keeps it from adding a second full bound (T-1006).
	const pidTimeout, quietTimeout = "OBOR_HOOK_TIMEOUT=0.5", "OBOR_HOOK_TIMEOUT=0.1"
	rc, _, stderr, took := k.hook(t, "pre-tool-use", event, pidTimeout)
	if rc != 2 || stderr != k.hint {
		t.Errorf("hanging dist/obor: exit %d, %q; want 2 with the hint", rc, stderr)
	}
	if took > hangBound {
		t.Errorf("a hanging has is bounded (%s): took %s", hookTimeout, took)
	}
	if got := k.stubLog(); got != "has hook pre-tool-use" {
		t.Errorf("stub log %q: want has asked once, never run", got)
	}
	k.reaped(t)
	if rc, _, stderr, _ := k.hook(t, "pre-tool-use", bashEvent(k.lane, "make -C "+k.dir+" build"), quietTimeout); rc != 0 || stderr != "" {
		t.Errorf("make -C <kit> build: exit %d, %q; want allowed", rc, stderr)
	}
	k.stub(t, dist, "deny")
	if rc, _, stderr, _ := k.hook(t, "pre-tool-use", event); rc != 2 || stderr != "obor: go stub" {
		t.Errorf("rebuilt dist/obor: exit %d, %q; want the Go guard", rc, stderr)
	}
}

// case_session_start_no_binary_allows (tests/lib/gostub.sh no_binary_allows, no_binary_outside): with no usable Go
// binary (missing, stale) the other hooks allow (no output, exit 0) with the hint once on stderr; a stale binary is never
// run; a good binary runs; no obor project → silent. The shim's path is the same whatever the hook's name: the first
// hook takes every step, each other hook the steps its name reaches (missing allows, good runs, asked by its name).
// The hanging binary: TestOtherHooksHangingBinaryAllow.
func TestOtherHooksNoBinaryAllow(t *testing.T) {
	t.Parallel()
	k := newGitNoBinKit(t)
	dist := filepath.Join(k.dir, "dist/obor")
	for i, c := range otherHooks {
		event, _ := json.Marshal(c.event(k.lane))
		os.Remove(dist)
		k.allows(t, c.hook, string(event), c.hook+": missing binary")
		k.stub(t, dist, "good")
		k.runs(t, c.hook, string(event), c.hook+": good binary")
		if i > 0 {
			continue
		}
		k.stub(t, dist, "stale")
		if rc, _, stderr, _ := shimRun(t, k.dir, "", nil, dist, "has", "hook", c.hook); rc != 2 ||
			!strings.Contains(stderr, "usage: obor hook noop") {
			t.Fatalf("precondition: the stale binary has no has: exit %d, %q", rc, stderr)
		}
		k.stub(t, dist, "stale")
		k.allows(t, c.hook, string(event), c.hook+": stale binary")
		if log := k.stubLog(); log != "has hook "+c.hook {
			t.Errorf("%s: stale binary: stub log %q, want has asked, never run", c.hook, log)
		}
		os.Remove(dist)
		outside, _ := json.Marshal(c.event(k.p.Base))
		if rc, out, stderr, _ := k.hook(t, c.hook, string(outside)); rc != 0 || out != "" || stderr != "" {
			t.Errorf("%s: outside an obor project: exit %d, %q, %q; want silent", c.hook, rc, out, stderr)
		}
	}
}

// case_session_start_no_binary_allows, the hanging binary (RV-22 #1): the other hooks allow with the hint, the hang is
// bounded and reaped, the binary never runs the hook; rebuilt, it runs.
// Slow: each hook waits out the shim's hang watchdog (OBOR_HOOK_TIMEOUT, 1 s here: whole seconds, bin/obor's sleep).
func TestOtherHooksHangingBinaryAllow(t *testing.T) {
	testkit.Slow(t)
	t.Parallel()
	for _, c := range otherHooks {
		t.Run(c.hook, func(t *testing.T) {
			t.Parallel()
			k := newGitNoBinKit(t)
			event, _ := json.Marshal(c.event(k.lane))
			dist := filepath.Join(k.dir, "dist/obor")
			k.stub(t, dist, "hang")
			if took := k.allows(t, c.hook, string(event), "hanging binary"); took > hangBound {
				t.Errorf("a hanging has is bounded (%s): took %s", hookTimeout, took)
			}
			if log := k.stubLog(); log != "has hook "+c.hook {
				t.Errorf("hanging binary: stub log %q, want has asked once, never run", log)
			}
			k.reaped(t)
			k.stub(t, dist, "good")
			k.runs(t, c.hook, string(event), "rebuilt binary")
		})
	}
}

// newGitNoBinKit is newNoBinKit in a git project (testkit.New, lane L-01 a real worktree): the hooks' binary-less
// paths the moved tests take run git.
func newGitNoBinKit(t *testing.T) *noBinKit {
	t.Helper()
	p := testkit.New(t)
	return noBinKitIn(t, p, p.Lane("L-01", "task/T-01"))
}
