package models

import "time"

// WorkflowEventType is the type of a workflow event in the append-only event log.
type WorkflowEventType string

const (
	// Run lifecycle
	WFEventRunStarted   WorkflowEventType = "run_started"
	WFEventRunCompleted WorkflowEventType = "run_completed"
	WFEventRunStuck     WorkflowEventType = "run_stuck"
	WFEventRunFailed    WorkflowEventType = "run_failed"

	// Agent execution (call_index scoped)
	WFEventAgentStarted   WorkflowEventType = "agent_started"
	WFEventAgentCompleted WorkflowEventType = "agent_completed" // source of truth for replay
	WFEventAgentFailed    WorkflowEventType = "agent_failed"

	// MCP audit (not used for replay)
	WFEventSignalReported WorkflowEventType = "signal_reported"

	// Human checkpoints (call_index scoped)
	WFEventCheckpointStarted WorkflowEventType = "checkpoint_started"
	WFEventCheckpointResumed WorkflowEventType = "checkpoint_resumed"

	// Lua log
	WFEventLogMessage WorkflowEventType = "log_message"
)

// EventPayload is a sealed interface — only types in this package implement it.
type EventPayload interface{ isEventPayload() }

// Run lifecycle payloads

type RunStartedPayload struct{}
type RunCompletedPayload struct{}
type RunStuckPayload struct{ Reason string }
type RunFailedPayload struct{ Error string }

// Agent payloads

type AgentStartedPayload struct {
	SessionID string
	PID       int
}

// AgentCompletedPayload carries the signal returned by the agent.
// This is the authoritative record used by BuildReplayCache.
type AgentCompletedPayload struct {
	Signal map[string]any
}

type AgentFailedPayload struct{ Reason string }

// MCP audit

type SignalReportedPayload struct {
	ExecutionID int64
	Signal      map[string]any
}

// Checkpoint payloads

type CheckpointStartedPayload struct{ Message string }

// CheckpointResumedPayload carries the pause() result written when a human resumes.
type CheckpointResumedPayload struct {
	Continue bool
	Reason   string
	Message  string
}

// Log

type LogMessagePayload struct{ Message string }

// Seal the interface.
func (RunStartedPayload) isEventPayload()        {}
func (RunCompletedPayload) isEventPayload()       {}
func (RunStuckPayload) isEventPayload()           {}
func (RunFailedPayload) isEventPayload()          {}
func (AgentStartedPayload) isEventPayload()       {}
func (AgentCompletedPayload) isEventPayload()     {}
func (AgentFailedPayload) isEventPayload()        {}
func (SignalReportedPayload) isEventPayload()     {}
func (CheckpointStartedPayload) isEventPayload()  {}
func (CheckpointResumedPayload) isEventPayload()  {}
func (LogMessagePayload) isEventPayload()         {}

// WorkflowEvent is an immutable record of something that happened during a workflow run.
// workflow_events is the append-only source of truth; executions is a derived cache.
type WorkflowEvent struct {
	ID        int64
	RunID     int64
	Type      WorkflowEventType
	CallIndex *int   // nil for run-level events
	AgentName string // empty for non-agent events
	Payload   EventPayload
	CreatedAt time.Time
}
