package app

import "obor/internal/app/wire"

// The socket's client half moved to internal/app/wire (T-692, app split 3/5): the methods, Call, the answers, the
// status types, Ask, Verdict and the Backup client. These aliases keep package app and its tests compiling unchanged.
// Removed in a later task.
type (
	Call                = wire.Call
	JobReply            = wire.JobReply
	Status              = wire.Status
	ProjectStatus       = wire.ProjectStatus
	RunningStep         = wire.RunningStep
	PendingStep         = wire.PendingStep
	ReviewRequestStatus = wire.ReviewRequestStatus
	BriefStatus         = wire.BriefStatus
	WatchStatus         = wire.WatchStatus
	TaskStatus          = wire.TaskStatus
	AgentStatus         = wire.AgentStatus
	ScratchStatus       = wire.ScratchStatus
	ScratchWait         = wire.ScratchWait
	MessageStatus       = wire.MessageStatus
	request             = wire.Request
	answer              = wire.Answer
)

var (
	Ask              = wire.Ask
	Verdict          = wire.Verdict
	SocketPath       = wire.SocketPath
	NewCallID        = wire.NewCallID
	ResumeArgs       = wire.ResumeArgs
	Backup           = wire.Backup
	BackupDir        = wire.BackupDir
	pruneStopFailure = wire.PruneStopFailure
)

const (
	backupKeep = wire.BackupKeep

	MethodAccept   = wire.MethodAccept
	MethodSendback = wire.MethodSendback
	MethodReject   = wire.MethodReject
	MethodResolved = wire.MethodResolved
	MethodHold     = wire.MethodHold
	MethodDrop     = wire.MethodDrop
	MethodResume   = wire.MethodResume
	MethodAnswer   = wire.MethodAnswer
	MethodDispatch = wire.MethodDispatch
	MethodWatch    = wire.MethodWatch
	MethodPause    = wire.MethodPause
	MethodReview   = wire.MethodReview
	MethodSession  = wire.MethodSession
	MethodSpecSet  = wire.MethodSpecSet
	MethodBackup   = wire.MethodBackup
	MethodSend     = wire.MethodSend
	MethodBrief    = wire.MethodBrief
	MethodStopped  = wire.MethodStopped
	MethodTurn     = wire.MethodTurn
	MethodJobNext  = wire.MethodJobNext
	MethodTaskDone = wire.MethodTaskDone

	ResumeRegate   = wire.ResumeRegate
	ResumeReprompt = wire.ResumeReprompt
	ResumeRetry    = wire.ResumeRetry

	ScratchGating     = wire.ScratchGating
	ScratchPublishing = wire.ScratchPublishing
	ScratchConflict   = wire.ScratchConflict
	ScratchResolving  = wire.ScratchResolving
	ScratchResolved   = wire.ScratchResolved
	ScratchHeld       = wire.ScratchHeld
)
