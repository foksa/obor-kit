package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
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
func askCall(id string) wire.Call {
	return wire.Call{Root: "/r", Agent: "pj-int", Role: "integrator", CallID: id, Task: "T-1", Job: "T-1/2/review",
		Patch: "p1", Note: "why"}
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
	reply, err := wire.Verdict(context.Background(), sock, wire.MethodAccept, askCall("c1"))
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
	reply, err := wire.Verdict(ctx, sock, wire.MethodAccept, askCall("c1"))
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
			reply, err := wire.Verdict(context.Background(), sock, wire.MethodAccept, askCall("c1"))
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
	if _, err := wire.Verdict(ctx, sock, wire.MethodAccept, askCall("c1")); err == nil {
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
	reply, err := wire.Verdict(context.Background(), sock, wire.MethodAccept, askCall("c1"))
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
	if _, err := wire.Verdict(ctx, sock, wire.MethodAccept, askCall("c1")); err == nil {
		t.Fatal("Verdict answered a call that never got a reply")
	}
	mu.Lock()
	defer mu.Unlock()
	if accepted != 1 {
		t.Errorf("timed-out call reached the app %d times, want once", accepted)
	}
}

// sockDir is a new short temp dir (a unix socket path is limited), removed at the test's end.
func sockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ask")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// storingApp is a fake serve that applies each call once, stores its reply by call id and answers a repeated id from
// the store, as the app does (docs/state-model.md, class F). With drop, the first connection is applied and closed
// unanswered (a reply lost to a crash after the record).
type storingApp struct {
	mu      sync.Mutex
	conns   int
	applied map[string]int
	ids     []string
}

// serveStoring listens at sock after delay (no listener before: a restart's socket not bound yet) and serves until
// the test ends.
func serveStoring(t *testing.T, sock string, delay time.Duration, drop bool) *storingApp {
	t.Helper()
	s := &storingApp{applied: map[string]int{}}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		select {
		case <-done:
			return
		case <-time.After(delay):
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Error(err)
			return
		}
		go func() {
			<-done
			ln.Close()
		}()
		stored := map[string]string{}
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, err := bufio.NewReader(c).ReadBytes('\n')
			if err != nil {
				c.Close()
				continue
			}
			id := callID(line)
			s.mu.Lock()
			s.conns++
			s.ids = append(s.ids, id)
			reply, ok := stored[id]
			if !ok {
				s.applied[id]++
				reply = `{"reply":"done ` + id + `"}`
				stored[id] = reply
			}
			first := s.conns == 1
			s.mu.Unlock()
			if !(drop && first) {
				_, _ = c.Write([]byte(reply + "\n"))
			}
			c.Close()
		}
	}()
	return s
}

// counts is s's connections, the calls applied per id and the ids seen.
func (s *storingApp) counts() (int, map[string]int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	applied := map[string]int{}
	for k, v := range s.applied {
		applied[k] = v
	}
	return s.conns, applied, append([]string(nil), s.ids...)
}

// T-659: a verdict made while serve restarts (no socket until the new serve has loaded) is retried inside its 15 s
// and lands once: one connection, one record.
func TestVerdictLandsAfterRestartOnce(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(sockDir(t), "serve.sock")
	s := serveStoring(t, sock, 3*time.Second, false)
	start := time.Now()
	reply, err := wire.Verdict(context.Background(), sock, wire.MethodAccept, askCall("c1"))
	if err != nil || reply != "done c1" {
		t.Fatalf("Verdict through a 3 s restart = %q, %v; want the reply", reply, err)
	}
	if d := time.Since(start); d < 3*time.Second {
		t.Fatalf("answered after %v, before the listener was there", d)
	}
	if conns, applied, _ := s.counts(); conns != 1 || applied["c1"] != 1 {
		t.Errorf("connections %d, applied %v; want the call received and recorded once", conns, applied)
	}
}

// T-659: the StopFailure hook's call (5 s) survives a serve back at 4 s: a retry near its deadline lands.
func TestStopFailureLandsNearItsDeadline(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(sockDir(t), "serve.sock")
	s := serveStoring(t, sock, 4*time.Second, false)
	c := wire.Call{Root: "/r", Agent: "pj-l-01", Role: "worker", Lane: "L-01", CallID: "s1", Error: "rate_limit"}
	start := time.Now()
	if _, err := wire.Verdict(context.Background(), sock, wire.MethodStopped, c); err != nil {
		t.Fatalf("stop-failure through a 4 s restart: %v; want it recorded", err)
	}
	if d := time.Since(start); d < 4*time.Second || d > 5*time.Second {
		t.Errorf("answered after %v; want after the 4 s restart, inside the 5 s deadline", d)
	}
	if _, applied, _ := s.counts(); applied["s1"] != 1 {
		t.Errorf("applied %v; want the stop recorded once", applied)
	}
}

// T-659: a turn call (8 s) whose serve never comes back fails at its deadline, and a serve that binds afterwards gets
// nothing: no call is applied after its caller gave up.
func TestTurnFailsAtItsDeadline(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(sockDir(t), "serve.sock")
	c := wire.Call{Root: "/r", Agent: "pj-l-01", Role: "worker", Lane: "L-01", CallID: "t1"}
	// The call's wait is in a synctest bubble: with no socket every dial fails at once and the retries' waits and the
	// deadline run on fake time (8.8 s before). A live listener cannot be in the bubble: a goroutine in Accept is not
	// durably blocked, so the fake clock would stall.
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		_, err := wire.Verdict(context.Background(), sock, wire.MethodTurn, c)
		d := time.Since(start)
		if err == nil {
			t.Fatal("a turn with no serve answered")
		}
		if d < 7*time.Second || d > 8500*time.Millisecond {
			t.Errorf("failed after %v; want at the turn's 8 s deadline", d)
		}
	})
	s := serveStoring(t, sock, 0, false) // the serve is back after the caller gave up
	time.Sleep(250 * time.Millisecond)   // real: a late listener needs no second, the caller is gone
	if conns, applied, _ := s.counts(); conns != 0 || len(applied) != 0 {
		t.Errorf("after the deadline the serve saw %d connections, applied %v; want nothing", conns, applied)
	}
}

// T-659: a reply lost after the serve stored the call is asked again with the same call id; the stored reply answers
// it and the call is applied once.
func TestLostReplyAnsweredFromStore(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(sockDir(t), "serve.sock")
	s := serveStoring(t, sock, 0, true)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("precondition: the fake serve never bound its socket")
		}
	}
	reply, err := wire.Verdict(context.Background(), sock, wire.MethodAccept, askCall("c1"))
	if err != nil || reply != "done c1" {
		t.Fatalf("Verdict after a lost reply = %q, %v; want the stored reply", reply, err)
	}
	conns, applied, ids := s.counts()
	if conns != 2 || len(ids) != 2 || ids[0] != "c1" || ids[1] != "c1" {
		t.Errorf("connections %d, ids %q; want the same call id twice", conns, ids)
	}
	if applied["c1"] != 1 {
		t.Errorf("applied %v; want once", applied)
	}
}
