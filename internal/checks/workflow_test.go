package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// fakeReview is a workflow's review git whose Detach is the fake's own, so no test here runs real git; its other
// methods are the port's (unused here).
type fakeReview struct {
	port.Review
	g *fakeGit
}

func (r fakeReview) Detach(_ context.Context, dir, sha string) error { return r.g.Detach(dir, sha) }

// TestDetachRunsLaneSetup: a role worktree serve moves to a head gets what a lane gets before its test: lanes.ready,
// lanes.setup when it is red, lanes.ready again. lanes.setup that stays red, and a ready still red after it, fail the
// move with LaneSetup's own error naming the role; no lanes.ready runs nothing. The checkout happens in every case,
// so the commands of a red one ran in the worktree the move put there.
func TestDetachRunsLaneSetup(t *testing.T) {
	t.Parallel()
	const head = "main0" // the fake model's main sha
	for _, c := range []struct {
		name  string
		edits []func(map[string]any)
		ran   bool   // lanes.setup ran: its marker file is in the worktree
		want  string // the move's error ("" : it passed)
	}{
		{
			name:  "ready red, setup fixes it",
			edits: []func(map[string]any){cmd("lane_ready", "test -f ready"), cmd("lane_setup", "touch ready")},
			ran:   true,
		},
		{
			name: "setup stays red",
			edits: []func(map[string]any){cmd("lane_ready", "test -f ready"),
				cmd("lane_setup", "touch ready; echo install broke; exit 3")},
			ran:  true,
			want: "lane_setup in reviewer: rc 3: install broke",
		},
		{
			name: "ready still red after setup",
			edits: []func(map[string]any){cmd("lane_ready", "false"),
				cmd("lane_setup", "touch ready")},
			ran:  true,
			want: "lane_setup in reviewer: ran, but lanes.ready still fails",
		},
		{
			name:  "no lanes.ready",
			edits: []func(map[string]any){cmd("lane_setup", "touch ready")},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newFk(t, c.edits...)
			dir := k.s.Cfg.RoleWorktree("reviewer")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if k.g.headOf(dir) == head {
				t.Fatal("precondition: the role worktree is not at the head the move brings")
			}
			// serve hands the workflow out as a port.Review to a review's start and a verify: the move is that call's.
			var review port.Review = NewScratchFF(k.s, fakeReview{g: k.g})
			err := review.Detach(context.Background(), dir, head)
			if got := k.g.headOf(dir); got != head {
				t.Errorf("reviewer worktree at %q, want the head %q", got, head)
			}
			if c.want == "" && err != nil {
				t.Fatalf("detach: %v; want the move to pass", err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("detach: %v; want %q", err, c.want)
			}
			_, statErr := os.Stat(filepath.Join(dir, "ready"))
			if c.ran && statErr != nil {
				t.Errorf("lanes.setup did not run in the worktree: %v", statErr)
			}
			if !c.ran && statErr == nil {
				t.Error("lanes.setup ran with no lanes.ready configured")
			}
		})
	}
}
