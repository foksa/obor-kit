package project

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"obor/internal/config"
)

// configAt puts a minimal obor config naming name at root.
func configAt(t *testing.T, root, name string) {
	t.Helper()
	configBytesAt(t, root, `{"name":"`+name+`"}`+"\n")
}

// configBytesAt puts config, an obor config's bytes as they are, at root.
func configBytesAt(t *testing.T, root, config string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(config), 0o644); err != nil {
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

// T-646, T-774: an entry whose root no longer holds a config of that name (the project moved, or another project took
// its place) is skipped by Projects and RootOf, and a register of another project leaves it in the file.
func TestProjectsSkipsStaleEntries(t *testing.T) {
	t.Parallel()
	env := stateEnv(t)
	dir := t.TempDir()
	moved, renamed, kit := filepath.Join(dir, "moved"), filepath.Join(dir, "renamed"), filepath.Join(dir, "kit")
	configAt(t, moved, "moved")
	configAt(t, renamed, "renamed")
	movedRoot := Physical(moved) // before the rename: Physical of a gone path does not resolve its symlinks
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
	kept := map[string]string{"moved": movedRoot, "renamed": Physical(renamed), "kit": Physical(kit)}
	if err := json.Unmarshal(b, &list); err != nil || !maps.Equal(list, kept) {
		t.Errorf("the file after a write %q: %v; want %v (the stale entries kept, T-774)", b, err, kept)
	}
}

// T-767: the list keeps a project whose config this binary cannot load (an older shape a release wrote, a newer shape a
// release wrote, an unparsable file): a read leaves its entry out only when its root has no .obor/config.json, or its
// config names another project; the entry itself stays in the file (T-774). A project on another obor version stays
// named, so obor send and obor projects reach it.
func TestProjectsKeepsConfigsItCannotLoad(t *testing.T) {
	t.Parallel()
	env := stateEnv(t)
	dir := t.TempDir()
	old, broken, renamed, gone := filepath.Join(dir, "axis-alies"), filepath.Join(dir, "obor-kit"),
		filepath.Join(dir, "renamed"), filepath.Join(dir, "gone")
	// The shape before T-765: this binary refuses it, and its name still counts.
	configBytesAt(t, old, `{"name":"axis-alies","prefix":"ax","lane_types":{"opus":{"kind":"claude"}}}`+"\n")
	configBytesAt(t, broken, `{"name": "obor-kit",`+"\n")
	configAt(t, renamed, "renamed")
	configAt(t, gone, "gone")
	if _, err := config.LoadDaemon(old); err == nil {
		t.Fatal("precondition: the old-shape config loads")
	}
	if _, err := config.LoadDaemon(broken); err == nil {
		t.Fatal("precondition: the unparsable config loads")
	}
	for _, root := range []string{old, broken, renamed, gone} {
		if _, err := Register(root, env); err != nil {
			t.Fatalf("register %s: %v", root, err)
		}
	}
	if root, ok := RootOf("axis-alies", env); !ok || root != Physical(old) {
		t.Errorf("RootOf(axis-alies) = %q, %v; want %q (the old-shape config keeps the entry)", root, ok, Physical(old))
	}
	want := []Project{{"axis-alies", Physical(old)}, {"gone", Physical(gone)}, {"obor-kit", Physical(broken)},
		{"renamed", Physical(renamed)}}
	if got := Projects(env); !slices.Equal(got, want) {
		t.Errorf("Projects = %+v; want all four, the two unloadable configs under their own names", got)
	}

	if err := os.Remove(filepath.Join(gone, ".obor", "config.json")); err != nil { // its project is gone
		t.Fatal(err)
	}
	configAt(t, renamed, "another-name") // the directory is there, its project is not the one the list names
	want = []Project{{"axis-alies", Physical(old)}, {"obor-kit", Physical(broken)}}
	if got := Projects(env); !slices.Equal(got, want) {
		t.Errorf("Projects = %+v; want only the two with a config of their name", got)
	}
	fifth := filepath.Join(dir, "fifth")
	configAt(t, fifth, "fifth")
	if _, err := Register(fifth, env); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ProjectsFile(env))
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]string
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	// T-774: the gone and renamed entries stay in the file; only a read leaves them out.
	kept := map[string]string{"axis-alies": Physical(old), "obor-kit": Physical(broken), "fifth": Physical(fifth),
		"gone": Physical(gone), "renamed": Physical(renamed)}
	if !maps.Equal(list, kept) {
		t.Errorf("the file after a write = %v; want %v (every entry kept, stale or not)", list, kept)
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
