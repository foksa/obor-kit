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

// T-646, T-767, T-774: the list keeps a project whose config this binary cannot load (an older or a newer shape, an
// unparsable file): a read leaves its entry out only when its root has no .obor/config.json, or its config names
// another project. Those stale entries stay in the file; a register of another project never drops one.
func TestProjectsKeepsWhatItsEntriesName(t *testing.T) {
	t.Parallel()
	env := stateEnv(t)
	dir := t.TempDir()
	old, broken, moved, renamed, gone, kit := filepath.Join(dir, "axis-alies"), filepath.Join(dir, "obor-kit"),
		filepath.Join(dir, "moved"), filepath.Join(dir, "renamed"), filepath.Join(dir, "gone"), filepath.Join(dir, "kit")
	// The shape before v0.23: this binary refuses it (its lane_types table is retired, T-847), and its name still
	// counts.
	configBytesAt(t, old, `{"name":"axis-alies","prefix":"ax","lane_types":{"opus":{"kind":"claude"}}}`+"\n")
	configBytesAt(t, broken, `{"name": "obor-kit",`+"\n")
	configAt(t, moved, "moved")
	configAt(t, renamed, "renamed")
	configAt(t, gone, "gone")
	configAt(t, kit, "kit")
	if _, err := config.Load(old); err == nil {
		t.Fatal("precondition: the pre-v0.23 config loads")
	}
	if _, err := config.Load(broken); err == nil {
		t.Fatal("precondition: the unparsable config loads")
	}
	for _, root := range []string{old, broken, moved, renamed, gone} {
		if _, err := Register(root, env); err != nil {
			t.Fatalf("register %s: %v", root, err)
		}
	}
	movedRoot := Physical(moved) // before the rename: Physical of a gone path does not resolve its symlinks
	if err := os.Rename(moved, moved+"-elsewhere"); err != nil {
		t.Fatal(err)
	}
	configAt(t, renamed, "another-name") // the directory is there, its project is not the one the list names
	if err := os.Remove(filepath.Join(gone, ".obor", "config.json")); err != nil {
		t.Fatal(err)
	}

	if root, ok := RootOf("axis-alies", env); !ok || root != Physical(old) {
		t.Errorf("RootOf(axis-alies) = %q, %v; want %q (the old-shape config keeps the entry)", root, ok, Physical(old))
	}
	want := []Project{{"axis-alies", Physical(old)}, {"obor-kit", Physical(broken)}}
	if got := Projects(env); !slices.Equal(got, want) {
		t.Errorf("Projects = %+v; want the two unloadable configs under their own names", got)
	}
	for _, name := range []string{"moved", "renamed", "gone"} {
		if _, ok := RootOf(name, env); ok {
			t.Errorf("%s: known; want the stale entry skipped", name)
		}
	}

	if _, err := Register(kit, env); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ProjectsFile(env))
	if err != nil {
		t.Fatal(err)
	}
	var list map[string]string
	kept := map[string]string{"axis-alies": Physical(old), "obor-kit": Physical(broken), "kit": Physical(kit),
		"moved": movedRoot, "renamed": Physical(renamed), "gone": Physical(gone)}
	if err := json.Unmarshal(b, &list); err != nil || !maps.Equal(list, kept) {
		t.Errorf("the file after a write %q: %v; want %v (every entry kept, stale or not)", b, err, kept)
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

// T-1124: NameAt reads the name in either config shape (project.name in shape 2, name in shape 1), else the root's.
func TestNameAtEitherShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, c := range []struct{ root, config, want string }{
		{"two", `{"version":2,"project":{"name":"shape-two","agent_prefix":"st"}}`, "shape-two"},
		{"one", `{"version":1,"name":"shape-one","prefix":"so"}`, "shape-one"},
		{"none", `{"version":2,"project":{"agent_prefix":"nn"}}`, "none"},
	} {
		root := filepath.Join(dir, c.root)
		configBytesAt(t, root, c.config+"\n")
		if got, ok := NameAt(root); !ok || got != c.want {
			t.Errorf("NameAt(%s) = %q, %v; want %q", c.config, got, ok, c.want)
		}
	}
}
