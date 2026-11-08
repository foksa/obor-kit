package app

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"

	"obor/internal/app/wire"
	"obor/internal/version"
)

// T-826: a call names its binary's version, and a serve that runs another one names both versions in its refusal; a
// call from a binary older than the field ("", no version) is "an older obor". A difference alone refuses nothing.

// askApp sends one call to a's socket (Listen on an in-memory listener) and gives its answer.
func askApp(t *testing.T, a *App) func(c wire.Req) wire.Answer {
	t.Helper()
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go a.Listen(ln)
	return func(c wire.Req) wire.Answer {
		client := ln.sendRaw(t, c)
		defer client.Close()
		return read(t, client)
	}
}

// callApp is a serving rootApp's app: its project loaded and its socket answering, its log in out.
func callApp(t *testing.T, out *bytes.Buffer) func(c wire.Req) wire.Answer {
	t.Helper()
	a := rootApp(projectRoot(t), out)
	a.loadProject()
	t.Cleanup(a.closeLog)
	if a.p == nil {
		t.Fatal("precondition: project pj loaded")
	}
	return askApp(t, a)
}

func TestRefusalNamesVersionSkew(t *testing.T) {
	t.Parallel()
	ask := callApp(t, &bytes.Buffer{})
	// job_next without a call id is refused before anything else: the ordinary refusal, unchanged at serve's version.
	same := ask(&wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01", Version: version.Version}})
	if want := "job_next: a call id is required"; same.Error != want {
		t.Fatalf("same version: error %q, want %q", same.Error, want)
	}
	older := ask(&wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01", Version: "v0.1.0"}})
	want := "job_next: a call id is required (caller pj-l-01 runs obor v0.1.0, serve " + version.Version + ")"
	if older.Error != want {
		t.Errorf("another version: error %q, want %q", older.Error, want)
	}
	none := ask(&wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01"}})
	want = "job_next: a call id is required (caller pj-l-01 runs an older obor, serve " + version.Version + ")"
	if none.Error != want {
		t.Errorf("no version: error %q, want %q", none.Error, want)
	}
}

// An accepted call is not refused for the mismatch: the caller wants its work done, not a version lecture.
func TestAcceptedCallIsNotRefusedForSkew(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	var out bytes.Buffer
	a := rootApp(root, &out)
	a.loadProject()
	t.Cleanup(a.closeLog)
	ask := askApp(t, a)
	ans := ask(&wire.JobNextReq{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-02", CallID: "c1",
		Version: "v0.1.0"}})
	if ans.Error != "" || ans.Reply != `{"state":"none"}` {
		t.Errorf("job_next at another version = %+v; want the ordinary none reply, refused nothing", ans)
	}
}

// The line is one per (agent, version), not per call: two calls from one agent at one version log once; another agent
// or another version logs its own.
func TestSkewLoggedOncePerAgentAndVersion(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	var out bytes.Buffer
	a := rootApp(root, &out)
	a.loadProject()
	t.Cleanup(a.closeLog)
	ask := askApp(t, a)
	if version.Version == "v0.1.0" || version.Version == "v0.2.0" {
		t.Fatalf("precondition: the test versions differ from serve's %q", version.Version)
	}
	calls := []wire.JobNextReq{
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c1", Version: "v0.1.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c2", Version: "v0.1.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-02", CallID: "c3", Version: "v0.1.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c4", Version: "v0.2.0"}},
		{Envelope: wire.Envelope{Root: root, Role: "worker", Agent: "pj-l-01", CallID: "c5"}},
	}
	for _, c := range calls {
		if ans := ask(&c); ans.Error != "" {
			t.Fatalf("job_next %s at %q: %q", c.Agent, c.Version, ans.Error)
		}
	}
	log := out.String()
	for _, c := range []struct {
		agent, skew string
		want        int
	}{
		{"pj-l-01", "obor v0.1.0", 1},
		{"pj-l-02", "obor v0.1.0", 1},
		{"pj-l-01", "obor v0.2.0", 1},
		{"pj-l-01", "an older obor", 1},
	} {
		line := "caller " + c.agent + " runs " + c.skew + ", serve " + version.Version
		if got := strings.Count(log, line); got != c.want {
			t.Errorf("log has %d %q lines, want %d:\n%s", got, line, c.want, log)
		}
	}
	if got := strings.Count(log, "caller "); got != 4 {
		t.Errorf("log has %d skew lines, want 4 (one per agent and version):\n%s", got, log)
	}
}

// T-1055: Listen reads a request's envelope, then its method's request type. A request with no call (none, or null)
// is refused as before the split, an unknown method is, and an old binary's line with keys of the union its method
// does not use (task, patch on a pause) reaches the method's handler, those keys ignored.
func TestListenDecodesTheMethodsRequest(t *testing.T) {
	t.Parallel()
	root := projectRoot(t)
	a := rootApp(root, &bytes.Buffer{})
	a.loadProject()
	t.Cleanup(a.closeLog)
	if a.p == nil {
		t.Fatal("precondition: project pj loaded")
	}
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go a.Listen(ln)
	ask := func(line string) wire.Answer {
		client, server := net.Pipe()
		ln.conns <- server
		go func() { _, _ = client.Write([]byte(line + "\n")) }()
		defer client.Close()
		return read(t, client)
	}
	envelope := `"root":` + strconv.Quote(root) + `,"agent":"pj-l-01","role":"worker","version":` +
		strconv.Quote(version.Version) + `,"call_id":"p1"`
	for _, c := range []struct{ name, line, want string }{
		{"no call", `{"method":"pause"}`, "pause: no call"},
		{"a null call", `{"method":"pause","call":null}`, "pause: no call"},
		{"an unknown method", `{"method":"pauses","call":{` + envelope + `}}`, "unknown method pauses"},
		{"a field of the wrong type", `{"method":"pause","call":{` + envelope + `,"until":"soon"}}`, "unreadable request"},
		{"an old line", `{"method":"pause","call":{` + envelope + `,"task":"T-9","patch":"p","to":"x","note":"why"}}`,
			"pause: the lead or the owner pauses, not pj-l-01 (role worker)"},
	} {
		if ans := ask(c.line); ans.Error != c.want || ans.Reply != "" {
			t.Errorf("%s: %+v, want the error %q", c.name, ans, c.want)
		}
	}
	// The handler got the old line's pause: the owner's (no role) is recorded with its why.
	owner := `"root":` + strconv.Quote(root) + `,"agent":"","role":"","version":` + strconv.Quote(version.Version) +
		`,"call_id":"p2"`
	if ans := ask(`{"method":"pause","call":{` + owner + `,"task":"T-9","patch":"p","note":"lunch"}}`); ans.Error != "" ||
		ans.Reply != "PAUSED until cleared" {
		t.Errorf("the owner's old pause line: %+v, want PAUSED until cleared", ans)
	}
}
