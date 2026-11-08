package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// fakeApp listens on a new unix socket and serves its connections one at a time: it reads the request line, records
// the call id it carries and writes reply(n) back, n counting connections from 1; an empty reply closes the connection
// unanswered. seen returns the call ids recorded so far.
func fakeApp(t *testing.T, reply func(n int) string) (sock string, seen func() []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ask") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var ids []string
	go func() {
		for n := 1; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, err := bufio.NewReader(c).ReadBytes('\n')
			if err == nil {
				var req struct {
					Call struct {
						CallID string `json:"call_id"`
					} `json:"call"`
				}
				_ = json.Unmarshal(line, &req)
				mu.Lock()
				ids = append(ids, req.Call.CallID)
				mu.Unlock()
				if r := reply(n); r != "" {
					_, _ = c.Write([]byte(r + "\n"))
				}
			}
			c.Close()
		}
	}()
	seen = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ids...)
	}
	return sock, seen
}

// A reply lost to a crash after the app recorded the verdict: the retry carries the same call id and gets the stored reply.
func TestAskRetriesLostReplyWithSameCallID(t *testing.T) {
	t.Parallel()
	sock, seen := fakeApp(t, func(n int) string {
		if n == 1 {
			return "" // the app went away after recording the verdict
		}
		return `{"reply":"accepted T-1"}`
	})
	reply, err := wire.Verdict(context.Background(), sock, askCall("c1"))
	if err != nil || reply != "accepted T-1" {
		t.Fatalf("Verdict = %q, %v; want the stored reply", reply, err)
	}
	if ids := seen(); len(ids) != 2 || ids[0] != "c1" || ids[1] != "c1" {
		t.Errorf("call ids seen = %q; want the same id twice", ids)
	}
}

// A serve that crashes after recording the call and restarts seconds later: the client keeps the same call id through
// the refusals and gets the reply the restarted serve replays from its store (docs/state-model.md, class F, N3).
func TestAskSurvivesServeRestart(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "ask") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false) // the file stays: the next dials are refused, not "no such file"
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	const restart = time.Second
	var mu sync.Mutex
	var ids []string
	served := make(chan error, 1)
	go func() {
		// the first serve: it takes the request, records the call and goes away before answering
		c, err := ln.Accept()
		if err != nil {
			served <- err
			return
		}
		line, err := bufio.NewReader(c).ReadBytes('\n')
		c.Close()
		if err != nil {
			served <- err
			return
		}
		first := callID(line)
		mu.Lock()
		ids = append(ids, first)
		mu.Unlock()
		ln.Close()
		time.Sleep(restart)
		// the restarted serve: replay found the recorded call, so it answers from its store
		os.Remove(sock) // a restarted serve clears the socket its predecessor left (cmd/obor/serve.go)
		ln2, err := net.Listen("unix", sock)
		if err != nil {
			served <- err
			return
		}
		defer ln2.Close()
		c2, err := ln2.Accept()
		if err != nil {
			served <- err
			return
		}
		defer c2.Close()
		line, err = bufio.NewReader(c2).ReadBytes('\n')
		if err != nil {
			served <- err
			return
		}
		again := callID(line)
		mu.Lock()
		ids = append(ids, again)
		mu.Unlock()
		ans := `{"error":"no job open for T-1"}`
		if again == first {
			ans = `{"reply":"accepted T-1"}`
		}
		_, _ = c2.Write([]byte(ans + "\n"))
		served <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	reply, err := wire.Verdict(ctx, sock, askCall("c1"))
	if err != nil || reply != "accepted T-1" {
		t.Fatalf("Verdict through the restart = %q, %v; want the stored reply", reply, err)
	}
	if d := time.Since(start); d < restart {
		t.Errorf("answered after %v, before the serve was back (%v): the request was not retried through the refusal", d, restart)
	}
	if err := <-served; err != nil {
		t.Fatalf("fake serve: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] != "c1" || ids[1] != "c1" {
		t.Errorf("call ids seen = %q; want the same id before and after the restart", ids)
	}
}

// T-656: a verdict waits out a serve that is not there yet (a restart: the socket file of the old serve refused or
// gone) until the new one answers. `obor status`, a read, fails at once instead.
func TestAskVerdictWaitsForServe(t *testing.T) {
	t.Parallel()
	const delay = 300 * time.Millisecond
	for _, c := range []struct {
		name string
		left func(t *testing.T, sock string)
	}{
		{"a refused dial", func(t *testing.T, sock string) {
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			ln.(*net.UnixListener).SetUnlinkOnClose(false)
			ln.Close() // the socket file stays, the listener is gone: a dial is refused
		}},
		{"no socket file", func(*testing.T, string) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir, err := os.MkdirTemp("", "ask") // short: a unix socket path is limited
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			sock := filepath.Join(dir, "serve.sock")
			c.left(t, sock)
			back := make(chan struct{})
			go func() {
				time.Sleep(delay)
				os.Remove(sock) // a restarted serve clears the socket its predecessor left (cmd/obor/serve.go)
				ln, err := net.Listen("unix", sock)
				if err != nil {
					close(back)
					return
				}
				defer ln.Close()
				close(back)
				// A serve that is back keeps accepting: a connection the kernel resets before its line (the case the
				// call's retry is for, T-767) must not end this stand-in.
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_, rerr := bufio.NewReader(conn).ReadBytes('\n')
					if rerr == nil {
						_, _ = conn.Write([]byte(`{"reply":"accepted T-1"}` + "\n"))
					}
					conn.Close()
					if rerr == nil {
						return
					}
				}
			}()
			start := time.Now()
			reply, err := wire.Verdict(context.Background(), sock, askCall("c1"))
			if err != nil || reply != "accepted T-1" {
				t.Fatalf("Verdict through the restart = %q, %v; want the reply", reply, err)
			}
			if d := time.Since(start); d < delay {
				t.Errorf("answered after %v, before the serve was back (%v): the refusal was not retried", d, delay)
			}
			<-back
		})
	}
}

// A read does not wait: obor status reports a serve that is not there at once, not after the verdicts' bound.
func TestAskStatusDoesNotWaitForServe(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(sockDir(t), "serve.sock") // a restart: no socket until the new serve has loaded (T-659)
	start := time.Now()
	if _, err := wire.Ask(context.Background(), sock); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("Ask on a missing socket: %v; want the missing socket", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("status returned after %v on a missing socket; want at once", d)
	}
	sock = filepath.Join(sockDir(t), "serve.sock") // the old serve gone, its socket file left: a dial is refused
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the socket file stays, the listener is gone: a dial is refused
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("precondition: no socket file to refuse the dial: %v", err)
	}
	refused(t, sock)
	start = time.Now()
	_, err = wire.Ask(context.Background(), sock)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Ask on a refused socket: %v; want the refusal", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("status returned after %v; want the refusal at once, not the retry bound", d)
	}
}

// refused waits until a dial to sock is refused: a closed listener releases its socket a moment later, and a dial
// right then is taken and closed (EOF) instead of refused (seen under load on macOS: T-813's gate).
func refused(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("precondition: the closed listener still takes dials to %s", sock)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The retries stop at the caller's deadline.
func TestAskRetriesStopAtCallerDeadline(t *testing.T) {
	t.Parallel()
	sock, seen := fakeApp(t, func(int) string { return "" }) // every connection is dropped unanswered
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := wire.Verdict(ctx, sock, askCall("c1")); err == nil {
		t.Fatal("Verdict answered a call the app never answered")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("returned after %v; want at the caller's deadline (500ms)", d)
	}
	if ids := seen(); len(ids) < 2 || ids[0] != "c1" || ids[1] != "c1" {
		t.Errorf("call ids seen = %q; want the same id on the retries", ids)
	}
}

// A refusal is the app's answer, not a lost reply: it is returned once, not retried.
func TestAskDoesNotRetryRefusal(t *testing.T) {
	t.Parallel()
	sock, seen := fakeApp(t, func(int) string { return `{"error":"no job open for T-1"}` })
	reply, err := wire.Verdict(context.Background(), sock, askCall("c1"))
	if err == nil || !strings.Contains(err.Error(), "no job open for T-1") || reply != "" {
		t.Fatalf("Verdict = %q, %v; want the refusal", reply, err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("refused call reached the app %d times, want once", n)
	}
}

// A live answer that does not come in time is not retried: the caller gave up on it.
func TestAskDoesNotRetryTimeout(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "ask") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var conns []net.Conn
	accepted := 0
	go func() {
		for {
			c, err := ln.Accept() // never answers
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := wire.Verdict(ctx, sock, askCall("c1")); err == nil {
		t.Fatal("Verdict answered a call that never got a reply")
	}
	mu.Lock()
	defer mu.Unlock()
	if accepted != 1 {
		t.Errorf("timed-out call reached the app %d times, want once", accepted)
	}
}

// A dispatch with the switch on types nothing: the start carries the wake as its first prompt, the work job's result
// is "sent (start prompt)" (T-387); the worker's status turning working is no delivery; its job_next is: the work job's text, fact delivered via mcp once, the task
// working. A repeated call id gets the stored reply, a new one the same job with no second record, an older job id
// superseded{current}; another agent's call is refused, another lane has none.
func TestFetchWorkJob(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
	same(t, r.result("work"), `"sent (start prompt)"`, "the work job's result")
	r.mu.Lock()
	launches := append([]port.Launch(nil), r.launches...)
	r.mu.Unlock()
	if len(launches) != 1 || launches[0].Agent != "pj-l-01" || launches[0].Prompt != wake("T-1/1/work") {
		t.Errorf("launches %+v, want pj-l-01's fresh start with the wake as its first prompt", launches)
	}
	var intent event.Record
	for _, rec := range r.records() {
		if rec.Kind == event.KindIntent && rec.Step == "work" {
			intent = rec
		}
	}
	if string(intent.Data) != `{"agent":"pj-l-01","job":"T-1/1/work"}` {
		t.Errorf("work intent %s, want {agent, job}", intent.Data)
	}

	r.workerIs("working")
	r.passes()
	if r.has("1 fact delivered") || r.taskState() != "dispatched" {
		t.Fatalf("delivered by the status with the switch on: %q (state %s)", r.steps(), r.taskState())
	}
	if _, _, err := r.jobNext("pj-l-02", "worker", "L-01", "x1", ""); err == nil ||
		!strings.Contains(err.Error(), "not your job: T-1/1/work is pj-l-01's in lane L-01, not pj-l-02") {
		t.Errorf("another agent's job_next: %v", err)
	}
	if jr, _, err := r.jobNext("pj-l-02", "worker", "L-02", "x2", ""); err != nil || jr.State != "none" {
		t.Errorf("another lane's job_next: %+v, %v; want none", jr, err)
	}
	if _, _, err := r.jobNext("pj-lead", "lead", "", "x3", ""); err == nil || !strings.Contains(err.Error(), "no jobs for the role lead") {
		t.Errorf("the lead's job_next: %v", err)
	}
	if jr, _, err := r.jobNext("pj-int", "integrator", "", "x4", ""); err != nil || jr.State != "none" {
		t.Errorf("the integrator's job_next with no job: %+v, %v; want none", jr, err)
	}
	r.f.Agent("w1:p5", "pj-x", "idle", r.p.Root)
	r.push()
	r.waitFor("pane w1:p5", func() bool { return len(r.app.Status().Project.Agents) == 2 })
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.JobNextReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-l-01", Role: "worker", CallID: "x5"}, Lane: "L-01", Pane: "w1:p5"}); err == nil ||
		!strings.Contains(err.Error(), "pane w1:p5 is not in lane L-01") {
		t.Errorf("job_next from a pane outside the lane: %v", err)
	}
	if n := len(r.deliveries()); n != 0 {
		t.Fatalf("a refused or empty job_next recorded: %q", r.steps())
	}
	if ts := r.task("T-1"); ts.Job != "T-1/1/work" || ts.Delivered { // T-664: obor status' open job
		t.Errorf("status before the fetch: job %q delivered %v; want T-1/1/work, not delivered", ts.Job, ts.Delivered)
	}

	jr, raw, err := r.jobNext("pj-l-01", "worker", "L-01", "n1", "")
	if err != nil || jr.State != "job" || jr.Job != "T-1/1/work" || jr.Task != "T-1" || jr.Attempt != 1 || jr.Step != "work" ||
		!strings.Contains(jr.Text, "implement the spec of T-1 exactly") {
		t.Fatalf("job_next: %+v, %v", jr, err)
	}
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts")
	if ts := r.task("T-1"); ts.Job != "T-1/1/work" || !ts.Delivered {
		t.Errorf("status after the fetch: job %q delivered %v; want T-1/1/work, delivered", ts.Job, ts.Delivered)
	}
	if _, again, err := r.jobNext("pj-l-01", "worker", "L-01", "n1", ""); err != nil || again != raw {
		t.Errorf("repeated call id: %q, %v; want the stored reply", again, err)
	}
	if jr := r.workerNext("n2"); jr.Job != "T-1/1/work" || jr.State != "job" {
		t.Errorf("a second fetch: %+v, want the same job", jr)
	}
	if jr, _, err := r.jobNext("pj-l-01", "worker", "L-01", "n3", "T-1/0/work"); err != nil || jr.State != "superseded" ||
		jr.Current != "T-1/1/work" || jr.Text != "" {
		t.Errorf("an older job id: %+v, %v; want superseded{T-1/1/work}", jr, err)
	}
	r.passes()
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts after the repeats")
	r.later(20 * time.Minute)
	if r.has("1 intent retry.work") || r.hasPrefix("1 fact needs-you") || len(r.promptsTo("pj-l-01")) != 0 {
		t.Errorf("a timer fired for a fetched job: %q", r.steps())
	}
}

// No fetch 5 min after the start's wake → the wake typed (retry.work, the line cleared first; the worker working is
// no delivery); 5 more → needs-you{job not delivered}.
func TestFetchUndelivered(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerIs("working")
	r.later(4*time.Minute + 59*time.Second)
	if r.has("1 intent retry.work") {
		t.Fatalf("retried before 5 min: %q", r.steps())
	}
	r.later(time.Second)
	r.waitStep("1 result retry.work")
	eq(t, r.promptsTo("pj-l-01"), []string{wake("T-1/1/work")}, "typed to pj-l-01")
	if got := r.cleared("pj-l-01"); len(got) != 1 || !got[0] {
		t.Errorf("input line cleared before prompts %v, want [true]", got)
	}
	r.later(4*time.Minute + 59*time.Second)
	if r.hasPrefix("1 fact needs-you") {
		t.Fatalf("needs-you before 5 min after the retry: %q", r.steps())
	}
	r.later(time.Second)
	r.waitStep("1 fact needs-you{job not delivered: T-1/1/work prompted twice to pj-l-01}")
	if r.has("1 fact delivered") {
		t.Errorf("delivered without a fetch: %q", r.steps())
	}
}

// The integrator's start carries its job's wake; its review job is a wake, fetched by job_next (no status rule), and
// accepted as today.
func TestFetchReviewJob(t *testing.T) {
	t.Parallel()
	r, text := fetched(t)
	r.mu.Lock()
	var prompt string
	for _, l := range r.launches {
		if l.Agent == "pj-int" {
			prompt = l.Prompt
		}
	}
	r.mu.Unlock()
	if prompt != wake("T-1/1/review") {
		t.Errorf("the integrator's start prompt %q, want the review job's wake", prompt)
	}
	if ts := r.task("T-1"); ts.Job != "T-1/1/review" || !ts.Delivered { // T-664: the integrator's job, fetched
		t.Errorf("status with the review job fetched: job %q delivered %v; want T-1/1/review, delivered", ts.Job,
			ts.Delivered)
	}
	r.accept("c1", r.patch(text))
	r.waitStep("1 result agent.stop")
	if r.mainMerges() != 1 {
		t.Errorf("not merged: %q", r.steps())
	}
}

// After a sendback the fix job is a wake, fetched by job_next (its text the notes). task_done with a full report
// submits the next attempt, its report and call id on the fact, and the next review job quotes the report; a repeated
// call id gets the stored reply; once submitted, another task_done is refused. The guards: the caller's role and
// agent, the report (empty, a section missing).
func TestFetchFixAndTaskDone(t *testing.T) {
	t.Parallel()
	r, _ := fetched(t)
	if reply, err := r.verdict(wire.MethodSendback, "s1", "", "add a test"); err != nil || !strings.HasPrefix(reply, "SENDBACK T-1") {
		t.Fatalf("sendback: %q, %v", reply, err)
	}
	r.integratorStops() // it ends its turn after the verdict, and serve stops it: no job is left for it (T-672)
	r.waitStep("2 result fix")
	eq(t, r.promptsTo("pj-l-01"), []string{wake("T-1/2/fix")}, "typed to pj-l-01")
	jr := r.workerNext("f1")
	if jr.Job != "T-1/2/fix" || !strings.Contains(jr.Text, "T-1 is sent back to you (attempt 2): add a test") ||
		!strings.Contains(jr.Text, "call task_done again") || strings.Contains(jr.Text, "done file") {
		t.Fatalf("the fix job: %+v", jr)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 3 })

	fixed := "Summary: added the test.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	for _, row := range []struct{ name, agent, role, report, want string }{
		{"another agent", "pj-l-02", "worker", fixed, "not your task (T-1 is pj-l-01's in lane L-01, not pj-l-02's)"},
		{"the integrator", "pj-int", "integrator", fixed, "a worker submits its task, not pj-int"},
		{"an empty report", "pj-l-01", "worker", "  \n", "the report is empty: pass your final report (Summary, " +
			"Self-check, Skipped, Open questions) as report"},
		{"a section missing", "pj-l-01", "worker", "Summary: x\nSelf-check: x\nSkipped: x", "the report has no section Open questions"},
	} {
		if _, err := r.callDone(row.agent, row.role, "t-"+row.name, row.report); err == nil ||
			!strings.Contains(err.Error(), row.want) {
			t.Errorf("%s: task_done %v, want %q", row.name, err, row.want)
		}
	}
	r.passes()
	if r.has("2 fact submitted") {
		t.Fatalf("a refused task_done recorded: %q", r.steps())
	}

	if reply, err := r.callDone("pj-l-01", "worker", "t1", fixed); err != nil || reply != "submitted" {
		t.Fatalf("task_done: %q, %v", reply, err)
	}
	if reply, err := r.callDone("pj-l-01", "worker", "t1", fixed); err != nil || reply != "submitted" {
		t.Errorf("repeated call id: %q, %v; want the stored reply", reply, err)
	}
	if _, err := r.callDone("pj-l-01", "worker", "t2", fixed); err == nil ||
		!strings.Contains(err.Error(), "not open for a submission") {
		t.Errorf("a second task_done: %v", err)
	}
	review := r.waitJob("pj-int", "T-1/2/review")
	if !strings.Contains(review, "The worker's report:\n"+fixed) {
		t.Errorf("the review job does not quote the report:\n%s", review)
	}
	if n := r.count("2 fact submitted"); n != 1 {
		t.Errorf("submitted facts of attempt 2: %d, want 1: %q", n, r.steps())
	}
	var d struct{ Call, Report string }
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepSubmitted && rec.Attempt == 2 {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	if d.Call != "t1" || d.Report != fixed {
		t.Errorf("the submitted fact's call id %q, report %q; want t1 and the report", d.Call, d.Report)
	}
}

// A Blocked: report submits nothing: it records needs-you{asks: <what it needs>} (the task stays working), notifies
// the owner and tells the worker to wait; no stall is added while the worker waits, and its activity does not clear it
// (T-511: the Stop hook's turn after the report is no answer): obor answer (answer_test.go) or its submission does. A
// full report then submits, and the question ends with it.
func TestFetchBlockedAsks(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.workerIs("working")
	asks := "0 fact needs-you{asks: may I edit internal/x.go?}"
	reply, err := r.callDone("pj-l-01", "worker", "b1", "Summary: half done\nBlocked: may I edit internal/x.go?\n")
	if err != nil || !strings.Contains(reply, "the owner is told") || !strings.Contains(reply, "stop and wait") {
		t.Fatalf("task_done Blocked: %q, %v", reply, err)
	}
	r.waitStep(asks)
	r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if r.hasPrefix("1 fact submitted") || r.taskState() != "working" {
		t.Fatalf("a Blocked: report submitted (%s): %q", r.taskState(), r.steps())
	}
	r.push()
	r.passes() // still the asking turn: working is no answer
	r.workerIs("idle")
	r.later(25 * time.Minute)
	r.push()
	r.passes()
	if r.hasPrefix("0 fact stalled") || r.hasPrefix("0 fact resumed") || r.count(asks) != 1 {
		t.Fatalf("while the worker waits: %q, want only the asks fact", r.steps())
	}
	if f := r.app.Status().Project.Facts; len(f) != 1 {
		t.Fatalf("facts in force %+v, want the asks fact", f)
	}
	r.workerIs("working") // the owner answered in its pane, or the Stop hook's turn
	r.push()
	r.passes()
	if r.hasPrefix("0 fact resumed") || len(r.app.Status().Project.Facts) != 1 {
		t.Fatalf("the worker's activity ended its question: %q", r.steps())
	}
	full := "Summary: done.\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	r.work("feature.txt", "feature\n") // its gate merges something: no needs-you of its own after the submission
	if reply, err := r.callDone("pj-l-01", "worker", "t1", full); err != nil || reply != "submitted" {
		t.Errorf("task_done after the answer: %q, %v", reply, err)
	}
	r.waitFor("the question ended by the submission", func() bool { return r.facts() == 0 })
}

// An unfetched fix job: its wake again after 5 min, the owner after 5 more; obor resume (its default: retry) sends it
// anew as fix.2, whose fetch delivers it.
func TestFetchFixUndelivered(t *testing.T) {
	t.Parallel()
	r, _ := fetched(t)
	if _, err := r.verdict(wire.MethodSendback, "s1", "", "add a test"); err != nil {
		t.Fatal(err)
	}
	r.waitStep("2 result fix")
	r.later(5 * time.Minute)
	r.waitStep("2 result retry.fix")
	r.later(5 * time.Minute)
	r.waitStep("2 fact needs-you{job not delivered: T-1/2/fix prompted twice to pj-l-01}")
	r.needsYouState()
	r.mustResume("r1", "", "RESUMED T-1: retry (job not delivered)")
	r.waitStep("2 result fix.2")
	eq(t, r.promptsTo("pj-l-01"), []string{wake("T-1/2/fix"), wake("T-1/2/fix"), wake("T-1/2/fix.2")},
		"typed to pj-l-01")
	if jr := r.workerNext("f2"); jr.Job != "T-1/2/fix.2" {
		t.Errorf("job_next after the resume: %+v, want T-1/2/fix.2", jr)
	} else if !strings.Contains(jr.Text, "If this session did not get the work job (a fresh start), read ") ||
		!strings.Contains(jr.Text, "templates/agents/worker.md") {
		t.Errorf("the fix job names no role text for a worker started fresh:\n%s", jr.Text)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 3 })
	r.later(20 * time.Minute)
	if r.has("2 intent retry.fix.2") {
		t.Errorf("a fetched fix job retried: %q", r.steps())
	}
}

// Replay with a wake pending: the crash came after the typing of the retry's wake began; the replay sends the wake once
// more (T-524: harmless twice) and the worker's job_next gets the job.
func TestFetchReplayWake(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.disp.set(func(d *dispatchFakes) { d.cut["prompt"] = "after" })
	r.clock.Add(5 * time.Minute)
	r.push()
	r.waitFor("the wake typed", func() bool { return len(r.promptsTo("pj-l-01")) == 1 })
	r.waitFor("the prompt cut", func() bool { r.disp.mu.Lock(); defer r.disp.mu.Unlock(); return r.disp.hit["prompt"] })
	r.stop()
	if !r.has("1 intent retry.work") || r.has("1 result retry.work") {
		t.Fatalf("precondition: the wake's intent pending: %q", r.steps())
	}
	r.disp.set(func(d *dispatchFakes) { delete(d.cut, "prompt") })
	r.start()
	r.waitStep("1 result retry.work")
	r.passes()
	eq(t, r.promptsTo("pj-l-01"), []string{wake("T-1/1/work"), wake("T-1/1/work")}, "typed to pj-l-01 after the replay")
	if jr := r.workerNext("n1"); jr.Job != "T-1/1/work" {
		t.Errorf("job_next after the replay: %+v", jr)
	}
}

// T-437: a Codex and an OpenCode worker (the rig's sonnet as that kind) finish as a Claude one does: the start carries the work job's wake, job_next
// fetches it, task_done (its report) submits the task, and the merge loop takes it to merged.
func TestFetchCodexOpenCodeFinishByTaskDone(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"codex", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := fetchRig(t, testkit.Set("models.sonnet", map[string]any{"kind": kind, "model": "x"}))
			r.work("feature.txt", "feature\n")
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 result work")
			r.mu.Lock()
			prompt := r.launches[0].Prompt
			r.mu.Unlock()
			same(t, prompt, wake("T-1/1/work"), kind+"'s start prompt")
			if jr := r.workerNext("n1"); jr.Job != "T-1/1/work" {
				t.Fatalf("job_next: %+v, want the work job", jr)
			}
			r.waitFor("working", func() bool { return r.taskState() == "working" })
			r.submit()
			r.waitStep("1 fact submitted")
			r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
			r.waitStep("1 result agent.stop")
			if r.mainMerges() != 1 {
				t.Errorf("not merged: %q", r.steps())
			}
		})
	}
}
