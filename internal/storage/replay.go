package storage

import "github.com/mpataki/shop/internal/models"

// ReplayEntry is the cached result for a single call_index, derived from the event log.
type ReplayEntry struct {
	AgentName string // for determinism check
	// Signal is set for agent_completed events (run() calls).
	Signal map[string]any
	// PauseResult is set for checkpoint_resumed events (pause() calls).
	PauseResult *models.CheckpointResumedPayload
}

// BuildReplayCache scans the event log and returns a map of call_index → result
// for all completed call sites. The runtime uses this to skip re-executing agents
// that already have a result on resume.
//
// Only agent_completed and checkpoint_resumed events contribute to the cache —
// they are the authoritative terminal events for run() and pause() respectively.
// If multiple agent_completed events exist for the same call_index (shouldn't happen
// in normal operation), the last one wins.
func BuildReplayCache(events []*models.WorkflowEvent) map[int]*ReplayEntry {
	cache := make(map[int]*ReplayEntry)

	for _, e := range events {
		if e.CallIndex == nil {
			continue
		}
		idx := *e.CallIndex

		switch e.Type {
		case models.WFEventAgentCompleted:
			p := e.Payload.(models.AgentCompletedPayload)
			cache[idx] = &ReplayEntry{AgentName: e.AgentName, Signal: p.Signal}

		case models.WFEventCheckpointResumed:
			p := e.Payload.(models.CheckpointResumedPayload)
			cache[idx] = &ReplayEntry{AgentName: e.AgentName, PauseResult: &p}
		}
	}

	return cache
}
