//go:build integration

package kind_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/kinds/agy/kind"
)

// stubObor is an executable obor whose `hook <name>` runs script (sh) with the event on stdin.
func stubObor(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "obor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Exec runs `<obor> hook <name>` with the event on stdin and this process's environment: its stdout, stderr and exit
// code come back. It fails (err) when the obor is unnamed or missing, and when it does not end within the bound, its
// process group killed (the child it started too) at once.
func TestExec(t *testing.T) {
	t.Setenv("OBOR_ROLE", "worker")
	exe := stubObor(t, `[ "$1 $2" = "hook pre-tool-use" ] || exit 9
read -r ev; echo "got $ev as $OBOR_ROLE"; echo "obor: denied" >&2; exit 2`)
	out, errOut, code, err := kind.Exec(exe, 5*time.Second)("pre-tool-use", []byte(`{"tool_name":"Bash"}`+"\n"))
	if err != nil || code != 2 || string(out) != `got {"tool_name":"Bash"} as worker`+"\n" || string(errOut) != "obor: denied\n" {
		t.Errorf("a run: %q, %q, %d, %v", out, errOut, code, err)
	}
	if _, _, code, err := kind.Exec(stubObor(t, "exit 3"), 5*time.Second)("stop", nil); err != nil || code != 3 {
		t.Errorf("a non-zero exit: %d, %v; want code 3, no error", code, err)
	}
	if _, _, _, err := kind.Exec("", 5*time.Second)("stop", nil); err == nil || !strings.Contains(err.Error(), "OBOR_EXE is not set") {
		t.Errorf("no obor named: %v", err)
	}
	if _, _, _, err := kind.Exec(filepath.Join(t.TempDir(), "obor"), 5*time.Second)("stop", nil); err == nil {
		t.Error("a missing obor: no error")
	}
	pidFile := filepath.Join(t.TempDir(), "child")
	slow := stubObor(t, "sleep 30 & echo $! > "+pidFile+"; wait")
	t0 := time.Now()
	_, _, _, err = kind.Exec(slow, 300*time.Millisecond)("stop", nil)
	if took := time.Since(t0); err == nil || !strings.Contains(err.Error(), "no answer within 300ms") || took > 5*time.Second {
		t.Errorf("a slow obor: %v after %s; want no answer within 300ms, at once", err, took)
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("precondition: the slow obor started its child: %v", rerr)
	}
	pid := strings.TrimSpace(string(b))
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		t.Errorf("the slow obor's child %s outlived the kill", pid)
	}
}

// alive: process pid exists (signal 0 reaches it).
func alive(pid string) bool {
	n, err := strconv.Atoi(pid)
	return err == nil && syscall.Kill(n, 0) == nil
}
