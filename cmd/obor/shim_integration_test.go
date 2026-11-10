//go:build integration

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/testkit"
)

// Tests for bin/obor, the kit's one bash file: self-hosting (inside an obor checkout, main or lane
// worktree, `obor` runs the MAIN checkout's bin/obor; elsewhere the installed ("stable") copy runs) and finding the Go
// binary (dev build, installed release, fetch + checksum; a hook checks `has` first). Every fixture kit carries its
// own VERSION (shimVersion), so the tests pass with any VERSION in the tree. A fake dist/obor marks which root ran.

// shimVersion is the VERSION of every fixture kit.
const shimVersion = "1.2.3-dev"

// shimAsset is the release asset name of version v on this platform (bin/obor's uname mapping is Go's GOOS/GOARCH).
func shimAsset(v string) string { return "obor-" + v + "-" + runtime.GOOS + "-" + runtime.GOARCH }

// treeFile is a file of the kit checkout these tests run in.
func treeFile(t *testing.T, rel string) []byte {
	t.Helper()
	return []byte(readFile(t, filepath.Join("..", "..", rel)))
}

func writeExec(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

// marker is a dev binary that prints "ran: <label> root=… hosted=…".
func marker(t *testing.T, path, label string) {
	writeExec(t, path, "#!/bin/bash\necho \"ran: "+label+" root=$OBOR_PLUGIN_ROOT hosted=${OBOR_SELF_HOSTED:-}\"\n")
}

// warmScript runs path once with a non-`has` argument: a platform scans a fresh executable before its first exec
// (macOS: 100+ ms idle, more under a loaded machine), and a hook test's watchdog must not be raced by that scan.
func warmScript(t *testing.T, path string) {
	t.Helper()
	if err := exec.Command(path, "warm").Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("warm %s: %v", path, err)
		}
	}
}

// fakeGo is a Go binary that prints its argv, OBOR_VERSION_SOURCE and OBOR_PLUGIN_ROOT.
func fakeGo(t *testing.T, path string) {
	writeExec(t, path, `#!/bin/bash
printf 'argv:'
for arg in "$@"; do printf ' <%s>' "$arg"; done
printf ' source=%s root=%s\n' "${OBOR_VERSION_SOURCE:-}" "${OBOR_PLUGIN_ROOT:-}"
`)
	warmScript(t, path)
}

// staleGo is built without `has`: it knows only `hook noop`, anything else is usage + exit 2.
func staleGo(t *testing.T, path string) {
	writeExec(t, path, "#!/bin/bash\n[ \"$1 $2\" = \"hook noop\" ] && exit 0\necho \"usage: obor hook noop\" >&2\nexit 2\n")
	warmScript(t, path)
}

// hang is how long hangGo's `has` sleeps: past the shim's bound (hookTimeoutEnv), so a shim that waited it out would
// run the dev build's hook instead of the release.
const hang = 3 * time.Second

// hangGo logs its argv to <dir>/stub.log; its `has` never returns (it writes its pid to <dir>/stub.pids, then execs
// sleep for hang: no child).
func hangGo(t *testing.T, path, dir string) {
	writeExec(t, path, fmt.Sprintf("#!/bin/bash\necho \"$*\" >> %q\n"+
		"[ \"$1\" = has ] && { echo \"$$\" > %q; exec sleep %d; }\necho \"ran: hang $*\"\n",
		filepath.Join(dir, "stub.log"), filepath.Join(dir, "stub.pids"), int(hang.Seconds())))
	warmScript(t, path)
	writeFile(t, filepath.Join(dir, "stub.log"), "") // the warm-up's line, not the hook's
}

// kit is a plugin root at dir: the tree's bin/obor and bin/go-commands, VERSION shimVersion, a release URL that
// never reaches the network, no checksums, a marker dist/obor labelled label, and a plugin.json named obor.
func kit(t *testing.T, dir, label string) {
	t.Helper()
	writeExec(t, filepath.Join(dir, "bin", "obor"), string(treeFile(t, "bin/obor")))
	for path, text := range map[string]string{
		"bin/go-commands":            string(treeFile(t, "bin/go-commands")),
		"bin/release-url":            "file://" + dir + "/no-such-release-%VERSION%\n",
		"bin/checksums.txt":          "",
		"VERSION":                    shimVersion + "\n",
		".claude-plugin/plugin.json": `{"name":"obor","version":"0.0.0"}` + "\n",
	} {
		writeFile(t, filepath.Join(dir, path), text)
	}
	marker(t, filepath.Join(dir, "dist", "obor"), label)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// plainMakeEnv is the test process's environment without the make variables an enclosing make exports into every
// recipe: the gate run as `MAKEFLAGS=' V=0.17.2' make check` (or a test inside `make release V=…`) would otherwise put
// a test's own `make release` on the V= path.
func plainMakeEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch name, _, _ := strings.Cut(kv, "="); name {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "V", "DRY":
			continue
		}
		env = append(env, kv)
	}
	return env
}

// shimFixture: a stable kit at <Base>/stable; an obor checkout at Root (kit committed, marker "main") with lane L-01
// (its own uncommitted marker "lane"); a plain repo at <Base>/other; HOME at <Base>/home.
type shimFixture struct {
	*testkit.Project
	stable, lane, other, home string
}

func newShimFixture(t *testing.T) *shimFixture {
	t.Helper()
	p := testkit.New(t)
	f := &shimFixture{Project: p, stable: filepath.Join(p.Base, "stable"), other: filepath.Join(p.Base, "other"),
		home: filepath.Join(p.Base, "home")}
	kit(t, f.stable, "stable")
	kit(t, p.Root, "main")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "plugin")
	f.lane = p.Lane("L-01", "task/T-1")
	marker(t, filepath.Join(f.lane, "dist", "obor"), "lane")
	p.Git(p.Base, "-c", "init.defaultBranch=main", "init", "-q", f.other)
	return f
}

// shim runs bash <script> args… in dir with stdin /dev/null, HOME = f.home, and env added: exit code, stdout, stderr.
func (f *shimFixture) shim(t *testing.T, dir string, env []string, script string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
	cmd.Env = append(append(os.Environ(), "HOME="+f.home), env...)
	err := cmd.Run()
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, stdout.String(), stderr.String()
}

var noSelfHost = []string{"OBOR_NO_SELF_HOST=1"}

// bash tests/bin.test.sh case_cli_dispatch_and_usage
func TestShimDispatchAndUsage(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	obor := filepath.Join(f.stable, "bin", "obor")
	for _, c := range []struct {
		args []string
		rc   int
		err  []string
		out  []string
	}{
		{[]string{"no-such-sub"}, 2, []string{"unknown subcommand: no-such-sub", "usage: obor"}, nil},
		{nil, 2, []string{"usage: obor"}, nil},
		{[]string{"../lib/obor"}, 2, []string{"usage: obor"}, nil},
		{[]string{"help"}, 0, nil, []string{"plugin root: " + f.stable + "\n", "ran: stable"}},
	} {
		rc, stdout, stderr := f.shim(t, f.Base, nil, obor, c.args...)
		ok := rc == c.rc && (c.out != nil || stdout == "")
		for _, s := range c.err {
			ok = ok && strings.Contains(stderr, s)
		}
		for _, s := range c.out {
			ok = ok && strings.Contains(stdout, s)
		}
		if !ok {
			t.Errorf("obor %v: exit %d, stdout %q, stderr %q; want %d, stderr %q, stdout %q", c.args, rc, stdout, stderr,
				c.rc, c.err, c.out)
		}
	}
	// via a symlink on PATH, from an unrelated directory
	bin, x := filepath.Join(f.Base, "bin"), filepath.Join(f.Base, "x")
	for _, d := range []string{bin, x} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(obor, filepath.Join(bin, "obor")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "obor help")
	cmd.Dir, cmd.Env = x, append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "ran: stable") ||
		!strings.Contains(string(out), "plugin root: "+f.stable) {
		t.Errorf("obor help via a symlink on PATH: %v\n%s", err, out)
	}
}

// `serve` is in bin/go-commands, so `bin/obor serve dump` reaches the binary instead of the shim's
// unknown-subcommand error (exit 2, usage). The stub dist/obor argv-echoes and fails as the real one does with no app
// running; the real serve's own output is covered by serve_test.go's tests.
func TestShimServeReachesBinary(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	writeExec(t, filepath.Join(f.Root, "dist", "obor"), `#!/bin/bash
printf 'argv:'
for arg in "$@"; do printf ' <%s>' "$arg"; done
printf '\n'
echo 'FAIL obor serve is not running' >&2
exit 1
`)
	rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, filepath.Join(f.Root, "bin", "obor"), "serve", "dump")
	if want := "argv: <serve> <dump>\n"; rc != 1 || stdout != want || strings.Contains(stderr, "unknown subcommand") ||
		!strings.Contains(stderr, "not running") {
		t.Errorf("obor serve dump: exit %d, stdout %q, stderr %q; want exit 1, %q and the binary's own not-running failure",
			rc, stdout, stderr, want)
	}
}

// bash tests/bin.test.sh case_self_host, case_lane_uses_main_bin, case_opt_out_and_no_loop, case_needs_obor_manifest
func TestShimSelfHost(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	stable, own := filepath.Join(f.stable, "bin", "obor"), filepath.Join(f.Root, "bin", "obor")
	if err := os.MkdirAll(filepath.Join(f.Root, "docs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The lane's docs dir (testkit's fixture carries no docs/tasks).
	if err := os.MkdirAll(filepath.Join(f.lane, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := "ran: main root=" + f.Root + " hosted=1\n"
	for _, c := range []struct {
		name, dir string
		env       []string
		script    string
		want      string
	}{
		{"main checkout of an obor project runs its own bin", f.Root, nil, stable, main},
		{"from a subdirectory too", filepath.Join(f.Root, "docs", "sub"), nil, stable, main},
		{"a non-obor repo runs the stable copy", f.other, nil, stable, "ran: stable root=" + f.stable + " hosted=\n"},
		{"outside any repo runs the stable copy", f.Base, nil, stable, "ran: stable root=" + f.stable + " hosted=\n"},
		{"a lane worktree runs the main checkout's bin, not the lane's", f.lane, nil, stable, main},
		{"from a lane subdirectory too", filepath.Join(f.lane, "docs"), nil, stable, main},
		{"the lane's own bin/obor hops to the main checkout", f.lane, nil, filepath.Join(f.lane, "bin", "obor"), main},
		{"OBOR_NO_SELF_HOST=1 ./bin/obor in the lane runs the lane's copy", f.lane, noSelfHost, "./bin/obor",
			"ran: lane root=" + f.lane + " hosted=\n"},
		{"OBOR_NO_SELF_HOST=1 keeps the stable copy", f.Root, noSelfHost, stable,
			"ran: stable root=" + f.stable + " hosted=\n"},
		{"OBOR_SELF_HOSTED=1 means no second hop", f.Root, []string{"OBOR_SELF_HOSTED=1"}, stable,
			"ran: stable root=" + f.stable + " hosted=1\n"},
		{"the project's own bin does not hop to itself", f.Root, nil, own, "ran: main root=" + f.Root + " hosted=\n"},
	} {
		if rc, stdout, stderr := f.shim(t, c.dir, c.env, c.script, "version"); rc != 0 || stdout != c.want {
			t.Errorf("%s: exit %d, %q (stderr %q); want %q", c.name, rc, stdout, stderr, c.want)
		}
	}
	// An obor checkout needs both bin/obor and a plugin.json named obor.
	f.Write(".claude-plugin/plugin.json", `{"name":"not-obor"}`+"\n")
	if _, stdout, _ := f.shim(t, f.Root, nil, stable, "version"); stdout != "ran: stable root="+f.stable+" hosted=\n" {
		t.Errorf("bin/obor without a name:obor manifest is not an obor checkout: %q", stdout)
	}
	f.Write(".claude-plugin/plugin.json", `{"name":"obor"}`+"\n")
	if _, stdout, _ := f.shim(t, f.Root, nil, stable, "version"); stdout != main {
		t.Fatalf("precondition: a name:obor manifest with bin/obor self-hosts: %q", stdout)
	}
	if err := os.Remove(own); err != nil {
		t.Fatal(err)
	}
	if _, stdout, _ := f.shim(t, f.Root, nil, stable, "version"); stdout != "ran: stable root="+f.stable+" hosted=\n" {
		t.Errorf("a manifest without bin/obor is not an obor checkout: %q", stdout)
	}
}

// bash tests/bin.test.sh case_help_prints_plugin_root: the command list is the binary's (`obor help`), so
// the shim prints the plugin root and runs the binary with help (-h and --help too), never bin/go-commands' names.
func TestShimHelpPrintsPluginRoot(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	stable := filepath.Join(f.stable, "bin", "obor")
	for _, c := range []struct {
		dir  string
		want []string
	}{
		{f.other, []string{"plugin root: " + f.stable + "\n", "ran: stable"}},
		{f.lane, []string{"plugin root: " + f.Root + "\n", "ran: main"}},
	} {
		rc, stdout, _ := f.shim(t, c.dir, nil, stable, "help")
		for _, s := range c.want {
			if rc != 0 || !strings.Contains(stdout, s) {
				t.Errorf("obor help in %s: exit %d, no %q:\n%s", c.dir, rc, s, stdout)
			}
		}
	}
	fakeGo(t, filepath.Join(f.stable, "dist", "obor"))
	for _, arg := range []string{"help", "-h", "--help"} {
		rc, stdout, _ := f.shim(t, f.other, nil, stable, arg)
		if want := "plugin root: " + f.stable + "\nargv: <help> "; rc != 0 || !strings.HasPrefix(stdout, want) ||
			strings.Contains(stdout, "  whoami") {
			t.Errorf("obor %s: exit %d, %q; want 0 and %q (the binary's help, not the shim's list)", arg, rc, stdout, want)
		}
	}
}

// bash tests/bin.test.sh case_dev_binary_preferred, case_pin_selects_installed_binary,
// case_hook_subcommand_reaches_binary: which binary runs, and that it gets the argv in order and the version source.
func TestShimPicksBinary(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f *shimFixture)
		args  []string
		want  string // %s: the project root
	}{
		{"a built dev binary in the checkout takes precedence", func(t *testing.T, f *shimFixture) {
			fakeGo(t, filepath.Join(f.Root, "dist", "obor"))
			fakeGo(t, filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion)))
		}, []string{"version"}, "argv: <version> source=dev build root=%s\n"},
		{"a leftover project pin is ignored: the kit's VERSION selects the release", func(t *testing.T, f *shimFixture) {
			os.Remove(filepath.Join(f.Root, "dist", "obor"))
			f.Config(testkit.Set("obor", `{"version": "pin-9"}`))
			fakeGo(t, filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion)))
		}, []string{"version"}, "argv: <version> source=kit default root=%s\n"},
		{"hook and its argument reach the installed release in order", func(t *testing.T, f *shimFixture) {
			os.Remove(filepath.Join(f.Root, "dist", "obor"))
			fakeGo(t, filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion)))
		}, []string{"hook", "noop"}, "argv: <hook> <noop> source=kit default root=%s\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newShimFixture(t)
			c.setup(t, f)
			rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, filepath.Join(f.Root, "bin", "obor"), c.args...)
			if want := fmt.Sprintf(c.want, f.Root); rc != 0 || stdout != want {
				t.Errorf("exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
			}
		})
	}
}

// bash tests/bin.test.sh case_stale_binary_skipped_for_hooks: a stale dist/obor is not run for a hook: the
// installed release that has it is; a good dist/obor wins. A command runs the dev build without asking (a stale one
// says "unknown subcommand": make build).
func TestShimHookSkipsStaleDevBuild(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	obor := filepath.Join(f.Root, "bin", "obor")
	staleGo(t, filepath.Join(f.Root, "dist", "obor"))
	fakeGo(t, filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion)))
	rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, obor, "hook", "pre-tool-use")
	if want := "argv: <hook> <pre-tool-use> source=kit default root=" + f.Root + "\n"; rc != 0 || stdout != want ||
		strings.Contains(stderr, "usage") {
		t.Errorf("stale dist/obor → the release runs the hook: exit %d, %q, stderr %q; want %q", rc, stdout, stderr, want)
	}
	if rc, _, stderr := f.shim(t, f.Root, noSelfHost, obor, "status"); rc != 2 || !strings.Contains(stderr, "usage: obor hook noop") {
		t.Errorf("a command runs the dev build as it is (its error shows): exit %d, %q", rc, stderr)
	}
	fakeGo(t, filepath.Join(f.Root, "dist", "obor"))
	rc, stdout, _ = f.shim(t, f.Root, noSelfHost, obor, "hook", "pre-tool-use")
	if want := "argv: <hook> <pre-tool-use> source=dev build root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("good dist/obor → it runs the hook: exit %d, %q; want %q", rc, stdout, want)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// bash tests/bin.test.sh case_hanging_binary_skipped_for_hooks: a dist/obor whose `has` never returns is
// skipped (OBOR_HOOK_TIMEOUT, 1 s here) and killed (its own pid: no process group); the release runs.
func TestShimHookSkipsHangingDevBuild(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	hangGo(t, filepath.Join(f.Root, "dist", "obor"), f.Base)
	fakeGo(t, filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion)))
	rc, stdout, stderr := f.shim(t, f.Root, append([]string{hookTimeoutEnv}, noSelfHost...),
		filepath.Join(f.Root, "bin", "obor"), "hook", "stop")
	if want := "argv: <hook> <stop> source=kit default root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Errorf("hanging dist/obor → the release runs the hook: exit %d, %q; want %q", rc, stdout, want)
	}
	if stderr != "" {
		t.Errorf("nothing on stderr (no job notice): %q", stderr)
	}
	// No wall-clock bound: the release's argv and the log say the shim gave up on the hang (had it waited, the dev
	// build's `has` returns 0 and the dev build runs the hook instead).
	if log := f.Read(filepath.Join(f.Base, "stub.log")); log != "has hook stop\n" {
		t.Errorf("asked has once, never run: %q", log)
	}
	var pid int
	if n, _ := fmt.Sscanf(f.Read(filepath.Join(f.Base, "stub.pids")), "%d", &pid); n != 1 {
		t.Fatalf("precondition: the hanging binary recorded its pid: %q", f.Read(filepath.Join(f.Base, "stub.pids")))
	}
	if !waitFor(func() bool { return !alive(pid) }) {
		t.Errorf("hanging binary %d still alive", pid)
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// bash tests/bin.test.sh case_local_fetch_verifies_checksum, case_checksum_mismatch_refused: without a dev build or an
// installed release, a command fetches the release from
// bin/release-url (a local file here) into ~/.obor/bin and runs it only when its bin/checksums.txt line matches.
func TestShimFetchVerifiesChecksum(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		version string // the kit's VERSION
		line    string // checksum line: "%s" the sha256, "%[2]s" the asset
		rc      int
		want    string // stdout (%s: the project root), or a stderr substring when rc != 0
	}{
		{"the kit's dev version", shimVersion, "%s  %s\n", 0, "argv: <version> source=kit default root=%s\n"},
		{"a release version, catalog line as make release writes it", "0.9.1", "%s  dist/%s\n", 0,
			"argv: <version> source=kit default root=%s\n"},
		{"a checksum mismatch is refused", shimVersion, strings.Repeat("0", 64) + "  %[2]s\n", 1, "checksum mismatch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newShimFixture(t)
			os.Remove(filepath.Join(f.Root, "dist", "obor"))
			f.Write("VERSION", c.version+"\n")
			asset := shimAsset(c.version)
			download := filepath.Join(f.Base, "download")
			fakeGo(t, filepath.Join(download, asset))
			sum := sha256.Sum256([]byte(readFile(t, filepath.Join(download, asset))))
			f.Write("bin/release-url", "file://"+download+"/obor-%VERSION%-%OS%-%ARCH%\n")
			f.Write("bin/checksums.txt", fmt.Sprintf(c.line, hex.EncodeToString(sum[:]), asset))
			installed := filepath.Join(f.home, ".obor", "bin", asset)
			if f.Exists(installed) {
				t.Fatal("precondition: cold cache")
			}
			rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, filepath.Join(f.Root, "bin", "obor"), "version")
			if c.rc == 0 {
				if want := fmt.Sprintf(c.want, f.Root); rc != 0 || stdout != want {
					t.Errorf("exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
				}
				if st, err := os.Stat(installed); err != nil || st.Mode().Perm()&0o111 == 0 {
					t.Errorf("the verified release is installed executable at %s: %v", installed, err)
				}
				return
			}
			if rc != c.rc || !strings.Contains(stderr, c.want) {
				t.Errorf("exit %d, stderr %q; want %d, %q", rc, stderr, c.rc, c.want)
			}
			if f.Exists(installed) {
				t.Errorf("a refused binary was installed at %s", installed)
			}
		})
	}
}

// the fetch also brings each kind binary the catalog lists for this version and platform
// (obor-kind-<name>-<v>-<os>-<arch>, from the release dir obor's asset is in) into ~/.obor/bin beside obor's, checked
// against its line; a kind binary that is missing alone is fetched at the next command; a catalog that lists no kind
// fetches obor's alone (TestShimFetchVerifiesChecksum).
func TestShimFetchesKindBinaries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		kindLine string // the kind's checksum line: "%s" its sha256, "%[2]s" the asset
		rc       int
		want     string // a stderr substring when rc != 0
	}{
		{"fetched beside obor's", "%s  dist/%s\n", 0, ""},
		{"a kind checksum mismatch is refused", strings.Repeat("0", 64) + "  dist/%[2]s\n", 1, "checksum mismatch for obor-kind-opencode-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newShimFixture(t)
			os.Remove(filepath.Join(f.Root, "dist", "obor"))
			f.Write("VERSION", "0.9.1\n")
			asset, kind := shimAsset("0.9.1"), "obor-kind-opencode-0.9.1-"+runtime.GOOS+"-"+runtime.GOARCH
			download := filepath.Join(f.Base, "download")
			fakeGo(t, filepath.Join(download, asset))
			writeExec(t, filepath.Join(download, kind), "#!/bin/sh\necho kind\n")
			sum := sha256.Sum256([]byte(readFile(t, filepath.Join(download, asset))))
			kindSum := sha256.Sum256([]byte(readFile(t, filepath.Join(download, kind))))
			f.Write("bin/release-url", "file://"+download+"/obor-%VERSION%-%OS%-%ARCH%\n")
			f.Write("bin/checksums.txt", fmt.Sprintf("%s  dist/%s\n", hex.EncodeToString(sum[:]), asset)+
				fmt.Sprintf(c.kindLine, hex.EncodeToString(kindSum[:]), kind))
			bin := filepath.Join(f.home, ".obor", "bin")
			if f.Exists(filepath.Join(bin, kind)) {
				t.Fatal("precondition: cold cache")
			}
			rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, filepath.Join(f.Root, "bin", "obor"), "version")
			if c.rc != 0 {
				if rc != c.rc || !strings.Contains(stderr, c.want) {
					t.Errorf("exit %d, stderr %q; want %d, %q", rc, stderr, c.rc, c.want)
				}
				if f.Exists(filepath.Join(bin, kind)) {
					t.Errorf("a refused kind binary was installed")
				}
				return
			}
			if rc != 0 || !strings.HasPrefix(stdout, "argv: <version>") {
				t.Fatalf("exit %d, %q (stderr %q)", rc, stdout, stderr)
			}
			if got := readFile(t, filepath.Join(bin, kind)); got != "#!/bin/sh\necho kind\n" {
				t.Errorf("the kind binary beside obor's: %q", got)
			}
			// the kind binary alone gone: the next command fetches it again, obor's kept
			os.Remove(filepath.Join(bin, kind))
			if rc, _, stderr := f.shim(t, f.Root, noSelfHost, filepath.Join(f.Root, "bin", "obor"), "version"); rc != 0 ||
				!f.Exists(filepath.Join(bin, kind)) {
				t.Errorf("a missing kind binary is fetched at the next command: exit %d, %q", rc, stderr)
			}
		})
	}
}

// shimVersionFiles writes version v's release and OpenCode kind binaries for this platform into dir, with mtime when
// (a fetched file's mtime is its fetch time, which ranks versions for pruning).
func shimVersionFiles(t *testing.T, dir, v string, when time.Time) {
	t.Helper()
	for _, name := range []string{shimAsset(v), "obor-kind-opencode-" + v + "-" + runtime.GOOS + "-" + runtime.GOARCH} {
		path := filepath.Join(dir, name)
		writeExec(t, path, "#!/bin/sh\necho "+v+"\n")
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

// A successful fetch removes the release and kind binaries of every version but
// the kit's and the one fetched before it (the previous kit, kept for a rollback); a file that is not one of those
// stays.
func TestShimFetchPrunesOldBinaries(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	os.Remove(filepath.Join(f.Root, "dist", "obor"))
	f.Write("VERSION", "0.9.1\n")
	asset := shimAsset("0.9.1")
	download := filepath.Join(f.Base, "download")
	fakeGo(t, filepath.Join(download, asset))
	sum := sha256.Sum256([]byte(readFile(t, filepath.Join(download, asset))))
	f.Write("bin/release-url", "file://"+download+"/obor-%VERSION%-%OS%-%ARCH%\n")
	f.Write("bin/checksums.txt", fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset))
	bin := filepath.Join(f.home, ".obor", "bin")
	// Three older versions, the last fetched (and so the previous kit) most recently: 0.8.1 stays.
	now := time.Now()
	for i, v := range []string{"0.6.0", "0.7.0", "0.8.1"} {
		shimVersionFiles(t, bin, v, now.Add(time.Duration(i-3)*time.Hour))
	}
	writeFile(t, filepath.Join(bin, "notes.txt"), "not a binary\n")
	if f.Exists(filepath.Join(bin, asset)) {
		t.Fatal("precondition: cold cache")
	}
	rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, filepath.Join(f.Root, "bin", "obor"), "version")
	if want := "argv: <version> source=kit default root=" + f.Root + "\n"; rc != 0 || stdout != want {
		t.Fatalf("the fetch runs the release: exit %d, %q (stderr %q); want %q", rc, stdout, stderr, want)
	}
	want := []string{
		asset,
		shimAsset("0.8.1"),
		"obor-kind-opencode-0.8.1-" + runtime.GOOS + "-" + runtime.GOARCH,
		"notes.txt",
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("~/.obor/bin after the fetch = %q; want the kit's asset, the previous version's release and kind binaries, and the file that is not one: %q",
			got, want)
	}
	if body := readFile(t, filepath.Join(bin, "notes.txt")); body != "not a binary\n" {
		t.Errorf("a file that is not an obor binary was changed: %q", body)
	}
}

// bash tests/bin.test.sh case_missing_hook_never_fetches_but_command_fails, case_missing_curl_fails_with_install_hint
func TestShimNoBinary(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	obor := filepath.Join(f.Root, "bin", "obor")
	os.Remove(filepath.Join(f.Root, "dist", "obor"))
	if rc, stdout, stderr := f.shim(t, f.Root, noSelfHost, obor, "hook", "noop"); rc != 0 || stdout != "" || stderr != "" {
		t.Errorf("no binary, not an obor project: the hook is silent (no fetch): exit %d, %q, %q", rc, stdout, stderr)
	}
	if rc, _, stderr := f.shim(t, f.Root, noSelfHost, obor, "version"); rc != 1 || !strings.Contains(stderr, "FAIL cannot fetch") {
		t.Errorf("a command fails when the fetch does: exit %d, %q", rc, stderr)
	}
	// PATH with only what bin/obor needs up to the fetch: no curl.
	path := filepath.Join(f.Base, "no-curl")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"git", "jq", "cat", "dirname", "uname", "tr", "mkdir"} {
		resolved, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(resolved, filepath.Join(path, tool)); err != nil {
			t.Fatal(err)
		}
	}
	want := "FAIL curl not found: install it or put the binary at " +
		filepath.Join(f.home, ".obor", "bin", shimAsset(shimVersion))
	if rc, _, stderr := f.shim(t, f.Root, append([]string{"PATH=" + path}, noSelfHost...), obor, "version"); rc != 1 ||
		!strings.Contains(stderr, want) {
		t.Errorf("no curl: exit %d, %q; want 1, %q", rc, stderr, want)
	}
}

// bash tests/bin.test.sh case_release_keeps_other_versions_and_sorts_catalog: make release (a fake go) replaces only
// its own version's lines of bin/checksums.txt, obor's and the OpenCode kind's, and keeps the catalog sorted.
func TestShimReleaseCatalog(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "0.6.5\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"0.6.5"}`+"\n")
	var old []string
	for _, p := range []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"} {
		old = append(old, fmt.Sprintf("%064x  dist/obor-0.6.4-dev-%s", len(old)+1, p))
	}
	// stale lines of the version being released, obor's and the kind's: replaced
	stale := strings.Repeat("f", 64) + "  dist/obor-0.6.5-linux-amd64"
	staleKind := strings.Repeat("e", 64) + "  dist/obor-kind-opencode-0.6.5-linux-amd64"
	writeFile(t, filepath.Join(dir, "bin", "checksums.txt"), strings.Join(append(slices.Clone(old), stale, staleKind), "\n")+"\n")
	fake := filepath.Join(dir, "fake-go")
	writeExec(t, fake, `#!/bin/bash
out=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then shift; out=$1; fi
  shift
done
mkdir -p "$(dirname "$out")"
printf '%s/%s\n' "$GOOS" "$GOARCH" > "$out"
`)
	cmd := exec.Command("make", "-C", dir, "release", "GO="+fake)
	cmd.Env = plainMakeEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make release: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(dir, "bin", "checksums.txt")), "\n"), "\n")
	var kept, released, kinds []string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "dist/obor-0.6.4-dev-"):
			kept = append(kept, l)
		case strings.Contains(l, "dist/obor-0.6.5-"):
			released = append(released, l)
		case strings.Contains(l, "dist/obor-kind-opencode-0.6.5-"):
			kinds = append(kinds, l)
		}
	}
	slices.Sort(old)
	if slices.Sort(kept); !slices.Equal(kept, old) {
		t.Errorf("release retains the prior version's exact checksum lines:\n%s\nwant\n%s", kept, old)
	}
	if len(released) != 4 || slices.Contains(released, stale) {
		t.Errorf("release writes exactly four fresh entries for its version (stale one replaced): %q", released)
	}
	if len(kinds) != 4 || slices.Contains(kinds, staleKind) {
		t.Errorf("release writes exactly four fresh entries for the kind (stale one replaced): %q", kinds)
	}
	if len(lines) != 12 {
		t.Errorf("catalog = four old and eight new entries, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !slices.IsSortedFunc(lines, func(a, b string) int { return strings.Compare(fields2(a), fields2(b)) }) {
		t.Errorf("release checksum catalog is sorted by name:\n%s", strings.Join(lines, "\n"))
	}
}

// fields2 is a checksum line's file name.
func fields2(line string) string {
	if f := strings.Fields(line); len(f) > 1 {
		return f[1]
	}
	return ""
}

// hookEvent runs `<shim> hook <name>` in dir with the event JSON on stdin: exit code and stderr.
func (f *shimFixture) hookEvent(t *testing.T, shim, name, tool, cwd string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command("bash", shim, "hook", name)
	cmd.Dir, cmd.Stderr = f.Base, &stderr
	cmd.Env = append(os.Environ(), "HOME="+f.home, "OBOR_NO_SELF_HOST=1")
	cmd.Stdin = strings.NewReader(`{"tool_name":"` + tool + `","cwd":"` + cwd + `","tool_input":{"command":"ls","file_path":"x"}}`)
	rc := 0
	if ee, ok := cmd.Run().(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if cmd.ProcessState == nil {
		t.Fatal("hook did not run")
	}
	return rc, stderr.String()
}

// An obor project with no usable binary: pre-tool-use denies with the repair hint.
func TestShimProjectNoBinary(t *testing.T) {
	t.Parallel()
	f := newShimFixture(t)
	f.Config()
	os.Remove(filepath.Join(f.Root, "dist", "obor"))
	want := "obor: no usable Go binary in " + f.Root + " — run: make -C " + f.Root + " build\n"
	if rc, stderr := f.hookEvent(t, filepath.Join(f.Root, "bin", "obor"), "pre-tool-use", "Edit", f.Root); rc != 2 || stderr != want {
		t.Errorf("obor project: exit %d, %q; want 2, %q", rc, stderr, want)
	}
}
