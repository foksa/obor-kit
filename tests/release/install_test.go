//go:build release

package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// Tests for install.sh (P7-1): a local kit repo with two release tags, whose bin/release-url is a file:// directory with
// the release binary and its checksum line, installed into a temp HOME. The tree's bin/obor runs, so `obor version`
// really fetches and checksum-checks the release.

// installFixture is the kit source repo (tags v0.1.0, v0.2.0), its bare clone (what OBOR_KIT_REPO names), the release
// download dir, the install script (the tree's install.sh) and a temp HOME.
type installFixture struct {
	*testkit.Project
	src, bare, download, home, script string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	f := &installFixture{
		Project:  testkit.New(t),
		download: filepath.Join(testkit.Dir(t), "download"),
		home:     filepath.Join(testkit.Dir(t), "home"),
	}
	f.src = f.Root
	f.bare = filepath.Join(filepath.Dir(f.download), "kit.git")
	f.script = treePath(t, "install.sh")
	for _, v := range []string{"0.1.0", "0.2.0"} {
		f.commitVersion(t, v)
	}
	f.Git(filepath.Dir(f.bare), "clone", "-q", "--bare", f.src, f.bare)
	return f
}

// treePath is an absolute path to a file of the kit checkout these tests run in.
func treePath(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// commitVersion writes a kit at version v into the source repo and tags it: the tree's shim and command list, VERSION v,
// a file:// release URL, and the checksum lines for a fake release binary and a fake OpenCode kind binary it
// places in the download dir.
func (f *installFixture) commitVersion(t *testing.T, v string) {
	t.Helper()
	asset := shimAsset(v)
	bin := filepath.Join(f.download, asset)
	writeExec(t, bin, "#!/bin/bash\necho \"release binary $*\"\n")
	sum := sha256.Sum256([]byte(readFile(t, bin)))
	kind := kindAsset(v)
	writeExec(t, filepath.Join(f.download, kind), "#!/bin/sh\necho kind "+v+"\n")
	kindSum := sha256.Sum256([]byte(readFile(t, filepath.Join(f.download, kind))))
	writeExec(t, filepath.Join(f.src, "bin", "obor"), string(treeFile(t, "bin/obor")))
	writeFile(t, filepath.Join(f.src, "bin", "go-commands"), string(treeFile(t, "bin/go-commands")))
	writeFile(t, filepath.Join(f.src, "VERSION"), v+"\n")
	writeFile(t, filepath.Join(f.src, "bin", "release-url"), "file://"+f.download+"/obor-%VERSION%-%OS%-%ARCH%\n")
	writeFile(t, filepath.Join(f.src, "bin", "checksums.txt"), fmt.Sprintf("%s  %s\n%s  dist/%s\n", hex.EncodeToString(sum[:]),
		asset, hex.EncodeToString(kindSum[:]), kind))
	f.Git(f.src, "add", "-A")
	f.Git(f.src, "commit", "-q", "-m", "release v"+v)
	f.Git(f.src, "tag", "v"+v)
}

// install runs the tree's install.sh under sh (not bash: it must be POSIX) in the base dir (outside any repo), with HOME
// and OBOR_KIT_REPO for the fixture and path as the child's PATH: exit code, stdout, stderr.
func (f *installFixture) install(t *testing.T, path string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("sh", f.script)
	cmd.Dir, cmd.Stdout, cmd.Stderr = filepath.Dir(f.bare), &stdout, &stderr
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "HOME" && k != "OBOR_KIT_REPO" && k != "PATH" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+f.home, "OBOR_KIT_REPO="+f.bare, "PATH="+path)
	err := cmd.Run()
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, stdout.String(), stderr.String()
}

// kitDir is the installed checkout, link the ~/.local/bin/obor symlink.
func (f *installFixture) kitDir() string { return filepath.Join(f.home, ".obor", "kit") }
func (f *installFixture) link() string   { return filepath.Join(f.home, ".local", "bin", "obor") }

// P7-1: bin/release-url points at the public repo's GitHub Releases (the placeholder here meant a fresh user's plugin
// copy, which has no dist/obor, could fetch no binary at all).
func TestReleaseURLLocation(t *testing.T) {
	t.Parallel()
	want := "https://github.com/foksa/obor-kit/releases/download/v%VERSION%/obor-%VERSION%-%OS%-%ARCH%\n"
	if got := string(treeFile(t, "bin/release-url")); got != want {
		t.Errorf("bin/release-url = %q, want %q", got, want)
	}
}

// P7-1: a fresh install clones the kit, checks out the newest release tag, links obor into ~/.local/bin and runs `obor
// version` once (the shim fetches the release binary and checks its checksum).
func TestInstallFresh(t *testing.T) {
	t.Parallel()
	f := newInstallFixture(t)
	if f.Exists(f.kitDir()) {
		t.Fatal("precondition: no kit checkout yet")
	}
	rc, stdout, stderr := f.install(t, os.Getenv("PATH"))
	if rc != 0 {
		t.Fatalf("install: exit %d\nstdout: %s\nstderr: %s", rc, stdout, stderr)
	}
	if tag := f.Git(f.kitDir(), "describe", "--tags", "--exact-match"); tag != "v0.2.0" {
		t.Errorf("kit checkout at %q, want the newest tag v0.2.0", tag)
	}
	if target, err := os.Readlink(f.link()); err != nil || target != filepath.Join(f.kitDir(), "bin", "obor") {
		t.Errorf("~/.local/bin/obor -> %q (%v), want %q", target, err, filepath.Join(f.kitDir(), "bin", "obor"))
	}
	if !strings.Contains(stdout, "release binary version") {
		t.Errorf("`obor version` did not run the fetched v0.2.0 release:\n%s", stdout)
	}
	if !f.Exists(filepath.Join(f.home, ".obor", "bin", shimAsset("0.2.0"))) {
		t.Errorf("the v0.2.0 release binary was not fetched into ~/.obor/bin")
	}
	// the kind binary the catalog lists lands beside it, checksum-checked, from the same release dir
	if got := readFile(t, filepath.Join(f.home, ".obor", "bin", kindAsset("0.2.0"))); got != "#!/bin/sh\necho kind 0.2.0\n" {
		t.Errorf("the v0.2.0 OpenCode kind binary beside obor's: %q", got)
	}
}

// P7-1: re-running install.sh after a new tag is the upgrade: the checkout moves to it and the new release is fetched.
func TestInstallUpgrades(t *testing.T) {
	t.Parallel()
	f := newInstallFixture(t)
	if rc, _, stderr := f.install(t, os.Getenv("PATH")); rc != 0 {
		t.Fatalf("first install: exit %d\n%s", rc, stderr)
	}
	if tag := f.Git(f.kitDir(), "describe", "--tags", "--exact-match"); tag != "v0.2.0" {
		t.Fatalf("precondition: the first install is at %q, want v0.2.0", tag)
	}
	f.commitVersion(t, "0.3.0")
	f.Git(f.src, "push", "-q", f.bare, "v0.3.0")
	rc, stdout, stderr := f.install(t, os.Getenv("PATH"))
	if rc != 0 {
		t.Fatalf("second install: exit %d\nstdout: %s\nstderr: %s", rc, stdout, stderr)
	}
	if tag := f.Git(f.kitDir(), "describe", "--tags", "--exact-match"); tag != "v0.3.0" {
		t.Errorf("re-running install moved the checkout to %q, want v0.3.0", tag)
	}
	if !f.Exists(filepath.Join(f.home, ".obor", "bin", shimAsset("0.3.0"))) {
		t.Errorf("the v0.3.0 release binary was not fetched into ~/.obor/bin")
	}
}

// P7-1: a missing release file fails the install (non-zero) with the shim's fetch message: the last step is what proves
// the release exists.
func TestInstallMissingRelease(t *testing.T) {
	t.Parallel()
	f := newInstallFixture(t)
	asset := shimAsset("0.2.0")
	if !f.Exists(filepath.Join(f.download, asset)) {
		t.Fatal("precondition: the release file exists before it is removed")
	}
	if err := os.Remove(filepath.Join(f.download, asset)); err != nil {
		t.Fatal(err)
	}
	rc, stdout, stderr := f.install(t, os.Getenv("PATH"))
	if rc == 0 {
		t.Fatalf("install succeeded with no release file:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if want := "FAIL cannot fetch " + asset; !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr %q; want the shim's %q", rc, stderr, want)
	}
	if strings.Contains(stdout, "release binary version") {
		t.Errorf("the install reported a version it never fetched:\n%s", stdout)
	}
}

// P7-1: the one export PATH line is printed when ~/.local/bin is not on PATH, and not when it is; nothing else touches
// the shell's rc.
func TestInstallPathLine(t *testing.T) {
	t.Parallel()
	f := newInstallFixture(t)
	bindir := filepath.Dir(f.link())
	path := os.Getenv("PATH")
	for _, p := range filepath.SplitList(path) {
		if p == bindir {
			t.Fatalf("precondition: the temp bindir %s is not on PATH", bindir)
		}
	}
	rc, stdout, stderr := f.install(t, path)
	if rc != 0 {
		t.Fatalf("install without %s on PATH: exit %d\n%s", bindir, rc, stderr)
	}
	if want := `export PATH="` + bindir + `:$PATH"`; !strings.Contains(stdout, want) {
		t.Errorf("~/.local/bin off PATH: no %q line:\n%s", want, stdout)
	}
	rc, stdout, stderr = f.install(t, bindir+string(os.PathListSeparator)+path)
	if rc != 0 {
		t.Fatalf("install with %s on PATH: exit %d\n%s", bindir, rc, stderr)
	}
	if strings.Contains(stdout, "export PATH=") {
		t.Errorf("~/.local/bin on PATH: the install printed a PATH line:\n%s", stdout)
	}
}

// a directory (or a symlink to one) where the ~/.local/bin/obor link goes fails the install naming it, before
// `ln` could drop a link inside it, and the directory is left as it was.
func TestInstallDirectoryAtLink(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"directory", "symlink to a directory"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newInstallFixture(t)
			target := f.link()
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			keep := target
			if kind == "directory" {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				keep = filepath.Join(f.home, "elsewhere")
				if err := os.MkdirAll(keep, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(keep, target); err != nil {
					t.Fatal(err)
				}
			}
			rc, stdout, stderr := f.install(t, os.Getenv("PATH"))
			if rc == 0 || !strings.Contains(stderr, "FAIL") || !strings.Contains(stderr, target) {
				t.Errorf("exit %d; want non-zero and a FAIL naming %s\nstdout: %s\nstderr: %s", rc, target, stdout, stderr)
			}
			if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
				t.Errorf("%s is no longer a directory: %v", target, err)
			}
			if entries, _ := os.ReadDir(keep); len(entries) != 0 {
				t.Errorf("the install put %d entries into %s", len(entries), keep)
			}
		})
	}
}
