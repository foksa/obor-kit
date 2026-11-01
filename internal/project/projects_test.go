package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// configAt puts a minimal obor config naming name at root.
func configAt(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"name":"`+name+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stateEnv is the environment of a machine's state home under t's temp dir.
func stateEnv(t *testing.T) Env {
	t.Helper()
	home := filepath.Join(t.TempDir(), "state")
	return func(key string) (string, bool) {
		if key == "OBOR_STATE_HOME" {
			return home, true
		}
		return "", false
	}
}

// T-646: the machine's list of obor projects by name: obor serve and obor init register a project under its config
// name, and obor send's names (Projects, RootOf) read it back from <state home>/projects.json.
func TestProjectsByName(t *testing.T) {
	t.Parallel()
	env := stateEnv(t)
	if got := Projects(env); len(got) != 0 {
		t.Fatalf("precondition: %+v known", got)
	}
	dir := t.TempDir()
	axis, kit := filepath.Join(dir, "axis-alies"), filepath.Join(dir, "obor-kit")
	configAt(t, axis, "axis-alies")
	configAt(t, kit, "")
	if _, err := Register(axis, env); err != nil {
		t.Fatal(err)
	}
	if root, ok := RootOf("axis-alies", env); !ok || root != Physical(axis) {
		t.Errorf("RootOf(axis-alies) = %q, %v; want %q", root, ok, Physical(axis))
	}
	if _, ok := RootOf("nobody", env); ok {
		t.Error("RootOf(nobody) is known")
	}
	if _, err := Register(kit, env); err != nil { // no name in the config: the directory's base name
		t.Fatal(err)
	}
	got := Projects(env)
	if len(got) != 2 || got[0] != (Project{"axis-alies", Physical(axis)}) || got[1] != (Project{"obor-kit", Physical(kit)}) {
		t.Errorf("Projects = %+v; want both, by name", got)
	}
	if changed, err := Register(axis, env); err != nil || changed {
		t.Errorf("a second register of axis-alies: changed %v, %v; want the list as it was", changed, err)
	}
	b, err := os.ReadFile(ProjectsFile(env))
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]string
	if err := json.Unmarshal(b, &list); err != nil || len(list) != 2 || list["axis-alies"] != Physical(axis) {
		t.Errorf("the file %q: %v; want {\"<name>\": \"<root>\"}", b, err)
	}
}

// T-646: an entry whose root no longer holds a config of that name (the project moved, or another project took its
// place) is skipped by Projects and RootOf, and dropped at the next write.
func TestProjectsSkipsStaleEntries(t *testing.T) {
	t.Parallel()
	env := stateEnv(t)
	dir := t.TempDir()
	moved, renamed, kit := filepath.Join(dir, "moved"), filepath.Join(dir, "renamed"), filepath.Join(dir, "kit")
	configAt(t, moved, "moved")
	configAt(t, renamed, "renamed")
	if _, err := Register(moved, env); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(renamed, env); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, moved+"-elsewhere"); err != nil {
		t.Fatal(err)
	}
	configAt(t, renamed, "another-name") // the directory is there, its project is not the one the list names

	for name, root := range map[string]string{"moved": moved, "renamed": renamed} {
		if _, ok := RootOf(name, env); ok {
			t.Errorf("%s at %s: known; want the stale entry skipped", name, root)
		}
	}
	if got := Projects(env); len(got) != 0 {
		t.Errorf("Projects = %+v; want the stale entries left out", got)
	}
	configAt(t, kit, "kit")
	if _, err := Register(kit, env); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ProjectsFile(env))
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]string
	if err := json.Unmarshal(b, &list); err != nil || len(list) != 1 || list["kit"] != Physical(kit) {
		t.Errorf("the file after a write %q: %v; want only kit (the stale entries dropped)", b, err)
	}
}

// T-646: two registers at once (two serves starting) both end up in the list: the read-modify-write holds
// projects.lock.
func TestProjectsRegisterAtOnce(t *testing.T) {
	t.Parallel()
	env := stateEnv(t)
	dir := t.TempDir()
	var roots []string
	for _, name := range []string{"a", "b", "c", "d"} {
		root := filepath.Join(dir, name)
		configAt(t, root, name)
		roots = append(roots, root)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(roots))
	for _, root := range roots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Register(root, env)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := Projects(env), []Project{{"a", Physical(roots[0])}, {"b", Physical(roots[1])},
		{"c", Physical(roots[2])}, {"d", Physical(roots[3])}}; len(got) != len(want) {
		t.Fatalf("Projects = %+v; want all four registers", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Projects[%d] = %+v; want %+v", i, got[i], want[i])
			}
		}
	}
}
