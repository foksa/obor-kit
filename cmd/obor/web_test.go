package main

import (
	"net"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/hc"
	"obor/internal/herdr"
	"obor/internal/project"
	"obor/internal/testkit"
)

// obor web is localhost only — a --listen whose host is not loopback (or has no host: every interface) is
// refused before anything starts — and the binary and the shim know the command.
func TestWebRefusesNonLocalhostListen(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	for _, args := range [][]string{
		{"web", "--listen", "0.0.0.0:8787"},
		{"web", "--listen", ":8787"},
		{"web", "--listen", "example.com:80"},
		{"web", "--listen", "192.168.1.4:8787"},
		{"web", "--listen", "127.0.0.1"},
	} {
		rc, stdout, stderr := run(t, dir, nil, args...)
		if rc != 2 || stdout != "" || !strings.Contains(stderr, "obor web: ") {
			t.Errorf("obor %v: exit %d, stdout %q, stderr %q; want 2 and the refusal", args, rc, stdout, stderr)
		}
	}
	if rc, stdout, stderr := run(t, dir, nil, "web", "--bogus"); rc != 2 || stdout != "" ||
		!strings.Contains(stderr, "usage: obor web [--listen 127.0.0.1:<port>]") {
		t.Errorf("obor web --bogus: exit %d, %q, %q; want 2 and the usage", rc, stdout, stderr)
	}
	if rc, stdout, stderr := run(t, dir, nil, "has", "web"); rc != 0 || stdout+stderr != "" {
		t.Errorf("obor has web: exit %d, %q %q; want 0 and nothing", rc, stdout, stderr)
	}
}

// a web whose port is held exits non-zero with one line naming the port: launchd's retry is the agent's way back, and
// the port is not hunted.
func TestWebPortHeld(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rc, stdout, stderr := run(t, testkit.Dir(t), nil, "web", "--listen", ln.Addr().String())
	if rc != 1 || stdout != "" || !strings.Contains(stderr, ln.Addr().String()) ||
		strings.Count(strings.TrimSpace(stderr), "\n") != 0 {
		t.Errorf("a held port: exit %d, %q, %q; want 1, nothing on stdout and one line naming %s", rc, stdout, stderr,
			ln.Addr())
	}
}

// obor web reads each project's panes through that project's own terminal (project.terminal): hc's client for an hc
// project, herdr's socket for one on the default, beside each other in one web; a project with no config reads none.
func TestWebProcessPicksEachProjectsTerminal(t *testing.T) {
	t.Parallel()
	env := project.Env(func(k string) (string, bool) {
		v, ok := map[string]string{"HC_SOCKET": "/tmp/hc.sock", "HERDR_SOCKET_PATH": "/tmp/herdr.sock"}[k]
		return v, ok
	})
	onHC := testkit.NewPlain(t)
	onHC.Config(func(c map[string]any) { c["project"].(map[string]any)["terminal"] = config.TerminalHC })
	onHerdr := testkit.NewPlain(t)
	onHerdr.Config()
	if cfg, err := config.Load(onHC.Root); err != nil || cfg.TerminalKind() != config.TerminalHC {
		t.Fatalf("precondition: the hc project's config names hc: %v", err)
	}
	ht, err := webTerminal(env, onHC.Root)
	if cl, ok := ht.Control.(*hc.Client); err != nil || !ok || cl.Path != "/tmp/hc.sock" {
		t.Errorf("the hc project's terminal: control %T, %v; want hc's client of /tmp/hc.sock", ht.Control, err)
	}
	dt, err := webTerminal(env, onHerdr.Root)
	if s, ok := dt.Control.(*herdr.Socket); err != nil || !ok || s.Path != "/tmp/herdr.sock" {
		t.Errorf("the herdr project's terminal: control %T, %v; want herdr's socket /tmp/herdr.sock", dt.Control, err)
	}
	if webProcess(env)(onHC.Root) == nil || webProcess(env)(onHerdr.Root) == nil {
		t.Errorf("webProcess reads no process info of a project whose terminal builds")
	}
	if f := webProcess(env)(testkit.NewDir(t).Root); f != nil {
		t.Errorf("webProcess of a project with no config reads process info, want none")
	}
}
