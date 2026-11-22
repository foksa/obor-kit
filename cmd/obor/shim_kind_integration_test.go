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
// It never fetches, runs the first main binary pair that has `kind` and an executable obor-kind-<name> beside it, and
// with none answers one generic deny JSON naming the repair. Every other kind call keeps the generic path (the fetch
// when there is no binary).

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

// kindExe is the kind binary `obor kind <name>` execs beside a main binary: config.KitKind's layout (dist/obor's
// dist/obor-kind-<name>, a release obor-<v>-<os>-<arch>'s obor-kind-<name>-<v>-<os>-<arch>).
func kindExe(main, name string) string {
	base, suffix := filepath.Base(main), ""
	if strings.HasPrefix(base, "obor-") {
		suffix = "-" + strings.TrimPrefix(base, "obor-")
	}
	return filepath.Join(filepath.Dir(main), "obor-kind-"+name+suffix)
}

// putKind makes the kind binary beside main executable: a stub, the shim only asks that it is there (the main binary
// runs it).
func putKind(t *testing.T, main, name string) {
	t.Helper()
	writeExec(t, kindExe(main, name), "#!/bin/sh\nexit 0\n")
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

// denyReason is the repair hint `obor kind <name> hook` answers with when no binary pair has `kind`.
func denyReason(root string) string {
	return "obor: no usable Go binary in " + root + " — run: make -C " + root + " build"
}

// denyJSON is the one generic deny `obor kind <name> hook` answers when no binary pair has `kind`: agy's PreToolUse
// deny with the repair hint, as a JSON object (json.Marshal's escaping).
func denyJSON(root string) string {
	b, _ := json.Marshal(map[string]any{"decision": "deny", "reason": denyReason(root)})
	return string(b) + "\n"
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

// No usable binary pair for `kind <name> hook` (none, a dev build or a release without its kind binary, a stale or
// hanging dev build, a stale release): one generic deny JSON naming the repair, no fetch, no release installed. A
// stale binary is asked `has kind` and never run.
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
		{"a good dev build without its kind binary", func(t *testing.T, k *kindFixture) { k.logStub(t, 0) }, nil, "", 0},
		{"a release without its kind binary", func(t *testing.T, k *kindFixture) { release(t, k, "0") }, nil, "", 1},
		{"a stale dev build", func(t *testing.T, k *kindFixture) {
			k.logStub(t, 1)
			putKind(t, filepath.Join(k.Root, "dist", "obor"), "agy")
		}, nil, "has kind", 0},
		{"a hanging dev build", func(t *testing.T, k *kindFixture) {
			k.logStub(t, -1)
			putKind(t, filepath.Join(k.Root, "dist", "obor"), "agy")
		}, []string{hookTimeoutEnv}, "has kind", 0},
		{"a stale release", func(t *testing.T, k *kindFixture) {
			putKind(t, release(t, k, "1"), "agy")
		}, nil, "", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newKindFixture(t)
			c.setup(t, k)
			want := denyJSON(k.Root)
			rc, stdout, stderr := k.run(t, c.env, "kind", "agy", "hook")
			if rc != 0 || stdout != want || stderr != "" {
				t.Errorf("obor kind agy hook: exit %d, stdout %q, stderr %q; want 0, %q, nothing", rc, stdout, stderr, want)
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

// The pair a kind's hook runs is the first that has `kind` and its kind binary: the dev build with its companion
// wins, a dev build without one falls through to the release's pair, and it gets the whole call.
func TestShimKindHookRunsBinary(t *testing.T) {
	t.Parallel()
	f := newKindFixture(t)
	dev := filepath.Join(f.Root, "dist", "obor")
	rel := filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion))
	fakeGo(t, dev)
	fakeGo(t, rel)
	putKind(t, rel, "agy")
	rc, stdout, stderr := f.run(t, nil, "kind", "agy", "hook")
	if want := "argv: <kind> <agy> <hook> source=kit default root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("a dev build without its kind binary → the release's pair runs: exit %d, %q (stderr %q); want %q",
			rc, stdout, stderr, want)
	}
	putKind(t, dev, "agy")
	rc, stdout, stderr = f.run(t, nil, "kind", "agy", "hook")
	if want := "argv: <kind> <agy> <hook> source=dev build root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("the dev build's pair runs the transport: exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
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
// (encoding/json is the Go test's jq -e).
func TestShimKindHookDenyJSONEscapesKitPath(t *testing.T) {
	t.Parallel()
	f := newKindFixture(t)
	root := filepath.Join(f.Base, `ki"t\path`)
	kit(t, root, "quoted")
	os.Remove(filepath.Join(root, "dist", "obor"))
	rc, stdout, stderr := f.runAt(t, root, nil, "kind", "agy", "hook")
	if rc != 0 || stderr != "" {
		t.Errorf("obor kind agy hook: exit %d, stderr %q; want 0, nothing", rc, stderr)
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
