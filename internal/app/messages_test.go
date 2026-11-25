package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// recSeq is the seq of the first record of task with step and kind (0: none).
func recSeq(t *testing.T, h *harness, task, step, kind string) int64 {
	t.Helper()
	for _, rec := range readLog(t, h.root) {
		if rec.Task == task && rec.Step == step && rec.Kind == kind {
			return rec.Seq
		}
	}
	return 0
}

// registerLead rewrites root's config with prefix: the lead is <prefix>-lead (obor setup rename's change: a
// lead's agent name is its prefix's).
func registerLead(t *testing.T, root, prefix string) {
	t.Helper()
	cfg := `{"version":2,"project":{"name":"proj","agent_prefix":"` + prefix + `"},"agents":{"roles":{"lead":"","integrator":"","reviewer":""}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestQueuedLeadLineAfterRename: a lead line (a send, a finished line) queued for want of an inbox is
// neither typed into the old lead's idle pane after the lead is renamed and serve restarts, nor lost: it is posted
// once its inbox is recorded (V1).
func TestQueuedLeadLineAfterRename(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"send", "finished"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			a.opt.Inboxed = func(string) (string, bool) { return "", false }
			queueLine(t, a, p, kind, now)
			first := &linerSpy{}
			finishBriefs(a, p, withAppCfg(a, &Loop{Prompter: first, Inbox: &inboxSpy{}})) // records the finished line
			a.deliver(context.Background(), p, withAppCfg(a, &Loop{Prompter: first, Inbox: &inboxSpy{}}))
			if len(first.calls) != 0 || len(p.out.Queued()) != 1 || !p.out.Queued()[0].IsLead() {
				t.Fatalf("precondition: one lead line queued, nothing typed; typed %v, queued %d", first.calls, len(p.out.Queued()))
			}

			registerLead(t, p.root, "pj-new")
			b, q := lineReload(t, a, p, &now)
			if q.out.Lead() != "pj-new-lead" || len(q.out.Queued()) != 1 {
				t.Fatalf("precondition: renamed lead %q with one line queued, got %d", q.out.Lead(), len(q.out.Queued()))
			}
			pane := b.obs["w:lead"].pane
			pane.Label = "pj-lead" // the old lead's session still open and idle under its old name
			eventNow(b, port.Event{Kind: port.EventStatus, Pane: pane.ID, New: pane})
			if w := b.whereIs(q, "pj-lead"); w.State != Identified || !Ready(w.Pane.Status) {
				t.Fatal("precondition: the old lead's pane is idle under its old name")
			}

			b.opt.Inboxed = func(string) (string, bool) { return "", false }
			lines, posts := &linerSpy{}, &inboxSpy{}
			b.deliver(context.Background(), q, withAppCfg(b, &Loop{Prompter: lines, Inbox: posts}))
			if len(lines.calls) != 0 || len(posts.calls) != 0 || len(q.out.Queued()) != 1 {
				t.Fatalf("no inbox: typed %q, posted %q, queued %d; want nothing tried, still queued", lines.calls,
					posts.calls, len(q.out.Queued()))
			}

			b.opt.Inboxed = func(string) (string, bool) { return "", true }
			b.deliver(context.Background(), q, withAppCfg(b, &Loop{Prompter: lines, Inbox: posts}))
			b.deliver(context.Background(), q, withAppCfg(b, &Loop{Prompter: lines, Inbox: posts}))
			if len(lines.calls) != 0 || len(posts.calls) != 1 || !strings.HasPrefix(posts.calls[0], "pj-lead: [obor") ||
				len(q.out.Queued()) != 0 {
				t.Errorf("inbox recorded: typed %q, posted %q, queued %d; want posted once, never typed", lines.calls,
					posts.calls, len(q.out.Queued()))
			}
		})
	}
}

// TestRunningLeadHint: the queued send's reply names `obor restart <lead>` (obor lead starts nothing for a
// running lead), and the upgrade guide and state-model row 47 say the same (V2).
func TestRunningLeadHint(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	a.opt.Inboxed = func(string) (string, bool) { return "", false }
	reply, err := a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "s1"}, From: "other",
		Text: "hello"})
	hint := "`obor restart pj-lead` once it is idle registers it, or quit it and run `obor lead`"
	if err != nil || !strings.Contains(reply, hint) || strings.Contains(reply, "obor lead registers") {
		t.Errorf("reply %q (%v); want it to name %s", reply, err, hint)
	}
	for file, want := range map[string]string{
		"state-model.md": strings.ReplaceAll(hint, "pj-lead", "<lead>"),
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", file))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(b)), " ") // the guide wraps its lines
		if !strings.Contains(text, want) || strings.Contains(text, "obor lead registers it") ||
			strings.Contains(text, "restarted once (`obor lead`)") {
			t.Errorf("docs/%s: want %q, and no recipe that has obor lead register a running lead", file, want)
		}
	}
}
