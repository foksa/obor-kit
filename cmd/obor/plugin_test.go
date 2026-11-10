package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The opencode plugin: opencode/obor.js ships as a package dir and each start's generated config names it (the
// kind, its own binary). Its event mapping and hooks are the tool's behaviour: `node --check` and `node --test` run
// in conformance OC-S2, not here.
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
