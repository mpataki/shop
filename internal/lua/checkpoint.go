package lua

import (
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/storage"
)

// luaPause implements the pause(message) API for explicit human checkpoints.
func (r *Runtime) luaPause(L *lua.LState) int {
	message := L.CheckString(1)

	r.callIndex++
	idx := r.callIndex

	// ── 1. Event replay cache (primary path) ─────────────────────────────
	if entry, ok := r.replayCache[idx]; ok {
		if entry.PauseResult != nil {
			return r.pauseResultFromPayload(L, entry.PauseResult)
		}
		// entry.Signal set means we have an agent_completed here (shouldn't happen for pause),
		// or a checkpoint that is still NEEDS_HUMAN (human hasn't responded yet).
		if entry.Signal != nil {
			if status, _ := entry.Signal["status"].(string); status == string(models.SignalNeedsHuman) {
				r.waitingHuman = true
				r.waitingAgent = "_checkpoint"
				r.waitingReason = message
				r.waitingSessionID, _ = entry.Signal["_session_id"].(string)
				if exec, _ := r.storage.GetExecutionByCallIndex(r.run.ID, idx); exec != nil {
					r.waitingExecID = exec.ID
				}
				L.RaiseError("waiting for human: %s", message)
				return 0
			}
		}
	}

	// ── 2. Crash recovery: execution exists with result ──────────────────
	exec, err := r.storage.GetExecutionByCallIndex(r.run.ID, idx)
	if err != nil {
		L.RaiseError("failed to check execution cache: %v", err)
		return 0
	}

	if exec != nil && exec.OutputSignal != nil {
		signal := exec.OutputSignal
		if status, _ := signal["status"].(string); status != string(models.SignalNeedsHuman) {
			// Emit the missing event so future resumes hit the cache.
			payload := checkpointPayloadFromSignal(signal)
			r.appendEvent(models.WFEventCheckpointResumed, &idx, "_checkpoint", payload)
			r.replayCache[idx] = &storage.ReplayEntry{AgentName: "_checkpoint", PauseResult: &payload}
			return r.pauseResultFromPayload(L, &payload)
		}
		// Still NEEDS_HUMAN — fall through to re-run check.
	}

	// ── 3. Waiting from a previous run that was interrupted ──────────────
	if exec != nil && exec.Status == models.ExecStatusWaitingHuman {
		r.waitingHuman = true
		r.waitingAgent = "_checkpoint"
		r.waitingReason = message
		r.waitingSessionID = exec.ClaudeSessionID
		r.waitingExecID = exec.ID
		L.RaiseError("waiting for human: %s", message)
		return 0
	}

	// ── 4. Run checkpoint fresh ──────────────────────────────────────────
	result, err := r.runCheckpoint(message, exec)
	if err != nil {
		if r.waitingHuman {
			L.RaiseError("waiting for human: %s", message)
			return 0
		}
		L.RaiseError("checkpoint failed: %v", err)
		return 0
	}

	return r.pauseResultFromPayload(L, result)
}

// pauseResultFromPayload pushes a Lua table from a CheckpointResumedPayload.
func (r *Runtime) pauseResultFromPayload(L *lua.LState, p *models.CheckpointResumedPayload) int {
	tbl := L.NewTable()
	L.SetField(tbl, "continue", lua.LBool(p.Continue))
	if p.Reason != "" {
		L.SetField(tbl, "reason", lua.LString(p.Reason))
	}
	if p.Message != "" {
		L.SetField(tbl, "message", lua.LString(p.Message))
	}
	L.Push(tbl)
	return 1
}

// checkpointPayloadFromSignal converts a raw signal map to a CheckpointResumedPayload.
func checkpointPayloadFromSignal(signal map[string]any) models.CheckpointResumedPayload {
	var p models.CheckpointResumedPayload
	if status, ok := signal["status"].(string); ok {
		p.Continue = status == string(models.SignalContinue)
	}
	p.Reason, _ = signal["reason"].(string)
	p.Message, _ = signal["message"].(string)
	return p
}

// runCheckpoint runs a built-in checkpoint agent for pause().
// exec is the existing execution record if one exists (crash recovery), or nil for fresh runs.
func (r *Runtime) runCheckpoint(message string, exec *models.Execution) (*models.CheckpointResumedPayload, error) {
	const agent = "_checkpoint"
	idx := r.callIndex

	if exec == nil {
		execs, err := r.storage.GetExecutionsForRun(r.run.ID)
		if err != nil {
			return nil, err
		}
		exec = &models.Execution{
			RunID:       r.run.ID,
			AgentName:   agent,
			Status:      models.ExecStatusPending,
			SequenceNum: len(execs) + 1,
			CallIndex:   idx,
			Prompt:      message,
		}
		execID, err := r.storage.CreateExecution(exec)
		if err != nil {
			return nil, err
		}
		exec.ID = execID
	}

	r.run.CurrentAgent = agent
	if err := r.storage.UpdateRun(r.run); err != nil {
		return nil, err
	}

	if err := r.ws.CreateAgentScratchpad(agent); err != nil {
		return nil, err
	}

	now := time.Now()
	exec.StartedAt = &now
	exec.Status = models.ExecStatusRunning
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	checkpointPrompt := r.buildCheckpointPrompt(message)

	r.appendEvent(models.WFEventCheckpointStarted, &idx, agent, models.CheckpointStartedPayload{Message: message})

	result, err := r.runClaude("", agent, checkpointPrompt, "", exec.ID)
	if err != nil {
		exec.Status = models.ExecStatusFailed
		r.storage.UpdateExecution(exec)
		return nil, fmt.Errorf("checkpoint agent failed: %w", err)
	}

	exec.ClaudeSessionID = result.SessionID

	// Read signal from DB (written by MCP server's report_signal tool).
	latest, _ := r.storage.GetExecution(exec.ID)
	var signal map[string]any
	if latest != nil && latest.OutputSignal != nil {
		signal = latest.OutputSignal
	} else {
		// No signal written — checkpoint waits for human.
		signal = map[string]any{
			"status": string(models.SignalNeedsHuman),
			"reason": message,
		}
		if err := r.storage.UpdateExecutionSignal(exec.ID, signal); err != nil {
			return nil, fmt.Errorf("failed to write checkpoint signal: %w", err)
		}
	}

	if status, _ := signal["status"].(string); status == string(models.SignalNeedsHuman) {
		exec.Status = models.ExecStatusWaitingHuman
		if err := r.storage.UpdateExecution(exec); err != nil {
			return nil, err
		}
		r.waitingHuman = true
		r.waitingSessionID = result.SessionID
		r.waitingAgent = agent
		r.waitingExecID = exec.ID
		r.waitingReason = message
		return nil, fmt.Errorf("checkpoint paused: %s", message)
	}

	// Checkpoint completed immediately.
	completedAt := time.Now()
	exec.CompletedAt = &completedAt
	exec.OutputSignal = signal
	exec.Status = models.ExecStatusComplete
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	payload := checkpointPayloadFromSignal(signal)
	r.appendEvent(models.WFEventCheckpointResumed, &idx, agent, payload)

	return &payload, nil
}
