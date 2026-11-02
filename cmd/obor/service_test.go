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

// serveRestarter (T-656, T-719): launchd's own serve ends as a signal ends it (launchd starts the new build), never by
// kickstart; any other serve (launchd's agent not installed, or another process: this one runs in a pane) ends and is
// marked to exec the kit's shim; a kit without its shim is an error, nothing ended.
func TestServeRestarter(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd: darwin only (TestServeRestarterOffLaunchd covers the exec path everywhere)")
	}
	p := servedProject(t)
	cfg, err := config.LoadDaemon(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	pid, calls := filepath.Join(bin, "pid"), filepath.Join(bin, "calls")
	launchctl := "#!/bin/sh\necho \"$*\" >> " + calls + "\ncase \"$1\" in print) [ -f " + pid + " ] || exit 113; " +
		"echo \"\tpid = $(cat " + pid + ")\" ;; kickstart) exit 1 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(launchctl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	env := kitEnv(t, true)
	ran := func() string { b, _ := os.ReadFile(calls); return string(b) }
	try := func(what string, wantExec bool) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		end := &serveEnd{stop: cancel}
		if err := serveRestarter(cfg, env, end)(context.Background()); err != nil || ctx.Err() == nil ||
			end.exec.Load() != wantExec {
			t.Errorf("%s: %v, cancelled %v, exec %v; want nil, serve's context ended, exec %v", what, err, ctx.Err(),
				end.exec.Load(), wantExec)
		}
	}
	try("not installed", true)
	if err := os.WriteFile(pid, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	try("another process is launchd's (this one in a pane)", true)
	if err := os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	try("this serve is launchd's", false)
	if got := ran(); strings.Contains(got, "kickstart") {
		t.Errorf("the restart kicked launchd instead of exiting gracefully:\n%s", got)
	}
}

// Off launchd (Linux, or no agent: launchd is no restarter there) serve's restart ends it and marks the exec of the
// kit's shim; a kit without bin/obor is an error and serve goes on.
func TestServeRestarterOffLaunchd(t *testing.T) {
	p := servedProject(t)
	cfg, err := config.LoadDaemon(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir() // a launchctl that knows no agent (darwin); unused elsewhere
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\nexit 113\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for _, c := range []struct {
		name string
		shim bool
	}{{"the kit's shim there", true}, {"no shim", false}} {
		ctx, cancel := context.WithCancel(context.Background())
		end := &serveEnd{stop: cancel}
		err := serveRestarter(cfg, kitEnv(t, c.shim), end)(context.Background())
		switch {
		case c.shim && (err != nil || ctx.Err() == nil || !end.exec.Load()):
			t.Errorf("%s: %v, cancelled %v, exec %v; want nil, ended, exec", c.name, err, ctx.Err(), end.exec.Load())
		case !c.shim && (err == nil || !strings.Contains(err.Error(), "no kit shim") || ctx.Err() != nil || end.exec.Load()):
			t.Errorf("%s: %v, cancelled %v, exec %v; want the error, serve running", c.name, err, ctx.Err(), end.exec.Load())
		}
		cancel()
	}
}

// kitEnv is the process env with HOME a temp dir and OBOR_PLUGIN_ROOT a temp kit (with an executable bin/obor when
// shim).
func kitEnv(t *testing.T, shim bool) project.Env {
	t.Helper()
	home, kit := t.TempDir(), t.TempDir()
	if shim {
		if err := os.MkdirAll(filepath.Join(kit, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(kit, "bin", "obor"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return project.Env(func(k string) (string, bool) {
		switch k {
		case "HOME":
			return home, true
		case "OBOR_PLUGIN_ROOT":
			return kit, true
		}
		return os.LookupEnv(k)
	})
}
