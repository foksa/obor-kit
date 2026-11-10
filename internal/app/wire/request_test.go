package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"obor/internal/store"
)

// env is the golden rows' envelope.
var env = Envelope{Root: "/p", Agent: "ag", Role: "ro", Version: "v1", CallID: "c1"}

// droppedKey is a key a method's request type does not send, with why no serve of either version reads it for that
// method. A key a method's handler does read belongs in no row.
type droppedKey struct{ key, why string }

var (
	dropTask = droppedKey{"task", "no serve reads task for this method: it takes no task, and a missing key decodes as " +
		`the "" the old line sent`}
	dropPaneVerdict = droppedKey{"pane", "no serve reads pane for a verdict: v0.30.0's verdict handler reads task, " +
		"job, patch and note, and its only caller, the integrator, has no lane's pane"}
	dropPaneDone = droppedKey{"pane", "no serve reads pane for task_done: v0.30.0's taskDone resolves the worker's " +
		"lane to its task and reads no pane"}
	dropPauseSource = droppedKey{"source", "no serve reads source for pause: only config_set's handler does (v0.30.0)"}
)

// requestLine is one row of the socket's oracle: the request a caller (or the test) builds, the exact line the union
// Call of an older obor marshalled for it, and every key the type does not send with why no serve of
// either version reads it for the method.
type requestLine struct {
	req   Req
	old   string
	drops []droppedKey
}

// goldenRequests is the socket's oracle: per method a request with every field the method uses set, and
// the exact line the union Call of an older obor marshalled for it (captured from that code, as
// Request{Method, Call: &Call{…}}, in the union's key order, kept as captured). drops names every
// key its line carries that the type does not send, each with why no serve of either version reads it for the
// method: the guarantee is that every key a serve of either version reads is sent and every older line decodes into the
// type, not that the bytes are equal.
var goldenRequests = []requestLine{
	{&VerdictReq{Envelope: env, Verb: MethodAccept, Task: "T-1", Job: "T-1/1/review", Patch: "ph", Note: "n"},
		`{"method":"accept","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","patch":"ph","note":"n"}}`, nil},
	{&VerdictReq{Envelope: env, Verb: MethodSendback, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"sendback","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why"}}`, nil},
	{&VerdictReq{Envelope: env, Verb: MethodReject, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"reject","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why"}}`, nil},
	{&VerdictReq{Envelope: env, Verb: MethodResolved, Task: "T-1", Job: "T-1/1/resolve"},
		`{"method":"resolved","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/resolve"}}`, nil},
	{&VerdictReq{Envelope: env, Verb: MethodHold, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"hold","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why"}}`, nil},
	{&DropReq{Envelope: env, Task: "T-1", Why: "why", Force: true, Discard: true},
		`{"method":"drop","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"why","force":true,"discard":true}}`, nil},
	{&ResumeReq{Envelope: env, Task: "T-1", Text: "yes", Flag: ResumeRegate},
		`{"method":"resume","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"yes","to":"regate"}}`, nil},
	{&DispatchReq{Envelope: env, Task: "T-1", Lane: "L-01", Types: []string{"go"}, Force: true, After: []string{"T-0"},
		Level: "hard"},
		`{"method":"dispatch","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","lane":"L-01","types":["go"],"force":true,"after":["T-0"],"level":"hard"}}`, nil},
	{&WatchReq{Envelope: env, Task: "T-1,T-2", Note: "n", Clear: true},
		`{"method":"watch","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1,T-2","note":"n","clear":true}}`, nil},
	{&PauseReq{Envelope: env, Why: "why", Until: 99, Model: "opus", Clear: true},
		`{"method":"pause","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"why","until":99,"model":"opus","clear":true}}`, []droppedKey{dropTask}},
	{&ReviewReq{Envelope: env, Note: "n", After: []string{"T-1"}, Clear: true},
		`{"method":"review","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"n","after":["T-1"],"clear":true}}`, []droppedKey{dropTask}},
	{&SessionReq{Envelope: env, Session: &store.Session{Agent: "ag", Kind: "claude", ID: "s1", Transcript: "/t",
		Pane: "p1", Lane: "L-01", Task: "T-1", Started: 7, Source: "startup"}},
		`{"method":"session","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","session_fact":{"agent":"ag","kind":"claude","id":"s1","transcript":"/t","pane":"p1","lane":"L-01","task":"T-1","started":7,"source":"startup"}}}`, []droppedKey{dropTask}},
	// started is a method this release's serve answers: no older line carries it, so its row is the union's line, the
	// envelope and no field of its own.
	{&StartedReq{Envelope: env},
		`{"method":"started","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":""}}`, []droppedKey{dropTask}},
	{&SpecSetReq{Envelope: env, Task: "T-1", Changed: "changed", Spec: "# T-1"},
		`{"method":"spec_set","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"changed","spec":"# T-1"}}`, nil},
	{&ConfigSetReq{Envelope: env, Changes: []ConfigChange{{Key: "k", Value: json.RawMessage(`{"a":1}`)}}, Base: "r1",
		Source: "web"},
		`{"method":"config_set","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","changes":[{"key":"k","value":{"a":1}}],"base":"r1","source":"web"}}`, []droppedKey{dropTask}},
	{&BackupReq{Envelope: env},
		`{"method":"backup","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":""}}`, []droppedKey{dropTask}},
	{&SendReq{Envelope: env, Text: "hi", From: "other"},
		`{"method":"send","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"hi","from":"other"}}`, []droppedKey{dropTask}},
	{&BriefReq{Envelope: env, Text: "brief", To: "lead", Prompt: "go", Fresh: true},
		`{"method":"brief","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"brief","to":"lead","prompt":"go","fresh":true}}`, []droppedKey{dropTask}},
	{&StoppedReq{Envelope: env, Lane: "L-01", Error: "overloaded", Details: "d"},
		`{"method":"stopped","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","lane":"L-01","error":"overloaded","details":"d"}}`, []droppedKey{dropTask}},
	{&TurnReq{Envelope: env, Lane: "L-01", Ended: true},
		`{"method":"turn","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","lane":"L-01","ended":true}}`, []droppedKey{dropTask}},
	{&RestartJobReq{Envelope: env, Lane: "L-01"},
		`{"method":"restart_job","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","lane":"L-01"}}`, []droppedKey{dropTask}},
	{&ExitReq{Envelope: env, Why: "obor stop: why"},
		`{"method":"exit","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"obor stop: why"}}`, []droppedKey{dropTask}},
	{&DriveReq{Envelope: env, Task: "T-1", Name: "name", Types: []string{"go"}},
		`{"method":"drive","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"name","types":["go"]}}`, nil},
	{&LandReq{Envelope: env, Task: "T-1", Report: "r"},
		`{"method":"land","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","report":"r"}}`, nil},
	{&TellLeadReq{Envelope: env, Text: "text", Lane: "L-01"},
		`{"method":"tell_lead","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"text","lane":"L-01"}}`, []droppedKey{dropTask}},
	{&IdleReq{Envelope: env, Why: "why"},
		`{"method":"idle","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"why"}}`, []droppedKey{dropTask}},
	{&ReviewCloseReq{Envelope: env, Summary: "summary", Force: true, Accept: []string{"- a"}},
		`{"method":"review_close","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"summary","force":true,"accept":["- a"]}}`, []droppedKey{dropTask}},
	{&EventsReq{Envelope: env, Seq: 5, Follow: true},
		`{"method":"events","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","seq":5,"follow":true}}`, []droppedKey{dropTask}},
	{&JobNextReq{Envelope: env, Job: "T-1/1/work", Lane: "L-01", Pane: "p1"},
		`{"method":"job_next","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","job":"T-1/1/work","lane":"L-01","pane":"p1"}}`, []droppedKey{dropTask}},
	{&TaskDoneReq{Envelope: env, Task: "T-1", Lane: "L-01", Report: "r"},
		`{"method":"task_done","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","lane":"L-01","report":"r"}}`, nil},
}

// callerRequests is the same oracle for the requests a real caller builds with the fields it sets: the MCP
// tools' (Pane, from project.Pane, on every tool call, and Lane from OBOR_LANE) and the web's
// (Source "web" on every page's call). These rows are separate from goldenRequests because those lines stay exactly as
// captured; a caller that sets "pane" or "source" is a different line, not an edit to them.
//
// The builders are not called here: this package cannot import internal/mcp or internal/web (both import wire), and each
// sits behind a serve call: MCP's task_done request is a closure inside mcp.register, reached through s.call and
// wire.Verdict, and web's pause is pauseAPI, reachable only with a project and an http request (its s.Call hook replaces
// the socket). Each row is the value that builder makes, with the pane or source that caller added; the integrator is
// not lane-bound, so an old verdict's Lane is empty and its line carries none.
var callerRequests = []requestLine{
	{&VerdictReq{Envelope: env, Verb: MethodAccept, Task: "T-1", Job: "T-1/1/review", Patch: "ph", Note: "n"},
		`{"method":"accept","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","patch":"ph","note":"n","pane":"p1"}}`, []droppedKey{dropPaneVerdict}},
	{&VerdictReq{Envelope: env, Verb: MethodSendback, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"sendback","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why","pane":"p1"}}`, []droppedKey{dropPaneVerdict}},
	{&VerdictReq{Envelope: env, Verb: MethodReject, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"reject","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why","pane":"p1"}}`, []droppedKey{dropPaneVerdict}},
	{&VerdictReq{Envelope: env, Verb: MethodResolved, Task: "T-1", Job: "T-1/1/resolve"},
		`{"method":"resolved","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/resolve","pane":"p1"}}`, []droppedKey{dropPaneVerdict}},
	{&VerdictReq{Envelope: env, Verb: MethodHold, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"hold","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why","pane":"p1"}}`, []droppedKey{dropPaneVerdict}},
	{&TaskDoneReq{Envelope: env, Task: "T-1", Lane: "L-01", Report: "r"},
		`{"method":"task_done","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","lane":"L-01","pane":"p1","report":"r"}}`, []droppedKey{dropPaneDone}},
	{&PauseReq{Envelope: env, Why: "quota", Until: 99},
		`{"method":"pause","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"quota","source":"web","until":99}}`, []droppedKey{dropTask, dropPauseSource}},
}

// callObject is line's call as its JSON object: each key's exact value, independent of key order.
func callObject(t *testing.T, method, line string) map[string]json.RawMessage {
	t.Helper()
	var req Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("%s: the line does not decode: %v: %s", method, err, line)
	}
	if req.Method != method {
		t.Fatalf("%s: the line's method is %q", method, req.Method)
	}
	var call map[string]json.RawMessage
	if err := json.Unmarshal(req.Call, &call); err != nil {
		t.Fatalf("%s: the call is not a JSON object: %v: %s", method, err, line)
	}
	return call
}

// checkRequestLine asserts g's guarantee: its request's line equals its old line less exactly g.drops, and its old line
// decodes into the request type with every field the method reads equal.
func checkRequestLine(t *testing.T, g requestLine) {
	t.Helper()
	method := g.req.Method()
	call, err := json.Marshal(g.req)
	if err != nil {
		t.Fatalf("%s: marshal: %v", method, err)
	}
	b, _ := json.Marshal(Request{Method: method, Call: call})
	oldCall := callObject(t, method, g.old)
	newCall := callObject(t, method, string(b))
	named := map[string]bool{}
	for _, d := range g.drops {
		if named[d.key] || d.why == "" {
			t.Fatalf("precondition: %s's drops names %q once and says why it is unread", method, d.key)
		}
		named[d.key] = true
		if _, ok := oldCall[d.key]; !ok {
			t.Fatalf("precondition: %s's old line carries the key its drops names (%q): %s", method, d.key, g.old)
		}
		delete(oldCall, d.key)
	}
	if !reflect.DeepEqual(newCall, oldCall) {
		for k, want := range oldCall {
			got, ok := newCall[k]
			switch {
			case !ok:
				t.Errorf("%s's line sends no %q, a key no drops entry names\n got %s\nold %s", method, k, b, g.old)
			case string(got) != string(want):
				t.Errorf("%s's %q changed:\n got %s\nold %s", method, k, got, want)
			}
		}
		for k := range newCall {
			if _, ok := oldCall[k]; !ok {
				t.Errorf("%s's line sends %q, which the old line does not: not a key of the union", method, k)
			}
		}
	}
	for _, line := range []string{g.old, string(b)} {
		m, got := decodeAs(t, line, g.req)
		if m != method || !reflect.DeepEqual(got, g.req) {
			t.Errorf("%s: %s decodes to %s %+v, want %+v", method, line, m, got, g.req)
		}
	}
}

// decodeAs decodes a request line's call into a new value of want's request type, its method the line's (a
// VerdictReq's Verb is the method it came under, as serve sets it).
func decodeAs(t *testing.T, line string, want Req) (string, Req) {
	t.Helper()
	var req Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("the line %s does not decode: %v", line, err)
	}
	got := reflect.New(reflect.TypeOf(want).Elem()).Interface().(Req)
	if err := json.Unmarshal(req.Call, got); err != nil {
		t.Fatalf("%s: the call does not decode into %T: %v", req.Method, got, err)
	}
	if v, ok := got.(*VerdictReq); ok {
		v.Verb = req.Method
	}
	return req.Method, got
}

// Every method's request sends every key a serve of either version reads for it, and every older line decodes into
// the request type with the fields the method reads equal. The only keys a line may lack are its row's drops, each
// named with why no serve reads it for the method: byte-for-byte equality with an older line is not the guarantee.
//
// A drops entry that names a key the method's handler does read would pass here: the handlers are in internal/app, which
// imports this package, so this test cannot inspect them. The rows' drops were checked against them by hand instead
// (v0.30.0's verdict and taskDone handlers read no Pane, pause reads no Source, and every no-task method reads
// no Task).
func TestRequestLinesDropOnlyUnreadKeys(t *testing.T) {
	t.Parallel()
	methods := map[string]bool{}
	for _, g := range goldenRequests {
		method := g.req.Method()
		if method == "" || methods[method] {
			t.Fatalf("precondition: one row per method, %q twice or empty", method)
		}
		methods[method] = true
		if !strings.Contains(g.old, `"task":`) {
			t.Fatalf("precondition: %s's old line carries the union's task key: %s", method, g.old)
		}
		checkRequestLine(t, g)
	}
	// Every method of the socket but status (no call) has its row.
	for _, m := range []string{MethodAccept, MethodSendback, MethodReject, MethodResolved, MethodHold, MethodDrop,
		MethodResume, MethodDispatch, MethodWatch, MethodPause, MethodReview, MethodSession, MethodStarted, MethodSpecSet,
		MethodConfig,
		MethodBackup, MethodSend, MethodBrief, MethodStopped, MethodTurn, MethodRestartJob, MethodExit, MethodDrive,
		MethodLand, MethodTellLead, MethodIdle, MethodReviewClose, MethodEvents, MethodJobNext, MethodTaskDone} {
		if !methods[m] {
			t.Errorf("no golden row for %s", m)
		}
	}
}

// The caller-built rows keep the same guarantee, and each differs from its golden row (it has a dropped key the
// caller set and the type cannot send).
func TestCallerBuiltRequestLinesDropOnlyUnreadKeys(t *testing.T) {
	t.Parallel()
	for _, g := range callerRequests {
		if len(g.drops) == 0 {
			t.Fatalf("precondition: %s's caller row has a key the new type drops", g.req.Method())
		}
		checkRequestLine(t, g)
	}
}

// An older binary's line may carry any key of the union Call, one its method does not use among them: it still
// decodes into the method's request, the unused keys ignored (as any unknown key).
func TestRequestIgnoresTheUnionsOtherKeys(t *testing.T) {
	t.Parallel()
	line := `{"method":"pause","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-9",` +
		`"job":"T-9/1/work","patch":"ph","to":"lead","lane":"L-01","types":["go"],"force":true,"after":["T-0"],` +
		`"level":"hard","accept":["- a"],"discard":true,"pane":"p1","report":"r","spec":"# T-9","changes":[],"base":"r1",` +
		`"source":"web","from":"x","prompt":"p","fresh":true,"error":"e","details":"d","ended":true,` +
		`"session_fact":{"agent":"ag","id":"s1","started":1},"seq":3,"follow":true,"note":"why","until":99,"model":"opus","clear":true}}`
	want := &PauseReq{Envelope: env, Why: "why", Until: 99, Model: "opus", Clear: true}
	m, got := decodeAs(t, line, want)
	if m != MethodPause || !reflect.DeepEqual(got, want) {
		t.Errorf("the old line decodes to %s %+v, want %+v", m, got, want)
	}
}
