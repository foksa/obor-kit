package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// bashHook is `obor hook pre-tool-use` (the binary) on a Bash call of cmd in dir: its exit code and stderr.
func bashHook(t *testing.T, dir, cmd string) (int, string) {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": dir, "tool_input": map[string]string{"command": cmd}})
	c := exec.Command(oborBin, "hook", "pre-tool-use")
	c.Dir, c.Stdin = dir, bytes.NewReader(in)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	err := c.Run()
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, stderr.String()
}

// T-1054 (RV-150 #1): a project whose OpenCode manifest is not cached has no headless forms for opencode, so the guard
// denies every opencode command (also wrapped) naming obor setup agents; after obor serve's start, which caches the
// manifest of the kit's real obor-kind-opencode, the manifest's rule applies: `opencode run` is denied as headless and
// another opencode command is let through.
func TestGuardDeniesAnUncachedPluginKindUntilServeCachesIt(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	cache := filepath.Join(p.Root, ".obor", "state", "kinds", "opencode.json")
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("precondition: no manifest cached (%v)", err)
	}
	for _, cmd := range []string{"opencode run hello", "env opencode run hello", "opencode --version"} {
		if rc, stderr := bashHook(t, p.Root, cmd); rc != 2 ||
			!strings.Contains(stderr, "opencode is the CLI of plugin kind opencode, whose manifest is not cached") ||
			!strings.Contains(stderr, "run obor setup agents") {
			t.Errorf("no cache, %q: exit %d, %q; want the uncached deny", cmd, rc, stderr)
		}
	}

	srv := herdrtest.Serve(t, herdrtest.New())
	cmd, out := startServe(t, p.Root, []string{"OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH=" + srv.Path})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(out.String(), "obor serve: running") {
			break
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	b, err := os.ReadFile(cache)
	if err != nil || !strings.Contains(out.String(), "obor serve: kind opencode: manifest of ") {
		t.Fatalf("serve's start cached no manifest (%v):\n%s", err, out.String())
	}
	var c struct{ Command string }
	if json.Unmarshal(b, &c) != nil || filepath.Base(c.Command) != "obor-kind-opencode" {
		t.Errorf("the cache describes %q, want the kit's obor-kind-opencode", c.Command)
	}
	for _, cmd := range []string{"opencode run hello", "env opencode run hello"} {
		if rc, stderr := bashHook(t, p.Root, cmd); rc != 2 || !strings.Contains(stderr, "opencode run runs an agent headless") {
			t.Errorf("cached, %q: exit %d, %q; want the manifest's headless deny", cmd, rc, stderr)
		}
	}
	if rc, stderr := bashHook(t, p.Root, "opencode --version"); rc != 0 {
		t.Errorf("cached, opencode --version: exit %d, %q; want allowed", rc, stderr)
	}
}

// T-1054 (RV-150 #3): obor config validate asks OpenCode for its project warnings as it just described it, not the
// registry's uncached kind: with no cache, an opencode.json overriding mcp.servers.obor is a WARN.
func TestConfigValidateWarnsOfTheProjectFileWithoutACache(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	p.Write("opencode.json", `{"mcp":{"servers":{"obor":{"type":"local","command":["obor","mcp"]}}}}`+"\n")
	if _, err := os.Stat(filepath.Join(p.Root, ".obor", "state", "kinds", "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("precondition: no manifest cached (%v)", err)
	}
	// the bare config fails validate for its own reasons (no integrator): only the warning is this test's
	rc, stdout, stderr := run(t, p.Root, nil, "config", "validate")
	if want := "WARN opencode.json: mcp.servers.obor replaces obor's own server"; !strings.Contains(stdout+stderr, want) {
		t.Errorf("config validate: exit %d, %q %q; want %q", rc, stdout, stderr, want)
	}
}
