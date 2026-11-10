package port

import "obor/internal/store"

// FreeState is a lane's state by the one rule that decides free (architecture-target §3), from serve's facts
// and the lane's own git, never the main checkout's history: row is the lane's row in the lanes table (the zero row:
// the lane has none; nil: the table could not be read), head the lane's HEAD (full sha), dirty whether the lane has
// uncommitted changes. The row's Landed is serve's projection of its task's merged or dropped state (a task merged by
// hand included once serve's merge-evidence read recorded it); its Head the head serve recorded for the task's last
// submission (the lane commit the gate merged). A landed task frees its lane only while the lane's HEAD is
// that head: a commit after the submit pinned it (a driver after obor land) is work no merge holds. A row with no
// recorded head (no submission: a hand merge; a table an older serve wrote) is free only when merged (nil: none)
// proves the lane's HEAD merged, read only then.
//
//	unknown     the table could not be read: never free
//	working     the row holds a task that has not landed, or whose lane HEAD is not what landed
//	dirty-free  no task holds the lane (no row, or its task landed at the lane's HEAD), with uncommitted changes: never
//	            free
//	free        no task holds the lane and nothing is uncommitted
func FreeState(row *store.LaneRow, head string, dirty bool, merged func() bool) string {
	switch {
	case row == nil:
		return LaneUnknown
	case row.Task != "" && !row.Landed:
		return LaneWorking
	case row.Task != "" && row.Head == "" && (merged == nil || !merged()):
		return LaneWorking
	case row.Task != "" && row.Head != "" && head != row.Head:
		return LaneWorking
	case dirty:
		return LaneDirtyFree
	}
	return LaneFree
}
