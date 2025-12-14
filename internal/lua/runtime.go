package lua

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/storage"
	"github.com/mpataki/shop/internal/workspace"
)

// ErrWaitingHuman is returned when the workflow is suspended waiting for human input
var ErrWaitingHuman = fmt.Errorf("waiting for human input")

// Runtime executes Lua workflow scripts in a sandboxed environment
type Runtime struct {
	storage   *storage.Storage
	run       *models.Run
	ws        *workspace.Workspace
	callIndex int
	logs      []string

	// stuckReason is set when stuck() is called
	stuckReason string
	isStuck     bool

	// waitingHuman is set when an agent returns NEEDS_HUMAN
	waitingHuman       bool
	waitingReason      string
	waitingSessionID   string
	waitingAgent       string
	waitingExecID      int64
}

// NewRuntime creates a new Lua runtime for executing a workflow
func NewRuntime(store *storage.Storage, run *models.Run, ws *workspace.Workspace) *Runtime {
	return &Runtime{
		storage:   store,
		run:       run,
		ws:        ws,
		callIndex: 0,
		logs:      make([]string, 0),
	}
}

// Execute runs the Lua workflow script with the given prompt
func (r *Runtime) Execute(scriptPath, prompt string) error {
	// Read the script
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("failed to read script: %w", err)
	}

	// Create new Lua state
	L := lua.NewState(lua.Options{
		SkipOpenLibs: true, // Don't load any libraries by default
	})
	defer L.Close()

	// Load only safe libraries
	r.openSafeLibs(L)

	// Register our API functions
	r.registerAPI(L)

	// Load and run the script to define the workflow function
	if err := L.DoString(string(script)); err != nil {
		return fmt.Errorf("failed to load script: %w", err)
	}

	// Get the workflow function
	workflow := L.GetGlobal("workflow")
	if workflow == lua.LNil {
		return fmt.Errorf("script must define a 'workflow' function")
	}

	// Call workflow(prompt)
	L.Push(workflow)
	L.Push(lua.LString(prompt))
	if err := L.PCall(1, 0, nil); err != nil {
		// Check if this was a stuck() call
		if r.isStuck {
			return r.markStuck()
		}
		// Check if we're waiting for human input
		if r.waitingHuman {
			return r.markWaitingHuman()
		}
		return fmt.Errorf("workflow execution failed: %w", err)
	}

	// Check if stuck() was called
	if r.isStuck {
		return r.markStuck()
	}

	// Check if we're waiting for human input
	if r.waitingHuman {
		return r.markWaitingHuman()
	}

	// Normal completion
	return r.markComplete()
}

// openSafeLibs loads only the safe standard libraries
func (r *Runtime) openSafeLibs(L *lua.LState) {
	// Base library (pairs, ipairs, type, tostring, tonumber, error, etc.)
	// But we'll be selective about what we expose
	lua.OpenBase(L)

	// Remove dangerous base functions
	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("load", lua.LNil)
	L.SetGlobal("loadstring", lua.LNil)
	L.SetGlobal("print", lua.LNil) // Use log() instead

	// Table library
	lua.OpenTable(L)

	// String library
	lua.OpenString(L)

	// Math library (we'll remove random functions)
	lua.OpenMath(L)

	// Remove non-deterministic math functions
	math := L.GetGlobal("math")
	if tbl, ok := math.(*lua.LTable); ok {
		L.SetField(tbl, "random", lua.LNil)
		L.SetField(tbl, "randomseed", lua.LNil)
	}
}

// registerAPI registers the shop-specific API functions
func (r *Runtime) registerAPI(L *lua.LState) {
	L.SetGlobal("run", L.NewFunction(r.luaRun))
	L.SetGlobal("stuck", L.NewFunction(r.luaStuck))
	L.SetGlobal("context", L.NewFunction(r.luaContext))
	L.SetGlobal("log", L.NewFunction(r.luaLog))
	L.SetGlobal("pause", L.NewFunction(r.luaPause))
}

// luaRun implements the run(agent, prompt?) API
func (r *Runtime) luaRun(L *lua.LState) int {
	agent := L.CheckString(1)
	prompt := L.OptString(2, "")

	r.callIndex++

	// Check for cached execution at this call index
	exec, err := r.storage.GetExecutionByCallIndex(r.run.ID, r.callIndex)
	if err != nil {
		L.RaiseError("failed to check execution cache: %v", err)
		return 0
	}

	var signal map[string]any

	if exec != nil {
		// Found existing execution
		if exec.Status == models.ExecStatusComplete {
			// Already completed - return cached signal
			signal = exec.OutputSignal
			if signal == nil {
				signal = map[string]any{"status": "ERROR", "reason": "no signal in cache"}
			}
		} else if exec.Status == models.ExecStatusRunning || exec.Status == models.ExecStatusWaitingHuman {
			// Was in progress or waiting for human - check if agent finished
			signal, err = r.recoverExecution(exec)
			if err != nil {
				// Check if we're now waiting for human
				if r.waitingHuman {
					L.RaiseError("waiting for human: %s", r.waitingReason)
					return 0
				}
				L.RaiseError("failed to recover execution: %v", err)
				return 0
			}
		} else {
			// Failed - re-run
			signal, err = r.runAgent(agent, prompt, exec)
			if err != nil {
				// Check if we're now waiting for human
				if r.waitingHuman {
					L.RaiseError("waiting for human: %s", r.waitingReason)
					return 0
				}
				L.RaiseError("failed to run agent: %v", err)
				return 0
			}
		}

		// Check for agent mismatch (determinism violation)
		if exec.AgentName != agent {
			r.logs = append(r.logs, fmt.Sprintf("WARNING: determinism violation at call %d: expected %s, got %s", r.callIndex, exec.AgentName, agent))
			// Invalidate remaining cached executions
			r.storage.InvalidateExecutionsAfterIndex(r.run.ID, r.callIndex-1)
			// Run fresh
			signal, err = r.runAgentFresh(agent, prompt)
			if err != nil {
				L.RaiseError("failed to run agent: %v", err)
				return 0
			}
		}
	} else {
		// No cached execution - run fresh
		signal, err = r.runAgentFresh(agent, prompt)
		if err != nil {
			// Check if we're now waiting for human
			if r.waitingHuman {
				L.RaiseError("waiting for human: %s", r.waitingReason)
				return 0
			}
			L.RaiseError("failed to run agent: %v", err)
			return 0
		}
	}

	// Convert signal to Lua table
	tbl := r.signalToTable(L, signal)
	L.Push(tbl)
	return 1
}

// runAgentFresh creates a new execution and runs the agent
func (r *Runtime) runAgentFresh(agent, prompt string) (map[string]any, error) {
	// Get next sequence number
	execs, err := r.storage.GetExecutionsForRun(r.run.ID)
	if err != nil {
		return nil, err
	}
	seqNum := len(execs) + 1

	// Create execution record
	exec := &models.Execution{
		RunID:       r.run.ID,
		AgentName:   agent,
		Status:      models.ExecStatusPending,
		SequenceNum: seqNum,
		CallIndex:   r.callIndex,
		Prompt:      prompt,
	}

	execID, err := r.storage.CreateExecution(exec)
	if err != nil {
		return nil, err
	}
	exec.ID = execID

	return r.runAgent(agent, prompt, exec)
}

// runAgent executes the Claude agent and returns its signal
func (r *Runtime) runAgent(agent, prompt string, exec *models.Execution) (map[string]any, error) {
	// Update run's current agent
	r.run.CurrentAgent = agent
	if err := r.storage.UpdateRun(r.run); err != nil {
		return nil, err
	}

	// Create scratchpad for this agent
	if err := r.ws.CreateAgentScratchpad(agent); err != nil {
		return nil, err
	}

	// Write run metadata
	meta := &workspace.RunMetadata{
		RunID:         r.run.ID,
		SpecName:      r.run.SpecName,
		InitialPrompt: r.run.InitialPrompt,
		CurrentAgent:  agent,
		Iteration:     r.callIndex,
	}
	if err := r.ws.WriteRunMetadata(meta); err != nil {
		return nil, err
	}

	// Mark execution as running
	now := time.Now()
	exec.StartedAt = &now
	exec.Status = models.ExecStatusRunning
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	// Build agent prompt
	agentPrompt := r.buildAgentPrompt(agent, prompt)

	// Run Claude
	sessionID, exitCode, err := r.runClaude(agent, agentPrompt, exec.ID)
	if err != nil {
		exec.Status = models.ExecStatusFailed
		r.storage.UpdateExecution(exec)
		return map[string]any{"status": "ERROR", "reason": fmt.Sprintf("agent execution failed: %v", err)}, nil
	}

	// Read signal
	signal, err := r.ws.ReadSignal(agent)
	if err != nil {
		exec.Status = models.ExecStatusFailed
		r.storage.UpdateExecution(exec)
		return map[string]any{"status": "ERROR", "reason": fmt.Sprintf("no signal produced: %v", err)}, nil
	}

	// Update execution with results
	completedAt := time.Now()
	exec.ClaudeSessionID = sessionID
	exec.ExitCode = &exitCode
	exec.CompletedAt = &completedAt
	exec.OutputSignal = signal
	exec.Status = models.ExecStatusComplete
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	// Check for NEEDS_HUMAN signal
	if status, ok := signal["status"].(string); ok && status == "NEEDS_HUMAN" {
		// Mark execution as waiting
		exec.Status = models.ExecStatusWaitingHuman
		if err := r.storage.UpdateExecution(exec); err != nil {
			return nil, err
		}

		// Set up waiting state
		r.waitingHuman = true
		r.waitingSessionID = sessionID
		r.waitingAgent = agent
		r.waitingExecID = exec.ID
		if reason, ok := signal["reason"].(string); ok {
			r.waitingReason = reason
		} else {
			r.waitingReason = "Agent needs human input"
		}

		// Raise Lua error to suspend execution
		return nil, fmt.Errorf("agent %s needs human input: %s", agent, r.waitingReason)
	}

	// Append to context for next agent
	if err := r.ws.AppendContext(agent, signal); err != nil {
		return nil, err
	}

	// Add session ID to signal for debugging
	signal["_session_id"] = sessionID

	return signal, nil
}

// recoverExecution tries to recover from a running or waiting execution
func (r *Runtime) recoverExecution(exec *models.Execution) (map[string]any, error) {
	// Check if signal file exists (agent finished, we just missed it)
	signal, err := r.ws.ReadSignal(exec.AgentName)
	if err == nil {
		// Signal exists - check if status has changed from NEEDS_HUMAN
		if status, ok := signal["status"].(string); ok {
			if status == "NEEDS_HUMAN" {
				// Still waiting for human - suspend again
				r.waitingHuman = true
				r.waitingSessionID = exec.ClaudeSessionID
				r.waitingAgent = exec.AgentName
				r.waitingExecID = exec.ID
				if reason, ok := signal["reason"].(string); ok {
					r.waitingReason = reason
				} else {
					r.waitingReason = "Agent needs human input"
				}
				return nil, fmt.Errorf("agent %s still needs human input", exec.AgentName)
			}
		}

		// Signal changed - agent completed after human interaction
		completedAt := time.Now()
		exec.CompletedAt = &completedAt
		exec.OutputSignal = signal
		exec.Status = models.ExecStatusComplete
		if err := r.storage.UpdateExecution(exec); err != nil {
			return nil, err
		}

		// Append to context for next agent
		if err := r.ws.AppendContext(exec.AgentName, signal); err != nil {
			return nil, err
		}

		return signal, nil
	}

	// No signal - need to re-run
	return r.runAgent(exec.AgentName, exec.Prompt, exec)
}

// buildAgentPrompt constructs the prompt for the agent
func (r *Runtime) buildAgentPrompt(agent, prompt string) string {
	result := prompt
	if result == "" {
		result = r.run.InitialPrompt
	}

	// Direct agent to read context file for history
	if r.callIndex > 1 {
		result += "\n\n---\n"
		result += "IMPORTANT: Read `.agents/context.md` for context from previous agents before starting work."
	}

	result += fmt.Sprintf("\n\nYou are the '%s' agent in the '%s' workflow.", agent, r.run.SpecName)

	// Add signal file instructions
	result += "\n\n---\n"
	result += "IMPORTANT: When you have completed your task, you MUST write a JSON signal file.\n\n"
	result += "Write to: .agents/signals/" + agent + ".json\n\n"
	result += "Example:\n```json\n{\"status\": \"DONE\", \"summary\": \"Completed the task.\"}\n```\n"

	return result
}

// runClaude executes the Claude CLI
func (r *Runtime) runClaude(agent, prompt string, execID int64) (sessionID string, exitCode int, err error) {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
	}

	if agent != "" {
		args = append([]string{"--agent", agent}, args...)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = r.ws.RepoPath

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, err
	}

	if err := cmd.Start(); err != nil {
		return "", 0, err
	}

	// Store PID
	if cmd.Process != nil {
		r.storage.UpdateExecutionPID(execID, cmd.Process.Pid)
	}

	// Read output
	output, _ := io.ReadAll(stdout)

	// Wait for completion
	err = cmd.Wait()
	exitCode = 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", 0, err
		}
	}

	// Parse session ID from JSON output
	var result struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &result); err == nil {
		sessionID = result.SessionID
	}

	return sessionID, exitCode, nil
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

// luaStuck implements the stuck(reason?) API
func (r *Runtime) luaStuck(L *lua.LState) int {
	reason := L.OptString(1, "workflow stuck")
	r.stuckReason = reason
	r.isStuck = true
	// Raise an error to stop execution
	L.RaiseError("stuck: %s", reason)
	return 0
}

// luaContext implements the context() API
func (r *Runtime) luaContext(L *lua.LState) int {
	tbl := L.NewTable()
	L.SetField(tbl, "run_id", lua.LNumber(r.run.ID))
	L.SetField(tbl, "repo", lua.LString(r.ws.RepoPath))
	L.SetField(tbl, "iteration", lua.LNumber(r.callIndex))
	L.SetField(tbl, "prompt", lua.LString(r.run.InitialPrompt))
	L.Push(tbl)
	return 1
}

// luaLog implements the log(message) API
func (r *Runtime) luaLog(L *lua.LState) int {
	message := L.CheckString(1)
	r.logs = append(r.logs, message)
	return 0
}

// luaPause implements the pause(message) API for explicit checkpoints
func (r *Runtime) luaPause(L *lua.LState) int {
	message := L.CheckString(1)

	r.callIndex++

	// Check for cached execution at this call index
	exec, err := r.storage.GetExecutionByCallIndex(r.run.ID, r.callIndex)
	if err != nil {
		L.RaiseError("failed to check execution cache: %v", err)
		return 0
	}

	if exec != nil {
		// Found existing execution
		if exec.Status == models.ExecStatusComplete {
			// Already completed - return cached result
			return r.pauseResultFromSignal(L, exec.OutputSignal)
		} else if exec.Status == models.ExecStatusWaitingHuman {
			// Still waiting - check if signal changed
			signal, err := r.ws.ReadSignal("_checkpoint")
			if err == nil {
				if status, ok := signal["status"].(string); ok && status != "NEEDS_HUMAN" {
					// Human responded - complete the execution
					completedAt := time.Now()
					exec.CompletedAt = &completedAt
					exec.OutputSignal = signal
					exec.Status = models.ExecStatusComplete
					if err := r.storage.UpdateExecution(exec); err != nil {
						L.RaiseError("failed to update execution: %v", err)
						return 0
					}
					return r.pauseResultFromSignal(L, signal)
				}
			}

			// Still waiting for human
			r.waitingHuman = true
			r.waitingSessionID = exec.ClaudeSessionID
			r.waitingAgent = "_checkpoint"
			r.waitingExecID = exec.ID
			r.waitingReason = message
			L.RaiseError("waiting for human: %s", message)
			return 0
		}
	}

	// Need to create new checkpoint execution
	result, err := r.runCheckpoint(message)
	if err != nil {
		if r.waitingHuman {
			L.RaiseError("waiting for human: %s", message)
			return 0
		}
		L.RaiseError("checkpoint failed: %v", err)
		return 0
	}

	return r.pauseResultFromSignal(L, result)
}

// pauseResultFromSignal converts a checkpoint signal to a Lua pause result
func (r *Runtime) pauseResultFromSignal(L *lua.LState, signal map[string]any) int {
	tbl := L.NewTable()

	status, _ := signal["status"].(string)
	if status == "CONTINUE" {
		L.SetField(tbl, "continue", lua.LBool(true))
	} else {
		L.SetField(tbl, "continue", lua.LBool(false))
	}

	if reason, ok := signal["reason"].(string); ok {
		L.SetField(tbl, "reason", lua.LString(reason))
	}

	if msg, ok := signal["message"].(string); ok {
		L.SetField(tbl, "message", lua.LString(msg))
	}

	L.Push(tbl)
	return 1
}

// runCheckpoint runs a built-in checkpoint agent for pause()
func (r *Runtime) runCheckpoint(message string) (map[string]any, error) {
	agent := "_checkpoint"

	// Get next sequence number
	execs, err := r.storage.GetExecutionsForRun(r.run.ID)
	if err != nil {
		return nil, err
	}
	seqNum := len(execs) + 1

	// Create execution record
	exec := &models.Execution{
		RunID:       r.run.ID,
		AgentName:   agent,
		Status:      models.ExecStatusPending,
		SequenceNum: seqNum,
		CallIndex:   r.callIndex,
		Prompt:      message,
	}

	execID, err := r.storage.CreateExecution(exec)
	if err != nil {
		return nil, err
	}
	exec.ID = execID

	// Update run's current agent
	r.run.CurrentAgent = agent
	if err := r.storage.UpdateRun(r.run); err != nil {
		return nil, err
	}

	// Create scratchpad for checkpoint
	if err := r.ws.CreateAgentScratchpad(agent); err != nil {
		return nil, err
	}

	// Write run metadata
	meta := &workspace.RunMetadata{
		RunID:         r.run.ID,
		SpecName:      r.run.SpecName,
		InitialPrompt: r.run.InitialPrompt,
		CurrentAgent:  agent,
		Iteration:     r.callIndex,
	}
	if err := r.ws.WriteRunMetadata(meta); err != nil {
		return nil, err
	}

	// Mark execution as running
	now := time.Now()
	exec.StartedAt = &now
	exec.Status = models.ExecStatusRunning
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	// Build checkpoint prompt
	checkpointPrompt := r.buildCheckpointPrompt(message)

	// Run Claude for checkpoint
	sessionID, _, err := r.runClaude("", checkpointPrompt, exec.ID)
	if err != nil {
		exec.Status = models.ExecStatusFailed
		r.storage.UpdateExecution(exec)
		return nil, fmt.Errorf("checkpoint agent failed: %w", err)
	}

	// Read signal
	signal, err := r.ws.ReadSignal(agent)
	if err != nil {
		// No signal yet - write initial NEEDS_HUMAN signal
		signal = map[string]any{
			"status": "NEEDS_HUMAN",
			"reason": message,
		}
		if err := r.ws.WriteSignal(agent, signal); err != nil {
			return nil, err
		}
	}

	// Check if waiting for human
	if status, ok := signal["status"].(string); ok && status == "NEEDS_HUMAN" {
		// Mark execution as waiting
		exec.ClaudeSessionID = sessionID
		exec.Status = models.ExecStatusWaitingHuman
		if err := r.storage.UpdateExecution(exec); err != nil {
			return nil, err
		}

		// Set up waiting state
		r.waitingHuman = true
		r.waitingSessionID = sessionID
		r.waitingAgent = agent
		r.waitingExecID = exec.ID
		r.waitingReason = message

		return nil, fmt.Errorf("checkpoint paused: %s", message)
	}

	// Checkpoint completed immediately (shouldn't normally happen)
	completedAt := time.Now()
	exec.ClaudeSessionID = sessionID
	exec.CompletedAt = &completedAt
	exec.OutputSignal = signal
	exec.Status = models.ExecStatusComplete
	if err := r.storage.UpdateExecution(exec); err != nil {
		return nil, err
	}

	return signal, nil
}

// buildCheckpointPrompt constructs the prompt for a checkpoint agent
func (r *Runtime) buildCheckpointPrompt(message string) string {
	prompt := fmt.Sprintf(`The workflow has paused for human input.

**Checkpoint:** %s

**What to do:**
1. Review the workspace state
2. Check recent changes and test results
3. Decide whether to continue or stop

When ready, write your decision to .agents/signals/_checkpoint.json:

To continue:
` + "```json\n{\"status\": \"CONTINUE\", \"message\": \"Your optional note here\"}\n```" + `

To stop:
` + "```json\n{\"status\": \"STOP\", \"reason\": \"Reason for stopping\"}\n```" + `

Wait for the human to provide guidance before writing your decision.`, message)

	return prompt
}

// markComplete marks the run as complete
func (r *Runtime) markComplete() error {
	now := time.Now()
	r.run.Status = models.RunStatusComplete
	r.run.CompletedAt = &now
	return r.storage.UpdateRun(r.run)
}

// markStuck marks the run as stuck
func (r *Runtime) markStuck() error {
	now := time.Now()
	r.run.Status = models.RunStatusStuck
	r.run.CompletedAt = &now
	r.run.Error = r.stuckReason
	return r.storage.UpdateRun(r.run)
}

// markWaitingHuman marks the run as waiting for human input
func (r *Runtime) markWaitingHuman() error {
	r.run.Status = models.RunStatusWaitingHuman
	r.run.WaitingReason = r.waitingReason
	r.run.WaitingSessionID = r.waitingSessionID
	r.run.CurrentAgent = r.waitingAgent
	if err := r.storage.UpdateRun(r.run); err != nil {
		return err
	}
	return ErrWaitingHuman
}

// GetLogs returns the logs collected during execution
func (r *Runtime) GetLogs() []string {
	return r.logs
}

// IsLuaSpec checks if a file is a Lua spec
func IsLuaSpec(path string) bool {
	return filepath.Ext(path) == ".lua"
}
