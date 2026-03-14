package lua

import (
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/storage"
)

// luaRun implements the run(agent, prompt?) API
func (r *Runtime) luaRun(L *lua.LState) int {
	agent := L.CheckString(1)

	// Second arg: optional string (prompt) or table ({prompt, model})
	var prompt, model string
	if v := L.Get(2); v != lua.LNil {
		switch v := v.(type) {
		case lua.LString:
			prompt = string(v)
		case *lua.LTable:
			if p := v.RawGetString("prompt"); p != lua.LNil {
				prompt = p.String()
			}
			if m := v.RawGetString("model"); m != lua.LNil {
				model = m.String()
			}
		default:
			L.ArgError(2, "expected string or table")
			return 0
		}
	}

	r.callIndex++
	idx := r.callIndex

	// ── 1. Event replay cache (primary path) ─────────────────────────────
	if entry, ok := r.replayCache[idx]; ok {
		if entry.AgentName != "" && entry.AgentName != agent {
			// Determinism violation — script changed since last run.
			msg := fmt.Sprintf("WARNING: determinism violation at call %d: cached agent=%s, script agent=%s; re-running from here",
				idx, entry.AgentName, agent)
			r.logs = append(r.logs, msg)
			r.appendEvent(models.WFEventLogMessage, nil, "", models.LogMessagePayload{Message: msg})
			// Discard in-memory cache from this point forward.
			// New agent_completed events appended below will supersede old ones on next resume.
			for k := range r.replayCache {
				if k >= idx {
					delete(r.replayCache, k)
				}
			}
			// Fall through to fresh run.
		} else {
			signal := entry.Signal
			if status, _ := signal["status"].(string); status == string(models.SignalNeedsHuman) {
				// Orchestrator hasn't yet appended an updated agent_completed for this call —
				// the human hasn't responded. Suspend again.
				r.waitingHuman = true
				r.waitingAgent = agent
				r.waitingSessionID, _ = signal["_session_id"].(string)
				if reason, ok := signal["reason"].(string); ok {
					r.waitingReason = reason
				} else {
					r.waitingReason = "Agent needs human input"
				}
				if exec, _ := r.storage.GetExecutionByCallIndex(r.run.ID, idx); exec != nil {
					r.waitingExecID = exec.ID
				}
				L.RaiseError("waiting for human: %s", r.waitingReason)
				return 0
			}
			tbl := r.signalToTable(L, signal)
			L.Push(tbl)
			return 1
		}
	}

	// ── 2. Crash recovery: execution has signal but agent_completed wasn't emitted ──
	exec, err := r.storage.GetExecutionByCallIndex(r.run.ID, idx)
	if err != nil {
		L.RaiseError("failed to check execution cache: %v", err)
		return 0
	}

	if exec != nil && exec.OutputSignal != nil {
		signal := exec.OutputSignal
		signal["_session_id"] = exec.ClaudeSessionID
		// Emit the missing event so future resumes hit the cache.
		r.appendEvent(models.WFEventAgentCompleted, &idx, exec.AgentName, models.AgentCompletedPayload{Signal: signal})
		r.replayCache[idx] = &storage.ReplayEntry{AgentName: exec.AgentName, Signal: signal}

		if status, _ := signal["status"].(string); status == string(models.SignalNeedsHuman) {
			r.waitingHuman = true
			r.waitingAgent = exec.AgentName
			r.waitingSessionID = exec.ClaudeSessionID
			r.waitingExecID = exec.ID
			if reason, ok := signal["reason"].(string); ok {
				r.waitingReason = reason
			} else {
				r.waitingReason = "Agent needs human input"
			}
			L.RaiseError("waiting for human: %s", r.waitingReason)
			return 0
		}

		tbl := r.signalToTable(L, signal)
		L.Push(tbl)
		return 1
	}

	// ── 3. Run fresh ──────────────────────────────────────────────────────
	var signal map[string]any
	if exec != nil {
		// Execution record exists (was running when we crashed, no signal) — re-run using existing record.
		signal, err = r.runAgent(agent, prompt, model, exec)
	} else {
		signal, err = r.runAgentFresh(agent, prompt, model)
	}
	if err != nil {
		if r.waitingHuman {
			L.RaiseError("waiting for human: %s", r.waitingReason)
			return 0
		}
		L.RaiseError("failed to run agent: %v", err)
		return 0
	}

	tbl := r.signalToTable(L, signal)
	L.Push(tbl)
	return 1
}

// runAgentFresh creates a new execution record and runs the agent.
func (r *Runtime) runAgentFresh(agent, prompt, model string) (map[string]any, error) {
	execs, err := r.storage.GetExecutionsForRun(r.run.ID)
	if err != nil {
		return nil, err
	}

	exec := &models.Execution{
		RunID:       r.run.ID,
		AgentName:   agent,
		Status:      models.ExecStatusPending,
		SequenceNum: len(execs) + 1,
		CallIndex:   r.callIndex,
		Prompt:      prompt,
		Model:       model,
	}

	execID, err := r.storage.CreateExecution(exec)
	if err != nil {
		return nil, err
	}
	exec.ID = execID

	return r.runAgent(agent, prompt, model, exec)
}

// runAgent executes the Claude agent and returns its signal.
func (r *Runtime) runAgent(agent, prompt, model string, exec *models.Execution) (map[string]any, error) {
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

	agentPrompt := r.buildAgentPrompt(agent, prompt)

	idx := r.callIndex
	r.appendEvent(models.WFEventAgentStarted, &idx, agent, models.AgentStartedPayload{})

	result, err := r.runClaude(agent, agent, agentPrompt, model, exec.ID)
	if err != nil {
		exec.Status = models.ExecStatusFailed
		r.storage.UpdateExecution(exec)
		reason := fmt.Sprintf("agent execution failed: %v", err)
		r.appendEvent(models.WFEventAgentFailed, &idx, agent, models.AgentFailedPayload{Reason: reason})
		return map[string]any{"status": string(models.SignalError), "reason": reason}, nil
	}

	exec.ClaudeSessionID = result.SessionID
	exec.ExitCode = &result.ExitCode

	if result.ErrorResult != "" {
		exec.Status = models.ExecStatusFailed
		exec.OutputSignal = map[string]any{"status": string(models.SignalError), "reason": result.ErrorResult}
		r.storage.UpdateExecution(exec)
		reason := result.ErrorResult
		r.appendEvent(models.WFEventAgentFailed, &idx, agent, models.AgentFailedPayload{Reason: reason})
		return exec.OutputSignal, nil
	}

	// Read signal from DB — written by MCP server's report_signal tool during agent execution.
	latest, err := r.storage.GetExecution(exec.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to read execution from DB: %w", err)
	}

	var signal map[string]any
	if latest != nil && latest.OutputSignal != nil {
		signal = latest.OutputSignal
	} else {
		exec.Status = models.ExecStatusFailed
		errReason := fmt.Sprintf("no signal (exit %d)", result.ExitCode)
		if result.Stderr != "" {
			errReason += ": " + result.Stderr
		}
		exec.OutputSignal = map[string]any{"status": string(models.SignalError), "reason": errReason}
		r.storage.UpdateExecution(exec)
		r.appendEvent(models.WFEventAgentFailed, &idx, agent, models.AgentFailedPayload{Reason: errReason})
		return exec.OutputSignal, nil
	}

	// Always include session ID so the replay cache entry has it (needed for NEEDS_HUMAN resume).
	signal["_session_id"] = result.SessionID

	// Emit agent_completed — this is the authoritative event for the replay cache.
	r.appendEvent(models.WFEventAgentCompleted, &idx, agent, models.AgentCompletedPayload{Signal: signal})

	// Update execution record (write-through cache).
	completedAt := time.Now()
	exec.CompletedAt = &completedAt
	exec.OutputSignal = signal
	exec.Status = models.ExecStatusComplete
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	// Handle NEEDS_HUMAN — the agent_completed event is already written above.
	// The orchestrator will append an updated agent_completed when the human responds,
	// so the next resume() call sees the new signal in the replay cache.
	if status, ok := signal["status"].(string); ok && status == string(models.SignalNeedsHuman) {
		exec.Status = models.ExecStatusWaitingHuman
		if err := r.storage.UpdateExecution(exec); err != nil {
			return nil, err
		}

		r.waitingHuman = true
		r.waitingSessionID = exec.ClaudeSessionID
		r.waitingAgent = agent
		r.waitingExecID = exec.ID
		if reason, ok := signal["reason"].(string); ok {
			r.waitingReason = reason
		} else {
			r.waitingReason = "Agent needs human input"
		}
		return nil, fmt.Errorf("agent %s needs human input: %s", agent, r.waitingReason)
	}

	return signal, nil
}

// signalToTable converts a Go map to a Lua table
func (r *Runtime) signalToTable(L *lua.LState, signal map[string]any) *lua.LTable {
	tbl := L.NewTable()
	for k, v := range signal {
		L.SetField(tbl, k, r.goToLua(L, v))
	}
	return tbl
}

// goToLua converts a Go value to a Lua value
func (r *Runtime) goToLua(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case string:
		return lua.LString(val)
	case []any:
		tbl := L.NewTable()
		for i, item := range val {
			L.SetTable(tbl, lua.LNumber(i+1), r.goToLua(L, item))
		}
		return tbl
	case map[string]any:
		tbl := L.NewTable()
		for k, item := range val {
			L.SetField(tbl, k, r.goToLua(L, item))
		}
		return tbl
	default:
		return lua.LString(fmt.Sprintf("%v", val))
	}
}
