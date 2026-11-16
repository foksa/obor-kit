package port

import "obor/internal/store"

// FreeState is a lane's state by the one rule that decides free (architecture-target §3), from serve's facts
// and the lane's own git, never the main checkout's history: row is the lane's row in the lanes table (the zero row:
// the lane has none; nil: the table could not be read), head the lane's HEAD (full sha), dirty whether the lane has
// uncommitted changes. The row's Landed is serve's projection of its task's merged or dropped state (a task merged by
// hand included once serve's merge-evidence read recorded it); its Head the head serve recorded for the task's last
// submission (the lane commit the gate merged). A landed task frees its lane when the lane's HEAD is that head, else
// when merged proves the lane's HEAD is in the target branch's history: a commit after the submit (a driver after obor
// land) is in neither, so it still holds the lane. A row with no recorded head (no submission: a hand merge, or a
// table that names none) takes the same rule: its HEAD differs from none, so merged (nil: none) decides.
//
//	unknown     the table could not be read: never free
//	working     the row holds a task that has not landed, or whose lane HEAD is neither what landed nor in the target's
//	            history
//	dirty-free  no task holds the lane (no row, or its task landed and its HEAD passes the rule above), with
//	            uncommitted changes: never free
//	free        no task holds the lane and nothing is uncommitted
func FreeState(row *store.LaneRow, head string, dirty bool, merged func() bool) string {
	switch {
	case row == nil:
		return LaneUnknown
	case row.Task != "" && !row.Landed:
		return LaneWorking
	case row.Task != "" && head != row.Head && (merged == nil || !merged()):
		return LaneWorking
	case dirty:
		return LaneDirtyFree
	}
	return LaneFree
}
