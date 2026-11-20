package hc_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
)

// Event payloads as hc 0.1.21 sent them on a scratch session (2026-11-20), the shell terminal t1 of
// record_test.go's shellRecord.
const (
	fgSub      = `{"argv":["/bin/zsh","-l"],"cwd":"/private/tmp/obhc-3752525046/work/sub","name":"zsh","pid":34044}`
	statusWork = `{"records":{},"reported":true,"status":{"app":"claude-code","state":"working"}}`
	statusNull = `{"records":{},"reported":true,"status":null}`
	turn1      = `{"state":null,"turn":1}`
	labelsW2   = `{"labels":{"obor.agent":"w2","obor.project":"cap"}}`
	labelsOut  = `{"labels":{"obor.agent":"w2"}}`
	createdT2  = `{"address":"t2","argv":["/bin/sleep","600"],"cols":200,"created_ms":1795171305935,"cwd":"/private/tmp/obhc-3752525046/work","die_with_parent":false,"exit_code":null,"finished_ms":null,"identity":"ghostty","keep":false,"labels":{"obor.project":"cap"},"name":null,"notify":true,"parent":null,"pid":34138,"reason":null,"reported":false,"rm":false,"rows":120,"screen":"exact","signal":null,"state":"live","status":null,"status_ms":null,"template":null,"title":"","turn":0}`
	finishedT2 = `{"exit_code":null,"finished_ms":1795171306962,"reason":"holder_lost","signal":null}`
)

// startedCore is a core with t1, a shell at /w labelled cap/w1, and a stream subscribed to it.
func startedCore(t *testing.T) (*hctest.Core, *port.Stream) {
	t.Helper()
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", PID: 34044, Cwd: "/tmp/obhc-3752525046/work",
		Labels:     map[string]string{hc.LabelProject: "cap", hc.LabelAgent: "w1"},
		Foreground: &hc.Foreground{PID: 34044, Name: "zsh", Argv: []string{"/bin/zsh", "-l"}, Cwd: "/private/tmp/obhc-3752525046/work"}})
	st, err := hc.Panes{C: core.Client()}.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if core.Subscribers() != 1 {
		t.Fatalf("precondition: %d subscribers, want 1", core.Subscribers())
	}
	return core, st
}

// next is the stream's next event; false when C closed.
func next(t *testing.T, st *port.Stream) (port.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-st.C:
		return ev, ok
	case <-time.After(10 * time.Second):
		t.Fatal("no event and no end in 10 s")
	}
	return port.Event{}, false
}

// ended is the stream's end: C closes (any event before that is a failure) and Err says why.
func ended(t *testing.T, st *port.Stream) error {
	t.Helper()
	for {
		ev, ok := next(t, st)
		if !ok {
			return st.Err()
		}
		t.Errorf("event %+v before the end", ev)
	}
}

// The stream applies each event to its copy of the record and sends the whole pane when it changed: the cwd a
// foreground event follows, a status and its kind, labels (a label change; obor.project removed: out of the group),
// a new terminal, its end. A repeated foreground, a turn event, a heartbeat at the last seq and the removal of a
// terminal that already ended send nothing.
func TestStreamEvents(t *testing.T) {
	core, st := startedCore(t)
	if len(st.Snapshot.Panes) != 1 || st.Snapshot.Panes[0].Group != "cap" || st.Snapshot.Instance == "" {
		t.Fatalf("snapshot = %+v, want t1 in group cap and an instance", st.Snapshot)
	}
	base := port.Pane{ID: "t1", Terminal: "t1", Group: "cap", Label: "w1", Cwd: "/private/tmp/obhc-3752525046/work/sub"}
	steps := []struct {
		name, term, typ, data string
		want                  *port.Event // nil: nothing sent (the next step's event comes first)
	}{
		{"cd sub", "t1", "foreground", fgSub, &port.Event{Kind: port.EventStatus, Pane: "t1", New: base}},
		{"the same foreground again", "t1", "foreground", fgSub, nil},
		{"a status", "t1", "status", statusWork, &port.Event{Kind: port.EventStatus, Pane: "t1", Status: "working", Agent: "claude",
			New: func() port.Pane { p := base; p.Status, p.Agent = "working", "claude"; return p }()}},
		{"status cleared at the shell", "t1", "status", statusNull, &port.Event{Kind: port.EventStatus, Pane: "t1", New: base}},
		{"a turn", "t1", "turn", turn1, nil},
		{"relabelled", "t1", "labels", labelsW2, &port.Event{Kind: port.EventStatus, Pane: "t1",
			New: func() port.Pane { p := base; p.Label = "w2"; return p }()}},
		{"obor.project removed", "t1", "labels", labelsOut, &port.Event{Kind: port.EventStatus, Pane: "t1",
			New: func() port.Pane { p := base; p.Label, p.Group = "w2", ""; return p }()}},
		{"a new terminal", "t2", "created", createdT2, &port.Event{Kind: port.EventCreated, Pane: "t2",
			New: port.Pane{ID: "t2", Terminal: "t2", Group: "cap", Cwd: "/private/tmp/obhc-3752525046/work"}}},
		{"its end", "t2", "finished", finishedT2, &port.Event{Kind: port.EventExited, Pane: "t2"}},
		{"its removal after the end", "t2", "removed", `{}`, nil},
	}
	var pending []string
	for i, s := range steps {
		core.Event(s.term, s.typ, json.RawMessage(s.data))
		if i == 2 {
			core.Heartbeat() // at the last event's seq: no gap
		}
		if s.want == nil {
			pending = append(pending, s.name)
			continue
		}
		ev, ok := next(t, st)
		if !ok {
			t.Fatalf("%s: the stream ended: %v", s.name, st.Err())
		}
		if ev != *s.want {
			t.Errorf("%s (after %q sent nothing): event = %+v\nwant %+v", s.name, pending, ev, *s.want)
		}
		pending = nil
	}
	core.Event("t1", "removed", json.RawMessage(`{}`))
	if ev, ok := next(t, st); !ok || ev != (port.Event{Kind: port.EventClosed, Pane: "t1"}) {
		t.Errorf("after %q sent nothing: removed t1 = %+v, %v; want EventClosed t1", pending, ev, ok)
	}
	if err := st.Err(); err != nil {
		t.Errorf("the stream ended: %v", err)
	}
}

// Each way a stream ends: hc's missed and a seq gap (in an event or a heartbeat) as a refresh; the connection closing
// (a restart, a stalled reader), a stop's lost server_stop, and a lost holder's restarted as a lost connection.
func TestStreamEndings(t *testing.T) {
	for _, c := range []struct {
		name      string
		do        func(core *hctest.Core)
		refreshed bool
		says      string
	}{
		{"missed", func(core *hctest.Core) { core.Missed() }, true, "missed"},
		{"a seq gap", func(core *hctest.Core) { core.Skip(); core.Event("t1", "foreground", json.RawMessage(fgSub)) }, true, "events were lost"},
		{"a heartbeat past the last event", func(core *hctest.Core) { core.Skip(); core.Heartbeat() }, true, "events were lost"},
		{"the connection closed", func(core *hctest.Core) { core.Down() }, false, "connection closed"},
		{"a stop", func(core *hctest.Core) { core.Stop() }, false, "server_stop"},
		{"a lost holder", func(core *hctest.Core) { core.Restart("t1", 34182) }, false, "fresh shell"},
	} {
		t.Run(c.name, func(t *testing.T) {
			core, st := startedCore(t)
			c.do(core)
			err := ended(t, st)
			if port.Refreshed(err) != c.refreshed || port.NotRunning(err) == c.refreshed || err == nil ||
				!strings.Contains(err.Error(), c.says) {
				t.Errorf("Err = %v: Refreshed %v, NotRunning %v; want refreshed %v, saying %q", err, port.Refreshed(err),
					port.NotRunning(err), c.refreshed, c.says)
			}
		})
	}
}

// No line for HeartbeatLost ends the stream as a lost connection; a heartbeat in time keeps it.
func TestStreamHeartbeatLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core, st := startedCore(t)
		time.Sleep(hc.HeartbeatLost - time.Second)
		core.Heartbeat()
		time.Sleep(hc.HeartbeatLost - time.Second)
		synctest.Wait()
		if err := st.Err(); err != nil {
			t.Fatalf("ended %s after a heartbeat: %v", hc.HeartbeatLost-time.Second, err)
		}
		time.Sleep(2 * time.Second)
		err := ended(t, st)
		if !port.NotRunning(err) || port.Refreshed(err) || !strings.Contains(err.Error(), "no heartbeat") {
			t.Errorf("Err = %v: NotRunning %v, Refreshed %v; want a lost connection", err, port.NotRunning(err), port.Refreshed(err))
		}
		st.Close()
		core.Down()
	})
}

// The subscription's snapshot leaves a finished terminal out (hc lists it there, unlike terminal.list) and has the
// instance Snapshot reads; an event of that finished terminal sends nothing.
func TestSubscribeSnapshot(t *testing.T) {
	core := hctest.New()
	core.Add(hc.Record{Address: "t1", Cwd: "/w", Labels: map[string]string{hc.LabelProject: "cap"}})
	core.Add(hc.Record{Address: "t2", State: "finished", Reason: "holder_lost", Cwd: "/w", Labels: map[string]string{hc.LabelProject: "cap"}})
	panes := hc.Panes{C: core.Client()}
	st, err := panes.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	snap, err := panes.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []port.Pane{{ID: "t1", Terminal: "t1", Group: "cap", Cwd: "/w"}}
	if !slices.Equal(st.Snapshot.Panes, want) || st.Snapshot.Instance != snap.Instance || snap.Instance == "" {
		t.Errorf("stream snapshot = %+v, want panes %+v and Snapshot's instance %q", st.Snapshot, want, snap.Instance)
	}
	core.Event("t2", "labels", json.RawMessage(`{"labels":{"obor.project":"cap","obor.agent":"w9"}}`))
	core.Event("t1", "labels", json.RawMessage(`{"labels":{"obor.project":"cap","obor.agent":"w1"}}`))
	if ev, ok := next(t, st); !ok || ev.Pane != "t1" || ev.New.Label != "w1" {
		t.Errorf("first event = %+v, %v; want t1's relabel (finished t2's sends nothing)", ev, ok)
	}
}

// A core that is down refuses the subscription as NotRunning.
func TestSubscribeDown(t *testing.T) {
	core := hctest.New()
	core.Down()
	if _, err := (hc.Panes{C: core.Client()}).Subscribe(context.Background()); !port.NotRunning(err) {
		t.Errorf("Subscribe = %v, want NotRunning", err)
	}
}
