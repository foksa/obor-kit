package launch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"obor/internal/project"
)

// T-588: the default Open (lsof, or /proc/<pid>/fd) names a file this process holds open, by its physical path, and
// that path matches the transcript glob spelled through a symlinked directory (macOS: /var is /private/var).
func TestOpenFilesOfARealProcess(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "sessions", "2026", "10", "30")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-30T10-00-00-cx-real.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	open := (&Launcher{}).openFiles([]int{os.Getpid()})
	phys := project.Physical(path)
	if !slices.Contains(open, phys) {
		t.Fatalf("%s not among this process's open files: %q", phys, open)
	}
	glob := filepath.Join(link, "sessions", "*", "*", "*", "rollout-*-*.jsonl")
	if ok, _ := filepath.Match(glob, phys); ok {
		t.Fatalf("precondition: the glob %s spells another path than %s", glob, phys)
	}
	if !transcriptPath(glob, phys) {
		t.Errorf("%s does not match %s through its symlink", phys, glob)
	}
	if transcriptPath(glob, filepath.Join(filepath.Dir(phys), "notes.txt")) {
		t.Error("a file that is no rollout matched")
	}
}
