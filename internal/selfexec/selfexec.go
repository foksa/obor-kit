// Package selfexec lets a long-running obor process (serve, web) follow a new obor binary: it tells
// when the binary the kit's shim would run now is another file than the running one, and execs the shim in place.
package selfexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The watch's bounds: a candidate younger than Quiet may still be written (an install, a build); its `version` probe
// gets Probe to exit 0 (the shim's stale-binary probe, bin/obor has_hook).
const (
	Quiet = 5 * time.Second
	Probe = 2 * time.Second
)

// Candidate is the binary kit's shim (bin/obor) would exec now: <kit>/dist/obor when it is executable, else
// <home>/.obor/bin/obor-<VERSION>-<os>-<arch> ("": the kit has no VERSION). It may not exist (the shim fetches it).
func Candidate(kit, home string) string {
	dev := filepath.Join(kit, "dist", "obor")
	if executable(dev) {
		return dev
	}
	b, err := os.ReadFile(filepath.Join(kit, "VERSION"))
	ver := strings.TrimSpace(string(b))
	if err != nil || ver == "" {
		return ""
	}
	return filepath.Join(home, ".obor", "bin", fmt.Sprintf("obor-%s-%s-%s", ver, runtime.GOOS, runtime.GOARCH))
}

// executable: path is a regular file with an execute bit.
func executable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// Watch follows kit's binary for the running process. It is on only when the running executable is the kit's
// candidate when the watch is made: a test binary, `go run` or a lane's own build (OBOR_NO_SELF_HOST) never follows.
type Watch struct {
	Kit, Home string
	Now       func() time.Time // nil: time.Now

	self os.FileInfo // the running binary's file (nil: the watch is off)
	mu   sync.Mutex
	bad  os.FileInfo // the last candidate whose probe failed: not probed again until it changes
}

// New is the watch of kit's binary (home: $HOME, where the shim keeps its fetched binaries) for this process.
func New(kit, home string) *Watch {
	exe, _ := os.Executable()
	return watchOf(kit, home, exe)
}

// watchOf is New for the running binary exe ("": unknown, the watch is off).
func watchOf(kit, home, exe string) *Watch {
	w := &Watch{Kit: kit, Home: home}
	if exe != "" && kit != "" {
		w.self = sameAs(exe, Candidate(kit, home))
	}
	return w
}

// sameAs is exe's file when cand is the same file (dev and inode), else nil.
func sameAs(exe, cand string) os.FileInfo {
	if cand == "" {
		return nil
	}
	fe, err1 := os.Stat(exe)
	fc, err2 := os.Stat(cand)
	if err1 != nil || err2 != nil || !os.SameFile(fe, fc) {
		return nil
	}
	return fe
}

// On: the watch follows a binary (the running one was the kit's candidate at New).
func (w *Watch) On() bool { return w != nil && w.self != nil }

// Changed: the kit's candidate now is another file than the running binary: it exists, is executable, its mtime is
// at least Quiet old, and `<candidate> version` exits 0 within Probe. A rollback counts as much as an upgrade (no
// version order); after the exec the running binary is the candidate, so it is false again.
func (w *Watch) Changed() bool {
	if !w.On() {
		return false
	}
	cand := Candidate(w.Kit, w.Home)
	if cand == "" || !executable(cand) {
		return false
	}
	fi, err := os.Stat(cand)
	if err != nil || os.SameFile(fi, w.self) {
		return false
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	if now().Sub(fi.ModTime()) < Quiet {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bad != nil && os.SameFile(fi, w.bad) && fi.ModTime().Equal(w.bad.ModTime()) {
		return false
	}
	if !probe(cand) {
		w.bad = fi
		return false
	}
	return true
}

// probe: `<bin> version` exits 0 within Probe.
func probe(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), Probe)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.WaitDelay = time.Second
	return cmd.Run() == nil
}

// Shim is kit's shim, <kit>/bin/obor: what Exec runs.
func Shim(kit string) string { return filepath.Join(kit, "bin", "obor") }

// Exec replaces this process with kit's shim, <kit>/bin/obor args, in the same environment and directory: it returns
// only on failure. The shim picks the binary (Candidate). Go opens every file close-on-exec, so locks, sockets and
// listeners are released by the exec.
func Exec(kit string, args []string) error {
	shim := Shim(kit)
	return syscall.Exec(shim, append([]string{shim}, args...), os.Environ())
}
