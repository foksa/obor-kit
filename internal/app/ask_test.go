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

	"obor/internal/app"
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

// askCall is an integrator's accept of T-1 under call id id.
func askCall(id string) app.Call {
	return app.Call{Root: "/r", Agent: "pj-int", Role: "integrator", CallID: id, Task: "T-1", Job: "T-1/2/review",
		Patch: "p1", Note: "why"}
}

// A reply lost to a crash after the app recorded the verdict: the retry carries the same call id and gets the stored reply.
func TestAskRetriesLostReplyWithSameCallID(t *testing.T) {
	sock, seen := fakeApp(t, func(n int) string {
		if n == 1 {
			return "" // the app went away after recording the verdict
		}
		return `{"reply":"accepted T-1"}`
	})
	reply, err := app.Verdict(context.Background(), sock, app.MethodAccept, askCall("c1"))
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
	reply, err := app.Verdict(ctx, sock, app.MethodAccept, askCall("c1"))
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

// callID is the call id of a request line.
func callID(line []byte) string {
	var req struct {
		Call struct {
			CallID string `json:"call_id"`
		} `json:"call"`
	}
	_ = json.Unmarshal(line, &req)
	return req.Call.CallID
}

// A first dial refused carried nothing, so there is no reply to lose: ask returns the refusal at once instead of
// waiting out its budget for a serve that is not there.
func TestAskFirstDialRefusedReturnsAtOnce(t *testing.T) {
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
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the socket file stays, the listener is gone: a dial is refused
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("precondition: no socket file to refuse the dial: %v", err)
	}
	start := time.Now()
	_, err = app.Verdict(context.Background(), sock, app.MethodAccept, askCall("c1"))
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Verdict on a refused socket: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("first dial refused returned after %v; want the refusal at once, not the retry budget", d)
	}
}

// The retries stop at the caller's deadline, not after the budget.
func TestAskRetriesStopAtCallerDeadline(t *testing.T) {
	sock, seen := fakeApp(t, func(int) string { return "" }) // every connection is dropped unanswered
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := app.Verdict(ctx, sock, app.MethodAccept, askCall("c1")); err == nil {
		t.Fatal("Verdict answered a call the app never answered")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("returned after %v; want at the caller's deadline (500ms), not after the budget", d)
	}
	if ids := seen(); len(ids) < 2 || ids[0] != "c1" || ids[1] != "c1" {
		t.Errorf("call ids seen = %q; want the same id on the retries", ids)
	}
}

// A refusal is the app's answer, not a lost reply: it is returned once, not retried.
func TestAskDoesNotRetryRefusal(t *testing.T) {
	sock, seen := fakeApp(t, func(int) string { return `{"error":"no job open for T-1"}` })
	reply, err := app.Verdict(context.Background(), sock, app.MethodAccept, askCall("c1"))
	if err == nil || !strings.Contains(err.Error(), "no job open for T-1") || reply != "" {
		t.Fatalf("Verdict = %q, %v; want the refusal", reply, err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("refused call reached the app %d times, want once", n)
	}
}

// A live answer that does not come in time is not retried: the caller gave up on it.
func TestAskDoesNotRetryTimeout(t *testing.T) {
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
	if _, err := app.Verdict(ctx, sock, app.MethodAccept, askCall("c1")); err == nil {
		t.Fatal("Verdict answered a call that never got a reply")
	}
	mu.Lock()
	defer mu.Unlock()
	if accepted != 1 {
		t.Errorf("timed-out call reached the app %d times, want once", accepted)
	}
}
