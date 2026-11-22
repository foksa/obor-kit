package kind_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"obor/kinds/agy/kind"
)

// stubAgentAPI is agy's executable named name: "agy" (agy's binary: the first argument must be agentapi, else agy's
// refusal of a prompt in its arguments, exit 2) or "agentapi" (agy's wrapper, its arguments agentapi's). It writes each
// of agentapi's arguments as [arg] and then its address and token into the file it returns, then exits code with out
// on stdout.
func stubAgentAPI(t *testing.T, name string, code int, out string) (exe, ran string) {
	t.Helper()
	dir := t.TempDir()
	exe, ran = filepath.Join(dir, name), filepath.Join(dir, "ran")
	body := "#!/bin/sh\n"
	if name == "agy" {
		body += "if [ \"$1\" != agentapi ]; then echo '" + agyRefusal + "' >&2; exit 2; fi\nshift\n"
	}
	body += "for a in \"$@\"; do printf '[%s]' \"$a\"; done > " + ran + "\n" +
		"printf ' %s %s' \"$ANTIGRAVITY_LS_ADDRESS\" \"$ANTIGRAVITY_CSRF_TOKEN\" >> " + ran + "\n" +
		"printf '%s\\n' '" + out + "'\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, ran
}

// agyRefusal is agy's answer to a command line it takes for a prompt (agy send-message …).
const agyRefusal = "Prompts are read only from -p/--print, -i/--prompt-interactive, or stdin"

// agyEnv is a command's environment in an agy session whose agentapi is exe (the variables of P7).
func agyEnv(exe string) map[string]string {
	return map[string]string{kind.VarAddress: "localhost:5011", kind.VarToken: "tok-1", kind.VarAgentAPI: exe,
		kind.VarConversation: "conv-1", "HOME": "/h"}
}

// A session's record is its command environment's agentapi variables, all four (one missing: none). deliver with it
// runs `<agy> agentapi send-message <conversation> <text>` with the record's address and token, the text as one
// argument (several lines kept): sent; a record whose executable is agy's agentapi wrapper runs `<agentapi>
// send-message …`. The empty text checks the record and runs nothing. A refused send (agentapi's non-zero
// exit) is not sent: the delivery's Refused names the exit and agentapi's last line. No record: no inbox, nothing run.
func TestDeliver(t *testing.T) {
	k := agy(t, t.TempDir())
	exe, ran := stubAgentAPI(t, "agy", 0, `{"response": {"sendMessage": {}}}`)
	env := agyEnv(exe)
	rec := k.InboxRecord(func(v string) string { return env[v] })
	if rec == nil {
		t.Fatal("precondition: no record from the command's environment")
	}
	missing := agyEnv(exe)
	delete(missing, kind.VarToken)
	if got := k.InboxRecord(func(v string) string { return missing[v] }); got != nil {
		t.Errorf("a record without the token: %s, want none", got)
	}

	if d, ok := k.Deliver("", rec); !ok || !d.Sent || d.Typed || d.Socket != "" {
		t.Errorf("the record's check: %+v, %v; want sent", d, ok)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the record's check ran agentapi")
	}
	d, ok := k.Deliver("[obor] one\n[obor] two", rec)
	if !ok || !d.Sent || d.Refused != "" {
		t.Fatalf("Deliver = %+v, %v; want sent", d, ok)
	}
	b, err := os.ReadFile(ran)
	if want := "[send-message][conv-1][[obor] one\n[obor] two] localhost:5011 tok-1"; err != nil || string(b) != want {
		t.Errorf("agentapi ran with %q, %v; want %q", b, err, want)
	}
	wrapper, wran := stubAgentAPI(t, "agentapi", 0, `{"response": {"sendMessage": {}}}`)
	wenv := agyEnv(wrapper)
	if d, ok := k.Deliver("[obor] wrapped", k.InboxRecord(func(v string) string { return wenv[v] })); !ok || !d.Sent {
		t.Errorf("Deliver through agy's agentapi wrapper = %+v, %v; want sent", d, ok)
	}
	if b, err := os.ReadFile(wran); err != nil || string(b) != "[send-message][conv-1][[obor] wrapped] localhost:5011 tok-1" {
		t.Errorf("the wrapper ran with %q, %v", b, err)
	}

	refusing, _ := stubAgentAPI(t, "agy", 1, "rpc error: code = Unauthenticated desc = unauthenticated: invalid CSRF token")
	stale := agyEnv(refusing)
	d, ok = k.Deliver("[obor] three", k.InboxRecord(func(v string) string { return stale[v] }))
	want := "agentapi send-message: exit 1: rpc error: code = Unauthenticated desc = unauthenticated: invalid CSRF token"
	if ok || d.Sent || !strings.HasSuffix(d.Refused, want) {
		t.Errorf("a refused send: %+v, %v; want refused with %q", d, ok, want)
	}

	if d, ok := k.Deliver("[obor] four", nil); ok || d.Sent || d.Refused != "" {
		t.Errorf("no record: %+v, %v; want no inbox", d, ok)
	}
}
