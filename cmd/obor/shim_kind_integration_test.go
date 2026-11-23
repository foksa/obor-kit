//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Tests for `obor kind <name> hook`, a kind's hook transport (agy's .agents/hooks.json runs it): bin/obor's hook path.
// It never fetches, runs the first main binary that has `kind` (the kind runs in it, no kind binary beside it), and
// with none answers by event: PreToolUse in a running obor project denies with the repair hint, PreToolUse anywhere
// else asks (agy's own check), and SessionStart, PreInvocation and Stop answer nothing. Every other kind call keeps
// the generic path (the fetch when there is no binary).

// kindFixture is a kit copy (the tree's bin/obor and VERSION, no dev build) with a fake curl first on PATH, recording
// each call in curl.log and failing, so a fetch is seen and never reaches a release.
type kindFixture struct {
	*shimFixture
	curlLog string
}

func newKindFixture(t *testing.T) *kindFixture {
	t.Helper()
	f := newKitFixture(t)
	os.Remove(filepath.Join(f.Root, "dist", "obor"))
	log := filepath.Join(f.Base, "curl.log")
	writeExec(t, filepath.Join(f.Base, "bin", "curl"), "#!/bin/bash\necho \"$*\" >> '"+log+"'\nexit 22\n")
	return &kindFixture{shimFixture: f, curlLog: log}
}

// newKindGitFixture is newKindFixture on newShimFixture's git project: the main checkout at Root and its lane L-01,
// each without a dev build, and a fake curl that records each call and fails (a hook never fetches).
func newKindGitFixture(t *testing.T) *kindFixture {
	t.Helper()
	f := newShimFixture(t)
	os.Remove(filepath.Join(f.Root, "dist", "obor"))
	os.Remove(filepath.Join(f.lane, "dist", "obor"))
	log := filepath.Join(f.Base, "curl.log")
	writeExec(t, filepath.Join(f.Base, "bin", "curl"), "#!/bin/bash\necho \"$*\" >> '"+log+"'\nexit 22\n")
	return &kindFixture{shimFixture: f, curlLog: log}
}

// run runs the kit's bin/obor with args from Base (not a checkout), with env added and the fake curl on PATH.
func (k *kindFixture) run(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	return k.runAt(t, k.Root, env, args...)
}

// runAt is run of the kit at root (the fixture's Root, or another kit copy under Base).
func (k *kindFixture) runAt(t *testing.T, root string, env []string, args ...string) (int, string, string) {
	t.Helper()
	env = append([]string{"PATH=" + filepath.Join(k.Base, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")},
		append(env, noSelfHost...)...)
	return k.shim(t, k.Base, env, filepath.Join(root, "bin", "obor"), args...)
}

// fetched reports whether a fetch was attempted (the fake curl ran).
func (k *kindFixture) fetched(t *testing.T) bool {
	t.Helper()
	return k.Exists(k.curlLog) && strings.TrimSpace(readFile(t, k.curlLog)) != ""
}

// releases are the files in ~/.obor/bin (none: no directory).
func (k *kindFixture) releases(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(k.home, ".obor", "bin"))
	if os.IsNotExist(err) {
		return 0
	} else if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// denyReason is the repair hint `obor kind <name> hook` answers with when no binary has `kind`.
func denyReason(root string) string {
	return "obor: no usable Go binary in " + root + " — run: make -C " + root + " build"
}

// denyJSON is agy's PreToolUse deny `obor kind <name> hook` answers in a running obor project when no binary has
// `kind`: the repair hint as a JSON object (json.Marshal's escaping).
func denyJSON(root string) string {
	b, _ := json.Marshal(map[string]any{"decision": "deny", "reason": denyReason(root)})
	return string(b) + "\n"
}

// askJSON is agy's no-opinion PreToolUse answer: agy's own check decides (a tool runs in auto mode).
const askJSON = `{"decision":"ask"}` + "\n"

// kindEvent is agy's hook event for event name, as agy sends it on stdin (the shim reads hookEventName from it, not
// only as the first field).
func kindEvent(name string) string {
	if name == "PreToolUse" {
		return `{"conversationId":"c","cwd":"/nowhere","hookEventName":"PreToolUse","toolCall":{"name":"run_command","args":{"CommandLine":"ls"}}}`
	}
	return `{"conversationId":"c","cwd":"/nowhere","hookEventName":"` + name + `"}`
}

// hook runs `<root>/bin/obor kind agy hook` in dir with agy's event for name on stdin, the fake curl on PATH and env
// added: exit code, stdout, stderr.
func (k *kindFixture) hook(t *testing.T, dir, root string, env []string, name string) (int, string, string) {
	t.Helper()
	env = append([]string{"PATH=" + filepath.Join(k.Base, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")},
		append(env, noSelfHost...)...)
	return k.shimStdin(t, dir, env, kindEvent(name), filepath.Join(root, "bin", "obor"), "kind", "agy", "hook")
}

// logStub is a dev build whose argv is logged: `has` exits hasRC (or hangs, hasRC < 0), anything else prints "kind ran".
func (k *kindFixture) logStub(t *testing.T, hasRC int) {
	t.Helper()
	body := "[ \"$1\" = has ] && exit " + strconv.Itoa(hasRC)
	if hasRC < 0 {
		body = "[ \"$1\" = has ] && exec /bin/sleep 3"
	}
	path := filepath.Join(k.Root, "dist", "obor")
	writeExec(t, path, "#!/bin/bash\necho \"$*\" >> \""+filepath.Join(k.Base, "stub.log")+"\"\n"+body+"\necho \"kind ran\"\n")
	warmScript(t, path)
	writeFile(t, filepath.Join(k.Base, "stub.log"), "") // the warm-up's line, not the run's
}

func (k *kindFixture) stubLog(t *testing.T) string {
	t.Helper()
	if !k.Exists(filepath.Join(k.Base, "stub.log")) {
		return ""
	}
	return strings.TrimSpace(readFile(t, filepath.Join(k.Base, "stub.log")))
}

// No usable binary for `kind <name> hook` (none, a stale or hanging dev build, a stale release): outside an obor
// project a PreToolUse asks (agy's own check decides), no fetch, no release installed. A stale binary is asked
// `has kind` and never run.
func TestShimKindHookNoBinary(t *testing.T) {
	t.Parallel()
	release := func(t *testing.T, k *kindFixture, has string) string {
		t.Helper()
		path := filepath.Join(k.home, ".obor", "bin", shimAsset(shimVersion))
		writeExec(t, path, "#!/bin/bash\n[ \"$1\" = has ] && exit "+has+"\nexit 0\n")
		warmScript(t, path)
		return path
	}
	for _, c := range []struct {
		name     string
		setup    func(t *testing.T, k *kindFixture)
		env      []string
		log      string
		releases int
	}{
		{"no binary", func(*testing.T, *kindFixture) {}, nil, "", 0},
		{"a stale dev build", func(t *testing.T, k *kindFixture) { k.logStub(t, 1) }, nil, "has kind", 0},
		{"a hanging dev build", func(t *testing.T, k *kindFixture) { k.logStub(t, -1) }, []string{hookTimeoutEnv}, "has kind", 0},
		{"a stale release", func(t *testing.T, k *kindFixture) { release(t, k, "1") }, nil, "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newKindFixture(t)
			c.setup(t, k)
			rc, stdout, stderr := k.hook(t, k.Base, k.Root, c.env, "PreToolUse")
			if rc != 0 || stdout != askJSON || stderr != "" {
				t.Errorf("obor kind agy hook PreToolUse outside a project: exit %d, stdout %q, stderr %q; want 0, %q, nothing",
					rc, stdout, stderr, askJSON)
			}
			if got := k.stubLog(t); got != c.log {
				t.Errorf("stub log %q; want %q", got, c.log)
			}
			if k.fetched(t) {
				t.Errorf("a hook fetched: curl %q", readFile(t, k.curlLog))
			}
			if n := k.releases(t); n != c.releases {
				t.Errorf("~/.obor/bin holds %d files; want %d", n, c.releases)
			}
		})
	}
}

// With no usable binary `kind <name> hook` answers by agy's event, never one generic deny: PreToolUse in a running
// obor project denies with the repair hint; in a stopped project or outside one it asks; SessionStart, PreInvocation
// and Stop answer nothing (exit 0, empty stdout), in and out of a project. The hook runs under the project's .agents,
// as agy runs it, so the config is up from the cwd.
func TestShimKindHookNoBinaryByEvent(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"PreToolUse", "SessionStart", "PreInvocation", "Stop"} {
		for _, state := range []string{"running", "stopped", "outside"} {
			t.Run(event+" in a "+state+" project", func(t *testing.T) {
				t.Parallel()
				k := newKindFixture(t)
				repo := filepath.Join(k.Base, "repo")
				writeFile(t, filepath.Join(repo, ".agents", "hooks.json"), "{}\n")
				if state != "outside" {
					writeFile(t, filepath.Join(repo, ".obor", "config.json"), "{}\n")
				}
				if state == "stopped" {
					writeFile(t, filepath.Join(repo, ".obor", "state", "stopped"), "stopped\n")
				}
				want := ""
				switch {
				case event != "PreToolUse":
				case state == "running":
					want = denyJSON(k.Root)
				default:
					want = askJSON
				}
				rc, stdout, stderr := k.hook(t, filepath.Join(repo, ".agents"), k.Root, nil, event)
				if rc != 0 || stdout != want || stderr != "" {
					t.Errorf("obor kind agy hook %s: exit %d, stdout %q, stderr %q; want 0, %q, nothing",
						event, rc, stdout, stderr, want)
				}
				if k.fetched(t) {
					t.Errorf("a hook fetched: curl %q", readFile(t, k.curlLog))
				}
			})
		}
	}
}

// A linked worktree answers as its main checkout, the same rows as the Go transport's own worktree cases
// (kinds/agy/kind/hook_test.go's TestInProject): a lane with a config of its own is still its main checkout's
// project, and a lane whose config lives only in the main checkout is still in the project. The stop marker is the
// main checkout's, where obor stop writes it. Beside TestShimKindHookNoBinaryByEvent's stop-marker rows, so a change
// to either side of that contract shows the mismatch.
func TestShimKindHookNoBinaryWorktree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, k *kindFixture) string // the dir the hook runs in
		stop  bool                                      // obor stop's marker in the main checkout
		deny  bool
	}{
		{
			name: "a lane's committed config, the main checkout stopped",
			setup: func(t *testing.T, k *kindFixture) string {
				writeFile(t, filepath.Join(k.Root, ".obor", "config.json"), "{}\n")
				k.Git(k.Root, "add", ".obor/config.json")
				k.Git(k.Root, "commit", "-q", "-m", "config")
				lane := k.Lane("L-02", "task/T-2")
				os.Remove(filepath.Join(lane, "dist", "obor"))
				if !k.Exists(filepath.Join(lane, ".obor", "config.json")) {
					t.Fatal("precondition: the lane tracks a config of its own")
				}
				return lane
			},
			stop: true,
		},
		{
			name: "a lane with the config only in the main checkout, running",
			setup: func(t *testing.T, k *kindFixture) string {
				writeFile(t, filepath.Join(k.Root, ".obor", "config.json"), "{}\n")
				if k.Exists(filepath.Join(k.lane, ".obor", "config.json")) {
					t.Fatal("precondition: the lane has no config of its own")
				}
				return k.lane
			},
			deny: true,
		},
		{
			name: "the main checkout running",
			setup: func(t *testing.T, k *kindFixture) string {
				writeFile(t, filepath.Join(k.Root, ".obor", "config.json"), "{}\n")
				return k.Root
			},
			deny: true,
		},
		{
			name: "the main checkout stopped",
			setup: func(t *testing.T, k *kindFixture) string {
				writeFile(t, filepath.Join(k.Root, ".obor", "config.json"), "{}\n")
				return k.Root
			},
			stop: true,
		},
		{
			// no .obor/config.json at the plain repo or above it: the git call answers, the project check does not.
			name:  "outside a project",
			setup: func(t *testing.T, k *kindFixture) string { return k.other },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newKindGitFixture(t)
			dir := c.setup(t, k)
			if c.stop {
				writeFile(t, filepath.Join(k.Root, ".obor", "state", "stopped"), "stopped\n")
			}
			want := askJSON
			if c.deny {
				want = denyJSON(k.Root)
			}
			rc, stdout, stderr := k.hook(t, dir, k.Root, nil, "PreToolUse")
			if rc != 0 || stdout != want || stderr != "" {
				t.Errorf("obor kind agy hook PreToolUse in %q: exit %d, stdout %q, stderr %q; want 0, %q, nothing",
					dir, rc, stdout, stderr, want)
			}
			if k.fetched(t) {
				t.Errorf("a hook fetched: curl %q", readFile(t, k.curlLog))
			}
		})
	}
}

// The binary a kind's hook runs is the first that has `kind`, no kind binary beside it: the release alone runs it, a
// dev build wins over the release, and it gets the whole call.
func TestShimKindHookRunsBinary(t *testing.T) {
	t.Parallel()
	f := newKindFixture(t)
	dev := filepath.Join(f.Root, "dist", "obor")
	rel := filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion))
	fakeGo(t, rel)
	rc, stdout, stderr := f.run(t, nil, "kind", "agy", "hook")
	if want := "argv: <kind> <agy> <hook> source=kit default root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("no dev build → the release runs: exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
	}
	fakeGo(t, dev)
	rc, stdout, stderr = f.run(t, nil, "kind", "agy", "hook")
	if want := "argv: <kind> <agy> <hook> source=dev build root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("the dev build runs the transport: exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
	}
	os.Remove(dev)
	rc, stdout, stderr = f.run(t, nil, "kind", "agy", "hook")
	if want := "argv: <kind> <agy> <hook> source=kit default root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("the release runs the transport: exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
	}
	if f.fetched(t) {
		t.Errorf("a hook fetched: curl %q", readFile(t, f.curlLog))
	}
}

// A kit path with a quote and a backslash: the deny JSON escapes them, so agy reads a valid reason naming the repair
// (encoding/json is the Go test's jq -e). A running project is where the deny is answered.
func TestShimKindHookDenyJSONEscapesKitPath(t *testing.T) {
	t.Parallel()
	f := newKindFixture(t)
	root := filepath.Join(f.Base, `ki"t\path`)
	kit(t, root, "quoted")
	os.Remove(filepath.Join(root, "dist", "obor"))
	project := filepath.Join(f.Base, "proj")
	writeFile(t, filepath.Join(project, ".obor", "config.json"), "{}\n")
	rc, stdout, stderr := f.hook(t, project, root, nil, "PreToolUse")
	if rc != 0 || stderr != "" {
		t.Errorf("obor kind agy hook PreToolUse in a project: exit %d, stderr %q; want 0, nothing", rc, stderr)
	}
	var got struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("the deny is not valid JSON: %v\n%q", err, stdout)
	}
	if got.Decision != "deny" || got.Reason != denyReason(root) {
		t.Errorf("the deny = %+v; want decision %q, reason %q", got, "deny", denyReason(root))
	}
	if f.fetched(t) {
		t.Errorf("a hook fetched: curl %q", readFile(t, f.curlLog))
	}
}

// A kind call that is no hook keeps the generic path: the dev build runs it, and with no binary it fetches (and fails
// here: the fake curl answers nothing) — never the hook's deny JSON.
func TestShimKindGenericPath(t *testing.T) {
	t.Parallel()
	f := newKindFixture(t)
	fakeGo(t, filepath.Join(f.Root, "dist", "obor"))
	rc, stdout, stderr := f.run(t, nil, "kind", "agy", "describe")
	if want := "argv: <kind> <agy> <describe> source=dev build root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("a dev build runs any kind call: exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
	}
	os.Remove(filepath.Join(f.Root, "dist", "obor"))
	rc, stdout, stderr = f.run(t, nil, "kind", "agy", "describe")
	if rc != 1 || !strings.Contains(stderr, "FAIL cannot fetch") || strings.Contains(stdout, "decision") {
		t.Errorf("no binary, not a hook: exit %d, %q, %q; want the fetch's failure, not the hook's deny", rc, stdout, stderr)
	}
	if !f.fetched(t) {
		t.Error("no binary, not a hook: no fetch attempted")
	}
}
