package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// SubDirs names a dir's subdirectories, a symlink to a dir among them, and no file; a missing dir is an error.
func TestSubDirs(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	got, err := SubDirs(dir)
	if err != nil || !slices.Equal(got, []string{"a", "linked"}) {
		t.Errorf("SubDirs = %q, %v; want a and linked", got, err)
	}
	if _, err := SubDirs(filepath.Join(dir, "none")); err == nil {
		t.Error("a missing dir lists without an error")
	}
}
