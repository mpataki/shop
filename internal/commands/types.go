package commands

import (
	"encoding/json"
	"time"

	"github.com/mpataki/shop/internal/events"
)

// CommandType identifies the kind of command.
type CommandType string

const (
	CmdStartRun          CommandType = "StartRun"
	CmdExecuteWorkflow   CommandType = "ExecuteWorkflow"
	CmdExecuteAgent      CommandType = "ExecuteAgent"
	CmdReportSignal      CommandType = "ReportSignal"
	CmdPauseForHuman     CommandType = "PauseForHuman"
	CmdProvideHumanInput CommandType = "ProvideHumanInput"
	CmdResumeRun         CommandType = "ResumeRun"
	CmdKillRun           CommandType = "KillRun"
	CmdStopRun           CommandType = "StopRun"
	CmdDeleteRun         CommandType = "DeleteRun"
)

// CommandStatus represents the processing state of a command.
type CommandStatus string

const (
	CommandPending    CommandStatus = "pending"
	CommandProcessing CommandStatus = "processing"
	CommandProcessed  CommandStatus = "processed"
	CommandFailed     CommandStatus = "failed"
)

// Command represents a submitted command.
type Command struct {
	ID          string
	RunID       int64
	Type        CommandType
	Payload     json.RawMessage
	Status      CommandStatus
	Error       string
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

// NewCommand creates a Command with a JSON-encoded payload and a generated ID.
func NewCommand(runID int64, cmdType CommandType, payload any) (Command, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Command{}, err
	}
	return Command{
		ID:      events.NewID(),
		RunID:   runID,
		Type:    cmdType,
		Payload: data,
		Status:  CommandPending,
	}, nil
}

// ── Payload structs ───────────────────────────────────────────────────────────

type StartRunPayload struct {
	WorkflowPath string `json:"workflow_path"`
	WorkflowName string `json:"workflow_name"`
	InitialPrompt string `json:"initial_prompt"`
	SourceRepo   string `json:"source_repo"`
}

type ExecuteWorkflowPayload struct{}

type ExecuteAgentPayload struct {
	AgentName string `json:"agent_name"`
	CallIndex int    `json:"call_index"`
	Prompt    string `json:"prompt,omitempty"`
	Model     string `json:"model,omitempty"`
}

type ReportSignalPayload struct {
	CallIndex int            `json:"call_index"`
	Status    string         `json:"status"`
	Summary   string         `json:"summary,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Signal    map[string]any `json:"signal,omitempty"`
}

type PauseForHumanPayload struct {
	CallIndex int    `json:"call_index"`
	Message   string `json:"message"`
}

type ProvideHumanInputPayload struct {
	CallIndex int            `json:"call_index"`
	Signal    map[string]any `json:"signal"`
}

type ResumeRunPayload struct{}

type KillRunPayload struct{}

type StopRunPayload struct {
	Reason string `json:"reason,omitempty"`
}

type DeleteRunPayload struct{}
