package kind_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"obor/kinds/agy/kind"
)

// stubSend puts a stub agy first on the test's PATH: its first argument must be agentapi (else agy's refusal of a
// prompt in its arguments, exit 2). It writes each of agentapi's arguments as [arg] and then its address and token into
// the file it returns, then exits code with out on stdout.
func stubSend(t *testing.T, code int, out string) (ran string) {
	t.Helper()
	dir := t.TempDir()
	ran = filepath.Join(dir, "ran")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" != agentapi ]; then echo '" + agyRefusal + "' >&2; exit 2; fi\nshift\n" +
		"for a in \"$@\"; do printf '[%s]' \"$a\"; done > " + ran + "\n" +
		"printf ' %s %s' \"$ANTIGRAVITY_LS_ADDRESS\" \"$ANTIGRAVITY_CSRF_TOKEN\" >> " + ran + "\n" +
		"printf '%s\\n' '" + out + "'\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, kind.Name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return ran
}

// agyRefusal is agy's answer to a command line it takes for a prompt (agy send-message …).
const agyRefusal = "Prompts are read only from -p/--print, -i/--prompt-interactive, or stdin"

// agyEnv is a command's environment in an agy session (the variables of P7), agy's own ANTIGRAVITY_AGENTAPI_EXE among
// them.
func agyEnv() map[string]string {
	return map[string]string{kind.VarAddress: "localhost:5011", kind.VarToken: "tok-1",
		"ANTIGRAVITY_AGENTAPI_EXE": "/h/.gemini/antigravity-cli/bin/agentapi", kind.VarConversation: "conv-1", "HOME": "/h"}
}

// A session's record is its command environment's inbox variables, all three (one missing: none), and never agy's
// ANTIGRAVITY_AGENTAPI_EXE. deliver with it runs `agy agentapi send-message <conversation> <text>`, agy from the kind's
// PATH, with the record's address and token, the text as one argument (several lines kept): sent. The empty text
// checks the record and runs nothing. A refused send (agentapi's non-zero exit) is not sent: the delivery's Refused
// names the exit and agentapi's last line. No record: no inbox, nothing run.
func TestDeliver(t *testing.T) {
	k := agy(t, t.TempDir())
	ran := stubSend(t, 0, `{"response": {"sendMessage": {}}}`)
	env := agyEnv()
	rec := k.InboxRecord(func(v string) string { return env[v] })
	if rec == nil {
		t.Fatal("precondition: no record from the command's environment")
	}
	if strings.Contains(string(rec), "AGENTAPI") || strings.Contains(string(rec), "/h/.gemini") {
		t.Errorf("the record names agy's executable: %s", rec)
	}
	missing := agyEnv()
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

	stubSend(t, 1, "rpc error: code = Unauthenticated desc = unauthenticated: invalid CSRF token")
	d, ok = k.Deliver("[obor] three", rec)
	want := "agentapi send-message: exit 1: rpc error: code = Unauthenticated desc = unauthenticated: invalid CSRF token"
	if ok || d.Sent || !strings.HasSuffix(d.Refused, want) {
		t.Errorf("a refused send: %+v, %v; want refused with %q", d, ok, want)
	}

	if d, ok := k.Deliver("[obor] four", nil); ok || d.Sent || d.Refused != "" {
		t.Errorf("no record: %+v, %v; want no inbox", d, ok)
	}
}

// A record that names an executable of its own (an old record's ANTIGRAVITY_AGENTAPI_EXE, or one an agent wrote) still
// runs the agy on the kind's PATH: the named program never runs.
func TestDeliverRunsOnlyAgy(t *testing.T) {
	k := agy(t, t.TempDir())
	ran := stubSend(t, 0, `{"response": {"sendMessage": {}}}`)
	dir := t.TempDir()
	other, otherRan := filepath.Join(dir, "agentapi"), filepath.Join(dir, "ran")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho \"$@\" > "+otherRan+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(map[string]string{kind.VarAddress: "localhost:5011", kind.VarToken: "tok-1",
		"ANTIGRAVITY_AGENTAPI_EXE": other, kind.VarConversation: "conv-1"})
	d, ok := k.Deliver("[obor] hi", rec)
	if !ok || !d.Sent {
		t.Fatalf("Deliver = %+v, %v; want sent", d, ok)
	}
	if b, err := os.ReadFile(ran); err != nil || string(b) != "[send-message][conv-1][[obor] hi] localhost:5011 tok-1" {
		t.Errorf("agy ran with %q, %v; want agy agentapi send-message", b, err)
	}
	if _, err := os.Stat(otherRan); err == nil {
		t.Error("the record's executable ran")
	}
}

// A record whose address is not localhost:<port> or 127.0.0.1:<port> is no inbox (ok false, why naming the address;
// the line typed or waiting), its check included, and nothing runs; 127.0.0.1:<port> is local. (The records carry an
// old record's executable variable too, naming agy itself, so the old code would have sent with them.)
func TestDeliverOnlyToALocalAddress(t *testing.T) {
	k := agy(t, t.TempDir())
	ran := stubSend(t, 0, `{"response": {"sendMessage": {}}}`)
	record := func(addr string) []byte {
		b, _ := json.Marshal(map[string]string{kind.VarAddress: addr, kind.VarToken: "tok-1", kind.VarConversation: "conv-1",
			"ANTIGRAVITY_AGENTAPI_EXE": filepath.Join(filepath.Dir(ran), kind.Name)})
		return b
	}
	for _, addr := range []string{"evil.example:5011", "10.0.0.2:5011", "[::1]:5011", "localhost", "localhost:",
		"localhost:0", "localhost:99999", "localhost:50x1", "localhost.evil.example:5011", "127.0.0.1.nip.io:5011"} {
		for _, text := range []string{"", "[obor] hi"} {
			if d, ok := k.Deliver(text, record(addr)); ok || d.Sent || d.Refused != "" {
				t.Errorf("address %q, text %q: %+v, %v; want no inbox", addr, text, d, ok)
			}
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a non-local address ran agy")
	}
	req, _ := json.Marshal(map[string]any{"version": 1, "text": "[obor] hi", "record": string(record("evil.example:5011"))})
	out, err := kind.Handle("deliver", req)
	var reply struct {
		OK  bool   `json:"ok"`
		Why string `json:"why"`
	}
	if err != nil || json.Unmarshal(out, &reply) != nil || reply.OK || !strings.Contains(reply.Why, `"evil.example:5011"`) {
		t.Errorf("deliver's reply for a non-local address: %s, %v; want ok false, why naming the address", out, err)
	}
	if d, ok := k.Deliver("[obor] local", record("127.0.0.1:5011")); !ok || !d.Sent {
		t.Errorf("127.0.0.1:<port>: %+v, %v; want sent", d, ok)
	}
	if b, err := os.ReadFile(ran); err != nil || !strings.Contains(string(b), "[[obor] local] 127.0.0.1:5011") {
		t.Errorf("agy ran with %q, %v; want the local address", b, err)
	}
}
