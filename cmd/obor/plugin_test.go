package main

import (
	"os"
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
