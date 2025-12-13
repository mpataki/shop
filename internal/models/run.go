package models

import "time"

type RunStatus string

const (
	RunStatusPending  RunStatus = "pending"
	RunStatusRunning  RunStatus = "running"
	RunStatusComplete RunStatus = "complete"
	RunStatusFailed   RunStatus = "failed"
	RunStatusStuck    RunStatus = "stuck"
)

type Run struct {
	ID            int64
	CreatedAt     time.Time
	CompletedAt   *time.Time
	InitialPrompt string
	SpecName      string
	WorkspacePath string
	Status        RunStatus
	CurrentAgent  string
}
