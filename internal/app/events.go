package app

import "obor/internal/app/event"

// The event log's records, steps, task keys and fact data moved to internal/app/event (T-691, app split 2/5); these
// aliases keep package app and its tests compiling unchanged. Removed in a later task.
type (
	Record     = event.Record
	Key        = event.Key
	Intent     = event.Intent
	FactData   = event.FactData
	Log        = event.Log
	Merge      = event.Merge
	Pause      = event.Pause
	Message    = event.Message
	mergedData = event.MergedData
)

var (
	OpenLog    = event.OpenLog
	ReadLog    = event.ReadLog
	DBFile     = event.DBFile
	merge      = event.MergeOf
	StepN      = event.StepN
	recordTime = event.RecordTime
)

const (
	KindFact   = event.KindFact
	KindIntent = event.KindIntent
	KindResult = event.KindResult

	ClassFact   = event.ClassFact
	ClassPrompt = event.ClassPrompt
	ClassStart  = event.ClassStart
	ClassGit    = event.ClassGit

	DiskTask    = event.DiskTask
	WatchTask   = event.WatchTask
	NightlyTask = event.NightlyTask
	PauseTask   = event.PauseTask
	LoopTask    = event.LoopTask
	ReviewTask  = event.ReviewTask
	MsgTask     = event.MsgTask
	ServeTask   = event.ServeTask
	RoleTask    = event.RoleTask

	CauseHeld = event.CauseHeld

	StepStalled  = event.StepStalled
	StepNeedsYou = event.StepNeedsYou
	StepResumed  = event.StepResumed

	StepAnswered = event.StepAnswered

	StepDispatched = event.StepDispatched
	StepWaiting    = event.StepWaiting
	StepPrepare    = event.StepPrepare
	StepStartWork  = event.StepStartWork
	StepWork       = event.StepWork

	StepWatch   = event.StepWatch
	StepUnwatch = event.StepUnwatch

	StepNightly = event.StepNightly

	StepPaused   = event.StepPaused
	StepUnpaused = event.StepUnpaused

	StepSubmitted  = event.StepSubmitted
	StepSubmit     = event.StepSubmit
	StepGate       = event.StepGate
	StepKept       = event.StepKept
	StepSendback   = event.StepSendback
	StepFix        = event.StepFix
	StepPublish    = event.StepPublish
	StepMoved      = event.StepMoved
	StepMerged     = event.StepMerged
	StepAfterMerge = event.StepAfterMerge
	StepStop       = event.StepStop
	StepUnpin      = event.StepUnpin
	StepDelivered  = event.StepDelivered
	StepDropped    = event.StepDropped
	StepLost       = event.StepLost
	StepUnblocked  = event.StepUnblocked
	StepFailed     = event.StepFailed

	StepReviewRequest = event.StepReviewRequest
	StepReviewRm      = event.StepReviewRm
	StepReviewFire    = event.StepReviewFire
	StepReviewStarted = event.StepReviewStarted

	StepMessage    = event.StepMessage
	StepBriefed    = event.StepBriefed
	StepWorked     = event.StepWorked
	StepSuperseded = event.StepSuperseded

	StepReview  = event.StepReview
	StepResolve = event.StepResolve

	StepServeRestart = event.StepServeRestart

	StepStopped = event.StepStopped
	StepTurned  = event.StepTurned
)
