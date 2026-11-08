//go:build release

package release

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/testkit"
)

// Tests for the Makefile's check target, the merge gate (T-281): build first, a sandbox-safe GOCACHE. A fake go logs
// each call (args and the GOCACHE it got) and answers `go env GOCACHE` with $FAKE_GOCACHE; `go build` fails.

// makeCheck runs `make -s check` on a kit copy with the fake go (no GOCACHE in the environment, TMPDIR the copy); it
// returns the output, whether make failed, the fake go's log lines and TMPDIR.
func makeCheck(t *testing.T, gocache string) (string, bool, []string, string) {
	t.Helper()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	log := filepath.Join(dir, "go.log")
	fake := filepath.Join(dir, "fake-go")
	writeExec(t, fake, `#!/bin/bash
echo "$* GOCACHE=$GOCACHE" >> "`+log+`"
case "$1" in
  env) echo "$FAKE_GOCACHE" ;;
  build) echo "cmd/obor: broken" >&2; exit 1 ;;
esac
`)
	cmd := exec.Command("make", "-s", "-C", dir, "check", "GO="+fake)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOCACHE=") && !strings.HasPrefix(kv, "TMPDIR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "FAKE_GOCACHE="+gocache, "TMPDIR="+dir)
	out, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(log)
	return string(out), err != nil, strings.Split(strings.TrimSpace(string(data)), "\n"), dir
}

// writeKit makes dir a kit copy for `make build`: the tree's Makefile, VERSION 1.2.3 and a fake go at dir/bin/go that
// logs its argv to dir/go.log and answers the affected list (`go run ./tools/affected`) with one package. dir/bin goes
// first on PATH, so a test can stub git there too.
func writeKit(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeExec(t, filepath.Join(dir, "bin", "go"), "#!/bin/bash\necho \"$*\" >> \""+filepath.Join(dir, "go.log")+"\"\n"+
		"case \"$1 $2\" in \"run ./tools/affected\") echo fx/a ;; esac\n")
}

// makeBuild runs `make -s -C dir build` on a kit copy (writeKit) and returns the fake go's logged argv.
func makeBuild(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("make", "-s", "-C", dir, "build", "GO="+filepath.Join(dir, "bin", "go"))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make build: %v\n%s", err, out)
	}
	return strings.TrimSpace(readFile(t, filepath.Join(dir, "go.log")))
}

// stamp is the version makeBuild handed the linker's -X.
func stamp(t *testing.T, build string) string {
	t.Helper()
	const key = "obor/internal/version.Version="
	i := strings.Index(build, key)
	if i < 0 {
		t.Fatalf("precondition: the build stamps %s: %q", key, build)
	}
	v := build[i+len(key):]
	if j := strings.IndexAny(v, " \t"); j >= 0 {
		v = v[:j]
	}
	return v
}

// repo is a throwaway repo that is a kit copy: its files committed, so the test can tag and commit on top.
func repo(t *testing.T) *testkit.Project {
	t.Helper()
	p := testkit.New(t)
	writeKit(t, p.Root)
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "kit")
	return p
}

// releaseRepo is main past the old tag v0.4.0 while the newest release tag, v0.14.0, sits on a release commit off main
// (the release procedure's shape); it returns the repo and main's short sha. It fails unless the fixture reproduces
// the bug the stamp fixes: `git describe` on main names v0.4.0.
func releaseRepo(t *testing.T) (*testkit.Project, string) {
	t.Helper()
	p := repo(t)
	p.Git(p.Root, "tag", "v0.4.0")
	p.Git(p.Root, "checkout", "-q", "-b", "release")
	writeFile(t, filepath.Join(p.Root, "RELEASE"), "0.14.0\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "release v0.14.0")
	p.Git(p.Root, "tag", "v0.14.0")
	p.Git(p.Root, "checkout", "-q", "main")
	writeFile(t, filepath.Join(p.Root, "main.txt"), "main moves on\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "main goes past v0.4.0")
	if d := p.Git(p.Root, "describe", "--tags", "--always", "--dirty"); !strings.HasPrefix(d, "v0.4.0-") {
		t.Fatalf("precondition: describe on main names the old tag (the bug), got %q", d)
	}
	return p, p.Git(p.Root, "rev-parse", "--short", "HEAD")
}

// T-559: the build stamps the tag when HEAD is exactly at one, else the newest release tag by version order plus
// -dev+<short sha> (-dirty when the tree is modified): release tags sit on release commits off main, so describe on
// main names an older tag. Outside a git checkout it falls back to the VERSION file.
func TestMakeBuildStampsVersion(t *testing.T) {
	t.Parallel()
	t.Run("HEAD exactly at a tag", func(t *testing.T) {
		t.Parallel()
		p := repo(t)
		p.Git(p.Root, "tag", "v0.14.0")
		if got := stamp(t, makeBuild(t, p.Root)); got != "v0.14.0" {
			t.Errorf("HEAD at the tag v0.14.0 stamps %q, want %q", got, "v0.14.0")
		}
	})
	t.Run("dirty tree at a tag", func(t *testing.T) {
		t.Parallel()
		p := repo(t)
		p.Git(p.Root, "tag", "v0.14.0")
		writeFile(t, filepath.Join(p.Root, "VERSION"), "1.2.4\n")
		if got := stamp(t, makeBuild(t, p.Root)); got != "v0.14.0-dirty" {
			t.Errorf("a modified tracked file at the tag stamps %q, want %q", got, "v0.14.0-dirty")
		}
	})
	t.Run("past a tag: newest release tag, dev sha", func(t *testing.T) {
		t.Parallel()
		p, sha := releaseRepo(t)
		want := "v0.14.0-dev+" + sha
		if got := stamp(t, makeBuild(t, p.Root)); got != want {
			t.Errorf("main past v0.4.0 stamps %q, want %q", got, want)
		}
	})
	t.Run("dirty tree", func(t *testing.T) {
		t.Parallel()
		p, sha := releaseRepo(t)
		writeFile(t, filepath.Join(p.Root, "main.txt"), "uncommitted\n")
		want := "v0.14.0-dev+" + sha + "-dirty"
		if got := stamp(t, makeBuild(t, p.Root)); got != want {
			t.Errorf("a modified tracked file stamps %q, want %q", got, want)
		}
	})
	t.Run("not a git checkout: VERSION", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		writeKit(t, dir)
		writeExec(t, filepath.Join(dir, "bin", "git"), "#!/bin/bash\nexit 128\n")
		if got := stamp(t, makeBuild(t, dir)); got != "v1.2.3" {
			t.Errorf("outside a checkout the VERSION file stamps %q, want %q", got, "v1.2.3")
		}
	})
	t.Run("a checkout with no release tag: VERSION", func(t *testing.T) {
		t.Parallel()
		p := repo(t)
		if got := stamp(t, makeBuild(t, p.Root)); got != "v1.2.3" {
			t.Errorf("a checkout with no v* tag stamps %q, want the VERSION file's %q", got, "v1.2.3")
		}
	})
}

func TestMakeCheckFailsOnBrokenBuild(t *testing.T) {
	t.Parallel()
	out, failed, calls, _ := makeCheck(t, t.TempDir())
	if !strings.Contains(strings.Join(calls, "\n"), "build ") {
		t.Fatalf("precondition: make check runs go build: %q", calls)
	}
	if !failed || !slices.Contains(strings.Split(out, "\n"), "FAIL make build") {
		t.Errorf("a failing build fails make check with FAIL make build (failed %v):\n%s", failed, out)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "vet ") || strings.HasPrefix(c, "test ") {
			t.Errorf("make check goes on after a failed build: %s", c)
		}
	}
}

func TestMakeCheckGocacheFallback(t *testing.T) {
	t.Parallel()
	writable := filepath.Join(t.TempDir(), "cache")
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	for _, tc := range []struct {
		name, gocache string
		fallback      bool
	}{
		{"writable kept", writable, false},
		{"unwritable falls back", filepath.Join(file, "cache"), true}, // under a regular file: mkdir fails
		{"empty falls back", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, calls, tmp := makeCheck(t, tc.gocache)
			var build string
			for _, c := range calls {
				if strings.HasPrefix(c, "build ") {
					build = c
				}
			}
			if build == "" {
				t.Fatalf("precondition: make check runs go build: %q", calls)
			}
			got := build[strings.LastIndex(build, "GOCACHE=")+len("GOCACHE="):]
			if tc.fallback {
				if got != filepath.Join(tmp, "obor-go-build") {
					t.Errorf("GOCACHE %q: the build gets the $TMPDIR/obor-go-build fallback, got %q", tc.gocache, got)
				}
			} else if got != "" {
				t.Errorf("writable GOCACHE: the build keeps go's default (no override), got %q", got)
			}
		})
	}
}

// makeCheckShort runs `make -s check` on a kit copy whose go logs each call and passes (writeKit) and whose gofmt passes,
// with env added (an inherited CHECK_SHORT, this lane test's own, removed first); it returns the logged go calls.
func makeCheckShort(t *testing.T, env ...string) []string {
	t.Helper()
	dir := testkit.Dir(t)
	writeKit(t, dir)
	log := filepath.Join(dir, "go.log")
	writeExec(t, filepath.Join(dir, "stub", "gofmt"), "#!/bin/bash\nexit 0\n")
	cmd := exec.Command("make", "-s", "-C", dir, "check", "GO="+filepath.Join(dir, "bin", "go"))
	for _, kv := range plainMakeEnv() {
		if !strings.HasPrefix(kv, "CHECK_SHORT=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Join(dir, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make check: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
}

// T-989: CHECK_SHORT=1 makes check pass -short (a lane test runs the short tier whatever the class); without it, or with
// another value, check runs the full suite, as the promotion, the nightly and a release do.
func TestMakeCheckShort(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		env   []string
		short bool
	}{
		{"CHECK_SHORT=1", []string{"CHECK_SHORT=1"}, true},
		{"unset", nil, false},
		{"CHECK_SHORT=0", []string{"CHECK_SHORT=0"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			calls := makeCheckShort(t, c.env...)
			var test string
			for _, call := range calls {
				if strings.HasPrefix(call, "test ") {
					test = call
				}
			}
			if test == "" {
				t.Fatalf("precondition: make check runs go test: %q", calls)
			}
			if got := slices.Contains(strings.Fields(test), "-short"); got != c.short {
				t.Errorf("go test %q passes -short %v, want %v", test, got, c.short)
			}
		})
	}
}

// makeCheckTier runs `make -s <target>` on a kit copy whose go and gofmt log their argv to go.log; the fake go answers
// `go run ./tools/affected` with one package (fx/a), as the kit's tool would. It returns the output, the logged go argv
// lines and the affected list's call ("": the tool was not asked). env is appended to the make environment (an
// inherited CHECK_DIRECT, this lane test's own, removed first).
func makeCheckTier(t *testing.T, target string, env ...string) (string, []string, string) {
	t.Helper()
	dir := testkit.Dir(t)
	writeKit(t, dir)
	log := filepath.Join(dir, "go.log")
	writeExec(t, filepath.Join(dir, "stub", "gofmt"), "#!/bin/bash\necho \"gofmt $*\" >> \""+log+"\"\n")
	cmd := exec.Command("make", "-s", "-C", dir, target, "GO="+filepath.Join(dir, "bin", "go"))
	var base []string
	for _, kv := range plainMakeEnv() {
		if !strings.HasPrefix(kv, "CHECK_DIRECT=") {
			base = append(base, kv)
		}
	}
	cmd.Env = append(base, "PATH="+filepath.Join(dir, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make %s: %v\n%s", target, err, out)
	}
	calls := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	return string(out), calls, goCall(calls, "run ./tools/affected")
}

// T-961/T-995: check-compile (commands.compile, the merge slot's tier) runs the compile steps only — build, gofmt, vet
// and deadcode — with no go test (nor the affected list it would test). The lane's class checks run go test and nothing
// else of the compile tier: check-affected asks tools/affected for the list and runs go test (no build, gofmt, vet or
// deadcode), and check-readers and check-web skip the build too — the slot compiled the same commit.
func TestMakeCheckTiers(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		target   string
		build    bool
		gate     bool // gofmt and go vet
		deadcode bool
		test     bool
		affected bool
	}{
		{"check-compile", true, true, true, false, false},
		{"check-affected", false, false, false, true, true},
		{"check-readers", false, false, false, true, false},
		{"check-web", false, false, false, true, false},
	} {
		t.Run(c.target, func(t *testing.T) {
			t.Parallel()
			out, calls, affected := makeCheckTier(t, c.target)
			ran := func(prefix string) bool {
				return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
			}
			if got := ran("build "); got != c.build {
				t.Errorf("the tier runs go build %v; want %v: %q", got, c.build, calls)
			}
			for _, step := range []string{"vet ", "gofmt -l cmd internal tools"} {
				if got := ran(step); got != c.gate {
					t.Errorf("the tier runs %q %v; want %v: %q", step, got, c.gate, calls)
				}
			}
			if got := ran("run golang.org/x/tools/cmd/deadcode"); got != c.deadcode {
				t.Errorf("the tier runs deadcode %v; want %v: %q", got, c.deadcode, calls)
			}
			var test string
			for _, call := range calls {
				if strings.HasPrefix(call, "test ") {
					test = call
				}
			}
			if got := test != ""; got != c.test {
				t.Errorf("go test ran %v; want %v (calls %q)", got, c.test, calls)
			}
			if c.target == "check-affected" {
				for _, flag := range []string{"-short", "-count=1"} {
					if !slices.Contains(strings.Fields(test), flag) {
						t.Errorf("check-affected's go test %q passes no %s", test, flag)
					}
				}
			}
			if got := affected != ""; got != c.affected {
				t.Errorf("the affected list was asked %v; want %v (only check-affected asks it)", got, c.affected)
			}
			if got := strings.Contains(out, "go test: 0 pass, 0 fail"); got != c.test {
				t.Errorf("the tests' summary printed %v; want %v:\n%s", got, c.test, out)
			}
			if got := strings.Contains(out, "compile: build, gofmt, vet, deadcode OK"); got != c.gate {
				t.Errorf("the compile tier's line printed %v; want %v:\n%s", got, c.gate, out)
			}
		})
	}
}

// goCall is the last logged go call whose text starts with prefix ("" when there is none).
func goCall(calls []string, prefix string) string {
	for i := len(calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(calls[i], prefix) {
			return calls[i]
		}
	}
	return ""
}

// tagged reports whether a logged go call passes tag to -tags.
func tagged(call, tag string) bool {
	f := strings.Fields(call)
	for i, a := range f {
		if a == "-tags" && i+1 < len(f) {
			return slices.Contains(strings.Split(f[i+1], ","), tag)
		}
	}
	return false
}

// T-1014: every check that gates or measures passes -tags integration to go test, go vet and deadcode, so a test behind
// `//go:build integration` keeps running wherever it gates; the worker's plain `make test` / `go test` does not (the next
// test shows that).
func TestMakeChecksPassIntegrationTag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		target string
		env    []string // make variables the target needs
		test   bool
		vet    bool // GO_GATE's vet
		dead   bool // make deadcode
	}{
		{target: "check", test: true, vet: true, dead: true},
		{target: "check-affected", test: true},
		{target: "check-retest", env: []string{"OBOR_FAILED_TESTS=^TestX$", "OBOR_FAILED_PKGS=example.com/pkg"}, test: true},
		{target: "check-readers", test: true},
		{target: "check-web", test: true},
		{target: "check-release", test: true, vet: true},
		{target: "check-compile", vet: true, dead: true},
	} {
		t.Run(c.target, func(t *testing.T) {
			t.Parallel()
			var calls []string
			if c.target == "check" {
				calls = makeCheckShort(t) // the fixture's go passes, so check reaches deadcode too
			} else {
				_, calls, _ = makeCheckTier(t, c.target, c.env...)
			}
			check := func(prefix, step string, want bool) {
				t.Helper()
				call := goCall(calls, prefix)
				if got := call != ""; got != want {
					t.Errorf("%s runs %s %v; want %v: %q", c.target, step, got, want, calls)
					return
				}
				if call != "" && !tagged(call, "integration") {
					t.Errorf("%s: %s %q passes no -tags integration", c.target, step, call)
				}
			}
			check("test ", "go test", c.test)
			check("vet ", "go vet", c.vet)
			check("run golang.org/x/tools/cmd/deadcode", "deadcode", c.dead)
			if c.target == "check-release" && !tagged(goCall(calls, "test "), "release") {
				t.Errorf("check-release dropped its release tag: %q", goCall(calls, "test "))
			}
		})
	}
}

// T-1014: the tag's point — a test behind `//go:build integration` runs in check and check-affected and is left out by a
// plain go test. The fixture is a tiny module whose scratch package has a unit test and a tagged test that fails, so a
// run of the tagged one is visible in the output; its go is the real go except build (it writes the affected-list
// stub), the affected list (it prints the fixture's package) and deadcode.
func TestIntegrationTagRunsInChecksNotInPlainGoTest(t *testing.T) {
	t.Parallel()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/fx\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "scratch", "unit_test.go"),
		"package scratch\n\nimport \"testing\"\n\nfunc TestUnit(t *testing.T) {}\n")
	writeFile(t, filepath.Join(dir, "scratch", "integration_test.go"),
		"//go:build integration\n\npackage scratch\n\nimport \"testing\"\n\n"+
			"func TestIntegration(t *testing.T) { t.Fatal(\"the integration tag's test ran\") }\n")
	writeExec(t, filepath.Join(dir, "bin", "go"), `#!/bin/bash
echo "$*" >> "`+filepath.Join(dir, "go.log")+`"
case "$1" in
  build)
    out=; prev=
    for a; do [ "$prev" != -o ] || out=$a; prev=$a; done
    mkdir -p "$(dirname "$out")" || exit 1
    printf '#!/bin/sh\necho example.com/fx/scratch\n' > "$out" || exit 1
    chmod +x "$out"; exit 0 ;;
  run)
    [ "$2" != ./tools/affected ] || { echo example.com/fx/scratch; exit 0; }
    exit 0 ;; # make deadcode
esac
exec `+goBin+` "$@"
`)
	writeExec(t, filepath.Join(dir, "stub", "gofmt"), "#!/bin/sh\nexit 0\n")
	env := append(plainMakeEnv(), "PATH="+filepath.Join(dir, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	run := func(args ...string) (string, error) {
		cmd := exec.Command(goBin, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("test", "-count=1", "./...")
	if err != nil {
		t.Fatalf("plain go test failed; it must leave the tagged test out: %v\n%s", err, out)
	}
	if strings.Contains(out, "the integration tag's test ran") {
		t.Fatalf("plain go test ran the tagged test:\n%s", out)
	}
	// The precondition: the file is there and fails with the tag on, so the plain run's pass is the tag's doing.
	if out, err := run("test", "-count=1", "-tags", "integration", "./..."); err == nil {
		t.Fatalf("precondition: go test -tags integration passed; the tagged test must fail:\n%s", out)
	} else if !strings.Contains(out, "the integration tag's test ran") {
		t.Fatalf("precondition: go test -tags integration did not run the tagged test:\n%s", out)
	}
	for _, target := range []string{"check", "check-affected"} {
		cmd := exec.Command("make", "-s", "-C", dir, target, "GO="+filepath.Join(dir, "bin", "go"))
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("%s passed; a test behind //go:build integration failed:\n%s", target, out)
			continue
		}
		if !strings.Contains(string(out), "the integration tag's test ran") {
			t.Errorf("%s did not run the tagged test:\n%s", target, out)
		}
	}
}

// T-995: check-affected asks tools/affected for the list and runs go test, dropping the rest of GO_GATE, but the lane
// still catches a vet error: go test runs vet's high-confidence subset (printf here) on the packages it builds, so a
// changed file with a format mistake fails the tier. The fixture's tool is a real build whose binary prints the changed
// package.
func TestMakeCheckAffectedCatchesVetError(t *testing.T) {
	t.Parallel()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module obor\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "tools", "affected", "main.go"),
		"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"obor/bad\") }\n") // the affected-list stub
	writeFile(t, filepath.Join(dir, "bad", "bad.go"),
		"package bad\n\nimport \"fmt\"\n\nfunc Wrong() { fmt.Printf(\"%d\\n\", \"not a number\") }\n")
	cmd := exec.Command("make", "-s", "-C", dir, "check-affected", "GO="+goBin)
	cmd.Env = plainMakeEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("check-affected passed a changed file with a printf error:\n%s", out)
	}
	for _, want := range []string{"affected: 1 package(s): obor/bad", "fmt.Printf", "go test: 0 pass, 1 fail"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the vet failure does not print %q:\n%s", want, out)
		}
	}
	for _, gate := range []string{"FAIL make build", "FAIL gofmt", "FAIL go vet"} {
		if strings.Contains(string(out), gate) {
			t.Errorf("check-affected ran the compile gate (%q):\n%s", gate, out)
		}
	}
}

// T-991: CHECK_DIRECT=1 makes check-affected ask tools/affected for the changed packages only (--direct; serve's lane
// test sets it when branches.promote is); without it, or with another value, the list keeps the importers' expansion.
func TestMakeCheckAffectedDirect(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"CHECK_DIRECT=1", []string{"CHECK_DIRECT=1"}, "run ./tools/affected --direct"},
		{"unset", nil, "run ./tools/affected"},
		{"CHECK_DIRECT=0", []string{"CHECK_DIRECT=0"}, "run ./tools/affected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, _, affected := makeCheckTier(t, "check-affected", c.env...)
			if affected != c.want {
				t.Errorf("check-affected asked %q, want %q", affected, c.want)
			}
		})
	}
}

// T-1001: go test runs with Go's own 10-minute per-package timeout (T-991's TEST_TIMEOUT ?= 20m is gone); the check's
// own commands.check budget still bounds the run.
func TestMakeCheckGoTestNoTimeout(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"check-affected", "check-release"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			_, calls, _ := makeCheckTier(t, target)
			var test string
			for _, c := range calls {
				if strings.HasPrefix(c, "test ") {
					test = c
				}
			}
			if test == "" {
				t.Fatalf("precondition: %s runs go test: %q", target, calls)
			}
			if slices.Contains(strings.Fields(test), "-timeout") {
				t.Errorf("%s: go test %q passes -timeout; want Go's 10-minute default", target, test)
			}
		})
	}
}

// T-964/T-976: check-retest (commands.retest, a red lane test's one rerun) tests only what the red named: the -run
// regexp of OBOR_FAILED_TESTS and the packages of OBOR_FAILED_PKGS, CHECK_CPUS as -p, the other tiers' summary line.
// No -short (a test that failed ran, so the retry runs it as it ran) and no build, gofmt or vet.
func TestMakeCheckRetest(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	log := filepath.Join(dir, "go.log")
	fake := filepath.Join(dir, "fake-go")
	writeExec(t, fake, "#!/bin/bash\necho \"$*\" >> \""+log+"\"\ncase \"$1\" in env) echo \""+dir+"/cache\" ;; esac\n"+
		"echo \"ok example.com/pkg 0.01s\"\n") // go env GOCACHE: a writable cache, so GO_CACHE keeps go's default
	var env []string // the test's own: an inherited OBOR_FAILED_* or CHECK_CPUS must not win over the ones set here
	for _, kv := range plainMakeEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "OBOR_FAILED_") && name != "CHECK_CPUS" {
			env = append(env, kv)
		}
	}
	cmd := exec.Command("make", "-s", "-C", dir, "check-retest", "GO="+fake)
	cmd.Env = append(env, "OBOR_FAILED_TESTS=^TestAlpha$|^TestBeta$",
		"OBOR_FAILED_PKGS=example.com/pkg example.com/other", "CHECK_CPUS=3")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make check-retest: %v\n%s", err, out)
	}
	calls := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	want := "test -count=1 -tags integration -p 3 -run ^TestAlpha$|^TestBeta$ example.com/pkg example.com/other"
	if got := calls[len(calls)-1]; got != want {
		t.Errorf("go test: %q, want %q", got, want)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "build ") || strings.HasPrefix(c, "vet ") {
			t.Errorf("check-retest runs no %q", c)
		}
	}
	if !strings.Contains(string(out), "go test: 1 pass, 0 fail") {
		t.Errorf("the tests' summary missing:\n%s", out)
	}
}

// slowKit is a kit copy (the tree's Makefile and VERSION) beside one dependency-free module, example.com/slow/slow:
// TestSlow skips under -short, as testkit.Slow does, and fails while SLOW_FAILS=1.
func slowKit(t *testing.T) string {
	t.Helper()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/slow\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "slow", "slow_test.go"), `package slow

import (
	"os"
	"testing"
)

func TestSlow(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs in make check")
	}
	if os.Getenv("SLOW_FAILS") == "1" {
		t.Fatal("the failure the retry must see")
	}
}
`)
	return dir
}

// checkRetest runs `make -s check-retest` on the slowKit at dir for its TestSlow with the real go, env added; it
// returns the output and whether make failed.
func checkRetest(t *testing.T, dir string, env ...string) (string, bool) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}
	cmd := exec.Command("make", "-s", "-C", dir, "check-retest", "GO="+goBin,
		"OBOR_FAILED_TESTS=^TestSlow$", "OBOR_FAILED_PKGS=example.com/slow/slow")
	cmd.Env = append(plainMakeEnv(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err != nil
}

// T-976 (RV-135 #1): a failed testkit.Slow test is rerun for real — check-retest passes no -short, so it runs instead
// of skipping; still failing is red, and passing then is green.
func TestMakeCheckRetestRunsSlowTests(t *testing.T) {
	t.Parallel()
	dir := slowKit(t)
	out, failed := checkRetest(t, dir, "SLOW_FAILS=1")
	if !failed {
		t.Fatalf("a Slow test that still fails passed the retry (skipped?):\n%s", out)
	}
	if !strings.Contains(out, "--- FAIL: TestSlow") {
		t.Errorf("the retry ran the Slow test and failed it:\n%s", out)
	}
	if strings.Contains(out, "--- SKIP") {
		t.Errorf("the retry skipped the Slow test:\n%s", out)
	}
	out, failed = checkRetest(t, dir, "SLOW_FAILS=")
	if failed {
		t.Fatalf("the passing retry is green:\n%s", out)
	}
	if !strings.Contains(out, "go test: 1 pass, 0 fail") {
		t.Errorf("the retry's summary missing:\n%s", out)
	}
}

// T-976 (RV-135 #2): check-retest selects GOCACHE as GO_GATE does, so a retry under an unwritable default cache
// (/dev/null here) uses the $TMPDIR fallback and runs instead of failing before any test.
func TestMakeCheckRetestGocacheFallback(t *testing.T) {
	t.Parallel()
	dir := slowKit(t)
	tmp := filepath.Join(dir, "tmp") // not dir itself: go ignores a go.mod in the system temp root
	writeFile(t, filepath.Join(tmp, ".keep"), "")
	out, failed := checkRetest(t, dir, "GOCACHE=/dev/null", "TMPDIR="+tmp)
	if failed {
		t.Fatalf("GOCACHE=/dev/null: the retry used no fallback:\n%s", out)
	}
	if strings.Contains(out, "failed to initialize build cache") {
		t.Errorf("the retry failed on the cache instead of falling back:\n%s", out)
	}
	if !strings.Contains(out, "go test: 1 pass, 0 fail") {
		t.Errorf("the retry's summary missing:\n%s", out)
	}
}

// P7-1: make release refuses a plugin.json version that differs from VERSION, before it builds anything (the plugin
// version keys Claude's cache dir: a mismatch serves the old copy).
func TestMakeReleaseVersionMismatch(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"0.9.9"}`+"\n")
	log := filepath.Join(dir, "go.log")
	fake := filepath.Join(dir, "fake-go")
	writeExec(t, fake, "#!/bin/bash\necho \"$*\" >> \""+log+"\"\n")
	cmd := exec.Command("make", "-s", "-C", dir, "release", "GO="+fake)
	cmd.Env = plainMakeEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("make release with plugin 0.9.9 and VERSION 1.2.3 succeeded:\n%s", out)
	}
	for _, want := range []string{"0.9.9", "1.2.3"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the failure names %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Errorf("make release built before failing the version check: %s", readFile(t, log))
	}
}

var publishPlatforms = []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"}

// releaseMarketplace is the marketplace name a release tag ships (T-592; main says obor-kit-dev).
// It is devMarketplace with only its top-level name flipped (T-645).
var releaseMarketplace = strings.Replace(devMarketplace, `"name": "obor-kit-dev"`, `"name": "obor-kit"`, 1)

// releaseNotes is a release's notes file (docs/releases/v1.2.3.md in publishFixture): a summary line, bold-led bullets
// and an Upgrading line, no Full Changelog line (publish adds that).
const releaseNotes = "A release of the kit.\n\n- **New:** something changed.\n- **Fixed:** a bug.\n\nUpgrading: re-run the installer.\n"

// publishFixture is a repo shaped like a finished release of 1.2.3: the four dist files, bin/checksums.txt listing them,
// VERSION and plugin.json at 1.2.3, the release notes at docs/releases/v1.2.3.md, all committed and tagged v1.2.3; a
// logging gh stub is first on PATH. The stub reads the releases to compare with from releases (one tag per line) and the
// repo slug from slug, both outside the repo, and keeps the notes file gh release create got in notes.
type publishFixture struct {
	*testkit.Project
	log      string
	notes    string
	releases string
	slug     string
}

func newPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	p := testkit.New(t)
	dir := p.Root
	f := &publishFixture{
		Project:  p,
		log:      filepath.Join(p.Base, "gh.log"),
		notes:    filepath.Join(p.Base, "created-notes"),
		releases: filepath.Join(p.Base, "releases"),
		slug:     filepath.Join(p.Base, "slug"),
	}
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeExec(t, filepath.Join(dir, "scripts", "publish"), string(treeFile(t, "scripts/publish")))
	writeFile(t, filepath.Join(dir, "scripts", "release-lib.sh"), string(treeFile(t, "scripts/release-lib.sh")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.3"}`+"\n")
	writeFile(t, filepath.Join(dir, "docs", "releases", "v1.2.3.md"), releaseNotes)
	// The tag's source parent: main's dev marketplace (T-645 compares the tag's against it).
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), devMarketplace+"\n")
	p.Git(dir, "add", "-A")
	p.Git(dir, "commit", "-q", "-m", "obor v1.2.3: VERSION, plugin version")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
	var catalog string
	for _, pl := range publishPlatforms {
		writeFile(t, filepath.Join(dir, "dist", "obor-1.2.3-"+pl), "binary "+pl)
		catalog += fileSum(t, filepath.Join(dir, "dist", "obor-1.2.3-"+pl)) + "  dist/obor-1.2.3-" + pl + "\n"
	}
	writeFile(t, filepath.Join(dir, "bin", "checksums.txt"), catalog)
	log := f.log
	writeExec(t, filepath.Join(dir, "stub", "gh"), `#!/bin/bash
printf '%s\n' "$*" | sed 's/--notes-file [^ ]*/--notes-file <notes>/' >> "`+log+`"
case "$1 $2" in
  "release list") [ ! -f "`+filepath.Join(p.Base, "list-fail")+`" ] || exit 1; cat "`+f.releases+`" 2> /dev/null || true ;;
  "repo view") cat "`+f.slug+`" 2> /dev/null ;;
  "release create")
    prev=
    for a; do
      [ "$prev" != --notes-file ] || cat "$a" > "`+f.notes+`"
      prev=$a
    done ;;
esac
`)
	p.Git(dir, "add", "-A")
	p.Git(dir, "commit", "-q", "-m", "release 1.2.3")
	p.Git(dir, "tag", "v1.2.3")
	return f
}

// fileSum is the sha256 hex of a file.
func fileSum(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(readFile(t, path)))
	return hex.EncodeToString(sum[:])
}

// publish runs `make publish` (plus extra make args) and returns the output, whether it failed and gh's calls, one line
// each (the notes file's path as <notes>; nil when gh was not called).
func (f *publishFixture) publish(t *testing.T, extra ...string) (string, bool, []string) {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-s", "-C", f.Root, "publish"}, extra...)...)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(f.Root, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	data, rerr := os.ReadFile(f.log)
	if rerr != nil {
		return string(out), err != nil, nil
	}
	return string(out), err != nil, strings.Split(strings.TrimSpace(string(data)), "\n")
}

// publishCheck runs `sh scripts/publish <v> --check` in the fixture (the tag checks ship runs before it pushes).
func (f *publishFixture) publishCheck(t *testing.T, v string) (string, bool) {
	t.Helper()
	cmd := exec.Command("sh", "scripts/publish", v, "--check")
	cmd.Dir = f.Root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(f.Root, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err != nil
}

// wantFail asserts a failed publish: FAIL naming want, gh never called.
func wantPublishFail(t *testing.T, out string, failed bool, gh []string, want string) {
	t.Helper()
	if !failed || !strings.Contains(out, "FAIL") || !strings.Contains(out, want) {
		t.Errorf("want a FAIL naming %q (failed %v):\n%s", want, failed, out)
	}
	if gh != nil {
		t.Errorf("gh was called despite the failure: %q", gh)
	}
}

// commitAll commits the working tree as a replacement of the tag's commit, on the same source parent (the tag stays
// where it was until a test moves it): the tag's marketplace is still compared with main's dev one (T-645).
func (f *publishFixture) commitAll(t *testing.T) {
	t.Helper()
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "--amend", "-m", "release 1.2.3, changed")
}

// RV-63: make publish uploads exactly the four checked binaries of the tag: a literal platform list passed to gh as
// explicit arguments (no glob, no GOOS_LIST override), each file's sha256 equal to its line in the tag's catalog.
// T-682: the create carries the release's title and its notes from the tag.
func TestMakePublish(t *testing.T) {
	t.Parallel()
	create := "release create v1.2.3 --verify-tag --title obor 1.2.3 --notes-file <notes>"
	for _, pl := range publishPlatforms {
		create += " dist/obor-1.2.3-" + pl
	}
	wantCalls := []string{"release list --limit 100 --json tagName --jq .[].tagName", create}
	t.Run("control: release, commit catalog, tag, publish", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		out, failed, gh := f.publish(t)
		if failed || !slices.Equal(gh, wantCalls) {
			t.Errorf("publish failed %v, gh got %q; want %q\n%s", failed, gh, wantCalls, out)
		}
	})
	t.Run("an extra .bak file is not uploaded", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, "dist", "obor-1.2.3-darwin-arm64.bak"), "old")
		if !f.Exists(filepath.Join(f.Root, "dist", "obor-1.2.3-darwin-arm64.bak")) {
			t.Fatal("precondition: the extra file exists")
		}
		out, failed, gh := f.publish(t)
		if failed || !slices.Equal(gh, wantCalls) {
			t.Errorf("publish failed %v, gh got %q; want exactly %q\n%s", failed, gh, wantCalls, out)
		}
	})
	t.Run("GOOS_LIST/GOARCH_LIST overrides do not narrow the four", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		for _, pl := range publishPlatforms[1:] {
			if err := os.Remove(filepath.Join(f.Root, "dist", "obor-1.2.3-"+pl)); err != nil {
				t.Fatal(err)
			}
		}
		out, failed, gh := f.publish(t, "GOOS_LIST=darwin", "GOARCH_LIST=arm64")
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-darwin-amd64")
	})
	t.Run("a missing file", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		if err := os.Remove(filepath.Join(f.Root, "dist", "obor-1.2.3-linux-amd64")); err != nil {
			t.Fatal(err)
		}
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-linux-amd64")
	})
	t.Run("an artifact altered after release", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, "dist", "obor-1.2.3-linux-arm64"), "tampered")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-linux-arm64")
	})
	t.Run("the tag's catalog lacks a line", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		catalog := readFile(t, filepath.Join(f.Root, "bin", "checksums.txt"))
		var kept []string
		for _, l := range strings.Split(strings.TrimSpace(catalog), "\n") {
			if !strings.HasSuffix(l, "-linux-amd64") {
				kept = append(kept, l)
			}
		}
		writeFile(t, filepath.Join(f.Root, "bin", "checksums.txt"), strings.Join(kept, "\n")+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		// the working catalog is complete again: only the tag's lacks the line
		writeFile(t, filepath.Join(f.Root, "bin", "checksums.txt"), catalog)
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-linux-amd64")
	})
	t.Run("the working catalog is right but the tag's differs", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		path := filepath.Join(f.Root, "bin", "checksums.txt")
		good := readFile(t, path)
		writeFile(t, path, strings.Replace(good, fileSum(t, filepath.Join(f.Root, "dist", "obor-1.2.3-darwin-arm64")), strings.Repeat("0", 64), 1))
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, path, good)
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-darwin-arm64")
	})
	t.Run("the tag's plugin version differs from VERSION", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.2"}`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.3"}`+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "plugin.json")
	})
	t.Run("the tag's marketplace name differs", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		// main's name: the dev checkout's marketplace, which a release tag must rename before publishing.
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), `{"name":"obor-kit-dev","plugins":[{"name":"obor","source":"./"}]}`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "marketplace.json")
	})
	// T-645: v0.17.2's flip renamed every "name": the plugin entry too, so obor@obor-kit no longer resolved.
	t.Run("the tag's flip renamed the plugin entry too", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), v0172Marketplace+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "plugin name at v1.2.3 is 'obor-kit', want 'obor'")
	})
	t.Run("the tag's marketplace differs from main's beyond the name", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"),
			`{"name": "obor-kit","owner":{"name":"obor-kit"},"plugins":[{"name":"obor","source":"./"}]}`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "differs from its source's in more than the top-level name")
	})
	t.Run("the tag's marketplace does not parse", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), `{"name": "obor-kit",`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "does not parse")
	})
	t.Run("the tag's VERSION differs", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, "VERSION"), "1.2.2\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, "VERSION"), "1.2.3\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "VERSION")
	})
	t.Run("no tag", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		f.Git(f.Root, "tag", "-d", "v1.2.3")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "v1.2.3")
	})
}

// T-817, T-1027: commands.trim_go_cache runs make trim-go-cache, which deletes every file in the Go build cache — however
// recently used (the owner's 11-08 decision: the nightly empties it; at 18-25 GB a 48 h trim freed nothing) — and
// prints "go cache: freed <size>, <size> left". It keeps the 00-ff directories and README: a Go command running beside
// it sees misses, not a missing cache directory. GOCACHE names a throwaway cache: a test never trims the machine's.
// (Moved here from internal/checks, T-902: it tests the kit's Makefile, not obor.)
func TestOborKitTrimGoCache(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../..")
	cache := t.TempDir()
	env := append(os.Environ(), "GOCACHE="+cache)
	// Precondition: the target trims the cache this test made, never the machine's — it reads GOCACHE with `go env`.
	probe := exec.Command("go", "env", "GOCACHE")
	probe.Env = env
	got, err := probe.Output()
	if err != nil {
		t.Skipf("no go to ask for GOCACHE: %v", err)
	}
	if want := cache; strings.TrimSpace(string(got)) != want {
		t.Fatalf("precondition: go env GOCACHE = %q, want the test's %q", got, want)
	}
	// cacheFile is a cache entry of size bytes, last used age ago.
	cacheFile := func(name string, size int64, age time.Duration) string {
		t.Helper()
		path := filepath.Join(cache, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, size); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil { // after the truncate: it sets the mtime
			t.Fatal(err)
		}
		return path
	}
	old := cacheFile("aa/aa-1-a", 3<<20, 72*time.Hour)
	fresh := cacheFile("bb/bb-1-b", 2<<20, time.Hour)
	readme := cacheFile("README", 100, time.Hour)
	for _, p := range []string{old, fresh, readme} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("precondition: %v", err)
		}
	}
	cmd := exec.Command("make", "-s", "-C", repo, "trim-go-cache")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make trim-go-cache: %v\n%s", err, out)
	}
	for _, p := range []string{old, fresh} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the entry %s is still there (%v): every entry goes, however recently used", p, err)
		}
	}
	for _, d := range []string{filepath.Join(cache, "aa"), filepath.Join(cache, "bb")} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("the cache's directory %s is gone (%v): a Go command beside the trim sees misses, not a missing cache", d, err)
		}
	}
	if _, err := os.Stat(readme); err != nil {
		t.Errorf("README is gone (%v): the trim deletes the entries, not the cache's own file", err)
	}
	if want := "go cache: freed 5.0 MiB, 100 B left"; !strings.Contains(string(out), want) {
		t.Errorf("output %q lacks %q", out, want)
	}
}

// T-1027: the Makefile exports GOFLAGS with -trimpath for every target's Go command, keeping a GOFLAGS from the
// environment (a lane's: config.LaneEnv sets -p and -trimpath) and adding -trimpath once, never twice. The go the target
// runs is a stub that records the GOFLAGS it got; trim-go-cache is the cheapest target that calls go.
func TestOborKitGoFlagsTrimpath(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../..")
	for _, c := range []struct {
		name    string
		goflags string // GOFLAGS in the environment
		want    string
	}{
		{"none", "", "-trimpath"},
		{"a lane's -p kept", "-p=2", "-p=2 -trimpath"},
		{"already there: not doubled", "-p=2 -trimpath", "-p=2 -trimpath"},
		{"other flags kept in front", "-mod=mod", "-mod=mod -trimpath"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			record, stub := filepath.Join(dir, "goflags"), filepath.Join(dir, "go")
			missing := filepath.Join(dir, "no-cache")
			writeExec(t, stub, `#!/bin/sh
printf '%s' "$GOFLAGS" > "`+record+`"
echo "`+missing+`"
`)
			// Precondition: the stub names a cache that does not exist, so the target stops after `go env GOCACHE` and the
			// only go call is the stub's; and the record is absent until it writes it.
			if _, err := os.Stat(missing); !os.IsNotExist(err) {
				t.Fatalf("precondition: the stub's cache exists: %v", err)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatalf("precondition: %s exists before make: %v", record, err)
			}
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "GOFLAGS=") {
					env = append(env, kv)
				}
			}
			env = append(env, "GOFLAGS="+c.goflags)
			cmd := exec.Command("make", "-s", "-C", repo, "trim-go-cache", "GO="+stub)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make trim-go-cache: %v\n%s", err, out)
			}
			b, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("the Makefile ran no go command: %v\n%s", err, out)
			}
			if got := string(b); got != c.want {
				t.Errorf("GOFLAGS=%q, want %q", got, c.want)
			}
		})
	}
}
