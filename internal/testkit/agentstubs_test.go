package testkit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/project"
)

// The guard: Isolate puts a stub first on PATH for every agent kind obor drives, so the real installed CLI a developer
// has later on PATH is never what a test, or a serve a test starts, runs. The lookup of each kind resolves to the stub,
// serve's version read finds only stubs and no version, and running one answers as the stub (a clear message) and
// leaves the real CLI's marker unwritten.
func TestIsolateStubsAgentCLIs(t *testing.T) {
	Isolate()
	real := t.TempDir()
	ran := filepath.Join(real, "ran")
	for _, name := range Kinds().Names() {
		body := "#!/bin/sh\ntouch '" + ran + "'\necho 'a real cli 1.0'\n"
		if err := os.WriteFile(filepath.Join(real, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+real)

	for _, name := range Kinds().Names() {
		bin, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("precondition: %s not on the isolated PATH: %v", name, err)
		}
		if filepath.Dir(bin) != agentStubDir() {
			t.Errorf("%s resolves to %s, want the stub in %s", name, bin, agentStubDir())
		}
	}
	if got := launch.CLIVersions(context.Background(), project.OSEnv, Kinds(), Kinds().Names()); len(got) != 0 {
		t.Errorf("the version read found %q, want no version: it ran a CLI that answers, not a stub", got)
	}
	out, err := exec.Command("claude", "--version").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "a test ran the agent CLI claude") {
		t.Fatalf("the stub CLI: exit %v, %q; want a failure naming the agent CLI", err, out)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Errorf("the CLI later on PATH ran (%v): the stub is not first", err)
	}
}
