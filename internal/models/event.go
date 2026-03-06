package models

type EventType int

const (
	EventRunStatusChanged EventType = iota
	EventRunDeleted
	EventAgentStarted
	EventAgentCompleted
)

type Event struct {
	Type   EventType
	RunID  int64
	Agent  string
	Status RunStatus
}
