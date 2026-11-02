package selfexec

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// kit is a temp kit with VERSION 1.2.3 and a home; bin writes an executable script at path.
type kit struct {
	t          *testing.T
	root, home string
}

func newKit(t *testing.T) *kit {
	t.Helper()
	k := &kit{t: t, root: t.TempDir(), home: t.TempDir()}
	if err := os.WriteFile(filepath.Join(k.root, "VERSION"), []byte("1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return k
}

// dev is the kit's dist/obor; fetched the shim's binary for VERSION.
func (k *kit) dev() string { return filepath.Join(k.root, "dist", "obor") }
func (k *kit) fetched() string {
	return filepath.Join(k.home, ".obor", "bin", "obor-1.2.3-"+runtime.GOOS+"-"+runtime.GOARCH)
}

// bin writes an executable script at path whose `version` exits code, its mtime a minute ago (past the quiet period).
func (k *kit) bin(path string, code string) {
	k.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		k.t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("#!/bin/sh\nexit "+code+"\n"), 0o755); err != nil {
		k.t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil { // a new inode, as the shim's fetch and go build -o write it
		k.t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		k.t.Fatal(err)
	}
}

// The candidate is the shim's rule: dist/obor when executable, else the fetched binary of the kit's VERSION; no
// VERSION, none.
func TestCandidate(t *testing.T) {
	k := newKit(t)
	if got := Candidate(k.root, k.home); got != k.fetched() {
		t.Errorf("no dist/obor: candidate %q, want the fetched binary %q", got, k.fetched())
	}
	if err := os.MkdirAll(filepath.Dir(k.dev()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(k.dev(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Candidate(k.root, k.home); got != k.fetched() {
		t.Errorf("dist/obor not executable: candidate %q, want the fetched binary", got)
	}
	k.bin(k.dev(), "0")
	if got := Candidate(k.root, k.home); got != k.dev() {
		t.Errorf("dist/obor executable: candidate %q, want %q", got, k.dev())
	}
	if got := Candidate(t.TempDir(), k.home); got != "" {
		t.Errorf("a kit without VERSION or dist/obor: candidate %q, want none", got)
	}
}

// The watch follows only a process running the kit's candidate; it fires on a new inode at the candidate's path (a
// rebuild) and on another candidate (dist/obor removed: the fetched binary), not on the same file.
func TestChangedOnNewInode(t *testing.T) {
	k := newKit(t)
	k.bin(k.dev(), "0")
	w := watchOf(k.root, k.home, k.dev())
	if !w.On() {
		t.Fatal("precondition: the watch of the kit's candidate is on")
	}
	if w.Changed() {
		t.Fatal("the running binary is the candidate: changed, want not")
	}
	k.bin(k.dev(), "0") // a rebuild: a new inode at the same path
	if !w.Changed() {
		t.Error("dist/obor rebuilt: not changed, want changed")
	}
	k.bin(k.fetched(), "0")
	if err := os.Remove(k.dev()); err != nil {
		t.Fatal(err)
	}
	if !w.Changed() {
		t.Error("dist/obor gone, the fetched binary there: not changed, want changed")
	}
	other := filepath.Join(t.TempDir(), "obor")
	k.bin(other, "0")
	if off := watchOf(k.root, k.home, other); off.On() || off.Changed() {
		t.Error("a binary that is not the kit's candidate (a lane or test build): the watch is on, want off")
	}
}

// A candidate that is missing (a kit checked out at a new tag before its binary is fetched) is no change.
func TestChangedMissingBinary(t *testing.T) {
	k := newKit(t)
	k.bin(k.fetched(), "0")
	w := watchOf(k.root, k.home, k.fetched())
	if !w.On() {
		t.Fatal("precondition: the watch of the fetched binary is on")
	}
	if err := os.WriteFile(filepath.Join(k.root, "VERSION"), []byte("1.2.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := Candidate(k.root, k.home); c == k.fetched() {
		t.Fatal("precondition: the new VERSION names another binary")
	}
	if w.Changed() {
		t.Error("the new VERSION's binary is not fetched: changed, want not")
	}
	k.bin(Candidate(k.root, k.home), "0")
	if !w.Changed() {
		t.Error("the new VERSION's binary fetched: not changed, want changed")
	}
}

// A candidate whose `version` fails is no change, and it is not probed again until it changes.
func TestChangedFailingProbe(t *testing.T) {
	k := newKit(t)
	k.bin(k.dev(), "0")
	w := watchOf(k.root, k.home, k.dev())
	k.bin(k.dev(), "3")
	if w.Changed() {
		t.Fatal("a candidate whose version exits 3: changed, want not")
	}
	if w.bad == nil {
		t.Fatal("the failed candidate is not remembered")
	}
	// Made to pass in place (same inode, same mtime): still the remembered failure, not probed again.
	old, _ := os.Stat(k.dev())
	if err := os.WriteFile(k.dev(), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(k.dev(), old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	if w.Changed() {
		t.Error("the failed candidate unchanged: probed again")
	}
	k.bin(k.dev(), "0")
	if !w.Changed() {
		t.Error("a new candidate after a failed one: not changed, want changed")
	}
}

// A candidate younger than the quiet period may still be written: no change until it is Quiet old.
func TestChangedQuietPeriod(t *testing.T) {
	k := newKit(t)
	k.bin(k.dev(), "0")
	w := watchOf(k.root, k.home, k.dev())
	k.bin(k.dev(), "0")
	fi, err := os.Stat(k.dev())
	if err != nil {
		t.Fatal(err)
	}
	w.Now = func() time.Time { return fi.ModTime().Add(Quiet - time.Second) }
	if w.Changed() {
		t.Error("a candidate written 4 s ago: changed, want not")
	}
	w.Now = func() time.Time { return fi.ModTime().Add(Quiet) }
	if !w.Changed() {
		t.Error("a candidate written 5 s ago: not changed, want changed")
	}
}
