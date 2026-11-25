package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/version"
	"sync"
	"testing"
	"time"
)

// memListener is an in-memory net.Listener: dial hands Listen one end of a net.Pipe.
type memListener struct {
	conns chan net.Conn
	once  sync.Once
	done  chan struct{}
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }

func (l *memListener) Addr() net.Addr { return &net.UnixAddr{Name: "mem", Net: "unix"} }

// send writes one request line to the app listening on l and returns the connection to read the answer from: the
// call carries this binary's version, as wire.Verdict's does.
func (l *memListener) send(t *testing.T, c wire.Req) net.Conn {
	t.Helper()
	c.Env().Version = version.Version
	return l.sendRaw(t, c)
}

// sendRaw writes one request line as given: a test of the skew itself sends another version, or none (an older binary).
func (l *memListener) sendRaw(t *testing.T, c wire.Req) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	l.conns <- server
	call, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(wire.Request{Method: c.Method(), Call: call})
	go func() { _, _ = client.Write(append(b, '\n')) }()
	return client
}

// MemServe is a's socket answered in memory: Listen on an in-memory listener until the test ends. Call is one
// request as wire.Verdict sends it (the envelope stamped with this binary's version): the reply, or the app's refusal
// as the error. It has none of the wire client's retries or deadlines: a test of those uses a unix socket.
type MemServe struct{ ln *memListener }

// ServeMem answers a's socket in memory until t ends (the connections a testing/synctest bubble can wait on).
func ServeMem(t *testing.T, a *App) *MemServe {
	t.Helper()
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go a.Listen(ln)
	return &MemServe{ln: ln}
}

// Call sends c and gives the app's reply, or its refusal as the error.
func (s *MemServe) Call(t *testing.T, c wire.Req) (string, error) {
	t.Helper()
	client := s.ln.send(t, c)
	defer client.Close()
	ans := read(t, client)
	if ans.Error != "" {
		return "", errors.New(ans.Error)
	}
	return ans.Reply, nil
}

// read is the answer on client.
func read(t *testing.T, client net.Conn) wire.Answer {
	t.Helper()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	b, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	var ans wire.Answer
	if err := json.Unmarshal(b, &ans); err != nil {
		t.Fatal(err)
	}
	return ans
}

// obeying is a FakeAgents.OnLaunch that runs start inside each launch before it returns, as an agent that obeys its CLI
// first prompt before the launch returns.
func obeying(start func(port.Launch)) func(context.Context, port.Launch) (port.Started, error) {
	return func(_ context.Context, l port.Launch) (port.Started, error) {
		start(l)
		return port.Started{Line: "STARTED " + l.Agent}, nil
	}
}

func resultOf(p *proj, k event.Key) string {
	p.log.Mu.Lock()
	defer p.log.Mu.Unlock()
	return string(p.log.Results[k].Data)
}

func done(p *proj, k event.Key) bool { return p.log.Done(k) }

func deliveredOf(r *concRig, job string) int {
	recs, err := event.ReadLog(r.roots["pj"])
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered && rec.Fact().Job == job {
			n++
		}
	}
	return n
}

// same fails t when got is not want.
func same[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// heldAtTrust is launch's result for l held by a Claude trust dialog in pane: a fresh start (its prompt in the argv)
// is its Started with a port.TrustDialog (agents.TrustHint); a resumed one is the failed start launch gives up on.
func heldAtTrust(l port.Launch, pane, line string, resumed bool) (port.Started, error) {
	hint := agents.TrustHint(agents.Default, l.Agent, pane)
	if resumed {
		return port.Started{}, fmt.Errorf("start failed in %s (%s)", pane, hint)
	}
	return port.Started{Line: line, Pane: pane}, &port.TrustDialog{Hint: hint}
}

// trustAt is a FakeAgents.OnLaunch whose every start is held by a trust dialog in pane (heldAtTrust; resume: a launch
// without Fresh resumes a recorded session).
func trustAt(pane string, resume bool) func(context.Context, port.Launch) (port.Started, error) {
	return func(_ context.Context, l port.Launch) (port.Started, error) {
		return heldAtTrust(l, pane, "STARTED "+l.Agent, resume && !l.Fresh)
	}
}

// launchFails is a FakeAgents.OnLaunch whose every start fails with err.
func launchFails(err error) func(context.Context, port.Launch) (port.Started, error) {
	return func(context.Context, port.Launch) (port.Started, error) { return port.Started{}, err }
}

// stopFails is a FakeAgents.OnStop whose every stop fails with err.
func stopFails(err error) func(context.Context, string, string, bool) error {
	return func(context.Context, string, string, bool) error { return err }
}

// allVerbs are the task verbs, verbName their names for a failure.
var (
	allVerbs = []verb{vResume, vDrop, vLand, vMove, vSubmit}
	verbName = map[verb]string{vResume: "resume", vDrop: "drop", vLand: "land", vMove: "move", vSubmit: "submit"}
)

// needsOf is a needs-you fact of cause.
func needsOf(cause string) event.Record {
	data, _ := json.Marshal(event.FactData{Merge: true, Cause: cause, Reason: cause + ": why"})
	return event.Record{Seq: 1, Kind: event.KindFact, Step: event.StepNeedsYou, Data: data}
}

// heldOf is a held block: the needs-you fact of cause in force.
func heldOf(cause string) block { return block{fact: needsOf(cause)} }

// closedOf is a held block that began due (a closed one): its phase's work ended with cause.
func closedOf(cause string) block {
	return block{cause: cause, why: cause + ": why", fact: needsOf(cause)}
}

// busy: another task than task holds p's scratch (scratchHolder).
func busy(p *proj, task string) bool {
	t, _ := p.scratchHolder(task)
	return t != ""
}
