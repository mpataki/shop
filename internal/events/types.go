package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// EventType identifies the kind of event in the event store.
type EventType string

const (
	// Run lifecycle
	EventRunStarted      EventType = "RunStarted"
	EventRunResumed      EventType = "RunResumed"
	EventRunCompleted    EventType = "RunCompleted"
	EventRunFailed       EventType = "RunFailed"
	EventRunStuck        EventType = "RunStuck"
	EventRunWaitingHuman EventType = "RunWaitingHuman"
	EventRunKilled       EventType = "RunKilled"
	EventRunStopped      EventType = "RunStopped"
	EventRunDeleted      EventType = "RunDeleted"

	// Agent lifecycle
	EventAgentStarted   EventType = "AgentStarted"
	EventAgentCompleted EventType = "AgentCompleted"
	EventAgentFailed    EventType = "AgentFailed"
	EventSignalReceived EventType = "SignalReceived"

	// Checkpoint lifecycle
	EventCheckpointStarted   EventType = "CheckpointStarted"
	EventCheckpointCompleted EventType = "CheckpointCompleted"
	EventHumanInputReceived  EventType = "HumanInputReceived"

	// Workflow runtime
	EventReplayInvalidated EventType = "ReplayInvalidated"
	EventLogMessage        EventType = "LogMessage"
)

// Event is an immutable fact stored in the event log.
type Event struct {
	ID        int64
	RunID     int64
	EventType EventType
	Payload   json.RawMessage
	Version   int
	CreatedAt time.Time
}

// NewEvent creates an Event with a JSON-encoded payload.
func NewEvent(runID int64, eventType EventType, payload any) (Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload: %w", err)
	}
	return Event{
		RunID:     runID,
		EventType: eventType,
		Payload:   data,
	}, nil
}

// MustNewEvent is like NewEvent but panics on error (for use in tests).
func MustNewEvent(runID int64, eventType EventType, payload any) Event {
	e, err := NewEvent(runID, eventType, payload)
	if err != nil {
		panic(err)
	}
	return e
}

// DecodePayload unmarshals the event payload into a typed struct.
func DecodePayload[T any](e Event) (T, error) {
	var t T
	err := json.Unmarshal(e.Payload, &t)
	return t, err
}

// NewID generates a new UUID string for command IDs.
func NewID() string {
	return uuid.New().String()
}

// ── Payload structs ───────────────────────────────────────────────────────────

type RunStartedPayload struct {
	WorkflowPath  string `json:"workflow_path"`
	WorkflowName  string `json:"workflow_name"`
	InitialPrompt string `json:"initial_prompt"`
	WorkspacePath string `json:"workspace_path"`
}

type RunResumedPayload struct{}

type RunCompletedPayload struct{}

type RunFailedPayload struct {
	Error string `json:"error"`
}

type RunStuckPayload struct {
	Reason string `json:"reason"`
}

type RunWaitingHumanPayload struct {
	Reason    string `json:"reason"`
	CallIndex int    `json:"call_index"`
	SessionID string `json:"session_id"`
}

type RunKilledPayload struct{}

type RunStoppedPayload struct {
	Reason string `json:"reason"`
}

type RunDeletedPayload struct{}

type AgentStartedPayload struct {
	AgentName string `json:"agent_name"`
	CallIndex int    `json:"call_index"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Prompt    string `json:"prompt,omitempty"`
	Model     string `json:"model,omitempty"`
}

type AgentCompletedPayload struct {
	AgentName string         `json:"agent_name"`
	CallIndex int            `json:"call_index"`
	Signal    map[string]any `json:"signal"`
}

type AgentFailedPayload struct {
	AgentName string `json:"agent_name"`
	CallIndex int    `json:"call_index"`
	Error     string `json:"error"`
	ExitCode  int    `json:"exit_code,omitempty"`
}

type SignalReceivedPayload struct {
	CallIndex int            `json:"call_index"`
	Signal    map[string]any `json:"signal"`
}

type CheckpointStartedPayload struct {
	CallIndex int    `json:"call_index"`
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
}

type CheckpointCompletedPayload struct {
	CallIndex int            `json:"call_index"`
	Signal    map[string]any `json:"signal"`
}

type HumanInputReceivedPayload struct {
	CallIndex int            `json:"call_index"`
	Signal    map[string]any `json:"signal"`
}

type ReplayInvalidatedPayload struct {
	FromCallIndex int    `json:"from_call_index"`
	Reason        string `json:"reason"`
}

type LogMessagePayload struct {
	Message string `json:"message"`
}
