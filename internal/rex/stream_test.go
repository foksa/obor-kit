package rex

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/linestream"
	"obor/internal/testkit"
)

// streamLines are testdata/stream.jsonl's lines: a `rex events --all --json` recorded on a scratch server while a
// session was made, its block relabelled, the session relabelled, a second block run (a sleep in it), closed, the
// first block's shell exited, then a second session made and destroyed.
func streamLines(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open("testdata/stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		out = append(out, append(append([]byte(nil), sc.Bytes()...), '\n'))
	}
	if len(out) != 96 {
		t.Fatalf("precondition: stream.jsonl has its 96 lines, got %d", len(out))
	}
	return out
}

// The client lines (every CLI call's join and leave, a session attach's connect and disconnect) are dropped, with
// each join's pid; every other line is kept.
func TestClientLines(t *testing.T) {
	var kept []string
	joins := 0
	for _, l := range streamLines(t) {
		typ, pid, ok := clientLine(l)
		if !ok {
			kept = append(kept, typ)
			continue
		}
		if !strings.HasPrefix(typ, "client_") {
			t.Errorf("dropped a %s line", typ)
		}
		if typ == EventClientJoined {
			joins++
			if pid <= 0 {
				t.Errorf("a client_joined without its pid: %s", l)
			}
		}
	}
	if len(kept) != 30 || joins != 20 {
		t.Errorf("kept %d lines with %d joins, want the recording's 30 and 20", len(kept), joins)
	}
	for _, typ := range kept {
		if strings.HasPrefix(typ, "client_") {
			t.Errorf("kept a %s line", typ)
		}
	}
}

// The recording's blocks: the first session's shell block (rex new: login runs the shell), its second block (rex run
// -- /bin/zsh -l) and the second session's block.
const (
	sesCap  = "session:01a21238-4d58-72a3-828b-6c2e7af66f83"
	sesCap2 = "session:01a21239-09b7-7d71-b273-b56f9c6f95fe"
	blkB    = "block:01a21238-4d58-7b65-b12f-fb4043ae59fa"
	blkB2   = "block:01a21238-bfe6-7dde-97e5-1d444f8f5757"
	blkB3   = "block:01a21239-09b8-7111-9241-d7dab15aa335"
)

// recorded are the reads the server answered at line n of the recording (1-based): each session's blocks as the
// commands between the lines left them, and a block's processes as recorded for its kind of block.
func recorded(t *testing.T, n *int) reads {
	var login, shell Processes
	readJSON(t, "process-login.json", &login)
	readJSON(t, "process-shell.json", &shell)
	return reads{
		sessions: func(context.Context) ([]Session, error) {
			t.Fatal("every session's label came with its session_created")
			return nil, nil
		},
		blocks: func(_ context.Context, sid string) ([]Block, error) {
			var out []Block
			switch {
			case sid == sesCap:
				if *n < 65 {
					label := "shell"
					if *n >= 22 {
						label = "w1"
					}
					out = append(out, Block{ID: blkB, Label: label, Creator: Terminal})
				}
				if *n >= 33 && *n < 55 {
					out = append(out, Block{ID: blkB2, Label: "w2", Creator: Terminal})
				}
			case sid == sesCap2 && *n < 79:
				out = append(out, Block{ID: blkB3, Label: "shell", Creator: Terminal})
			}
			return out, nil
		},
		term: func(_ context.Context, tm *term) error {
			tm.Proc = login
			if tm.Block.ID == blkB2 {
				tm.Proc = shell
			}
			return nil
		},
	}
}

// what is an event as the replay asserts it: its kind and pane, and for a pane sent its label, group and status.
func what(e port.Event) string {
	short := map[string]string{blkB: "B", blkB2: "B2", blkB3: "B3"}[e.Pane]
	if e.New.ID == "" {
		return e.Kind + " " + short
	}
	return fmt.Sprintf("%s %s %s@%s %s", e.Kind, short, e.New.Label, e.New.Group, e.New.Status)
}

// The recorded stream as pane events: a session's first block is created with no block_created (read on
// session_created), a relabel is read on session_view_changed, a session's relabel is every block's group, a run
// block is created on block_created, its sleep is unknown while it runs, block_closed closes it, the shell's exit is
// the block's exit (its block_closed after it sends nothing), and a second session's block is created and closed.
func TestStreamRecorded(t *testing.T) {
	n := 0
	st := &stream{ctx: context.Background(), kinds: testkit.Kinds, reads: recorded(t, &n), labels: map[string]string{}, terms: map[string]term{}}
	var got []string
	for i, l := range streamLines(t) {
		n = i + 1
		if _, _, ok := clientLine(l); ok {
			continue
		}
		evs, err := st.apply(l)
		if err != nil {
			t.Fatalf("line %d: %v", n, err)
		}
		for _, e := range evs {
			got = append(got, what(e))
		}
	}
	want := []string{
		"created B @cap ", // its label is Rex's default (label): none
		"status B w1@cap ",
		"status B w1@capproj ",
		"created B2 w2@capproj ",
		"status B2 w2@capproj unknown",
		"status B2 w2@capproj ",
		"closed B2",
		"exited B",
		"created B3 @cap2 ",
		"closed B3",
	}
	// Every milestone, in order; between them only status events of a pane's cwd or foreground.
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
			continue
		}
		if !strings.HasPrefix(g, "status ") {
			t.Errorf("event %q not in the milestones %q", g, want)
		}
	}
	if i != len(want) {
		t.Errorf("events %q: milestone %q missing", got, want[i])
	}
	if len(st.terms) != 0 {
		t.Errorf("blocks left after every one closed: %v", st.terms)
	}
}

// A session's blocks that cannot be read again end the stream as a refresh; a session gone closes its blocks; a block
// event of another creator, and an event Rex adds that obor does not read, send nothing; a line that is no JSON ends
// the stream as no refresh.
func TestStreamEdges(t *testing.T) {
	known := func() *stream {
		return &stream{ctx: context.Background(), kinds: testkit.Kinds, labels: map[string]string{sesCap: "cap"},
			terms: map[string]term{blkB: {Session: sesCap, Group: "cap", Block: Block{ID: blkB, Label: "w1", Creator: Terminal},
				Proc: Processes{Child: &Process{PID: 1}}}}}
	}
	view := []byte(`{"type":"session_event","payload":{"session_id":"` + sesCap + `","event":{"type":"session_view_changed","payload":{"revision":2}}}}`)

	t.Run("a read that fails is a refresh", func(t *testing.T) {
		st := known()
		st.reads.blocks = func(context.Context, string) ([]Block, error) {
			return nil, &Error{Op: "session.list_blocks", Msg: "no Rex server answers", down: true}
		}
		_, err := st.apply(view)
		if !port.Refreshed(err) || !strings.Contains(err.Error(), "no Rex server answers") {
			t.Errorf("apply = %v, want a refresh with the read's error", err)
		}
	})
	t.Run("a session gone closes its blocks", func(t *testing.T) {
		st := known()
		st.reads.blocks = func(context.Context, string) ([]Block, error) {
			return nil, &Error{Op: "session.list_blocks", Code: "session_not_found", Msg: "session not found"}
		}
		evs, err := st.apply(view)
		if err != nil || len(evs) != 1 || evs[0].Kind != port.EventClosed || evs[0].Pane != blkB {
			t.Errorf("apply = %+v, %v; want B closed", evs, err)
		}
	})
	t.Run("another creator's block event and an unknown event send nothing", func(t *testing.T) {
		st := known()
		for _, l := range []string{
			`{"type":"session_event","payload":{"session_id":"` + sesCap + `","event":{"type":"block_event","payload":{"block_id":"block:x","creator_name":"com.superlogical.tunnel","name":"process_changed","payload":{}}}}}`,
			`{"type":"session_event","payload":{"session_id":"` + sesCap + `","event":{"type":"block_event","payload":{"block_id":"` + blkB + `","creator_name":"` + Terminal + `","name":"title_changed","payload":{"title":"x"}}}}}`,
			`{"type":"window_created","payload":{}}`,
		} {
			if evs, err := st.apply([]byte(l)); err != nil || len(evs) != 0 {
				t.Errorf("apply(%s) = %+v, %v; want nothing", l, evs, err)
			}
		}
	})
	t.Run("a line that is no JSON ends the stream, no refresh", func(t *testing.T) {
		_, err := known().apply([]byte("Error: something\n"))
		if err == nil || port.Refreshed(err) {
			t.Errorf("apply = %v, want an error that is no refresh", err)
		}
	})
}

// The CLI exiting ends the stream as a refresh naming its status and stderr: status 4, "server went away", when the
// server stops. The lines before it are applied first.
func TestStreamEndsOnExit(t *testing.T) {
	status := `{"type":"session_event","payload":{"session_id":"` + sesCap + `","event":{"type":"block_event","payload":{"block_id":"` + blkB +
		`","creator_name":"` + Terminal + `","name":"program_status_changed","payload":{"id":"","record":{"state":"working","app":"claude-code","owner":{"pid":7}},"reason":"report"}}}}}` + "\n"
	joined := `{"type":"client_joined","payload":{"client":{"principal":{"pid":42}}}}` + "\n"
	src := &source{r: bufio.NewReader(strings.NewReader(joined + status)), joins: newJoins(), done: make(chan struct{}), exited: make(chan struct{}),
		code: 4, stderr: "Error: server went away: reading API control: reading control message: failed to get reader: failed to read frame header: EOF\n"}
	close(src.exited)
	st := &stream{ctx: context.Background(), kinds: testkit.Kinds, labels: map[string]string{sesCap: "cap"},
		terms: map[string]term{blkB: {Session: sesCap, Block: Block{ID: blkB, Creator: Terminal},
			Proc: Processes{Child: &Process{PID: 1}, Foreground: &Process{PID: 7}, Ancestors: []Process{{PID: 1}}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ls := linestream.Start[port.Event](ctx, cancel, src.read, 4, &Error{Op: "events", Msg: "closed", down: true})
	ls.Run(linestream.Rules[port.Event]{Apply: st.apply, End: src.end})
	var got []port.Event
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case e, ok := <-ls.C():
			if !ok {
				done = true
				break
			}
			got = append(got, e)
		case <-timeout:
			t.Fatal("the stream did not end")
		}
	}
	if len(got) != 1 || got[0].Status != "working" || got[0].Agent != "claude" {
		t.Errorf("events = %+v, want B working (claude) before the end", got)
	}
	err := ls.Err()
	if !port.Refreshed(err) || port.NotRunning(err) {
		t.Fatalf("Err = %v, want a refresh", err)
	}
	if !strings.Contains(err.Error(), "status 4") || !strings.Contains(err.Error(), "server went away") {
		t.Errorf("Err = %v, want the status and Rex's message", err)
	}
	if src.joins.wait(context.Background(), 42, src.done) != nil {
		t.Error("the client_joined line's pid was not seen")
	}
}

// A sync's wait: the pid seen before or while it waits is nil; the stream ending first is errEnded; ctx ending first
// errLate; once synced, later joins are not kept.
func TestJoinsWait(t *testing.T) {
	j := newJoins()
	j.saw(7)
	if err := j.wait(context.Background(), 7, nil); err != nil {
		t.Errorf("seen before: %v, want nil", err)
	}
	j.saw(8)
	if j.seen != nil {
		t.Errorf("joins after the sync kept: %v", j.seen)
	}

	j = newJoins()
	go func() { time.Sleep(10 * time.Millisecond); j.saw(3); j.saw(9) }()
	if err := j.wait(context.Background(), 9, nil); err != nil {
		t.Errorf("seen while waiting: %v, want nil", err)
	}

	j = newJoins()
	done := make(chan struct{})
	close(done)
	if err := j.wait(context.Background(), 9, done); err != errEnded {
		t.Errorf("stream ended: %v, want errEnded", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := j.wait(ctx, 9, make(chan struct{})); err != errLate {
		t.Errorf("ctx ended: %v, want errLate", err)
	}
}

// Rex's failures as errors: exit status 4 (no server at the address, or it went away) is NotRunning; Rex's not_found and
// session_not_found are NotFound; another code is an answer, not NotFound; any other exit carries its status.
func TestExitError(t *testing.T) {
	for _, c := range []struct {
		code                       int
		stderr, msg, rexCode       string
		notRunning, notFound, answ bool
	}{
		{4, "Error: no Rex server is running at /private/tmp/claude-501/rxl02/none.sock, and autostart is disabled\n",
			"no Rex server is running at /private/tmp/claude-501/rxl02/none.sock, and autostart is disabled", "", true, false, false},
		{3, `Error: stream response "control-2" failed: block not found: block:01a21238-0000-7000-8000-000000000000 (status=404 code=not_found)` + "\n",
			"block not found: block:01a21238-0000-7000-8000-000000000000", "not_found", false, true, true},
		{3, `Error: stream response "control-1" failed: session session:01a21238-0000-7000-8000-000000000000 not found (status=404 code=session_not_found)` + "\n",
			"session session:01a21238-0000-7000-8000-000000000000 not found", "session_not_found", false, true, true},
		{3, `Error: stream response "control-1" failed: netapi: no handler for method "nope.nope" (status=404 code=method_not_found)` + "\n",
			`netapi: no handler for method "nope.nope"`, "method_not_found", false, false, true},
		{1, "panic: oops\n", "panic: oops (exit status 1)", "", false, false, false},
	} {
		e := exitError("m", c.code, c.stderr)
		if e.Msg != c.msg || e.Code != c.rexCode || port.NotRunning(e) != c.notRunning || port.NotFound(e) != c.notFound || port.Answered(e) != c.answ {
			t.Errorf("exitError(%d, %q) = %+v (NotRunning %v NotFound %v Answered %v), want msg %q code %q %v %v %v", c.code, c.stderr, e,
				port.NotRunning(e), port.NotFound(e), port.Answered(e), c.msg, c.rexCode, c.notRunning, c.notFound, c.answ)
		}
	}
}

// The CLI's global options: the server's address and autostart off when the client has them, none by default.
func TestArgs(t *testing.T) {
	if got := (&Client{}).args("events"); !slices.Equal(got, []string{"events"}) {
		t.Errorf("default args = %q, want none before the command", got)
	}
	got := (&Client{Server: "unix:///s.sock", NoAutostart: true}).args("api", "call", "session.list", "{}")
	if want := []string{"-S", "unix:///s.sock", "--autostart=false", "api", "call", "session.list", "{}"}; !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
}
