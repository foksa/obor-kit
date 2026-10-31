package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// state (a file: bootstrap loads it, bootout unloads it, print answers "pid = 4242" while loaded and exits 113
// otherwise).
func fakeLaunchctl(t *testing.T) (log, state string) {
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	state = filepath.Join(dir, "loaded")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
state="` + state + `"
case "$1" in
bootstrap) : > "$state" ;;
bootout) [ -f "$state" ] || exit 3; rm "$state" ;;
kickstart) [ -f "$state" ] || exit 113 ;;
print) [ -f "$state" ] || { echo "Could not find service" >&2; exit 113; }; printf 'gui/501/x = {\n\tstate = running\n\tpid = 4242\n}\n' ;;
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
	want := []string{"print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootstrap gui/501 " + plist, "print gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootout gui/501/obor.serve.pj", "bootstrap gui/501 " + plist,
		"print gui/501/obor.serve.pj", "kickstart -k gui/501/obor.serve.pj",
		"print gui/501/obor.serve.pj", "bootout gui/501/obor.serve.pj"}
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
