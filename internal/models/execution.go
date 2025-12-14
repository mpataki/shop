package models

import "time"

type ExecStatus string

const (
	ExecStatusPending  ExecStatus = "pending"
	ExecStatusRunning  ExecStatus = "running"
	ExecStatusComplete ExecStatus = "complete"
	ExecStatusFailed   ExecStatus = "failed"
)

type Execution struct {
	ID              int64
	RunID           int64
	AgentName       string
	ClaudeSessionID string
	Status          ExecStatus
	ExitCode        *int
	StartedAt       *time.Time
	CompletedAt     *time.Time
	OutputSignal    map[string]any
	SequenceNum     int
	PID             *int
	CallIndex       int    // position in Lua script execution (for Lua workflows)
	Prompt          string // prompt passed to this specific run() call
}
