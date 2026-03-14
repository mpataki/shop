package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"github.com/mpataki/shop/internal/events"
	"github.com/mpataki/shop/internal/process"
)

// ErrWaitingHuman is returned when the workflow is suspended waiting for human input.
var ErrWaitingHuman = fmt.Errorf("waiting for human input")

// RuntimeDeps holds the dependencies injected into the workflow runtime.
type RuntimeDeps struct {
	Store          *events.Store
	State          *events.RunState
	ProcessManager process.Manager
	WorkspacePath  string
	RepoPath       string

	// Callbacks
	EmitEvents    func(evts []events.Event) ([]events.Event, error)
	DrainCommands func() error
	WriteMCPConfig func(callIndex int) error
}

// Runtime executes Lua workflow scripts in a sandboxed environment.
type Runtime struct {
	deps      RuntimeDeps
	callIndex int
	logs      []string

	// stuck state
	stuckReason string
	isStuck     bool

	// waiting human state
	waitingHuman     bool
	waitingReason    string
	waitingSessionID string
	waitingAgent     string
	waitingCallIndex int
}

// NewRuntime creates a new Lua runtime for executing a workflow.
func NewRuntime(deps RuntimeDeps) *Runtime {
	return &Runtime{
		deps: deps,
		logs: make([]string, 0),
	}
}

// WaitingInfo holds details about a NEEDS_HUMAN suspension.
type WaitingInfo struct {
	Reason    string
	SessionID string
	Agent     string
	CallIndex int
}

// GetWaitingInfo returns info about the waiting state, if any.
func (r *Runtime) GetWaitingInfo() *WaitingInfo {
	if !r.waitingHuman {
		return nil
	}
	return &WaitingInfo{
		Reason:    r.waitingReason,
		SessionID: r.waitingSessionID,
		Agent:     r.waitingAgent,
		CallIndex: r.waitingCallIndex,
	}
}

// Execute runs the Lua workflow script.
func (r *Runtime) Execute(scriptPath, prompt string) error {
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("failed to read script: %w", err)
	}

	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()

	r.openSafeLibs(L)
	r.registerAPI(L)

	if err := L.DoString(string(script)); err != nil {
		return fmt.Errorf("failed to load script: %w", err)
	}

	workflow := L.GetGlobal("workflow")
	if workflow == lua.LNil {
		return fmt.Errorf("script must define a 'workflow' function")
	}

	L.Push(workflow)
	L.Push(lua.LString(prompt))
	if err := L.PCall(1, 0, nil); err != nil {
		if r.isStuck {
			return nil // stuck() was called; terminal event emitted by caller
		}
		if r.waitingHuman {
			return ErrWaitingHuman
		}
		return fmt.Errorf("workflow execution failed: %w", err)
	}

	if r.isStuck {
		return nil
	}
	if r.waitingHuman {
		return ErrWaitingHuman
	}

	return nil
}

// IsStuck returns true if stuck() was called.
func (r *Runtime) IsStuck() bool { return r.isStuck }

// StuckReason returns the reason passed to stuck().
func (r *Runtime) StuckReason() string { return r.stuckReason }

// GetLogs returns the logs collected during execution.
func (r *Runtime) GetLogs() []string { return r.logs }

// openSafeLibs loads only the safe standard libraries.
func (r *Runtime) openSafeLibs(L *lua.LState) {
	lua.OpenBase(L)

	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("load", lua.LNil)
	L.SetGlobal("loadstring", lua.LNil)
	L.SetGlobal("print", lua.LNil)

	lua.OpenTable(L)
	lua.OpenString(L)
	lua.OpenMath(L)

	math := L.GetGlobal("math")
	if tbl, ok := math.(*lua.LTable); ok {
		L.SetField(tbl, "random", lua.LNil)
		L.SetField(tbl, "randomseed", lua.LNil)
	}
}

// registerAPI registers the shop-specific API functions.
func (r *Runtime) registerAPI(L *lua.LState) {
	L.SetGlobal("run", L.NewFunction(r.luaRun))
	L.SetGlobal("stuck", L.NewFunction(r.luaStuck))
	L.SetGlobal("context", L.NewFunction(r.luaContext))
	L.SetGlobal("log", L.NewFunction(r.luaLog))
	L.SetGlobal("pause", L.NewFunction(r.luaPause))
}

// ── run() ─────────────────────────────────────────────────────────────────────

func (r *Runtime) luaRun(L *lua.LState) int {
	agent := L.CheckString(1)

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

	// ── 1. Replay: check projection for completed execution at this call_index ──
	if exec := r.deps.State.GetExecutionByCallIndex(idx); exec != nil {
		if exec.Status == events.ExecStatusCompleted && exec.Signal != nil {
			// Determinism check
			if exec.AgentName != agent {
				msg := fmt.Sprintf("WARNING: determinism violation at call %d: cached agent=%s, script agent=%s; re-running",
					idx, exec.AgentName, agent)
				r.logs = append(r.logs, msg)
				r.emitLog(msg)
				// Fall through to fresh run
			} else {
				signal := exec.Signal
				if status, _ := signal["status"].(string); status == string(events.SignalNeedsHuman) {
					r.setWaitingHuman(agent, idx, exec.SessionID, signal)
					L.RaiseError("waiting for human: %s", r.waitingReason)
					return 0
				}
				tbl := r.signalToTable(L, signal)
				L.Push(tbl)
				return 1
			}
		}
		if exec.Status == events.ExecStatusWaitingHuman {
			r.setWaitingHuman(agent, idx, exec.SessionID, exec.Signal)
			L.RaiseError("waiting for human: %s", r.waitingReason)
			return 0
		}
	}

	// ── 2. Run fresh ──
	signal, err := r.runAgent(agent, prompt, model, idx)
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

func (r *Runtime) runAgent(agent, prompt, model string, callIndex int) (map[string]any, error) {
	// Create scratchpad
	scratchDir := filepath.Join(r.deps.WorkspacePath, "scratchpad", agent)
	os.MkdirAll(scratchDir, 0755)

	// Write MCP config
	if err := r.deps.WriteMCPConfig(callIndex); err != nil {
		return nil, fmt.Errorf("write MCP config: %w", err)
	}

	agentPrompt := r.buildAgentPrompt(agent, prompt)

	// Start agent via ProcessManager
	ctx := context.Background()
	sessionID, pid, done, err := r.deps.ProcessManager.StartAgent(ctx, process.AgentOpts{
		ClaudeAgent:   agent,
		SignalAgent:   agent,
		Prompt:        agentPrompt,
		Model:         model,
		WorkDir:       r.deps.RepoPath,
		MCPConfigPath: filepath.Join(r.deps.WorkspacePath, "mcp.json"),
	})
	if err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}

	// Emit AgentStarted
	startedEvt, _ := events.NewEvent(r.deps.State.ID, events.EventAgentStarted, events.AgentStartedPayload{
		AgentName: agent,
		CallIndex: callIndex,
		SessionID: sessionID,
		PID:       pid,
		Prompt:    prompt,
		Model:     model,
	})
	r.deps.EmitEvents([]events.Event{startedEvt})

	// Wait for agent to finish
	result := <-done

	// Drain pending commands (picks up ReportSignal from MCP)
	if r.deps.DrainCommands != nil {
		r.deps.DrainCommands()
	}

	// Re-read state to get the signal written by MCP
	// The DrainCommands call processes ReportSignal which emits SignalReceived
	// The processor updates the projection, but we need to re-project
	freshEvents, err := r.deps.Store.GetEvents(r.deps.State.ID)
	if err != nil {
		return nil, fmt.Errorf("re-read events: %w", err)
	}
	info, _ := r.deps.Store.GetRun(r.deps.State.ID)
	freshState := events.ProjectRun(info.ID, info.CreatedAt, freshEvents)

	if result.ErrorResult != "" {
		failEvt, _ := events.NewEvent(r.deps.State.ID, events.EventAgentFailed, events.AgentFailedPayload{
			AgentName: agent, CallIndex: callIndex, Error: result.ErrorResult, ExitCode: result.ExitCode,
		})
		r.deps.EmitEvents([]events.Event{failEvt})
		return map[string]any{"status": string(events.SignalError), "reason": result.ErrorResult}, nil
	}

	// Find signal from projection
	exec := freshState.GetExecutionByCallIndex(callIndex)
	var signal map[string]any
	if exec != nil && exec.Signal != nil {
		signal = exec.Signal
	}

	if signal == nil {
		errReason := fmt.Sprintf("no signal (exit %d)", result.ExitCode)
		if result.Stderr != "" {
			errReason += ": " + result.Stderr
		}
		failEvt, _ := events.NewEvent(r.deps.State.ID, events.EventAgentFailed, events.AgentFailedPayload{
			AgentName: agent, CallIndex: callIndex, Error: errReason, ExitCode: result.ExitCode,
		})
		r.deps.EmitEvents([]events.Event{failEvt})
		return map[string]any{"status": string(events.SignalError), "reason": errReason}, nil
	}

	// Include session ID
	signal["_session_id"] = sessionID

	// Emit AgentCompleted
	completedEvt, _ := events.NewEvent(r.deps.State.ID, events.EventAgentCompleted, events.AgentCompletedPayload{
		AgentName: agent, CallIndex: callIndex, Signal: signal,
	})
	r.deps.EmitEvents([]events.Event{completedEvt})

	// Handle NEEDS_HUMAN
	if status, ok := signal["status"].(string); ok && status == string(events.SignalNeedsHuman) {
		r.setWaitingHuman(agent, callIndex, sessionID, signal)
		return nil, fmt.Errorf("agent %s needs human input: %s", agent, r.waitingReason)
	}

	return signal, nil
}

// ── pause() ───────────────────────────────────────────────────────────────────

func (r *Runtime) luaPause(L *lua.LState) int {
	message := L.CheckString(1)

	r.callIndex++
	idx := r.callIndex

	// Replay: check projection for completed checkpoint at this call_index
	if exec := r.deps.State.GetExecutionByCallIndex(idx); exec != nil {
		if exec.Status == events.ExecStatusCompleted && exec.Signal != nil {
			return r.pauseResultFromSignal(L, exec.Signal)
		}
		if exec.Status == events.ExecStatusWaitingHuman {
			r.setWaitingHuman("_checkpoint", idx, exec.SessionID, exec.Signal)
			L.RaiseError("waiting for human: %s", message)
			return 0
		}
	}

	// Run checkpoint fresh
	result, err := r.runCheckpoint(message, idx)
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

func (r *Runtime) runCheckpoint(message string, callIndex int) (map[string]any, error) {
	const agent = "_checkpoint"

	// Create scratchpad
	scratchDir := filepath.Join(r.deps.WorkspacePath, "scratchpad", agent)
	os.MkdirAll(scratchDir, 0755)

	// Write MCP config
	if err := r.deps.WriteMCPConfig(callIndex); err != nil {
		return nil, fmt.Errorf("write MCP config: %w", err)
	}

	checkpointPrompt := r.buildCheckpointPrompt(message)

	ctx := context.Background()
	sessionID, pid, done, err := r.deps.ProcessManager.StartAgent(ctx, process.AgentOpts{
		ClaudeAgent:   "", // no agent mode for checkpoints
		SignalAgent:   agent,
		Prompt:        checkpointPrompt,
		WorkDir:       r.deps.RepoPath,
		MCPConfigPath: filepath.Join(r.deps.WorkspacePath, "mcp.json"),
	})
	if err != nil {
		return nil, fmt.Errorf("start checkpoint: %w", err)
	}

	// Emit CheckpointStarted
	startedEvt, _ := events.NewEvent(r.deps.State.ID, events.EventCheckpointStarted, events.CheckpointStartedPayload{
		CallIndex: callIndex, Message: message, SessionID: sessionID,
	})
	_ = pid
	r.deps.EmitEvents([]events.Event{startedEvt})

	// Wait
	result := <-done
	_ = result

	// Drain commands
	if r.deps.DrainCommands != nil {
		r.deps.DrainCommands()
	}

	// Re-read state
	freshEvents, err := r.deps.Store.GetEvents(r.deps.State.ID)
	if err != nil {
		return nil, fmt.Errorf("re-read events: %w", err)
	}
	info, _ := r.deps.Store.GetRun(r.deps.State.ID)
	freshState := events.ProjectRun(info.ID, info.CreatedAt, freshEvents)

	exec := freshState.GetExecutionByCallIndex(callIndex)
	var signal map[string]any
	if exec != nil && exec.Signal != nil {
		signal = exec.Signal
	}

	if signal == nil {
		signal = map[string]any{
			"status": string(events.SignalNeedsHuman),
			"reason": message,
		}
	}

	if status, _ := signal["status"].(string); status == string(events.SignalNeedsHuman) {
		r.setWaitingHuman(agent, callIndex, sessionID, signal)
		return nil, fmt.Errorf("checkpoint paused: %s", message)
	}

	// Checkpoint completed immediately
	completedEvt, _ := events.NewEvent(r.deps.State.ID, events.EventCheckpointCompleted, events.CheckpointCompletedPayload{
		CallIndex: callIndex, Signal: signal,
	})
	r.deps.EmitEvents([]events.Event{completedEvt})

	return signal, nil
}

func (r *Runtime) pauseResultFromSignal(L *lua.LState, signal map[string]any) int {
	tbl := L.NewTable()
	status, _ := signal["status"].(string)
	L.SetField(tbl, "continue", lua.LBool(status == string(events.SignalContinue)))
	if reason, ok := signal["reason"].(string); ok && reason != "" {
		L.SetField(tbl, "reason", lua.LString(reason))
	}
	if msg, ok := signal["message"].(string); ok && msg != "" {
		L.SetField(tbl, "message", lua.LString(msg))
	}
	L.Push(tbl)
	return 1
}

// ── stuck() ───────────────────────────────────────────────────────────────────

func (r *Runtime) luaStuck(L *lua.LState) int {
	reason := L.OptString(1, "workflow stuck")
	r.stuckReason = reason
	r.isStuck = true
	L.RaiseError("stuck: %s", reason)
	return 0
}

// ── context() ─────────────────────────────────────────────────────────────────

func (r *Runtime) luaContext(L *lua.LState) int {
	tbl := L.NewTable()
	L.SetField(tbl, "run_id", lua.LNumber(r.deps.State.ID))
	L.SetField(tbl, "repo", lua.LString(r.deps.RepoPath))
	L.SetField(tbl, "iteration", lua.LNumber(r.callIndex))
	L.SetField(tbl, "prompt", lua.LString(r.deps.State.InitialPrompt))
	L.Push(tbl)
	return 1
}

// ── log() ─────────────────────────────────────────────────────────────────────

func (r *Runtime) luaLog(L *lua.LState) int {
	message := L.CheckString(1)
	r.logs = append(r.logs, message)
	r.emitLog(message)
	return 0
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (r *Runtime) emitLog(message string) {
	evt, _ := events.NewEvent(r.deps.State.ID, events.EventLogMessage, events.LogMessagePayload{Message: message})
	r.deps.EmitEvents([]events.Event{evt})
}

func (r *Runtime) setWaitingHuman(agent string, callIndex int, sessionID string, signal map[string]any) {
	r.waitingHuman = true
	r.waitingAgent = agent
	r.waitingCallIndex = callIndex
	r.waitingSessionID = sessionID
	if reason, ok := signal["reason"].(string); ok {
		r.waitingReason = reason
	} else {
		r.waitingReason = "Agent needs human input"
	}
}

func (r *Runtime) signalToTable(L *lua.LState, signal map[string]any) *lua.LTable {
	tbl := L.NewTable()
	for k, v := range signal {
		L.SetField(tbl, k, r.goToLua(L, v))
	}
	return tbl
}

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

func (r *Runtime) buildAgentPrompt(agent, prompt string) string {
	result := prompt
	if result == "" {
		result = r.deps.State.InitialPrompt
	}

	if r.callIndex > 1 {
		result += "\n\n---\n"
		result += "IMPORTANT: Call the `get_context` tool to retrieve context and summaries from previous agents before starting work."
	}

	result += fmt.Sprintf("\n\nYou are the '%s' agent in the '%s' workflow.", agent, r.deps.State.WorkflowName)
	result += fmt.Sprintf("\nUse `%s` for drafts or intermediate work.",
		filepath.Join(r.deps.WorkspacePath, "scratchpad", agent))

	result += "\n\n---\n"
	result += "IMPORTANT: When you have completed your task, you MUST call the `report_signal` tool to report your status.\n"
	result += "Valid statuses: " + strings.Join(events.ValidAgentStatusStrings(), ", ") + "\n"

	return result
}

func (r *Runtime) buildCheckpointPrompt(message string) string {
	return fmt.Sprintf(`The workflow has paused for human input.

**Checkpoint:** %s

**What to do:**
1. Review the workspace state
2. Check recent changes and test results
3. Decide whether to continue or stop

When ready, call the report_signal tool with your decision:
- To continue: report_signal(status="%s", summary="your note")
- To stop: report_signal(status="%s", summary="reason for stopping")

Wait for the human to provide guidance before reporting your decision.`,
		message, events.SignalContinue, events.SignalStop)
}

// IsWorkflow checks if a file is a Lua workflow.
func IsWorkflow(path string) bool {
	return filepath.Ext(path) == ".lua"
}
