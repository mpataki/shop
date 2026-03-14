package lua

import (
	"fmt"
	"os"
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
	storage     *storage.Storage
	run         *models.Run
	ws          *workspace.Workspace
	events      chan<- *models.WorkflowEvent
	callIndex   int
	logs        []string
	replayCache map[int]*storage.ReplayEntry

	// stuckReason is set when stuck() is called
	stuckReason string
	isStuck     bool

	// waitingHuman is set when an agent returns NEEDS_HUMAN
	waitingHuman     bool
	waitingReason    string
	waitingSessionID string
	waitingAgent     string
	waitingExecID    int64
}

// NewRuntime creates a new Lua runtime for executing a workflow
func NewRuntime(store *storage.Storage, run *models.Run, ws *workspace.Workspace, events chan<- *models.WorkflowEvent) *Runtime {
	return &Runtime{
		storage: store,
		run:     run,
		ws:      ws,
		events:  events,
		logs:    make([]string, 0),
	}
}

// appendEvent writes an immutable event to the workflow_events log and notifies the TUI.
// Storage errors are silently dropped — the executions table is the fallback source of truth.
func (r *Runtime) appendEvent(eventType models.WorkflowEventType, callIndex *int, agentName string, payload models.EventPayload) {
	e := &models.WorkflowEvent{
		RunID:     r.run.ID,
		Type:      eventType,
		CallIndex: callIndex,
		AgentName: agentName,
		Payload:   payload,
	}
	r.storage.AppendWorkflowEvent(e) //nolint:errcheck
	select {
	case r.events <- e:
	default:
	}
}

// Execute runs the Lua workflow script with the given prompt
func (r *Runtime) Execute(scriptPath, prompt string) error {
	// Load replay cache from the append-only event log.
	// Completed call sites are returned from cache; everything else runs fresh.
	wfEvents, err := r.storage.GetWorkflowEvents(r.run.ID)
	if err != nil {
		return fmt.Errorf("failed to load workflow events: %w", err)
	}
	r.replayCache = storage.BuildReplayCache(wfEvents)

	// Read the script
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("failed to read script: %w", err)
	}

	// Create new Lua state
	L := lua.NewState(lua.Options{
		SkipOpenLibs: true,
	})
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
			return r.markStuck()
		}
		if r.waitingHuman {
			return r.markWaitingHuman()
		}
		return fmt.Errorf("workflow execution failed: %w", err)
	}

	if r.isStuck {
		return r.markStuck()
	}
	if r.waitingHuman {
		return r.markWaitingHuman()
	}

	return r.markComplete()
}

// openSafeLibs loads only the safe standard libraries
func (r *Runtime) openSafeLibs(L *lua.LState) {
	lua.OpenBase(L)

	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("load", lua.LNil)
	L.SetGlobal("loadstring", lua.LNil)
	L.SetGlobal("print", lua.LNil) // Use log() instead

	lua.OpenTable(L)
	lua.OpenString(L)
	lua.OpenMath(L)

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

// markComplete marks the run as complete
func (r *Runtime) markComplete() error {
	now := time.Now()
	r.run.Status = models.RunStatusComplete
	r.run.CompletedAt = &now
	if err := r.storage.UpdateRun(r.run); err != nil {
		return err
	}
	r.appendEvent(models.WFEventRunCompleted, nil, "", models.RunCompletedPayload{})
	return nil
}

// markStuck marks the run as stuck
func (r *Runtime) markStuck() error {
	now := time.Now()
	r.run.Status = models.RunStatusStuck
	r.run.CompletedAt = &now
	r.run.Error = r.stuckReason
	if err := r.storage.UpdateRun(r.run); err != nil {
		return err
	}
	r.appendEvent(models.WFEventRunStuck, nil, "", models.RunStuckPayload{Reason: r.stuckReason})
	return nil
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
	// No appendEvent here — the agent_completed (NEEDS_HUMAN) event was already emitted by runAgent/runCheckpoint.
	return ErrWaitingHuman
}

// GetLogs returns the logs collected during execution
func (r *Runtime) GetLogs() []string {
	return r.logs
}

// IsWorkflow checks if a file is a Lua workflow
func IsWorkflow(path string) bool {
	return filepath.Ext(path) == ".lua"
}
