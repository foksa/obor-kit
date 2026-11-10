package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ockind "obor/kinds/opencode/kind"
)

// The opencode plugin: opencode/obor.js ships as a package dir and the kind's generated start config names it (the
// kind, its own binary). Its event mapping and hooks are the tool's behaviour: `node --check` and `node --test` run
// in conformance OC-S2, not here.
func TestOpencodePlugin(t *testing.T) {
	root := checkoutRoot(t)
	js := filepath.Join(root, "opencode", "obor.js")
	if fi, err := os.Stat(js); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%s: %v", js, err)
	}
	// The start's generated config, asked of the kind itself: its first plugin is the kit's opencode package dir. (No
	// source text: where the kind keeps that expression is not the test's business.)
	kit := t.TempDir()
	var conf struct{ Plugins []string }
	if err := json.Unmarshal(ockind.Config(kit), &conf); err != nil {
		t.Fatalf("the kind's start config does not parse: %v", err)
	}
	if ockind.Name != "opencode" || len(conf.Plugins) == 0 || conf.Plugins[0] != "file://"+filepath.Join(kit, "opencode") {
		t.Errorf("the kind is %q and its start config's plugins %v; want opencode and file://<kit>/opencode first",
			ockind.Name, conf.Plugins)
	}
	// opencode loads only a package dir from an env config: <kit>/opencode/package.json, obor.js
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
