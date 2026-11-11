package herdr

import (
	"context"
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
	out := port.Snapshot{Version: s.Version, Protocol: s.Protocol, Speaks: Protocol, Started: p.started()}
	labels := s.labels()
	seen := observed(s)
	for _, pane := range s.Panes {
		ap := seen[pane.ID].labelled(labels)
		ap.Label, ap.Terminal = pane.Label, pane.Terminal
		out.Panes = append(out.Panes, ap)
	}
	return out
}

// started is when the herdr server on p's socket started: the socket file's modification time (herdr creates the
// socket at its start and nothing writes the file after; its API exposes no server pid, start time or instance id).
// Zero when the file cannot be read.
func (p Panes) started() time.Time {
	fi, err := os.Stat(p.S.Path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
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

// Screen is pane.read: the pane's visible text, its last lines (what a blocked pane asks a person).
func (p Panes) Screen(ctx context.Context, pane string, lines int) (string, error) {
	return p.S.Screen(ctx, pane, lines)
}

var _ port.Typer = Panes{}

// Type types text into pane (agent.prompt, no wait): a job's wake with its [obor job <id>] tag, or serve's line as it
// is. The agent's own proof of delivery (a job's job_next) counts, not herdr's answer. clear: the input line is
// cleared first (ctrl+u). No key follows the text: a bare Enter would act on a selection dialog herdr reports as idle,
// and an undelivered wake is the app's delivery timers' to send again. began is false only when nothing of the text
// was typed: the clear failed, or agent.prompt was refused by herdr or never sent (Reached); after that, any error
// (a lost answer) comes with began true.
func (p Panes) Type(ctx context.Context, pane, text string, clear bool) (bool, error) {
	if clear {
		if err := p.S.AgentKeys(ctx, pane, "ctrl+u"); err != nil {
			return false, err
		}
	}
	if _, err := p.S.Prompt(ctx, pane, text); err != nil {
		return Reached(err), err
	}
	return true, nil
}
