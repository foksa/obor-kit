package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// The plist: obor serve from the kit in the project, at load, kept alive, its output in serve.log, the caller's PATH
// and HERDR_SOCKET_PATH (escaped); without HERDR_SOCKET_PATH set, no such key.
func TestPlistGolden(t *testing.T) {
	s := Service{Name: "pj", Root: "/work/p&j", Kit: "/kit", Path: "/opt/bin:/usr/bin", Herdr: "/tmp/herdr.sock"}
	want, err := os.ReadFile(filepath.Join("testdata", "serve.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(s.Plist()); got != string(want) {
		t.Errorf("plist:\n%s\nwant:\n%s", got, want)
	}
	s.Herdr = ""
	if got := string(s.Plist()); strings.Contains(got, "HERDR_SOCKET_PATH") || !strings.Contains(got, "<key>PATH</key>") {
		t.Errorf("plist without HERDR_SOCKET_PATH:\n%s", got)
	}
	if s.PlistPath() != filepath.Join(s.Home, "Library", "LaunchAgents", "obor.serve.pj.plist") {
		t.Errorf("plist path %s", s.PlistPath())
	}
}

// fakeLaunchd is an in-process launchctl (ExecLaunchctl): it records every call in calls and keeps one label's state:
// loaded and pid (print's answer while loaded; 0: loaded, no serve). Its knobs are launchd's states a test needs: eio,
// the number of bootstraps that fail with launchd's "5: Input/output error"; noload, a bootstrap that returns 0 loading
// nothing; and kick, what kickstart -k does to a loaded label.
type fakeLaunchd struct {
	calls  []string
	loaded bool
	pid    int // print's pid while loaded (0: loaded, no serve)
	eio    int
	noload bool
	kick   kick
}

// kick is kickstart -k's work: the new serve's pid (nil: the pid stays), whether it unloads the label, how long it
// waits for the old serve to exit, and its exit code.
type kick struct {
	pid    *int
	unload bool
	wait   time.Duration
	exit   int
}

// pidOf is a pid a kickstart gives serve (0: no serve runs).
func pidOf(p int) *int { return &p }

// fakeLaunchctl installs a fake launchd for the test (the label not loaded, print's pid 4242 once it is) and returns
// it: its calls are the launchctl calls the verbs make.
func fakeLaunchctl(t *testing.T) *fakeLaunchd {
	t.Helper()
	f := &fakeLaunchd{pid: 4242}
	was := ExecLaunchctl
	ExecLaunchctl = f.run
	t.Cleanup(func() { ExecLaunchctl = was })
	return f
}

// run is the fake's ExecLaunchctl: one launchctl call's output and error. kickstart's wait is the bubble's clock in the
// tests that set it (synctest.Test), so a slow or a killed kickstart costs no wall time.
func (f *fakeLaunchd) run(ctx context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "bootstrap":
		if f.eio > 0 {
			f.eio--
			return []byte("Bootstrap failed: 5: Input/output error\n"), errors.New("exit status 5")
		}
		if !f.noload {
			f.loaded = true
		}
	case "bootout":
		if !f.loaded {
			return nil, errors.New("exit status 3")
		}
		f.loaded = false
	case "kickstart":
		if !f.loaded {
			return nil, errors.New("exit status 113")
		}
		if f.kick.pid != nil {
			f.pid = *f.kick.pid
		}
		if f.kick.unload {
			f.loaded = false
		}
		if f.kick.wait > 0 {
			select {
			case <-time.After(f.kick.wait):
			case <-ctx.Done(): // what launchctl's deadline makes of a kickstart still waiting
				return nil, errors.New("signal: killed")
			}
		}
		if f.kick.exit != 0 {
			return nil, fmt.Errorf("exit status %d", f.kick.exit)
		}
	case "print":
		if !f.loaded {
			return []byte("Could not find service\n"), errors.New("exit status 113")
		}
		return []byte(fmt.Sprintf("\tpid = %d\n", f.pid)), nil
	default:
		return nil, fmt.Errorf("unexpected launchctl call %q", strings.Join(args, " "))
	}
	return nil, nil
}

// joined is the calls, one per line.
func (f *fakeLaunchd) joined() string { return strings.Join(f.calls, "\n") }

// darwin is a service in a temp home, on darwin's launchd.
func darwin(t *testing.T) Service {
	home := t.TempDir()
	return Service{Name: "pj", Root: filepath.Join(home, "pj"), Kit: "/kit", Home: home, UID: 501, GOOS: "darwin",
		Path: "/usr/bin"}
}

// installed is s installed in f (loaded, pid 4242), f's calls emptied.
func installed(t *testing.T, f *fakeLaunchd) Service {
	t.Helper()
	s := darwin(t)
	if _, err := s.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pid, loaded := s.PID(context.Background()); !loaded || pid != 4242 {
		t.Fatalf("precondition: installed, pid 4242: %d %v", pid, loaded)
	}
	f.calls = nil
	return s
}

// install writes the plist and bootstraps it (a second install boots the loaded one out first); restart kickstarts
// it; uninstall boots it out and removes the plist; uninstall again and restart of nothing say it is not installed.
func TestVerbs(t *testing.T) {
	f := fakeLaunchctl(t)
	s := darwin(t)
	ctx := context.Background()
	if _, loaded := s.PID(ctx); loaded {
		t.Fatal("precondition: loaded before the install")
	}
	line, err := s.Install(ctx)
	if err != nil || !strings.HasPrefix(line, "INSTALLED obor.serve.pj (") {
		t.Fatalf("install: %q, %v", line, err)
	}
	if b, err := os.ReadFile(s.PlistPath()); err != nil || string(b) != string(s.Plist()) {
		t.Errorf("plist on disk: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Dir(s.LogPath())); err != nil {
		t.Errorf("log dir: %v", err)
	}
	if pid, loaded := s.PID(ctx); !loaded || pid != 4242 {
		t.Errorf("after install: pid %d, loaded %v; want 4242", pid, loaded)
	}
	if _, err := s.Install(ctx); err != nil {
		t.Fatalf("install again: %v", err)
	}
	if line, err := s.Restart(ctx); err != nil || line != "RESTARTED obor.serve.pj" {
		t.Errorf("restart: %q, %v", line, err)
	}
	if line, err := s.Uninstall(ctx); err != nil || line != "UNINSTALLED obor.serve.pj" {
		t.Errorf("uninstall: %q, %v", line, err)
	}
	if _, err := os.Stat(s.PlistPath()); !os.IsNotExist(err) {
		t.Errorf("plist left: %v", err)
	}
	plist := s.PlistPath()
	// each verb's owner print first; a bootstrap is followed by the print that it loaded, a bootout by the one that
	// it unloaded; the test's own PID reads are the 1st and 5th
	want := []string{"print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootstrap gui/501 " + plist, "print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootout gui/501/obor.serve.pj", "print gui/501/obor.serve.pj",
		"bootstrap gui/501 " + plist, "print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "kickstart -k gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootout gui/501/obor.serve.pj", "print gui/501/obor.serve.pj"}
	if got := f.joined(); got != strings.Join(want, "\n") {
		t.Errorf("launchctl calls:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if line, err := s.Uninstall(ctx); err != nil || line != "obor.serve.pj is not installed" {
		t.Errorf("uninstall again: %q, %v", line, err)
	}
	if _, err := s.Restart(ctx); err == nil || !strings.Contains(err.Error(), "not installed (obor serve install)") {
		t.Errorf("restart of nothing: %v", err)
	}
}

// A plist of this label that runs serve for another project: install FAILs and leaves it (and launchd) alone; the
// same project's plist is no clash.
func TestInstallLabelClash(t *testing.T) {
	f := fakeLaunchctl(t)
	s := darwin(t)
	other := s
	other.Root = "/elsewhere/pj"
	if err := os.MkdirAll(filepath.Dir(s.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.PlistPath(), other.Plist(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "runs obor serve for /elsewhere/pj, not "+s.Root) {
		t.Fatalf("install over another project's plist: %v", err)
	}
	if b, _ := os.ReadFile(s.PlistPath()); string(b) != string(other.Plist()) {
		t.Errorf("the other project's plist was changed:\n%s", b)
	}
	if len(f.calls) != 0 {
		t.Errorf("launchctl called: %q", f.calls)
	}
	if err := os.WriteFile(s.PlistPath(), s.Plist(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(context.Background()); err != nil {
		t.Errorf("install over its own plist: %v", err)
	}
}

// uninstall and restart check the plist's owner first: another project's plist of this label FAILs naming
// it, and launchctl is never called — its agent is not booted out or kickstarted.
func TestVerbsLeaveAnotherRootsAgent(t *testing.T) {
	f := fakeLaunchctl(t)
	s := darwin(t)
	other := s
	other.Root = "/elsewhere/pj"
	if err := os.MkdirAll(filepath.Dir(s.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.PlistPath(), other.Plist(), 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(other.Plist()), "<string>/elsewhere/pj</string>") {
		t.Fatal("precondition: the plist runs serve for another root")
	}
	f.loaded = true
	ctx := context.Background()
	if _, loaded := s.PID(ctx); !loaded {
		t.Fatal("precondition: the label is loaded")
	}
	f.calls = nil // the precondition's own print is not the verbs' doing
	for name, verb := range map[string]func(context.Context) (string, error){"uninstall": s.Uninstall, "restart": s.Restart} {
		_, err := verb(ctx)
		if err == nil || !strings.Contains(err.Error(), "runs obor serve for /elsewhere/pj, not "+s.Root) {
			t.Fatalf("%s over another project's plist: %v", name, err)
		}
		if b, _ := os.ReadFile(s.PlistPath()); string(b) != string(other.Plist()) {
			t.Errorf("%s changed the other project's plist:\n%s", name, b)
		}
		if !f.loaded {
			t.Errorf("%s unloaded the other project's agent", name)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("launchctl called: %q", f.calls)
	}
}

// a label launchd has loaded with no plist at all is nobody's to boot out or kickstart: uninstall and
// restart FAIL saying whose it is cannot be told, and launchctl is only asked whether it is loaded.
func TestVerbsRefuseUnownedLabel(t *testing.T) {
	f := fakeLaunchctl(t)
	s := darwin(t)
	f.loaded = true
	if _, err := os.Stat(s.PlistPath()); !os.IsNotExist(err) {
		t.Fatalf("precondition: no plist: %v", err)
	}
	ctx := context.Background()
	for name, verb := range map[string]func(context.Context) (string, error){"uninstall": s.Uninstall, "restart": s.Restart} {
		_, err := verb(ctx)
		if err == nil || !strings.Contains(err.Error(), "cannot tell whose it is (launchctl bootout gui/501/obor.serve.pj by hand)") {
			t.Fatalf("%s of a loaded label with no plist: %v", name, err)
		}
		if !f.loaded {
			t.Errorf("%s unloaded the label", name)
		}
	}
	want := []string{"print gui/501/obor.serve.pj", "print gui/501/obor.serve.pj"}
	if got := f.joined(); got != strings.Join(want, "\n") {
		t.Errorf("launchctl calls:\n%s\nwant only the prints that read it:\n%s", got, strings.Join(want, "\n"))
	}
}

// Off darwin every verb prints the one line and does nothing (no launchctl, no plist).
func TestElsewhere(t *testing.T) {
	f := fakeLaunchctl(t)
	s := darwin(t)
	s.GOOS = "linux"
	ctx := context.Background()
	for name, verb := range map[string]func(context.Context) (string, error){
		"install": s.Install, "uninstall": s.Uninstall, "restart": s.Restart} {
		if line, err := verb(ctx); err != nil || line != "run `obor serve` yourself (systemd --user later)" {
			t.Errorf("%s on linux: %q, %v", name, line, err)
		}
	}
	if _, loaded := s.PID(ctx); loaded {
		t.Error("loaded on linux")
	}
	if len(f.calls) != 0 {
		t.Errorf("launchctl called: %q", f.calls)
	}
	if _, err := os.Stat(s.PlistPath()); !os.IsNotExist(err) {
		t.Errorf("plist written on linux: %v", err)
	}
}

// Current: only after an install of this very plist that launchd has loaded; a changed plist (another PATH), an
// unloaded label and off darwin are not current.
func TestCurrent(t *testing.T) {
	f := fakeLaunchctl(t)
	s := darwin(t)
	ctx := context.Background()
	if s.Current(ctx) {
		t.Fatal("current before an install")
	}
	if _, err := s.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Current(ctx) {
		t.Error("not current after the install")
	}
	moved := s
	moved.Path = "/opt/bin:/usr/bin"
	if moved.Current(ctx) {
		t.Error("current with another PATH than the plist's")
	}
	f.loaded = false
	if s.Current(ctx) {
		t.Error("current while launchd has it unloaded")
	}
	s.GOOS = "linux"
	if s.Current(ctx) {
		t.Error("current off darwin")
	}
}

// Install and Restart move a serve.log over MaxLog to serve.log.1 (replacing an old one) before they start
// the agent; a smaller or missing log is left; a failed rename returns the error and launchctl is not called to
// start anything.
func TestRotateLog(t *testing.T) {
	for _, verb := range []string{"install", "restart"} {
		run := func(s Service) error {
			var err error
			if verb == "install" {
				_, err = s.Install(context.Background())
			} else {
				_, err = s.Restart(context.Background())
			}
			return err
		}
		prep := func(t *testing.T) (Service, *fakeLaunchd) {
			f := fakeLaunchctl(t)
			s := darwin(t)
			if err := os.MkdirAll(filepath.Dir(s.LogPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			if verb == "restart" {
				if _, err := s.Install(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !f.loaded {
					t.Fatal("precondition: loaded")
				}
				f.calls = nil
			}
			return s, f
		}
		t.Run(verb+"/over the limit is renamed and the old .1 replaced", func(t *testing.T) {
			s, _ := prep(t)
			bigLog(t, s.LogPath(), MaxLog+1)
			if err := os.WriteFile(s.LogPath()+".1", []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := run(s); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(s.LogPath()); !os.IsNotExist(err) {
				t.Errorf("serve.log kept: %v", err)
			}
			if fi, err := os.Stat(s.LogPath() + ".1"); err != nil || fi.Size() != MaxLog+1 {
				t.Errorf("serve.log.1: %v %v", fi, err)
			}
		})
		t.Run(verb+"/at the limit is kept", func(t *testing.T) {
			s, _ := prep(t)
			bigLog(t, s.LogPath(), MaxLog)
			if err := run(s); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(s.LogPath()); err != nil {
				t.Errorf("serve.log gone: %v", err)
			}
			if _, err := os.Stat(s.LogPath() + ".1"); !os.IsNotExist(err) {
				t.Errorf("serve.log.1 made: %v", err)
			}
		})
		t.Run(verb+"/missing does nothing", func(t *testing.T) {
			s, _ := prep(t)
			if err := run(s); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(s.LogPath() + ".1"); !os.IsNotExist(err) {
				t.Errorf("serve.log.1 made: %v", err)
			}
		})
		t.Run(verb+"/rename error is returned and nothing is started", func(t *testing.T) {
			s, f := prep(t)
			bigLog(t, s.LogPath(), MaxLog+1)
			// a non-empty directory at .1 cannot be replaced by a file
			if err := os.MkdirAll(filepath.Join(s.LogPath()+".1", "x"), 0o755); err != nil {
				t.Fatal(err)
			}
			before := len(f.calls)
			if err := run(s); err == nil {
				t.Fatal("no error")
			}
			// only the owner check's print may have run; no bootstrap, bootout or kickstart
			for _, call := range f.calls[before:] {
				if !strings.HasPrefix(call, "print") {
					t.Errorf("%s after the failed rotation: %q", verb, call)
				}
			}
		})
	}
}

// bigLog makes path a sparse log of size bytes.
func bigLog(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// serve's own start copies a serve.log over MaxLog to serve.log.1 (replacing an old one) and truncates
// serve.log in place, so the append-mode fd launchd holds keeps writing the same file at offset 0; a log at the limit
// or a missing one is left.
func TestRotateServeLog(t *testing.T) {
	const keep = "the old log\n"
	// setup is a project root with a serve.log of size bytes, its first bytes keep.
	setup := func(t *testing.T, size int64) (root, log, old string) {
		t.Helper()
		root = t.TempDir()
		log = ServeLog(root)
		if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
			t.Fatal(err)
		}
		bigLog(t, log, size)
		f, err := os.OpenFile(log, os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(keep); err != nil {
			t.Fatal(err)
		}
		return root, log, keep + strings.Repeat("\x00", int(size)-len(keep))
	}
	t.Run("over the limit is copied and truncated in place", func(t *testing.T) {
		root, log, old := setup(t, MaxLog+int64(len(keep)))
		if _, err := os.Stat(log + ".1"); !os.IsNotExist(err) {
			t.Fatalf("precondition: no .1: %v", err)
		}
		if err := os.WriteFile(log+".1", []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := RotateServeLog(root); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(log + ".1"); err != nil || string(b) != old {
			t.Errorf("serve.log.1: %v, %d bytes; want the old log, %d bytes", err, len(b), len(old))
		}
		if fi, err := os.Stat(log); err != nil || fi.Size() != 0 {
			t.Fatalf("serve.log: %v %v; want an empty file", fi, err)
		}
		// launchd's fd is in O_APPEND: its next write is the file's first byte, not the old offset.
		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("after\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(log); err != nil || string(b) != "after\n" {
			t.Errorf("serve.log after an append: %v %q; want %q", err, b, "after\n")
		}
	})
	t.Run("at the limit is untouched", func(t *testing.T) {
		root, log, old := setup(t, MaxLog)
		if err := RotateServeLog(root); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(log); err != nil || string(b) != old {
			t.Errorf("serve.log changed: %v, %d bytes; want %d", err, len(b), len(old))
		}
		if _, err := os.Stat(log + ".1"); !os.IsNotExist(err) {
			t.Errorf("serve.log.1 made: %v", err)
		}
	})
	t.Run("a failed copy keeps the log", func(t *testing.T) {
		root, log, old := setup(t, MaxLog+int64(len(keep)))
		// a non-empty directory at .1 cannot be replaced by a file
		if err := os.MkdirAll(filepath.Join(log+".1", "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := RotateServeLog(root); err == nil {
			t.Fatal("no error")
		}
		if b, err := os.ReadFile(log); err != nil || string(b) != old {
			t.Errorf("serve.log changed: %v, %d bytes; want the old %d", err, len(b), len(old))
		}
	})
	t.Run("missing does nothing", func(t *testing.T) {
		root := t.TempDir()
		if err := RotateServeLog(root); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(ServeLog(root) + ".1"); !os.IsNotExist(err) {
			t.Errorf("serve.log.1 made: %v", err)
		}
	})
}

// kickstart -k waits for the old serve to exit; one slower than callTimeout (which does not bound it) but inside
// stopTimeout is a plain RESTARTED, no warning, kickstart called once. The wait is the bubble's clock.
func TestRestartSlowKickstart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := fakeLaunchctl(t)
		s := installed(t, f)
		f.kick = kick{pid: pidOf(4243), wait: callTimeout + time.Second}
		if f.kick.wait >= stopTimeout {
			t.Fatal("precondition: the kickstart is inside stopTimeout")
		}
		line, err := s.Restart(context.Background())
		if err != nil || line != "RESTARTED obor.serve.pj" {
			t.Fatalf("restart with a slow kickstart: %q, %v", line, err)
		}
		if n := strings.Count(f.joined(), "kickstart -k"); n != 1 {
			t.Errorf("kickstart called %d times: %q", n, f.calls)
		}
	})
}

// launchctl ended by its deadline (signal: killed, as the axis leads saw) while launchd did restart serve —
// the pid changed: RESTARTED with the new pid and a warning line naming launchctl's error and the old pid.
func TestRestartKickstartErrorPidChanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := fakeLaunchctl(t)
		s := installed(t, f)
		was := stopTimeout
		stopTimeout = 300 * time.Millisecond
		t.Cleanup(func() { stopTimeout = was })
		f.kick = kick{pid: pidOf(4243), wait: 5 * time.Second}
		start := time.Now()
		line, err := s.Restart(context.Background())
		if err != nil {
			t.Fatalf("restart that happened reported a failure: %v", err)
		}
		lines := strings.Split(line, "\n")
		if len(lines) != 2 || lines[0] != "RESTARTED obor.serve.pj (pid 4243)" {
			t.Fatalf("restart: %q", line)
		}
		if lines[1] != "warning: launchctl kickstart -k gui/501/obor.serve.pj: signal: killed: serve restarted all the same (pid 4242 before)" {
			t.Errorf("warning line: %q", lines[1])
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("restart took %v: the killed kickstart was waited out", d)
		}
	})
}

// a kickstart error after which serve was not restarted is a FAIL naming launchctl's error and the state it
// left: the old pid still running, no serve running (the pid moved from 4242 to 0, which is no restart), or the label
// gone from launchd. The poll that finds it reads the whole settle window, on the bubble's clock.
func TestRestartKickstartErrorNotRestarted(t *testing.T) {
	for _, c := range []struct {
		name string
		kick kick
		want string
	}{
		{"pid unchanged", kick{exit: 1},
			"obor.serve.pj still runs the old serve (pid 4242), not restarted: obor serve restart again"},
		{"no serve runs", kick{exit: 1, pid: pidOf(0)},
			"obor.serve.pj is loaded but no serve runs (log "},
		{"unloaded", kick{exit: 1, unload: true},
			"obor.serve.pj is no longer loaded in launchd: obor serve install"},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := fakeLaunchctl(t)
				s := installed(t, f)
				f.kick = c.kick
				line, err := s.Restart(context.Background())
				if err == nil {
					t.Fatalf("restart: %q, no error", line)
				}
				if !strings.HasPrefix(err.Error(), "launchctl kickstart -k gui/501/obor.serve.pj: exit status 1") ||
					!strings.Contains(err.Error(), c.want) {
					t.Errorf("error: %v\nwant it to name %q", err, c.want)
				}
			})
		})
	}
}

// a reinstall whose bootstrap meets the label half torn down (5: Input/output error, nothing loaded) boots it
// out and bootstraps once more: INSTALLED, the label loaded.
func TestInstallBootstrapEIORetried(t *testing.T) {
	f := fakeLaunchctl(t)
	s := installed(t, f)
	f.eio = 1
	line, err := s.Install(context.Background())
	if err != nil || !strings.HasPrefix(line, "INSTALLED obor.serve.pj (") {
		t.Fatalf("install after a bootstrap EIO: %q, %v", line, err)
	}
	if !f.loaded {
		t.Error("the label is not loaded after the install")
	}
	var verbs []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "print") {
			verbs = append(verbs, strings.Fields(c)[0])
		}
	}
	if got := strings.Join(verbs, " "); got != "bootout bootstrap bootout bootstrap" {
		t.Errorf("launchctl verbs: %s; want bootout bootstrap bootout bootstrap", got)
	}
}

// a bootstrap that fails twice (before and after the bootout) is a FAIL naming both errors and the label's
// state (not loaded) with the next step; a bootstrap that returns without loading the label is a FAIL too.
func TestInstallEndsNotLoaded(t *testing.T) {
	t.Run("bootstrap fails twice", func(t *testing.T) {
		f := fakeLaunchctl(t)
		s := darwin(t)
		f.eio = 2
		line, err := s.Install(context.Background())
		if err == nil {
			t.Fatalf("install: %q, no error", line)
		}
		for _, want := range []string{"Bootstrap failed: 5: Input/output error; after a bootout, again: launchctl bootstrap",
			"obor.serve.pj is not loaded in launchd: obor serve install again (launchctl print gui/501/obor.serve.pj"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error: %v\nwant %q", err, want)
			}
		}
		if n := strings.Count(f.joined(), "bootstrap gui/501"); n != 2 {
			t.Errorf("bootstrap called %d times: %q", n, f.calls)
		}
		if f.loaded {
			t.Error("precondition of the message: the fake has the label loaded")
		}
	})
	t.Run("bootstrap loads nothing", func(t *testing.T) {
		f := fakeLaunchctl(t)
		s := darwin(t)
		f.noload = true
		line, err := s.Install(context.Background())
		if err == nil || !strings.Contains(err.Error(), "returned, but obor.serve.pj is not loaded in launchd: obor serve install again") {
			t.Errorf("install that loaded nothing: %q, %v", line, err)
		}
	})
}

// The exec boundary, the package's one test that starts a process: launchctl's arguments reach the binary, its output
// and exit code come back in the error, and a call past its deadline is killed. Every other test answers launchd in
// process (fakeLaunchctl).
func TestLaunchctlExec(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
exit) echo "launchd said no" >&2; exit 3 ;;
slow) exec sleep 30 ;;
esac
echo "$*"
`
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := launchctl(context.Background(), callTimeout, "print", "gui/501/obor.serve.pj")
	if err != nil || out != "print gui/501/obor.serve.pj\n" {
		t.Errorf("print: %q, %v", out, err)
	}
	if _, err := launchctl(context.Background(), callTimeout, "exit"); err == nil ||
		!strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "launchd said no") {
		t.Errorf("a failing call: %v", err)
	}
	start := time.Now()
	if _, err := launchctl(context.Background(), 100*time.Millisecond, "slow"); err == nil ||
		!strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("a call past its deadline: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the call took %v: its deadline did not end it", d)
	}
}
