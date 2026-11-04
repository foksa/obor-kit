package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// T-806: the herdr notice follows the server serve is connected to: each snapshot records its version and protocol,
// a change is logged once, and the status note comes from that state.
func TestHerdrNoteFollowsTheConnectedServer(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	a := rootApp(t.TempDir(), &out)
	snap := func(version string, protocol int) port.Snapshot {
		return port.Snapshot{Version: version, Protocol: protocol, Speaks: 22}
	}
	lines := func() int {
		return strings.Count(out.String(), "herdr: ") - strings.Count(out.String(), "herdr: connected")
	}
	ctx := context.Background()

	if n := a.Status().HerdrNote; n != "" || a.Status().Connected {
		t.Fatalf("before any connect: note %q, connected %v; want none", n, a.Status().Connected)
	}
	// Serve started with herdr down; herdr comes up newer than obor's protocol.
	a.connect(ctx, snap("0.9.4", 23))
	want := "herdr 0.9.4, protocol 23 (newer than obor's 22)"
	if n := a.Status().HerdrNote; n != want {
		t.Errorf("note after herdr came up = %q, want %q", n, want)
	}
	if got := strings.Count(out.String(), "herdr: 0.9.4 protocol 23\n"); got != 1 {
		t.Errorf("log names the first version %d times, want once:\n%s", got, &out)
	}
	// The same version again (a reconnect): no new line.
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()
	if n := a.Status().HerdrNote; n != "" {
		t.Errorf("note while disconnected = %q, want none", n)
	}
	a.connect(ctx, snap("0.9.4", 23))
	if got := strings.Count(out.String(), "herdr: 0.9.4 protocol 23\n"); got != 1 {
		t.Errorf("a reconnect to the same version logged it %d times in all, want 1:\n%s", got, &out)
	}
	if lines() != 1 {
		t.Errorf("version lines = %d, want 1:\n%s", lines(), &out)
	}
	// A different (newer) version: the note changes and the line is logged once.
	a.connect(ctx, snap("0.9.5", 24))
	if n, w := a.Status().HerdrNote, "herdr 0.9.5, protocol 24 (newer than obor's 22)"; n != w {
		t.Errorf("note after the upgrade = %q, want %q", n, w)
	}
	if got := strings.Count(out.String(), "herdr: 0.9.5 protocol 24\n"); got != 1 {
		t.Errorf("log names the upgraded version %d times, want once:\n%s", got, &out)
	}
	// Back to obor's own protocol: no note, and the change is logged.
	a.connect(ctx, snap("0.8.0", 22))
	if n := a.Status().HerdrNote; n != "" {
		t.Errorf("note on obor's own protocol = %q, want none", n)
	}
	if got := strings.Count(out.String(), "herdr: 0.8.0 protocol 22\n"); got != 1 {
		t.Errorf("log names the downgraded version %d times, want once:\n%s", got, &out)
	}
}
