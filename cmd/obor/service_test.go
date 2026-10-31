package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// obor serve install / restart / uninstall in a project: the plist of its config name under HOME's LaunchAgents,
// running the kit's bin/obor in the project with the caller's PATH, and launchctl (a fake on PATH) told; obor status
// says down (install it) before and after. Off darwin each verb prints the one line (internal/service tests it).
func TestServeServiceVerbs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd: darwin only")
	}
	p := servedProject(t)
	home := t.TempDir()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$1\" in print) [ -f " + bin + "/loaded ] || exit 113; " +
		"echo '\tpid = 999999' ;; bootstrap) : > " + bin + "/loaded ;; bootout) rm -f " + bin + "/loaded ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := bin + ":" + os.Getenv("PATH")
	env := []string{"HOME=" + home, "PATH=" + path, "OBOR_STATE_HOME=" + t.TempDir(), "HERDR_SOCKET_PATH="}
	if rc, stdout, stderr := run(t, p.Root, env, "status"); rc != 0 || !strings.HasPrefix(stdout, "serve: down (obor serve install)\n") {
		t.Fatalf("status before install: %d %q %q", rc, stdout, stderr)
	}
	rc, stdout, stderr := run(t, p.Root, env, "serve", "install")
	if rc != 0 || !strings.HasPrefix(stdout, "INSTALLED obor.serve.") {
		t.Fatalf("install: %d %q %q", rc, stdout, stderr)
	}
	plists, _ := filepath.Glob(filepath.Join(home, "Library", "LaunchAgents", "obor.serve.*.plist"))
	if len(plists) != 1 {
		t.Fatalf("plists: %q", plists)
	}
	b, _ := os.ReadFile(plists[0])
	for _, want := range []string{"<string>" + p.Root + "</string>", "/bin/obor</string>", "<string>serve</string>",
		"<string>" + path + "</string>", p.Root + "/.obor/state/logs/serve.log"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist has no %q:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "HERDR_SOCKET_PATH") {
		t.Errorf("plist names an unset HERDR_SOCKET_PATH:\n%s", b)
	}
	if rc, stdout, stderr := run(t, p.Root, env, "status"); rc != 0 || !strings.HasPrefix(stdout, "serve: down (") ||
		strings.HasPrefix(stdout, "serve: down (obor serve install)") {
		t.Errorf("status with the agent installed, serve down: %d %q %q", rc, stdout, stderr)
	}
	if rc, stdout, stderr := run(t, p.Root, env, "serve", "restart"); rc != 0 || !strings.HasPrefix(stdout, "RESTARTED obor.serve.") {
		t.Errorf("restart: %d %q %q", rc, stdout, stderr)
	}
	if rc, stdout, stderr := run(t, p.Root, env, "serve", "uninstall"); rc != 0 || !strings.HasPrefix(stdout, "UNINSTALLED obor.serve.") {
		t.Errorf("uninstall: %d %q %q", rc, stdout, stderr)
	}
	if _, err := os.Stat(plists[0]); !os.IsNotExist(err) {
		t.Errorf("plist left: %v", err)
	}
	calls, _ := os.ReadFile(log)
	for _, verb := range []string{"bootstrap gui/", "kickstart -k gui/", "bootout gui/"} {
		if !strings.Contains(string(calls), verb) {
			t.Errorf("launchctl never got %q:\n%s", verb, calls)
		}
	}
}
