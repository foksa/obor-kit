package main

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// restartStub is servedProject with its serve LaunchAgent installed as far as launchd is concerned: a plist naming the
// project's root under home, and a fake launchctl that reports the label loaded and accepts kickstart (recording its
// argv in log).
func restartStub(t *testing.T, p *testkit.Project) (home, bin, log string) {
	t.Helper()
	home, bin = t.TempDir(), t.TempDir()
	log = filepath.Join(bin, "calls")
	plist := filepath.Join(home, "Library", "LaunchAgents", "obor.serve.proj.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<key>WorkingDirectory</key><string>"+p.Root+"</string>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ndir=$(dirname \"$0\")\necho \"$*\" >> \"$dir/calls\"\n" +
		"case \"$1\" in print) printf '\\tpid = 999999\\n' ;; esac\n"
	testkit.Script(t, bin, "launchctl", script)
	return home, bin, log
}

// serveStatusAfter binds root's app socket after d and answers one status request, then leaves: the serve of a restart,
// which replays its log before it binds.
func serveStatusAfter(t *testing.T, root string, d time.Duration) {
	t.Helper()
	sock := wire.SocketPath(root)
	lnc := make(chan net.Listener, 1)
	go func() {
		time.Sleep(d)
		if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
			t.Error(err)
			lnc <- nil
			return
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Error(err)
			lnc <- nil
			return
		}
		lnc <- ln
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		_, _ = c.Write([]byte("{\"status\":{}}\n"))
	}()
	t.Cleanup(func() {
		if ln := <-lnc; ln != nil {
			_ = ln.Close()
		}
	})
}

// restart returns once the new serve answers, not when launchd has started it: a big replay binds its socket
// after that. Here the first serve's socket answers nothing and the new one answers 1.5 s in.
func TestServeRestartWaitsForServe(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd: darwin only")
	}
	p := servedProject(t)
	home, bin, log := restartStub(t, p)
	env := []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH"), "OBOR_STATE_HOME=" + t.TempDir(),
		"HERDR_SOCKET_PATH="}
	sock := wire.SocketPath(p.Root)
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket before the restart: %v", err)
	}
	serveStatusAfter(t, p.Root, 1500*time.Millisecond)
	start := time.Now()
	rc, stdout, stderr := run(t, p.Root, env, "serve", "restart")
	elapsed := time.Since(start)
	if rc != 0 || stdout != "RESTARTED obor.serve.proj\nserve ready\n" {
		t.Fatalf("restart: %d %q %q; want its line and serve ready", rc, stdout, stderr)
	}
	if elapsed < time.Second {
		t.Errorf("restart returned in %s, before the socket answered (1.5 s in): it did not wait", elapsed)
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), "kickstart -k gui/") {
		t.Errorf("launchctl never got the kickstart:\n%s", calls)
	}
}

// a serve that never answers fails after the wait (shortened here), naming the deadline and where to look, and
// prints no line as if the restart had succeeded.
func TestServeRestartServeNeverAnswers(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd: darwin only")
	}
	p := servedProject(t)
	home, bin, _ := restartStub(t, p)
	env := []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH"), "OBOR_STATE_HOME=" + t.TempDir(),
		"HERDR_SOCKET_PATH=", "OBOR_SERVE_READY_WAIT=0.1"}
	if _, err := os.Stat(wire.SocketPath(p.Root)); !os.IsNotExist(err) {
		t.Fatalf("socket before the restart: %v", err)
	}
	start := time.Now()
	rc, stdout, stderr := run(t, p.Root, env, "serve", "restart")
	elapsed := time.Since(start)
	want := "FAIL obor serve restart: launchd restarted it but serve did not answer in 100ms (obor status; its log: "
	if rc != 1 || stdout != "" || !strings.HasPrefix(stderr, want) || !strings.HasSuffix(stderr, "serve.log)\n") {
		t.Fatalf("restart: %d %q %q; want the FAIL line after %q … serve.log)", rc, stdout, stderr, want)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("restart failed in %s, before the 100ms deadline: it did not wait", elapsed)
	}
}

// serveRestarter: launchd's own serve ends as a signal ends it (launchd starts the new build), never by
// kickstart; any other serve (launchd's agent not installed, or another process: this one runs in a pane) ends and is
// marked to exec the kit's shim; a kit without its shim is an error, nothing ended.
func TestServeRestarter(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd: darwin only (TestServeRestarterOffLaunchd covers the exec path everywhere)")
	}
	p := servedProject(t)
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	pid, calls := filepath.Join(bin, "pid"), filepath.Join(bin, "calls")
	launchctl := "#!/bin/sh\ndir=$(dirname \"$0\")\necho \"$*\" >> \"$dir/calls\"\n" +
		"case \"$1\" in print) [ -f \"$dir/pid\" ] || exit 113; " +
		"echo \"\tpid = $(cat \"$dir/pid\")\" ;; kickstart) exit 1 ;; esac\n"
	testkit.Script(t, bin, "launchctl", launchctl)
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
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir() // a launchctl that knows no agent (darwin); unused elsewhere
	testkit.Script(t, bin, "launchctl", "#!/bin/sh\nexit 113\n")
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

// setup's Installed is the plist of the project's serve LaunchAgent under HOME, there or not.
func TestServeInstalledIsThePlistOfTheConfigName(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	installed := serveInstalled(testkit.MapEnv(map[string]string{"HOME": home}))
	plist := filepath.Join(home, "Library", "LaunchAgents", "obor.serve.proj.plist")
	if got, ok := installed(cfg); ok || got != plist {
		t.Fatalf("no plist: %s, %v; want %s, false", got, ok, plist)
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<plist/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := installed(cfg); !ok || got != plist {
		t.Errorf("a plist there: %s, %v; want %s, true", got, ok, plist)
	}
}
