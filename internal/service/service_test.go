package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// fakeLaunchctl puts a launchctl on PATH that logs each call (one line of its args) and keeps one label's state in
// state (a file: bootstrap loads it, bootout unloads it, print answers "pid = <the pid file, else 4242>" while loaded
// and exits 113 otherwise). Knobs (T-771), in state's directory: a file eio holding n fails the next n bootstraps
// with launchd's "5: Input/output error", nothing loaded; a file noload makes bootstrap exit 0 loading nothing. In
// the environment: FAKE_KICK_PID is written to the pid file as kickstart begins, FAKE_KICK_UNLOAD unloads the label
// then, FAKE_KICK_SLEEP seconds it then takes (exec sleep: a kill ends it), and FAKE_KICK_EXIT is its exit code.
func fakeLaunchctl(t *testing.T) (log, state string) {
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	state = filepath.Join(dir, "loaded")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
dir="` + dir + `"
state="` + state + `"
case "$1" in
bootstrap)
	n=$(cat "$dir/eio" 2>/dev/null || echo 0)
	if [ "$n" -gt 0 ]; then echo $((n-1)) > "$dir/eio"; echo "Bootstrap failed: 5: Input/output error" >&2; exit 5; fi
	[ -f "$dir/noload" ] || : > "$state" ;;
bootout) [ -f "$state" ] || exit 3; rm "$state" ;;
kickstart)
	[ -f "$state" ] || exit 113
	[ -n "$FAKE_KICK_PID" ] && echo "$FAKE_KICK_PID" > "$dir/pid"
	[ -n "$FAKE_KICK_UNLOAD" ] && rm "$state"
	[ -n "$FAKE_KICK_SLEEP" ] && exec sleep "$FAKE_KICK_SLEEP"
	exit "${FAKE_KICK_EXIT:-0}" ;;
print) [ -f "$state" ] || { echo "Could not find service" >&2; exit 113; }
	printf 'gui/501/x = {\n\tstate = running\n\tpid = %s\n}\n' "$(cat "$dir/pid" 2>/dev/null || echo 4242)" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log, state
}

func calls(t *testing.T, log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func darwin(t *testing.T) Service {
	home := t.TempDir()
	return Service{Name: "pj", Root: filepath.Join(home, "pj"), Kit: "/kit", Home: home, UID: 501, GOOS: "darwin",
		Path: "/usr/bin"}
}

// install writes the plist and bootstraps it (a second install boots the loaded one out first); restart kickstarts
// it; uninstall boots it out and removes the plist; uninstall again and restart of nothing say it is not installed.
func TestVerbs(t *testing.T) {
	log, _ := fakeLaunchctl(t)
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
	// it unloaded (T-771); the test's own PID reads are the 1st and 5th
	want := []string{"print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootstrap gui/501 " + plist, "print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootout gui/501/obor.serve.pj", "print gui/501/obor.serve.pj",
		"bootstrap gui/501 " + plist, "print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "kickstart -k gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootout gui/501/obor.serve.pj", "print gui/501/obor.serve.pj"}
	if got := calls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("launchctl calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
	log, _ := fakeLaunchctl(t)
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
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("launchctl called: %q", calls(t, log))
	}
	if err := os.WriteFile(s.PlistPath(), s.Plist(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(context.Background()); err != nil {
		t.Errorf("install over its own plist: %v", err)
	}
}

// T-607 #3: uninstall and restart check the plist's owner first: another project's plist of this label FAILs naming
// it, and launchctl is never called — its agent is not booted out or kickstarted.
func TestVerbsLeaveAnotherRootsAgent(t *testing.T) {
	log, state := fakeLaunchctl(t)
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
	if err := os.WriteFile(state, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, loaded := s.PID(ctx); !loaded {
		t.Fatal("precondition: the label is loaded")
	}
	if err := os.Remove(log); err != nil { // the precondition's own print is not the verbs' doing
		t.Fatal(err)
	}
	for name, verb := range map[string]func(context.Context) (string, error){"uninstall": s.Uninstall, "restart": s.Restart} {
		_, err := verb(ctx)
		if err == nil || !strings.Contains(err.Error(), "runs obor serve for /elsewhere/pj, not "+s.Root) {
			t.Fatalf("%s over another project's plist: %v", name, err)
		}
		if b, _ := os.ReadFile(s.PlistPath()); string(b) != string(other.Plist()) {
			t.Errorf("%s changed the other project's plist:\n%s", name, b)
		}
		if _, err := os.Stat(state); err != nil {
			t.Errorf("%s unloaded the other project's agent: %v", name, err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("launchctl called: %q", calls(t, log))
	}
}

// T-607 #3: a label launchd has loaded with no plist at all is nobody's to boot out or kickstart: uninstall and
// restart FAIL saying whose it is cannot be told, and launchctl is only asked whether it is loaded.
func TestVerbsRefuseUnownedLabel(t *testing.T) {
	log, state := fakeLaunchctl(t)
	s := darwin(t)
	if err := os.WriteFile(state, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.PlistPath()); !os.IsNotExist(err) {
		t.Fatalf("precondition: no plist: %v", err)
	}
	ctx := context.Background()
	for name, verb := range map[string]func(context.Context) (string, error){"uninstall": s.Uninstall, "restart": s.Restart} {
		_, err := verb(ctx)
		if err == nil || !strings.Contains(err.Error(), "cannot tell whose it is (launchctl bootout gui/501/obor.serve.pj by hand)") {
			t.Fatalf("%s of a loaded label with no plist: %v", name, err)
		}
		if _, err := os.Stat(state); err != nil {
			t.Errorf("%s unloaded the label: %v", name, err)
		}
	}
	want := []string{"print gui/501/obor.serve.pj", "print gui/501/obor.serve.pj"}
	if got := calls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("launchctl calls:\n%s\nwant only the prints that read it:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Off darwin every verb prints the one line and does nothing (no launchctl, no plist).
func TestElsewhere(t *testing.T) {
	log, _ := fakeLaunchctl(t)
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
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("launchctl called: %q", calls(t, log))
	}
	if _, err := os.Stat(s.PlistPath()); !os.IsNotExist(err) {
		t.Errorf("plist written on linux: %v", err)
	}
}

// Current: only after an install of this very plist that launchd has loaded; a changed plist (another PATH), an
// unloaded label and off darwin are not current.
func TestCurrent(t *testing.T) {
	_, state := fakeLaunchctl(t)
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
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if s.Current(ctx) {
		t.Error("current while launchd has it unloaded")
	}
	s.GOOS = "linux"
	if s.Current(ctx) {
		t.Error("current off darwin")
	}
}

// T-614: Install and Restart move a serve.log over MaxLog to serve.log.1 (replacing an old one) before they start
// the agent; a smaller or missing log is left; a failed rename returns the error and launchctl is not called to
// start anything.
func TestRotateLog(t *testing.T) {
	big := func(t *testing.T, path string, size int64) {
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
		prep := func(t *testing.T) (Service, string) {
			log, state := fakeLaunchctl(t)
			s := darwin(t)
			if err := os.MkdirAll(filepath.Dir(s.LogPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			if verb == "restart" {
				if _, err := s.Install(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(state); err != nil {
					t.Fatalf("precondition: loaded: %v", err)
				}
				if err := os.Remove(log); err != nil {
					t.Fatal(err)
				}
			}
			return s, log
		}
		t.Run(verb+"/over the limit is renamed and the old .1 replaced", func(t *testing.T) {
			s, _ := prep(t)
			big(t, s.LogPath(), MaxLog+1)
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
			big(t, s.LogPath(), MaxLog)
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
			s, log := prep(t)
			big(t, s.LogPath(), MaxLog+1)
			// a non-empty directory at .1 cannot be replaced by a file
			if err := os.MkdirAll(filepath.Join(s.LogPath()+".1", "x"), 0o755); err != nil {
				t.Fatal(err)
			}
			before := 0
			if b, err := os.ReadFile(log); err == nil {
				before = len(strings.Split(strings.TrimSpace(string(b)), "\n"))
			}
			if err := run(s); err == nil {
				t.Fatal("no error")
			}
			after := 0
			if b, err := os.ReadFile(log); err == nil {
				after = len(strings.Split(strings.TrimSpace(string(b)), "\n"))
			}
			// only the owner check's print may have run; no bootstrap, bootout or kickstart
			if b, _ := os.ReadFile(log); strings.Contains(string(b), "kickstart") && verb == "install" {
				t.Errorf("started: %s", b)
			}
			if verb == "install" && strings.Contains(strings.Join(calls(t, log), "\n"), "bootstrap") {
				t.Errorf("bootstrap called: %v", calls(t, log))
			}
			if verb == "restart" {
				if got := strings.Join(calls(t, log)[before:after], "\n"); strings.Contains(got, "kickstart") {
					t.Errorf("kickstart called: %s", got)
				}
			}
		})
	}
}

// fast shortens the launchctl timeouts for a test: callTimeout (the flat deadline every call once had) 300ms,
// stopTimeout 3s, settle 500ms read every 50ms.
func fast(t *testing.T) {
	c, st, se, p := callTimeout, stopTimeout, settle, pollEvery
	callTimeout, stopTimeout, settle, pollEvery = 300*time.Millisecond, 3*time.Second, 500*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { callTimeout, stopTimeout, settle, pollEvery = c, st, se, p })
}

// installed is s installed in the fake launchd (loaded, pid 4242), its calls log emptied.
func installed(t *testing.T, log string) Service {
	t.Helper()
	s := darwin(t)
	if _, err := s.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pid, loaded := s.PID(context.Background()); !loaded || pid != 4242 {
		t.Fatalf("precondition: installed, pid 4242: %d %v", pid, loaded)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	return s
}

// T-771: kickstart -k waits for the old serve to exit; one slower than the flat deadline launchctl once had
// (callTimeout) but inside stopTimeout is a plain RESTARTED, no warning, kickstart called once.
func TestRestartSlowKickstart(t *testing.T) {
	fast(t)
	log, _ := fakeLaunchctl(t)
	s := installed(t, log)
	t.Setenv("FAKE_KICK_SLEEP", "1")
	t.Setenv("FAKE_KICK_PID", "4243")
	if time.Second <= callTimeout || time.Second >= stopTimeout {
		t.Fatal("precondition: the kickstart outlasts callTimeout, not stopTimeout")
	}
	line, err := s.Restart(context.Background())
	if err != nil || line != "RESTARTED obor.serve.pj" {
		t.Fatalf("restart with a slow kickstart: %q, %v", line, err)
	}
	if n := strings.Count(strings.Join(calls(t, log), "\n"), "kickstart -k"); n != 1 {
		t.Errorf("kickstart called %d times: %q", n, calls(t, log))
	}
}

// T-771: launchctl ended by its deadline (signal: killed, as the axis leads saw) while launchd did restart serve —
// the pid changed: RESTARTED with the new pid and a warning line naming launchctl's error and the old pid.
func TestRestartKickstartErrorPidChanged(t *testing.T) {
	fast(t)
	log, _ := fakeLaunchctl(t)
	s := installed(t, log)
	stopTimeout = 300 * time.Millisecond
	t.Setenv("FAKE_KICK_PID", "4243")
	t.Setenv("FAKE_KICK_SLEEP", "5")
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
}

// T-771: a kickstart error after which serve was not restarted is a FAIL naming launchctl's error and the state it
// left: the old pid still running, no serve running, or the label gone from launchd.
func TestRestartKickstartErrorNotRestarted(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"pid unchanged", map[string]string{"FAKE_KICK_EXIT": "1"},
			"obor.serve.pj still runs the old serve (pid 4242), not restarted: obor serve restart again"},
		{"no serve runs", map[string]string{"FAKE_KICK_EXIT": "1", "FAKE_KICK_PID": "0"},
			"obor.serve.pj is loaded but no serve runs (log "},
		{"unloaded", map[string]string{"FAKE_KICK_EXIT": "1", "FAKE_KICK_UNLOAD": "1"},
			"obor.serve.pj is no longer loaded in launchd: obor serve install"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fast(t)
			log, _ := fakeLaunchctl(t)
			s := installed(t, log)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			line, err := s.Restart(context.Background())
			if err == nil {
				t.Fatalf("restart: %q, no error", line)
			}
			if !strings.HasPrefix(err.Error(), "launchctl kickstart -k gui/501/obor.serve.pj: exit status 1") ||
				!strings.Contains(err.Error(), c.want) {
				t.Errorf("error: %v\nwant it to name %q", err, c.want)
			}
		})
	}
}

// T-771: a reinstall whose bootstrap meets the label half torn down (5: Input/output error, nothing loaded) boots it
// out and bootstraps once more: INSTALLED, the label loaded.
func TestInstallBootstrapEIORetried(t *testing.T) {
	fast(t)
	log, state := fakeLaunchctl(t)
	s := installed(t, log)
	if err := os.WriteFile(filepath.Join(filepath.Dir(state), "eio"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	line, err := s.Install(context.Background())
	if err != nil || !strings.HasPrefix(line, "INSTALLED obor.serve.pj (") {
		t.Fatalf("install after a bootstrap EIO: %q, %v", line, err)
	}
	if _, loaded := s.PID(context.Background()); !loaded {
		t.Error("the label is not loaded after the install")
	}
	var verbs []string
	for _, c := range calls(t, log) {
		if !strings.HasPrefix(c, "print") {
			verbs = append(verbs, strings.Fields(c)[0])
		}
	}
	if got := strings.Join(verbs, " "); got != "bootout bootstrap bootout bootstrap" {
		t.Errorf("launchctl verbs: %s; want bootout bootstrap bootout bootstrap", got)
	}
}

// T-771: a bootstrap that fails twice (before and after the bootout) is a FAIL naming both errors and the label's
// state (not loaded) with the next step; a bootstrap that returns without loading the label is a FAIL too.
func TestInstallEndsNotLoaded(t *testing.T) {
	t.Run("bootstrap fails twice", func(t *testing.T) {
		fast(t)
		log, state := fakeLaunchctl(t)
		s := darwin(t)
		if err := os.WriteFile(filepath.Join(filepath.Dir(state), "eio"), []byte("2"), 0o644); err != nil {
			t.Fatal(err)
		}
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
		if n := strings.Count(strings.Join(calls(t, log), "\n"), "bootstrap gui/501"); n != 2 {
			t.Errorf("bootstrap called %d times: %q", n, calls(t, log))
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Errorf("precondition of the message: the fake has the label loaded: %v", err)
		}
	})
	t.Run("bootstrap loads nothing", func(t *testing.T) {
		fast(t)
		_, state := fakeLaunchctl(t)
		s := darwin(t)
		if err := os.WriteFile(filepath.Join(filepath.Dir(state), "noload"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		line, err := s.Install(context.Background())
		if err == nil || !strings.Contains(err.Error(), "returned, but obor.serve.pj is not loaded in launchd: obor serve install again") {
			t.Errorf("install that loaded nothing: %q, %v", line, err)
		}
	})
}
