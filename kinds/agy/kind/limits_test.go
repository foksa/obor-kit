package kind

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/agents/plugin"
)

// Each handler's own limit sits under the protocol timeout of the call that runs it: a slow agy fails the handler, whose
// error names what ran, before the transport gives up on the call.
func TestHandlerLimitsUnderCallTimeouts(t *testing.T) {
	for _, c := range []struct {
		name, call string
		limit      time.Duration
	}{
		{"agy models", "start", modelsLimit},
		{"agentapi send-message", "deliver", SendLimit},
	} {
		if c.limit <= 0 {
			t.Fatalf("precondition: %s's limit is %s", c.name, c.limit)
		}
		if call := plugin.Timeouts[c.call]; c.limit >= call {
			t.Errorf("%s's limit %s is not under %s's %s call timeout", c.name, c.limit, c.call, call)
		}
	}
}

// A send that runs past its limit stops agy's whole process group: the child agentapi leaves waiting on the language
// server does not outlive the refused send.
func TestSendStopsTheGroup(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" != agentapi ]; then echo 'a prompt' >&2; exit 2; fi\n" +
		"printf '%s\\n' $$ > " + pids + "\n" +
		"sleep 300 > /dev/null 2>&1 &\n" +
		"printf '%s\\n' $! >> " + pids + "\n" +
		"wait\n"
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	rec := map[string]string{VarAddress: "localhost:5011", VarToken: "tok-1", VarConversation: "conv-1"}
	limit := 500 * time.Millisecond
	err := sendWithin(rec, "[obor] hi", limit)
	if err == nil || err.Error() != "no answer within "+limit.String() {
		t.Fatalf("sendWithin = %v, want no answer within %s", err, limit)
	}
	b, err := os.ReadFile(pids)
	if err != nil {
		t.Fatalf("precondition: the stub wrote no pid: %v", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		t.Fatalf("the stub wrote %q, want its pid and its child's", b)
	}
	for _, f := range fields {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("the stub wrote %q, want pids", b)
		}
		t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
		waitGone(t, pid)
	}
}

// waitGone waits up to 2 s for pid to be gone (a signalled process may take a moment to be reaped).
func waitGone(t *testing.T, pid int) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(until) {
			t.Fatalf("pid %d still runs after the send's limit: its process group was not stopped", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
