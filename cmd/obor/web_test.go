package main

import (
	"strings"
	"testing"

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
