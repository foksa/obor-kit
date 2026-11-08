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

// goldenRequests is the socket's oracle (T-1055): per method a request with every field the method uses set, and the
// exact line the union Call of the obor before the split marshalled for it (captured from that code, as
// Request{Method, Call: &Call{…}}). The one difference: a method that takes no task sends no "task" key, where the
// union always sent "task":"" (dropsTask): a serve of either side reads a missing key as the "" it sent.
var goldenRequests = []struct {
	req       Req
	old       string
	dropsTask bool
}{
	{&VerdictReq{Envelope: env, Verb: MethodAccept, Task: "T-1", Job: "T-1/1/review", Patch: "ph", Note: "n"},
		`{"method":"accept","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","patch":"ph","note":"n"}}`, false},
	{&VerdictReq{Envelope: env, Verb: MethodSendback, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"sendback","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why"}}`, false},
	{&VerdictReq{Envelope: env, Verb: MethodReject, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"reject","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why"}}`, false},
	{&VerdictReq{Envelope: env, Verb: MethodResolved, Task: "T-1", Job: "T-1/1/resolve"},
		`{"method":"resolved","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/resolve"}}`, false},
	{&VerdictReq{Envelope: env, Verb: MethodHold, Task: "T-1", Job: "T-1/1/review", Note: "why"},
		`{"method":"hold","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","job":"T-1/1/review","note":"why"}}`, false},
	{&DropReq{Envelope: env, Task: "T-1", Why: "why", Force: true, Discard: true},
		`{"method":"drop","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"why","force":true,"discard":true}}`, false},
	{&ResumeReq{Envelope: env, Task: "T-1", Text: "yes", Flag: ResumeRegate},
		`{"method":"resume","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"yes","to":"regate"}}`, false},
	{&DispatchReq{Envelope: env, Task: "T-1", Lane: "L-01", Types: []string{"go"}, Force: true, After: []string{"T-0"},
		Level: "hard"},
		`{"method":"dispatch","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","lane":"L-01","types":["go"],"force":true,"after":["T-0"],"level":"hard"}}`, false},
	{&WatchReq{Envelope: env, Task: "T-1,T-2", Note: "n", Clear: true},
		`{"method":"watch","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1,T-2","note":"n","clear":true}}`, false},
	{&PauseReq{Envelope: env, Why: "why", Until: 99, Model: "opus", Clear: true},
		`{"method":"pause","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"why","until":99,"model":"opus","clear":true}}`, true},
	{&ReviewReq{Envelope: env, Note: "n", After: []string{"T-1"}, Clear: true},
		`{"method":"review","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"n","after":["T-1"],"clear":true}}`, true},
	{&SessionReq{Envelope: env, Session: &store.Session{Agent: "ag", Kind: "claude", ID: "s1", Transcript: "/t",
		Pane: "p1", Lane: "L-01", Task: "T-1", Started: 7, Source: "startup"}},
		`{"method":"session","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","session_fact":{"agent":"ag","kind":"claude","id":"s1","transcript":"/t","pane":"p1","lane":"L-01","task":"T-1","started":7,"source":"startup"}}}`, true},
	{&SpecSetReq{Envelope: env, Task: "T-1", Changed: "changed", Spec: "# T-1"},
		`{"method":"spec_set","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"changed","spec":"# T-1"}}`, false},
	{&ConfigSetReq{Envelope: env, Changes: []ConfigChange{{Key: "k", Value: json.RawMessage(`{"a":1}`)}}, Base: "r1",
		Source: "web"},
		`{"method":"config_set","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","changes":[{"key":"k","value":{"a":1}}],"base":"r1","source":"web"}}`, true},
	{&BackupReq{Envelope: env},
		`{"method":"backup","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":""}}`, true},
	{&SendReq{Envelope: env, Text: "hi", From: "other"},
		`{"method":"send","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"hi","from":"other"}}`, true},
	{&BriefReq{Envelope: env, Text: "brief", To: "lead", Prompt: "go", Fresh: true},
		`{"method":"brief","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"brief","to":"lead","prompt":"go","fresh":true}}`, true},
	{&StoppedReq{Envelope: env, Lane: "L-01", Error: "overloaded", Details: "d"},
		`{"method":"stopped","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","lane":"L-01","error":"overloaded","details":"d"}}`, true},
	{&TurnReq{Envelope: env, Lane: "L-01", Ended: true},
		`{"method":"turn","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","lane":"L-01","ended":true}}`, true},
	{&RestartJobReq{Envelope: env, Lane: "L-01"},
		`{"method":"restart_job","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","lane":"L-01"}}`, true},
	{&ExitReq{Envelope: env, Why: "obor stop: why"},
		`{"method":"exit","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"obor stop: why"}}`, true},
	{&DriveReq{Envelope: env, Task: "T-1", Name: "name", Types: []string{"go"}},
		`{"method":"drive","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","note":"name","types":["go"]}}`, false},
	{&LandReq{Envelope: env, Task: "T-1", Report: "r"},
		`{"method":"land","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","report":"r"}}`, false},
	{&TellLeadReq{Envelope: env, Text: "text", Lane: "L-01"},
		`{"method":"tell_lead","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"text","lane":"L-01"}}`, true},
	{&IdleReq{Envelope: env, Why: "why"},
		`{"method":"idle","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"why"}}`, true},
	{&ReviewCloseReq{Envelope: env, Summary: "summary", Force: true, Accept: []string{"- a"}},
		`{"method":"review_close","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","note":"summary","force":true,"accept":["- a"]}}`, true},
	{&EventsReq{Envelope: env, Seq: 5, Follow: true},
		`{"method":"events","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","seq":5,"follow":true}}`, true},
	{&JobNextReq{Envelope: env, Job: "T-1/1/work", Lane: "L-01", Pane: "p1"},
		`{"method":"job_next","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"","job":"T-1/1/work","lane":"L-01","pane":"p1"}}`, true},
	{&TaskDoneReq{Envelope: env, Task: "T-1", Lane: "L-01", Report: "r"},
		`{"method":"task_done","call":{"root":"/p","agent":"ag","role":"ro","version":"v1","call_id":"c1","task":"T-1","lane":"L-01","report":"r"}}`, false},
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

// T-1055: every method's request marshals to the line the union Call sent (less its empty "task" where the method takes
// none), and that line, and the new one, decode back into the request type equal to the value: the socket is unchanged.
func TestRequestsKeepTheSocketLines(t *testing.T) {
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
		want := g.old
		if g.dropsTask {
			want = strings.NewReplacer(`,"task":""}`, "}", `,"task":"",`, ",").Replace(g.old)
			if want == g.old {
				t.Fatalf("precondition: %s's old line has an empty task to drop: %s", method, g.old)
			}
		}
		call, err := json.Marshal(g.req)
		if err != nil {
			t.Fatalf("%s: marshal: %v", method, err)
		}
		b, _ := json.Marshal(Request{Method: method, Call: call})
		if string(b) != want {
			t.Errorf("%s's line changed:\n got %s\nwant %s", method, b, want)
		}
		for _, line := range []string{g.old, string(b)} {
			m, got := decodeAs(t, line, g.req)
			if m != method || !reflect.DeepEqual(got, g.req) {
				t.Errorf("%s: %s decodes to %s %+v, want %+v", method, line, m, got, g.req)
			}
		}
	}
	// Every method of the socket but status (no call) has its row.
	for _, m := range []string{MethodAccept, MethodSendback, MethodReject, MethodResolved, MethodHold, MethodDrop,
		MethodResume, MethodDispatch, MethodWatch, MethodPause, MethodReview, MethodSession, MethodSpecSet, MethodConfig,
		MethodBackup, MethodSend, MethodBrief, MethodStopped, MethodTurn, MethodRestartJob, MethodExit, MethodDrive,
		MethodLand, MethodTellLead, MethodIdle, MethodReviewClose, MethodEvents, MethodJobNext, MethodTaskDone} {
		if !methods[m] {
			t.Errorf("no golden row for %s", m)
		}
	}
}

// T-1055: an old binary's line may carry any key of the union Call, one its method does not use among them: it still
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
