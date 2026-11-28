package inbox_test

// The one post into an agent's session inbox: the record of the agent's slot in its project, the pane's kind's lines
// from it (or the kind sending it itself), and the socket they go to; a refused send or a dead socket marks the record
// refused, which takes it out of the project's live inboxes.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/inbox"
	"obor/internal/project"
	"obor/internal/testkit"
)

// the poster reads the inbox record of the lead's slot in its project, dials its socket and writes the auth line
// (the record's token) then the user line with the text as it is, and the session reads both before the connection
// ends; no record, or a record whose socket answers nothing (a dial refused), posts nothing (began false, the record
// marked refused); the project has a live inbox only for a record whose socket is there.
func TestPosterPostsToTheInbox(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	pj := testkit.NewPlain(t)
	pj.Config()
	cfg := pj.Cfg()
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c) // to the poster's half-close
		c.Close()
		got <- string(b)
	}()
	p := inbox.Poster{Root: pj.Root, Kinds: testkit.Kinds()}
	ctx := context.Background()

	if began, err := p.Post(ctx, agents.Default, "pj-lead", "x"); began || err == nil || !strings.Contains(err.Error(), "no session recorded for pj-lead") {
		t.Errorf("no record: began %v, %v; want refused before a byte", began, err)
	}
	if _, ok := project.InboxLive(cfg, "pj-lead"); ok {
		t.Error("inboxed with no record")
	}
	if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Socket: sock, Token: "tok-1", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if s, ok := project.InboxLive(cfg, "pj-lead"); !ok || s != "s1" {
		t.Errorf("inboxed = %q, %v; want the recording session s1 with a record whose socket is there", s, ok)
	}
	const line = `[obor other] release "v0.12" is out`
	if began, err := p.Post(ctx, agents.Default, "pj-lead", line); !began || err != nil {
		t.Fatalf("post: began %v, %v", began, err)
	}
	want := `{"type":"auth","token":"tok-1"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"[obor other] release \"v0.12\" is out"}}` + "\n"
	select {
	case b := <-got:
		if b != want {
			t.Errorf("the session read %q, want %q", b, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session read nothing")
	}

	if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Socket: filepath.Join(dir, "gone.sock")}); err != nil {
		t.Fatal(err)
	}
	if began, err := p.Post(ctx, agents.Default, "pj-lead", line); began || err == nil {
		t.Errorf("a dead socket: began %v, %v; want refused before a byte", began, err)
	}
	if in, err := project.ReadInbox(pj.Cfg(), "lead"); err != nil || !strings.Contains(in.Refused, "no such file") {
		t.Errorf("the record after the missing socket's dial: %+v, %v; want it marked refused", in, err)
	}
	if _, ok := project.InboxLive(cfg, "pj-lead"); ok {
		t.Error("inboxed with a record whose socket is gone")
	}
}

// A dial no socket answers (a session that ended left its socket) marks the record refused, as a refused agy send
// does: the project has no live inbox for the agent, so the next pass holds its lines for its hooks instead of posting
// to it again; a new session's record (its SessionStart's) is live again.
func TestPosterMarksADeadSocketRefused(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	pj := testkit.NewPlain(t)
	pj.Config()
	cfg := pj.Cfg()
	sock := filepath.Join(dir, "dead.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false) // the session's socket stays, as a dead one's does
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("precondition: the dead session's socket file: %v", err)
	}
	if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Socket: sock, Token: "tok-1", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if s, ok := project.InboxLive(cfg, "pj-lead"); !ok || s != "s1" {
		t.Fatalf("precondition: inboxed = %q, %v; want session s1 live (its socket file is there)", s, ok)
	}
	p := inbox.Poster{Root: pj.Root, Kinds: testkit.Kinds()}
	if began, err := p.Post(context.Background(), agents.Default, "pj-lead", "[obor other] hi"); began || err == nil ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("a dead socket: began %v, %v; want refused before a byte", began, err)
	}
	in, err := project.ReadInbox(pj.Cfg(), "lead")
	if err != nil || !strings.Contains(in.Refused, "connection refused") || in.Socket != sock || in.Session != "s1" {
		t.Fatalf("the record after the refused dial: %+v, %v; want it marked refused as it was read", in, err)
	}
	if _, ok := project.InboxLive(cfg, "pj-lead"); ok {
		t.Error("a refused record is live: the next pass would post to it again")
	}
	if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Socket: sock, Token: "tok-2", Session: "s2"}); err != nil {
		t.Fatal(err)
	}
	if in, err := project.ReadInbox(pj.Cfg(), "lead"); err != nil || in.Refused != "" {
		t.Fatalf("the new session's record: %+v, %v; want no refusal mark", in, err)
	}
	if s, ok := project.InboxLive(cfg, "pj-lead"); !ok || s != "s2" {
		t.Errorf("a new session's record: inboxed = %q, %v; want session s2 live again", s, ok)
	}
}

// An OpenCode session's record keeps the kind's own record (its plugin's socket under OBOR_OPENCODE_INBOX): the poster
// hands that to the kind's deliver, dials the socket it names and writes its one line, {"type":"user","text"}.
func TestPosterPostsAPluginKindsRecord(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	pj := testkit.NewPlain(t)
	pj.Config()
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		c.Close()
		got <- string(b)
	}()
	rec := `{"OBOR_OPENCODE_INBOX":"` + sock + `"}`
	if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Socket: sock, Session: "ses_1", Record: rec}); err != nil {
		t.Fatal(err)
	}
	if began, err := (inbox.Poster{Root: pj.Root, Kinds: testkit.Kinds()}).Post(context.Background(), "opencode", "pj-lead", "[obor other] hi"); !began || err != nil {
		t.Fatalf("post: began %v, %v", began, err)
	}
	select {
	case b := <-got:
		if want := `{"text":"[obor other] hi","type":"user"}` + "\n"; b != want {
			t.Errorf("the session read %q, want %q", b, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session read nothing")
	}
}

// An agy session's record (its inbox variables, from an obor command it ran) is one the kind sends with: the poster
// hands it to the kind's deliver, which runs `agy agentapi send-message`, agy from obor's PATH, never an executable an
// old record names (began, no socket). A refused send (agentapi's non-zero exit: a stale token) delivers nothing and
// marks the record refused, so it is not live (its lines go through the lead's hooks) until a command records it again.
// Not parallel: agy is looked up on the process's PATH, which the test sets.
func TestPosterSendsWithAnAgyRecord(t *testing.T) {
	pj := testkit.NewPlain(t)
	pj.Config(testkit.Set("agents.models.gem", map[string]any{"kind": "agy", "id": "gemini-3-flash"}),
		testkit.Set("agents.roles.lead", "gem"))
	dir := t.TempDir()
	ran, stale := filepath.Join(dir, "ran"), filepath.Join(dir, "stale")
	body := "#!/bin/sh\nprintf '%s|' \"$@\" \"$ANTIGRAVITY_CSRF_TOKEN\" > " + ran + "\n[ -e " + stale + " ] || exit 0\n" +
		"echo 'rpc error: code = Unauthenticated desc = invalid CSRF token'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	other := filepath.Join(t.TempDir(), "agentapi")
	if err := os.WriteFile(other, []byte("#!/bin/sh\ntouch "+other+".ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"ANTIGRAVITY_LS_ADDRESS": "localhost:5011", "ANTIGRAVITY_CSRF_TOKEN": "tok-1",
		"ANTIGRAVITY_AGENTAPI_EXE": other, "ANTIGRAVITY_CONVERSATION_ID": "conv-1"})
	rec := string(b)
	if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Sends: true, Record: rec}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(pj.Root)
	if err != nil {
		t.Fatal(err)
	}
	p := inbox.Poster{Root: pj.Root, Kinds: testkit.Kinds()}
	if began, err := p.Post(context.Background(), "agy", "pj-lead", "[obor other] hi"); !began || err != nil {
		t.Fatalf("post: began %v, %v; want sent", began, err)
	}
	if b, err := os.ReadFile(ran); err != nil || string(b) != "agentapi|send-message|conv-1|[obor other] hi|tok-1|" {
		t.Errorf("agy ran with %q, %v", b, err)
	}
	if _, err := os.Stat(other + ".ran"); err == nil {
		t.Error("the record's executable ran")
	}

	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, live := project.InboxLive(cfg, "pj-lead"); !live {
		t.Fatal("precondition: the record is not live before the send")
	}
	began, err := p.Post(context.Background(), "agy", "pj-lead", "[obor other] again")
	want := "pj-lead refused the send: kind agy: deliver: agentapi send-message: exit 1: rpc error: code = Unauthenticated desc = invalid CSRF token"
	if began || err == nil || err.Error() != want {
		t.Errorf("a refused send: began %v, %v; want not begun, %q", began, err, want)
	}
	in, err := project.ReadInbox(pj.Cfg(), "lead")
	if err != nil || in.Record != rec || !strings.HasPrefix(in.Refused, "kind agy: deliver: agentapi send-message: exit 1") {
		t.Errorf("the record after the refusal: %+v, %v; want it marked refused", in, err)
	}
	if _, live := project.InboxLive(cfg, "pj-lead"); live {
		t.Error("a refused record is live")
	}
}

// A sandboxed agy worker's record is in its lane's state dir (the main checkout refused the write): the poster reads
// it there and sends with it, and a refused send marks that file's record refused, so the worker is not live (its
// lines are typed) until a command records it again. Not parallel: agy is looked up on the process's PATH, which the
// test sets.
func TestPosterReadsALaneRecord(t *testing.T) {
	pj := testkit.NewPlain(t)
	lane := pj.PlainLane("L-01", "task/T-1")
	pj.Config()
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	exe := filepath.Join(dir, "agy")
	body := "#!/bin/sh\nprintf '%s|' \"$@\" > " + ran + "\n[ -e " + filepath.Join(dir, "stale") + " ] || exit 0\n" +
		"echo 'rpc error: code = Unauthenticated desc = invalid CSRF token'\nexit 1\n"
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, _ := json.Marshal(map[string]string{"ANTIGRAVITY_LS_ADDRESS": "localhost:5011", "ANTIGRAVITY_CSRF_TOKEN": "tok-1",
		"ANTIGRAVITY_CONVERSATION_ID": "conv-1"})
	b, _ = json.Marshal(project.Inbox{Sends: true, Record: string(b)})
	f := project.InboxFile(lane, "L-01")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := pj.Cfg()
	if _, err := os.Stat(project.InboxFile(pj.Root, "L-01")); err == nil {
		t.Fatal("precondition: a record in the main checkout")
	}
	if _, live := project.InboxLive(cfg, "pj-l-01"); !live {
		t.Fatal("precondition: the lane's record is not live")
	}
	p := inbox.Poster{Root: pj.Root, Kinds: testkit.Kinds()}
	if began, err := p.Post(context.Background(), "agy", "pj-l-01", "[obor job T-1/1/work] hi"); !began || err != nil {
		t.Fatalf("post: began %v, %v; want sent", began, err)
	}
	if b, err := os.ReadFile(ran); err != nil || string(b) != "agentapi|send-message|conv-1|[obor job T-1/1/work] hi|" {
		t.Errorf("agy ran with %q, %v", b, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "stale"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if began, err := p.Post(context.Background(), "agy", "pj-l-01", "[obor job T-1/1/work] again"); began || err == nil {
		t.Fatalf("a refused send: began %v, %v; want refused", began, err)
	}
	if in, err := project.ReadInbox(cfg, "L-01"); err != nil || !strings.Contains(in.Refused, "invalid CSRF token") {
		t.Errorf("the lane's record after the refusal: %+v, %v; want it marked refused", in, err)
	}
	if _, live := project.InboxLive(cfg, "pj-l-01"); live {
		t.Error("a refused lane record is live")
	}
}

// poster.Post posts with the kind it is given (the pane's), not the first kind with an inbox: the same record
// is posted for a kind with an inbox, and refused before a byte (nothing dialled) for a kind without one or a kind
// obor has no bridge for. The refusals name no kind's product ("no session recorded").
func TestPosterUsesTheGivenKind(t *testing.T) {
	t.Parallel()
	withInbox, without := agents.Default, "" // Claude's record names its socket itself
	kinds := testkit.Kinds()
	for _, name := range kinds.Names() {
		if k, _ := kinds.Lookup(name); !k.Caps().Inbox && without == "" {
			without = name
		}
	}
	if k, _ := kinds.Lookup(withInbox); !k.Caps().Inbox || without == "" {
		t.Fatalf("kinds %v: want one with an inbox and one without", kinds.Names())
	}
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	pj := testkit.NewPlain(t)
	pj.PlainLane("L-01", "task/T-1")
	pj.Config()
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	dials := make(chan struct{}, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials <- struct{}{}
			_, _ = io.Copy(io.Discard, c) // read to the poster's half-close: closing first races its write (EPIPE)
			c.Close()
		}
	}()
	p := inbox.Poster{Root: pj.Root, Kinds: testkit.Kinds()}
	ctx := context.Background()

	if began, err := p.Post(ctx, withInbox, "pj-l-01", "x"); began || err == nil ||
		!strings.Contains(err.Error(), "no session recorded for pj-l-01") || strings.Contains(err.Error(), "Claude") {
		t.Errorf("no record: began %v, %v; want refused, \"no session recorded\" naming no product", began, err)
	}
	if err := project.WriteInbox(pj.Cfg(), "L-01", project.Inbox{Socket: sock, Token: "t", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{without, "nokind"} {
		if began, err := p.Post(ctx, kind, "pj-l-01", "x"); began || err == nil {
			t.Errorf("kind %s: began %v, %v; want refused before a byte", kind, began, err)
		}
	}
	select {
	case <-dials:
		t.Fatal("a kind without an inbox dialled the record's socket")
	case <-time.After(100 * time.Millisecond):
	}
	if began, err := p.Post(ctx, withInbox, "pj-l-01", "x"); !began || err != nil {
		t.Fatalf("kind %s: began %v, %v; want posted", withInbox, began, err)
	}
	select {
	case <-dials:
	case <-time.After(5 * time.Second):
		t.Fatal("the kind with an inbox dialled nothing")
	}
}

// two projects with the same prefix both have a pj-lead; each one's record is in its own project,
// so serve's poster for each project posts its lead's line into that project's lead session, never the other's.
func TestPosterKeepsProjectsWithOnePrefixApart(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "in") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	type side struct {
		root string
		cfg  *config.Config
		got  chan string
	}
	var sides []side
	for i, name := range []string{"one", "two"} {
		pj := testkit.NewPlain(t)
		pj.Config(testkit.Set("project.name", name)) // both keep the prefix pj
		sock := filepath.Join(dir, strconv.Itoa(i)+".sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		got := make(chan string, 4)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				b, _ := io.ReadAll(c)
				c.Close()
				got <- string(b)
			}
		}()
		if err := project.WriteInbox(pj.Cfg(), "lead", project.Inbox{Socket: sock, Token: name, Session: "s-" + name}); err != nil {
			t.Fatal(err)
		}
		sides = append(sides, side{pj.Root, pj.Cfg(), got})
	}
	if sides[0].root == sides[1].root {
		t.Fatal("precondition: two projects")
	}
	for i, s := range sides {
		if sess, ok := project.InboxLive(s.cfg, "pj-lead"); !ok || sess != "s-"+[]string{"one", "two"}[i] {
			t.Errorf("project %d: inboxed = %q, %v; want its own lead's session", i, sess, ok)
		}
	}
	ctx := context.Background()
	for i, s := range sides {
		name := []string{"one", "two"}[i]
		if began, err := (inbox.Poster{Root: s.root, Kinds: testkit.Kinds()}).Post(ctx, agents.Default, "pj-lead", "to "+name); !began || err != nil {
			t.Fatalf("project %s: began %v, %v", name, began, err)
		}
		select {
		case b := <-s.got:
			if !strings.Contains(b, `"token":"`+name+`"`) || !strings.Contains(b, "to "+name) {
				t.Errorf("project %s's lead read %q, want its own line with its own token", name, b)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("project %s's lead read nothing", name)
		}
		select {
		case b := <-sides[1-i].got:
			t.Errorf("the other project's lead read %q", b)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
