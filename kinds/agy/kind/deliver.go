package kind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// The inbox (docs/agents/kind-protocol.md "deliver"; docs/research/agy-probes.md P7): agy's language server takes a
// message to a conversation over `agy agentapi send-message <conversationId> <text>`: an idle session starts a turn, a
// busy one reads it at its next model call, the owner's half-typed draft stays. The address and token it needs are only
// in the environment of the commands the agent runs (not in its hooks', its MCP servers' or on disk), so a session's
// record comes from an obor command it runs (obor records the manifest's inbox_record variables from every command's
// environment), and the kind sends with it. The record is the agent's to write, so it names no program: the kind runs
// the agy on its own PATH (the lookup `agy models` makes at every start), never an executable the record names (agy's
// ANTIGRAVITY_AGENTAPI_EXE is not recorded), and sends only to a local address. A refused send (a stale token: one per
// agy start) is the reply's error: obor marks the record refused and the lead's lines go through its hooks until a
// command records it again.

// The variables of InboxRecord, as agy sets them in a command's environment.
const (
	VarAddress      = "ANTIGRAVITY_LS_ADDRESS"      // the language server, localhost:<port>
	VarToken        = "ANTIGRAVITY_CSRF_TOKEN"      // its CSRF token
	VarConversation = "ANTIGRAVITY_CONVERSATION_ID" // the conversation the command runs in
)

// InboxRecord is the manifest's inbox_record: a session's record is these variables' values, all set.
var InboxRecord = []string{VarAddress, VarToken, VarConversation}

// SendLimit bounds one agentapi send-message, under deliver's own 3 s: a send with no answer by then is refused (sent
// again), not a deliver call with no reply.
const SendLimit = 2500 * time.Millisecond

// deliver is the deliver call: with a record that names all of InboxRecord's variables and a local address
// (localAddress), the text sent to the record's conversation through `agy agentapi send-message` with its address and
// token (ok, sent); the empty text is the record's check, nothing sent. No record, one missing a variable, or one whose
// address is not local: ok false, why the reason (no inbox: the line is typed, or waits), nothing run. A refused send:
// the reply's error, naming agentapi's exit and its output's last line.
func deliver(in request) map[string]any {
	rec := map[string]string{}
	if in.Record == "" || json.Unmarshal([]byte(in.Record), &rec) != nil {
		return map[string]any{"ok": false}
	}
	for _, v := range InboxRecord {
		if rec[v] == "" {
			return map[string]any{"ok": false}
		}
	}
	if !localAddress(rec[VarAddress]) {
		return map[string]any{"ok": false, "why": fmt.Sprintf("%s %q is not localhost:<port> or 127.0.0.1:<port>",
			VarAddress, rec[VarAddress])}
	}
	if in.Text != "" {
		if err := sendMessage(rec, in.Text); err != nil {
			return map[string]any{"error": "agentapi send-message: " + err.Error()}
		}
	}
	return map[string]any{"ok": true, "delivery": map[string]any{"sent": true}}
}

// localAddress: addr is localhost:<port> or 127.0.0.1:<port>, the port a number 1-65535 (agy's language server
// listens on the loopback; the record is the agent's, so any other host is refused).
func localAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || (host != "localhost" && host != "127.0.0.1") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

// sendMessage runs `agy agentapi send-message <conversation> <text>` of rec, agy from the kind's PATH, with rec's
// address and token laid over the kind's environment, under SendLimit: an error for a non-zero exit (its output's last
// line), no answer in time, or no agy on PATH.
func sendMessage(rec map[string]string, text string) error {
	exe, err := exec.LookPath(Name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), SendLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "agentapi", "send-message", rec[VarConversation], text)
	cmd.Env = append(os.Environ(), VarAddress+"="+rec[VarAddress], VarToken+"="+rec[VarToken])
	cmd.WaitDelay = 200 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("no answer within %s", SendLimit)
	case errors.As(err, &ee):
		why := lastLine(out.String())
		if why == "" {
			why = "no output"
		}
		return fmt.Errorf("exit %d: %s", ee.ExitCode(), why)
	}
	return err
}
