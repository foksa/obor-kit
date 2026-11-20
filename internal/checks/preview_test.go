package checks

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
)

// MergePreview's decisions on fakeGit: the lane's dirt (Dirt: an ignored file is none) is snapshotted on its HEAD and
// that commit merged onto the target's head; a lane without dirt merges its HEAD. No write is made, no ref moves.
func TestMergePreviewDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		status    []string
		conflicts []string // the paths merge-tree conflicts on for the lane's merged commit
		snapshot  []string // the paths the snapshot took (nil: no snapshot, the lane's HEAD merged)
		want      []port.PreviewConflict
	}{
		{name: "clean", status: []string{" M shared.txt"}, snapshot: []string{"shared.txt"}},
		{name: "one conflicted file", status: []string{" M shared.txt"}, conflicts: []string{"shared.txt"},
			snapshot: []string{"shared.txt"},
			want: []port.PreviewConflict{{Path: "shared.txt", Messages: []string{"Auto-merging shared.txt",
				"CONFLICT (content): Merge conflict in shared.txt"}}}},
		{name: "an untracked new file included", status: []string{"?? new.txt"}, conflicts: []string{"new.txt"},
			snapshot: []string{"new.txt"},
			want: []port.PreviewConflict{{Path: "new.txt", Messages: []string{"Auto-merging new.txt",
				"CONFLICT (content): Merge conflict in new.txt"}}}},
		{name: "an ignored file left out", status: []string{"?? new.txt", "!! build.log"}, snapshot: []string{"new.txt"}},
		{name: "only an ignored file: the HEAD merged", status: []string{"!! build.log"}},
		{name: "a lane with no changes: clean", status: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newFk(t)
			k.move("main edit")
			head := k.laneCommit("lane work")
			k.g.status[k.lane] = tc.status
			refs := maps.Clone(k.g.refs)
			work := head
			if tc.snapshot != nil {
				work = commitSha(append([]string{"snapshot", head}, tc.snapshot...)...)
			}
			if len(tc.conflicts) > 0 {
				k.g.conflicts[work] = tc.conflicts
			}
			p, err := k.s.MergePreview(ctx, "L-01")
			if err != nil {
				t.Fatalf("MergePreview: %v", err)
			}
			if p.Target != "main" || p.TargetSHA != k.main() {
				t.Errorf("target %s %s; want main at its head %s", p.Target, p.TargetSHA, k.main())
			}
			if !reflect.DeepEqual(p.Conflicts, tc.want) {
				t.Errorf("conflicts %+v; want %+v", p.Conflicts, tc.want)
			}
			merged := slices.ContainsFunc(k.g.calls, func(c string) bool { return c == "MergeTree "+k.main()+" "+work })
			if !merged {
				t.Errorf("calls %q; want merge-tree of the target's head and %s", k.g.calls, work)
			}
			if got := k.g.snapshots[work]; tc.snapshot != nil && !slices.Equal(got, tc.snapshot) {
				t.Errorf("the snapshot took %q; want %q", got, tc.snapshot)
			}
			if snap := slices.ContainsFunc(k.g.calls, func(c string) bool { return strings.HasPrefix(c, "Snapshot ") }); snap != (tc.snapshot != nil) {
				t.Errorf("calls %q: a snapshot %v; want %v", k.g.calls, snap, tc.snapshot != nil)
			}
			if w := k.g.writeCalls(); len(w) > 0 {
				t.Errorf("writes %q; want none", w)
			}
			if !maps.Equal(k.g.refs, refs) {
				t.Errorf("refs %v after the preview; want them unchanged %v", k.g.refs, refs)
			}
			if !slices.Equal(k.g.status[k.lane], tc.status) {
				t.Errorf("the lane's status %q after the preview; want it unchanged %q", k.g.status[k.lane], tc.status)
			}
		})
	}
}

// A lane with no dir is refused; merge-tree's message lines go to the path they name as a word, never to a path that
// is only a part of another (a.txt in data.txt).
func TestMergePreviewEdges(t *testing.T) {
	t.Parallel()
	k := newFk(t)
	if _, err := k.s.MergePreview(ctx, "L-09"); err == nil || err.Error() != "no lane L-09" {
		t.Errorf("an unknown lane: %v; want the refusal naming it", err)
	}
	out := "tree\na.txt\ndata.txt\n\nAuto-merging data.txt\nCONFLICT (content): Merge conflict in data.txt\n" +
		"CONFLICT (modify/delete): a.txt deleted in HEAD and modified in work.  Version work of a.txt left in tree."
	got := mergeTreeConflicts(out)
	want := []port.PreviewConflict{
		{Path: "a.txt", Messages: []string{"CONFLICT (modify/delete): a.txt deleted in HEAD and modified in work.  " +
			"Version work of a.txt left in tree."}},
		{Path: "data.txt", Messages: []string{"Auto-merging data.txt", "CONFLICT (content): Merge conflict in data.txt"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conflicts %+v; want %+v", got, want)
	}
}
