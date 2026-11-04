package app

// awaitsTaskDone: mt's attempt is its worker's and no task_done submitted it yet: dispatched, working (the work job or
// a fix job), also while a needs-you holds either.
func (mt *mtask) awaitsTaskDone() bool {
	return mt.state == stDispatched || mt.state == stWorking
}

// inLoop: mt (nil: none) is submitted and not ended: the merge loop holds it (gate, review, publish, a needs-you of
// those), not its worker. Merged or dropped while a lane still holds the task, the lane's watchdog is back.
// A drive (T-751) is in the loop too: no watchdog watches the owner's agent.
func (mt *mtask) inLoop() bool {
	return mt != nil && !mt.awaitsTaskDone() && mt.state != stMerged && mt.state != stDropped
}
