package herdr

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"obor/internal/app/port"
)

// Panes is port.Panes over the herdr socket.
type Panes struct {
	S *Socket
}

var _ port.Panes = Panes{}

// Subscribe is a Stream as the core sees it: lifecycle and status events.
// It pings first: a herdr on another protocol is refused with a clear error. An EventStatus carries the pane as read
// after the event in New (name, group, cwd and its session too), like an EventCreated. A pane's Group is its workspace's
// label as the stream's snapshot listed it: a new or renamed workspace ends the stream (Code refresh), so the labels
// hold for the stream's life.
func (p Panes) Subscribe(ctx context.Context) (*port.Stream, error) {
	var s *Socket
	var err error
	if p.S.Conn != nil { // given its connections (a herdr served in memory): no path to dial
		s, err = (&Socket{Conn: p.S.Conn}).checked(ctx)
	} else {
		s, err = Dial(ctx, p.S.Path)
	}
	if err != nil {
		return nil, err
	}
	s.Timeout = p.S.Timeout
	st, err := s.Subscribe(ctx)
	if err != nil {
		return nil, err
	}
	c, done := make(chan port.Event), make(chan struct{})
	var once sync.Once
	labels := st.Snapshot.labels()
	go func() {
		defer close(c)
		for ev := range st.C {
			select {
			case c <- p.appEvent(ev, labels):
			case <-done:
				return
			}
		}
	}()
	return &port.Stream{Snapshot: p.appSnapshot(st.Snapshot), C: c, Err: st.Err,
		Close: func() { once.Do(func() { close(done); st.Close() }) }}, nil
}

func (p Panes) appEvent(ev Event, labels map[string]string) port.Event {
	switch ev.Kind {
	case EventStatus:
		return port.Event{Kind: port.EventStatus, Pane: ev.Pane, Status: ev.Status, Agent: ev.Agent,
			New: ev.Now.labelled(labels)}
	case EventCreated:
		pane := ev.Now.labelled(labels)
		pane.Label, pane.Terminal = ev.New.Label, ev.New.Terminal
		return port.Event{Kind: port.EventCreated, Pane: ev.Pane, New: pane}
	case EventClosed:
		return port.Event{Kind: port.EventClosed, Pane: ev.Pane}
	}
	return port.Event{Kind: port.EventExited, Pane: ev.Pane}
}

func (p Panes) appSnapshot(s Snapshot) port.Snapshot {
	out := port.Snapshot{Version: s.Version, Instance: p.instance(), Note: note(s)}
	labels := s.labels()
	seen := observed(s)
	for _, pane := range s.Panes {
		ap := seen[pane.ID].labelled(labels)
		ap.Label, ap.Terminal = pane.Label, pane.Terminal
		out.Panes = append(out.Panes, ap)
	}
	return out
}

// instance is the herdr server's instance: its socket file's modification time in RFC 3339 with nanoseconds (herdr
// creates the socket at its start and nothing writes the file after; its API exposes no server pid, start time or
// instance id). "" when the file cannot be read.
func (p Panes) instance() string {
	fi, err := os.Stat(p.S.Path)
	if err != nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339Nano)
}

// note is herdr's own sentence when it speaks a newer protocol than this client knows ("" otherwise): the status note.
func note(s Snapshot) string {
	if s.Protocol <= Protocol {
		return ""
	}
	return fmt.Sprintf("herdr %s, protocol %d (newer than obor's %d)", s.Version, s.Protocol, Protocol)
}

// Snapshot is herdr's session now, as the subscription's Snapshot: the same source (session.snapshot), for a pane whose
// cwd changed with no event (a cd in it).
func (p Panes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	s, err := p.S.Snapshot(ctx)
	if err != nil {
		return port.Snapshot{}, err
	}
	return p.appSnapshot(s), nil
}

// Notify is notification.show.
func (p Panes) Notify(ctx context.Context, title, body string) error {
	return p.S.Notify(ctx, title, body)
}
