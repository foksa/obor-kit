package launch_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/launch"
	"obor/internal/store"
	"obor/internal/testkit"
)

// agyReviewer is the launcher's fixture with the reviewer on an agy model and the reviewer's session as rec (the
// SessionStart hook's record: its conversation id and kind; none: rec.ID "").
func agyReviewer(t *testing.T, recs ...store.Session) *fx {
	t.Helper()
	x := setup(t, testkit.AddModels(`{"neon":{"kind":"agy","id":"gemini-4-neon-high"}}`),
		testkit.Set("agents.roles", `{"integrator":"opus","lead":"opus","reviewer":"neon"}`))
	recordSessions(t, x.p.Root, recs...)
	return x
}

// obor restart of an agy agent without --fresh resumes from obor's own record, the conversation id and kind its
// SessionStart hook recorded: after its process exited (its pane at the shell, herdr reporting no session) and after
// its pane is gone (a new pane placed). A record of the agent's earlier kind never wins over agy's live session in
// the pane, nor resumes when there is none (a new session).
func TestRestartAgyResumesItsRecord(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	agyRec := store.Session{Agent: "pj-rev", Kind: "agy", ID: "conv-1", Started: now,
		Transcript: "/h/.gemini/antigravity-cli/brain/conv-1/.system_generated/logs/transcript_full.jsonl"}
	claudeRec := store.Session{Agent: "pj-rev", Kind: "claude", ID: "sid-c", Started: now}
	t.Run("exited: the record", func(t *testing.T) {
		t.Parallel()
		x := agyReviewer(t, agyRec)
		x.shell("w1:p7", "pj-rev", "")
		if x.f.At("w1:p7").Session != "" {
			t.Fatal("precondition: herdr reports no session for the pane")
		}
		out, ok := x.restart(launch.Request{Agent: "pj-rev"})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-rev", "w1:p7", "conv-1")
		contains(t, x.startCall(), "--conversation conv-1", "the recorded conversation resumed")
		contains(t, x.startCall(), "kind=agy", "an agy start")
	})
	t.Run("pane gone: the record, in a placed pane", func(t *testing.T) {
		t.Parallel()
		x := agyReviewer(t, agyRec)
		if len(x.f.All) != 0 {
			t.Fatal("precondition: no pane")
		}
		var pl placer
		out, ok := x.restart(launch.Request{Agent: "pj-rev", Place: pl.at(x, "w1:p9", "")})
		if !ok {
			t.Fatal(out)
		}
		restarted(t, last(out), "pj-rev", "w1:p9", "conv-1")
		contains(t, x.startCall(), "--conversation conv-1", "the recorded conversation resumed")
	})
	t.Run("a claude record, agy live in the pane: agy's session", func(t *testing.T) {
		t.Parallel()
		x := agyReviewer(t, claudeRec)
		x.agent("w1:p7", "pj-rev", "agy", "idle").Session = "conv-live"
		out, ok := x.restart(launch.Request{Agent: "pj-rev"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "session sid-c is claude's, the type neon is agy's: resuming the terminal's conv-live", "said")
		contains(t, x.startCall(), "--conversation conv-live", "agy's live session resumed")
		lacks(t, x.startCall(), "sid-c", "the claude record not resumed")
	})
	t.Run("a claude record, no agy session: a new one", func(t *testing.T) {
		t.Parallel()
		x := agyReviewer(t, claudeRec)
		x.shell("w1:p7", "pj-rev", "")
		out, ok := x.restart(launch.Request{Agent: "pj-rev"})
		if !ok {
			t.Fatal(out)
		}
		contains(t, out, "session sid-c is claude's, the type neon is agy's: new session", "said")
		restarted(t, last(out), "pj-rev", "w1:p7", "new")
		if call := x.startCall(); strings.Contains(call, "--conversation") || !strings.Contains(call, "kind=agy") {
			t.Errorf("start %q, want a new agy session", call)
		}
	})
}
