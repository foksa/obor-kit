package kind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// The inbox (docs/agents/kind-protocol.md "deliver"; docs/research/agy-probes.md P7): agy's language server takes a
// message to a conversation over `agentapi send-message <conversationId> <text>` (agy's own agentapi, a wrapper for
// `agy agentapi`): an idle session starts a turn, a busy one reads it at its next model call, the owner's half-typed
// draft stays. The address and token it needs are only in the environment of the commands the agent runs (not in its
// hooks', its MCP servers' or on disk), so a session's record comes from an obor command it runs (obor records the
// manifest's inbox_record variables from every command's environment), and the kind sends with it. A refused send (a
// stale token: one per agy start) is the reply's error: obor marks the record refused and the lead's lines go through
// its hooks until a command records it again.

// The variables of InboxRecord, as agy sets them in a command's environment.
const (
	VarAddress      = "ANTIGRAVITY_LS_ADDRESS"      // the language server, localhost:<port>
	VarToken        = "ANTIGRAVITY_CSRF_TOKEN"      // its CSRF token
	VarAgentAPI     = "ANTIGRAVITY_AGENTAPI_EXE"    // agy's agentapi executable
	VarConversation = "ANTIGRAVITY_CONVERSATION_ID" // the conversation the command runs in
)

// InboxRecord is the manifest's inbox_record: a session's record is these variables' values, all set.
var InboxRecord = []string{VarAddress, VarToken, VarAgentAPI, VarConversation}

// SendLimit bounds one agentapi send-message, under deliver's own 3 s: a send with no answer by then is refused (sent
// again), not a deliver call with no reply.
const SendLimit = 2500 * time.Millisecond

// deliver is the deliver call: with a record that names all of InboxRecord's variables, the text sent to the record's
// conversation through the record's agentapi with its address and token (ok, sent); the empty text is the record's
// check, nothing sent. No record, or one missing a variable: ok false (no inbox: the line is typed, or waits). A
// refused send: the reply's error, naming agentapi's exit and its output's last line.
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
	if in.Text != "" {
		if err := sendMessage(rec, in.Text); err != nil {
			return map[string]any{"error": "agentapi send-message: " + err.Error()}
		}
	}
	return map[string]any{"ok": true, "delivery": map[string]any{"sent": true}}
}

// sendMessage runs `<agentapi> send-message <conversation> <text>` of rec with rec's address and token laid over the
// kind's environment, under SendLimit: an error for a non-zero exit (its output's last line), no answer in time, or no
// executable.
func sendMessage(rec map[string]string, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), SendLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, rec[VarAgentAPI], "send-message", rec[VarConversation], text)
	cmd.Env = append(os.Environ(), VarAddress+"="+rec[VarAddress], VarToken+"="+rec[VarToken])
	cmd.WaitDelay = 200 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
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
