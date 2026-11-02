package app

import "obor/internal/app/port"

// The ports moved to internal/app/port (T-690, app split 1/5); these aliases keep package app and its tests compiling
// unchanged. Removed in a later task.
type (
	Agents         = port.Agents
	Started        = port.Started
	Launch         = port.Launch
	Typer          = port.Typer
	PaneControl    = port.PaneControl
	Rect           = port.Rect
	Plugin         = port.Plugin
	AgentEnv       = port.AgentEnv
	Start          = port.Start
	StartError     = port.StartError
	Starter        = port.Starter
	AgentInfo      = port.AgentInfo
	PaneInfo       = port.PaneInfo
	TabInfo        = port.TabInfo
	Process        = port.Process
	ProcessInfo    = port.ProcessInfo
	Candidate      = port.Candidate
	Scratch        = port.Scratch
	GateInput      = port.GateInput
	GateOutcome    = port.GateOutcome
	PublishOutcome = port.PublishOutcome
	Refused        = port.Refused
	LaneSetup      = port.LaneSetup
	Prepared       = port.Prepared
	FreeInput      = port.FreeInput
	PrepareInput   = port.PrepareInput
	Pane           = port.Pane
	Snapshot       = port.Snapshot
	Event          = port.Event
	Stream         = port.Stream
	Panes          = port.Panes
	Lane           = port.Lane
	Lanes          = port.Lanes
)

const (
	InlineDiff      = port.InlineDiff
	InputLane       = port.InputLane
	InputResolved   = port.InputResolved
	InputMoved      = port.InputMoved
	OutcomeGreen    = port.OutcomeGreen
	OutcomeRed      = port.OutcomeRed
	OutcomeConflict = port.OutcomeConflict
	OutcomeFail     = port.OutcomeFail
	PublishMerged   = port.PublishMerged
	PublishAlready  = port.PublishAlready
	PublishMoved    = port.PublishMoved
	PublishHold     = port.PublishHold
	EventStatus     = port.EventStatus
	EventCreated    = port.EventCreated
	EventClosed     = port.EventClosed
	EventExited     = port.EventExited
)

// The error probes are functions, so they are variables here.
var (
	NotFound   = port.NotFound
	NotRunning = port.NotRunning
	ErrorCode  = port.ErrorCode
)
