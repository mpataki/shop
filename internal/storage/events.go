package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/mpataki/shop/internal/models"
)

// payloadEnvelope is the on-disk JSON format for workflow event payloads.
// The type discriminator lets us deserialize back to the right concrete struct.
type payloadEnvelope struct {
	Type    models.WorkflowEventType `json:"type"`
	Payload json.RawMessage          `json:"payload"`
}

// AppendWorkflowEvent inserts an immutable event into the workflow_events log.
func (s *Storage) AppendWorkflowEvent(e *models.WorkflowEvent) (int64, error) {
	inner, err := json.Marshal(e.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal payload: %w", err)
	}
	envelope, err := json.Marshal(payloadEnvelope{Type: e.Type, Payload: inner})
	if err != nil {
		return 0, fmt.Errorf("marshal envelope: %w", err)
	}

	result, err := s.db.Exec(
		`INSERT INTO workflow_events (run_id, event_type, call_index, agent_name, payload) VALUES (?, ?, ?, ?, ?)`,
		e.RunID, string(e.Type), e.CallIndex, e.AgentName, string(envelope),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetWorkflowEvents returns all events for a run in insertion order.
func (s *Storage) GetWorkflowEvents(runID int64) ([]*models.WorkflowEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, event_type, call_index, agent_name, payload, created_at FROM workflow_events WHERE run_id = ? ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*models.WorkflowEvent
	for rows.Next() {
		e, err := scanWorkflowEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func scanWorkflowEvent(scanner interface{ Scan(...any) error }) (*models.WorkflowEvent, error) {
	var e models.WorkflowEvent
	var callIndex sql.NullInt64
	var agentName sql.NullString
	var payloadStr string

	err := scanner.Scan(&e.ID, &e.RunID, &e.Type, &callIndex, &agentName, &payloadStr, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	if callIndex.Valid {
		idx := int(callIndex.Int64)
		e.CallIndex = &idx
	}
	if agentName.Valid {
		e.AgentName = agentName.String
	}

	payload, err := deserializePayload(e.Type, payloadStr)
	if err != nil {
		return nil, fmt.Errorf("deserialize payload for event %d (%s): %w", e.ID, e.Type, err)
	}
	e.Payload = payload
	return &e, nil
}

// deserializePayload decodes the JSON envelope back to a typed EventPayload.
func deserializePayload(eventType models.WorkflowEventType, raw string) (models.EventPayload, error) {
	var env payloadEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, err
	}

	switch eventType {
	case models.WFEventRunStarted:
		var p models.RunStartedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventRunCompleted:
		var p models.RunCompletedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventRunStuck:
		var p models.RunStuckPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventRunFailed:
		var p models.RunFailedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventAgentStarted:
		var p models.AgentStartedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventAgentCompleted:
		var p models.AgentCompletedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventAgentFailed:
		var p models.AgentFailedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventSignalReported:
		var p models.SignalReportedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventCheckpointStarted:
		var p models.CheckpointStartedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventCheckpointResumed:
		var p models.CheckpointResumedPayload
		return p, json.Unmarshal(env.Payload, &p)
	case models.WFEventLogMessage:
		var p models.LogMessagePayload
		return p, json.Unmarshal(env.Payload, &p)
	default:
		return nil, fmt.Errorf("unknown event type: %s", eventType)
	}
}
