package port

import "obor/internal/store"

// FreeState is a lane's state by the one rule that decides free (T-1012, architecture-target §3), from serve's facts
// and the lane's own dirty status, never the main checkout's history: row is the lane's row in the lanes table (the
// zero row: the lane has none; nil: the table could not be read), landed whether the row's task has landed (its
// Landed column, serve's projection of the task's merged or dropped state, a task merged by hand included once
// serve's merge-evidence read recorded it; or a caller's own reading of the same) and dirty whether the lane has
// uncommitted changes.
//
//	unknown     the table could not be read: never free (T-871)
//	working     the row holds a task that has not landed
//	dirty-free  no task holds the lane (no row, or its task landed), with uncommitted changes: never free (T-196)
//	free        no task holds the lane and nothing is uncommitted
func FreeState(row *store.LaneRow, landed, dirty bool) string {
	switch {
	case row == nil:
		return LaneUnknown
	case row.Task != "" && !landed:
		return LaneWorking
	case dirty:
		return LaneDirtyFree
	}
	return LaneFree
}
