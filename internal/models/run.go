package models

import "time"

type RunStatus string

const (
	RunStatusPending      RunStatus = "pending"
	RunStatusRunning      RunStatus = "running"
	RunStatusComplete     RunStatus = "complete"
	RunStatusFailed       RunStatus = "failed"
	RunStatusStuck        RunStatus = "stuck"
	RunStatusWaitingHuman RunStatus = "waiting_human"
)

type Run struct {
	ID               int64
	CreatedAt        time.Time
	CompletedAt      *time.Time
	InitialPrompt    string
	WorkflowName     string
	WorkspacePath    string
	Status           RunStatus
	CurrentAgent     string
	WorkflowPath     string // path to .lua workflow file
	Error            string // error message if run failed
	WaitingReason    string // reason for NEEDS_HUMAN or pause() (when Status == waiting_human)
	WaitingSessionID string // Claude session ID to resume (when Status == waiting_human)
}
