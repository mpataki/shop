package models

type EventType int

const (
	EventRunStatusChanged EventType = iota
	EventAgentStarted
	EventAgentCompleted
)

type Event struct {
	Type   EventType
	RunID  int64
	Agent  string
	Status RunStatus
}
