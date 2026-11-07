package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-942: the reviewer's finished line names its review file and the file's Result line; other roles keep
// "[obor] <agent> finished (briefed <age> ago)" (messages_test.go TestBriefFinishedLineToTheLead and the advisor's).

// finishedLines are the log's finished lines (messages that end an agent) of agent, in order.
func finishedLines(t *testing.T, root, agent string) []string {
	t.Helper()
	var out []string
	for _, rec := range readLog(t, root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Ends == agent {
			out = append(out, m.Line)
		}
	}
	return out
}

// writeReview writes reviews/name.md in the rig's main checkout.
func writeReview(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reviews", name+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewerFinishedLineNamesReviewAndResult(t *testing.T) {
	t.Parallel()
	t.Run("a review file with a Result line", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		writeReview(t, r.p.root, "RV-5", "# RV-5\n\nResult: **changes requested** · 2b3c4d5 · T-a,T-b\n\n## Findings\n")
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5.md — changes requested · 2b3c4d5 · T-a,T-b"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("a verify file", func(t *testing.T) {
		t.Parallel()
		r := fixRig(t)
		r.merged("T-a")
		r.merged("T-b")
		r.verifyPass()
		if n := len(verifyBriefs(t, r.p.root)); n != 1 {
			t.Fatalf("precondition: %d verify briefs, want 1", n)
		}
		writeReview(t, r.p.root, "RV-5-verify", "# RV-5 verify\n\n**Result:** approved · all fixed\n")
		r.finish()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5-verify.md — approved · all fixed"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("no file", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5.md (no Result line)"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("a file with no Result line", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		writeReview(t, r.p.root, "RV-5", "# RV-5\n\nno result yet\n")
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		want := "[obor] pj-rev finished RV-5: reviews/RV-5.md (no Result line)"
		if len(got) != 1 || got[0] != want {
			t.Errorf("finished lines %q, want [%q]", got, want)
		}
	})
	t.Run("a long result is cut", func(t *testing.T) {
		t.Parallel()
		r := newReviewRig(t)
		writeReview(t, r.p.root, "RV-5", "Result: **approved** "+strings.Repeat("x", 300)+"\n")
		r.started()
		got := finishedLines(t, r.p.root, "pj-rev")
		if len(got) != 1 {
			t.Fatalf("finished lines %q, want one", got)
		}
		_, res, ok := strings.Cut(got[0], " — ")
		if !ok || !strings.HasPrefix(res, "approved xxx") || len([]rune(res)) > resultWidth+1 {
			t.Errorf("result %q (%d runes), want it cut to about %d", res, len([]rune(res)), resultWidth)
		}
	})
}
