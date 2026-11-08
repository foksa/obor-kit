package wire

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"

	"obor/internal/version"
)

// T-826: every call the client sends names the calling binary's version, set in one place (Verdict): a serve can then
// name the skew instead of leaving a wrong answer to be diagnosed.
func TestVerdictCarriesTheCallerVersion(t *testing.T) {
	dir, err := os.MkdirTemp("", "wire")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := dir + "/serve.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	reqs := make(chan Request, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := bufio.NewReader(c).ReadBytes('\n')
		var req Request
		if json.Unmarshal(b, &req) == nil {
			reqs <- req
			_, _ = c.Write([]byte("{\"reply\":\"ok\"}\n"))
		}
	}()
	reply, err := Verdict(context.Background(), sock, &VerdictReq{Verb: MethodAccept,
		Envelope: Envelope{Agent: "pj-int"}, Task: "T-1"})
	if err != nil || reply != "ok" {
		t.Fatalf("Verdict = %q, %v; want ok", reply, err)
	}
	req := <-reqs
	var env Envelope
	if req.Call == nil || json.Unmarshal(req.Call, &env) != nil {
		t.Fatalf("precondition: the request carries a call: %s", req.Call)
	}
	if env.Version != version.Version {
		t.Errorf("the call's version = %q, want the caller's %q", env.Version, version.Version)
	}
}
