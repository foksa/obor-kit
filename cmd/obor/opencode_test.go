package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The opencode plugin (T-306): opencode/obor.js ships, `obor setup agents` links it (the bridge names it, T-495), and its event mapping
// and hooks pass opencode/obor.test.js under `node --test` (skipped without node).
func TestOpencodePlugin(t *testing.T) {
	root := checkoutRoot(t)
	js := filepath.Join(root, "opencode", "obor.js")
	if fi, err := os.Stat(js); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%s: %v", js, err)
	}
	src, err := os.ReadFile(filepath.Join(root, "internal", "agents", "opencode", "opencode.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `filepath.Join(kit, name, "obor.js")`) || !strings.Contains(string(src), `const name = "opencode"`) {
		t.Error("internal/agents/opencode does not link <kit>/opencode/obor.js")
	}
	for _, sub := range []string{"agents", "opencode-plugin"} { // opencode-plugin: the alias (DD-v0.13 B1)
		if has([]string{"setup", sub}) != 0 {
			t.Errorf("obor has setup %s: no", sub)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	rc, stdout, stderr, _ := shimRun(t, root, "", nil, node, "--test", filepath.Join(root, "opencode", "obor.test.js"))
	if rc != 0 {
		t.Errorf("node --test opencode/obor.test.js: exit %d\n%s\n%s", rc, stdout, stderr)
	}
}
