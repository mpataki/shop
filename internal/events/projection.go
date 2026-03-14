package events

import "time"

// RunStatus represents the current status of a run, derived from events.
type RunStatus string

const (
	RunStatusPending      RunStatus = "pending"
	RunStatusRunning      RunStatus = "running"
	RunStatusComplete     RunStatus = "complete"
	RunStatusFailed       RunStatus = "failed"
	RunStatusStuck        RunStatus = "stuck"
	RunStatusWaitingHuman RunStatus = "waiting_human"
	RunStatusKilled       RunStatus = "killed"
	RunStatusDeleted      RunStatus = "deleted"
)

// IsTerminal returns true if the status represents a terminal state.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunStatusComplete, RunStatusFailed, RunStatusStuck, RunStatusKilled, RunStatusDeleted:
		return true
	}
	return false
}

// ExecStatus represents the status of an agent execution.
type ExecStatus string

const (
	ExecStatusStarted      ExecStatus = "started"
	ExecStatusCompleted    ExecStatus = "completed"
	ExecStatusFailed       ExecStatus = "failed"
	ExecStatusWaitingHuman ExecStatus = "waiting_human"
)

// RunState is the in-memory projection of a run, built by folding events.
type RunState struct {
	ID        int64
	CreatedAt time.Time
	Version   int

	// Derived from events
	Status           RunStatus
	WorkflowPath     string
	WorkflowName     string
	InitialPrompt    string
	WorkspacePath    string
	Error            string
	WaitingReason    string
	WaitingSessionID string
	CurrentAgent     string

	// Execution history
	Executions []ExecutionState

	// Log
	LogMessages []LogEntry
}

// ExecutionState represents a single agent execution within a run.
type ExecutionState struct {
	AgentName   string
	CallIndex   int
	SessionID   string
	PID         int
	Status      ExecStatus
	Signal      map[string]any
	Prompt      string
	Model       string
	StartedAt   time.Time
	CompletedAt *time.Time
}

// LogEntry represents a log message emitted during workflow execution.
type LogEntry struct {
	Message   string
	CreatedAt time.Time
}

// ProjectRun folds a sequence of events into a RunState.
func ProjectRun(id int64, createdAt time.Time, eventList []Event) *RunState {
	state := &RunState{
		ID:        id,
		CreatedAt: createdAt,
		Status:    RunStatusPending,
	}

	for _, e := range eventList {
		state.Version = e.Version
		applyEvent(state, e)
	}

	return state
}

func applyEvent(state *RunState, e Event) {
	switch e.EventType {
	case EventRunStarted:
		p, _ := DecodePayload[RunStartedPayload](e)
		state.Status = RunStatusRunning
		state.WorkflowPath = p.WorkflowPath
		state.WorkflowName = p.WorkflowName
		state.InitialPrompt = p.InitialPrompt
		state.WorkspacePath = p.WorkspacePath

	case EventRunResumed:
		state.Status = RunStatusRunning
		state.WaitingReason = ""
		state.WaitingSessionID = ""

	case EventRunCompleted:
		state.Status = RunStatusComplete
		state.CurrentAgent = ""

	case EventRunFailed:
		p, _ := DecodePayload[RunFailedPayload](e)
		state.Status = RunStatusFailed
		state.Error = p.Error
		state.CurrentAgent = ""

	case EventRunStuck:
		p, _ := DecodePayload[RunStuckPayload](e)
		state.Status = RunStatusStuck
		state.WaitingReason = p.Reason
		state.CurrentAgent = ""

	case EventRunWaitingHuman:
		p, _ := DecodePayload[RunWaitingHumanPayload](e)
		state.Status = RunStatusWaitingHuman
		state.WaitingReason = p.Reason
		state.WaitingSessionID = p.SessionID
		// Update the execution status at this call_index
		if exec := getExecution(state, p.CallIndex); exec != nil {
			exec.Status = ExecStatusWaitingHuman
		}

	case EventRunKilled:
		state.Status = RunStatusKilled
		state.CurrentAgent = ""

	case EventRunStopped:
		p, _ := DecodePayload[RunStoppedPayload](e)
		state.Status = RunStatusStuck
		state.WaitingReason = p.Reason
		state.WaitingSessionID = ""
		state.CurrentAgent = ""

	case EventRunDeleted:
		state.Status = RunStatusDeleted

	case EventAgentStarted:
		p, _ := DecodePayload[AgentStartedPayload](e)
		state.CurrentAgent = p.AgentName
		state.Executions = append(state.Executions, ExecutionState{
			AgentName: p.AgentName,
			CallIndex: p.CallIndex,
			SessionID: p.SessionID,
			PID:       p.PID,
			Status:    ExecStatusStarted,
			Prompt:    p.Prompt,
			Model:     p.Model,
			StartedAt: e.CreatedAt,
		})

	case EventAgentCompleted:
		p, _ := DecodePayload[AgentCompletedPayload](e)
		if exec := getExecution(state, p.CallIndex); exec != nil {
			exec.Status = ExecStatusCompleted
			exec.Signal = p.Signal
			now := e.CreatedAt
			exec.CompletedAt = &now
		}
		state.CurrentAgent = ""

	case EventAgentFailed:
		p, _ := DecodePayload[AgentFailedPayload](e)
		if exec := getExecution(state, p.CallIndex); exec != nil {
			exec.Status = ExecStatusFailed
			now := e.CreatedAt
			exec.CompletedAt = &now
		}
		state.CurrentAgent = ""

	case EventSignalReceived:
		p, _ := DecodePayload[SignalReceivedPayload](e)
		if exec := getExecution(state, p.CallIndex); exec != nil {
			exec.Signal = p.Signal
		}

	case EventCheckpointStarted:
		p, _ := DecodePayload[CheckpointStartedPayload](e)
		state.CurrentAgent = "_checkpoint"
		state.Executions = append(state.Executions, ExecutionState{
			AgentName: "_checkpoint",
			CallIndex: p.CallIndex,
			SessionID: p.SessionID,
			Status:    ExecStatusStarted,
			Prompt:    p.Message,
			StartedAt: e.CreatedAt,
		})

	case EventCheckpointCompleted:
		p, _ := DecodePayload[CheckpointCompletedPayload](e)
		if exec := getExecution(state, p.CallIndex); exec != nil {
			exec.Status = ExecStatusCompleted
			exec.Signal = p.Signal
			now := e.CreatedAt
			exec.CompletedAt = &now
		}
		state.CurrentAgent = ""

	case EventHumanInputReceived:
		p, _ := DecodePayload[HumanInputReceivedPayload](e)
		if exec := getExecution(state, p.CallIndex); exec != nil {
			exec.Signal = p.Signal
			exec.Status = ExecStatusCompleted
			now := e.CreatedAt
			exec.CompletedAt = &now
		}

	case EventReplayInvalidated:
		// Informational; no projection change needed

	case EventLogMessage:
		p, _ := DecodePayload[LogMessagePayload](e)
		state.LogMessages = append(state.LogMessages, LogEntry{
			Message:   p.Message,
			CreatedAt: e.CreatedAt,
		})
	}
}

// getExecution returns a pointer to the execution at callIndex, or nil.
// Searches backwards since the most recent match is usually desired.
func getExecution(state *RunState, callIndex int) *ExecutionState {
	for i := len(state.Executions) - 1; i >= 0; i-- {
		if state.Executions[i].CallIndex == callIndex {
			return &state.Executions[i]
		}
	}
	return nil
}

// GetExecutionByCallIndex returns the execution at callIndex, or nil.
func (s *RunState) GetExecutionByCallIndex(callIndex int) *ExecutionState {
	return getExecution(s, callIndex)
}

// ActivePID returns the PID of the currently running agent, or 0.
func (s *RunState) ActivePID() int {
	for i := len(s.Executions) - 1; i >= 0; i-- {
		if s.Executions[i].Status == ExecStatusStarted && s.Executions[i].PID > 0 {
			return s.Executions[i].PID
		}
	}
	return 0
}
