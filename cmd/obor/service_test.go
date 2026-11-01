package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
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

// serve's self-restart (T-652): only launchd's serve restarts itself. Not installed, or the agent's process another
// one (a serve in a pane): an error, nothing run. This serve's: `<exe> serve restart` in the project; its FAIL line is
// the error.
func TestServeRestarter(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd: darwin only")
	}
	p := servedProject(t)
	cfg, err := config.LoadDaemon(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	pid, calls, fail := filepath.Join(bin, "pid"), filepath.Join(bin, "calls"), filepath.Join(bin, "fail")
	launchctl := "#!/bin/sh\ncase \"$1\" in print) [ -f " + pid + " ] || exit 113; echo \"\tpid = $(cat " + pid + ")\" ;; esac\n"
	exe := filepath.Join(bin, "obor")
	fake := "#!/bin/sh\necho \"$(pwd -P) $*\" >> " + calls + "\n[ -f " + fail + " ] || exit 0\n" +
		"echo 'FAIL obor serve restart: launchctl kickstart -k: boom' >&2\nexit 1\n"
	for name, script := range map[string]string{"launchctl": launchctl, "obor": fake} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	home := t.TempDir()
	env := project.Env(func(k string) (string, bool) {
		if k == "HOME" {
			return home, true
		}
		return os.LookupEnv(k)
	})
	restart := serveRestarter(cfg, env, exe)
	ran := func() string { b, _ := os.ReadFile(calls); return string(b) }
	if err := restart(context.Background()); err == nil || !strings.Contains(err.Error(), "is not installed") || ran() != "" {
		t.Errorf("not installed: %v, ran %q; want the error, nothing run", err, ran())
	}
	if err := os.WriteFile(pid, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restart(context.Background()); err == nil || !strings.Contains(err.Error(), "is not launchd's") || ran() != "" {
		t.Errorf("another process: %v, ran %q; want the error, nothing run", err, ran())
	}
	if err := os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(p.Root)
	if err := restart(context.Background()); err != nil || ran() != root+" serve restart\n" {
		t.Errorf("this serve: %v, ran %q; want nil, %q", err, ran(), root+" serve restart")
	}
	if err := os.WriteFile(fail, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restart(context.Background()); err == nil || err.Error() != "launchctl kickstart -k: boom" {
		t.Errorf("a failing restart: %v; want its FAIL line's reason", err)
	}
}
