package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond for up to a minute. Detached commands (the restore below) and herdr over a socket are slow on a
// loaded machine: the gate's parallel packages took one past 15 s (T-960's check).
func waitFor(cond func() bool) bool {
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// The herdr manifest (herdr-plugin/herdr-plugin.toml): obor.events, the startup hook → obor plugin restore and no
// event hooks (the kick went with the heartbeat, T-465), through the kit's bin/obor relative to the plugin directory
// (herdr's cwd for plugin commands); this binary has restore, not kick. No TOML parser: each line is a `key = value`, a table header, a comment or blank.
// bash plugin.test.sh case_herdr_manifest
func TestHerdrManifest(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../herdr-plugin/herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`^(\s*(#.*)?|\[\[(events|startup)\]\]|[a-z_]+ = ("[^"]*"|\[("[^"]*"(, )?)+\]))$`)
	var top, tables []string
	table := ""
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		switch {
		case !line.MatchString(l):
			t.Errorf("not key = value / [[events]] / [[startup]] / comment: %q", l)
		case strings.HasPrefix(l, "[["):
			table = l
		case strings.HasPrefix(l, "#") || l == "":
		case table == "":
			top = append(top, l)
		default:
			tables = append(tables, table+" "+l)
		}
	}
	for _, want := range []string{`id = "obor.events"`, `name = "obor events"`, `min_herdr_version = "0.9.0"`,
		`platforms = ["macos", "linux"]`} {
		if !slices.Contains(top, want) {
			t.Errorf("top-level %s missing: %q", want, top)
		}
	}
	const restore = `command = ["/bin/bash", "../bin/obor", "plugin", "restore"]`
	want := []string{"[[startup]] " + restore}
	if !slices.Equal(tables, want) {
		t.Errorf("tables:\n%s\nwant:\n%s", strings.Join(tables, "\n"), strings.Join(want, "\n"))
	}
	if fi, err := os.Stat("../../herdr-plugin/../bin/obor"); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("../bin/obor from the plugin directory: %v", err)
	}
	if rc, _, _ := run(t, ".", nil, "has", "plugin", "restore"); rc != 0 {
		t.Errorf("obor has plugin restore: exit %d", rc)
	}
	if rc, _, _ := run(t, ".", nil, "has", "plugin", "kick"); rc != 1 {
		t.Errorf("obor has plugin kick: exit %d, want 1 (gone)", rc)
	}
}

// The opencode plugin (T-306): opencode/obor.js ships as a package dir and each start's generated config names it (the
// kind, T-495, T-809; its own binary since T-1046). Its event mapping and hooks are the tool's behaviour: `node --check` and `node --test` run in
// conformance OC-S2, not here.
func TestOpencodePlugin(t *testing.T) {
	root := checkoutRoot(t)
	js := filepath.Join(root, "opencode", "obor.js")
	if fi, err := os.Stat(js); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%s: %v", js, err)
	}
	src, err := os.ReadFile(filepath.Join(root, "kinds", "opencode", "kind", "kind.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"file://" + filepath.Join(kit, Name)`) || !strings.Contains(string(src), `const Name = "opencode"`) {
		t.Error("kinds/opencode does not name <kit>/opencode in the start's plugins")
	}
	// T-809 (T-804 OC-L4): opencode loads only a package dir from an env config: <kit>/opencode/package.json, obor.js
	// its main.
	b, err := os.ReadFile(filepath.Join(root, "opencode", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Name, Type, Main string }
	if err := json.Unmarshal(b, &pkg); err != nil || pkg.Name == "" || pkg.Type != "module" || pkg.Main != "obor.js" {
		t.Errorf("opencode/package.json = %s (%v): want a named ES module package, main obor.js", b, err)
	}
	if has([]string{"setup", "agents"}) != 0 || has([]string{"setup", "opencode-plugin"}) != 1 {
		t.Error("obor has setup agents: yes; the alias opencode-plugin: gone (T-774)")
	}
}
