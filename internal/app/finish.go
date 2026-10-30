package app

// awaitsTaskDone: mt's attempt is its worker's and no task_done submitted it yet: dispatched, working (the work job or
// a fix job), or a needs-you either of those left.
func (mt *mtask) awaitsTaskDone() bool {
	st := mt.state
	if st == stNeedsYou {
		st = mt.left
	}
	return st == stDispatched || st == stWorking
}

// inLoop: mt (nil: none) is submitted and not ended: the merge loop holds it (gate, review, publish, a needs-you of
// those), not its worker. Merged or dropped while a lane still holds the task, the lane's watchdog is back.
func (mt *mtask) inLoop() bool {
	return mt != nil && !mt.awaitsTaskDone() && mt.state != stMerged && mt.state != stDropped
}
