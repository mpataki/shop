package events

import (
	"testing"
	"time"
)

func TestProjectRunStarted(t *testing.T) {
	events := []Event{
		MustNewEvent(1, EventRunStarted, RunStartedPayload{
			WorkflowPath:  "/wf.js",
			WorkflowName:  "deploy",
			InitialPrompt: "deploy it",
			WorkspacePath: "/tmp/ws",
		}),
	}
	events[0].Version = 1

	state := ProjectRun(1, time.Now(), events)

	if state.Status != RunStatusRunning {
		t.Fatalf("expected running, got %s", state.Status)
	}
	if state.WorkflowName != "deploy" {
		t.Fatalf("expected deploy, got %s", state.WorkflowName)
	}
	if state.InitialPrompt != "deploy it" {
		t.Fatalf("expected 'deploy it', got %s", state.InitialPrompt)
	}
}

func TestProjectRunFullLifecycle(t *testing.T) {
	now := time.Now()
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{
			WorkflowName: "build", InitialPrompt: "build it", WorkspacePath: "/ws",
		}), 1, now),
		withVersion(MustNewEvent(1, EventAgentStarted, AgentStartedPayload{
			AgentName: "coder", CallIndex: 1, SessionID: "s1", PID: 100,
		}), 2, now),
		withVersion(MustNewEvent(1, EventAgentCompleted, AgentCompletedPayload{
			AgentName: "coder", CallIndex: 1, Signal: map[string]any{"status": "DONE", "summary": "built it"},
		}), 3, now),
		withVersion(MustNewEvent(1, EventAgentStarted, AgentStartedPayload{
			AgentName: "reviewer", CallIndex: 2, SessionID: "s2", PID: 200,
		}), 4, now),
		withVersion(MustNewEvent(1, EventAgentCompleted, AgentCompletedPayload{
			AgentName: "reviewer", CallIndex: 2, Signal: map[string]any{"status": "APPROVED"},
		}), 5, now),
		withVersion(MustNewEvent(1, EventRunCompleted, RunCompletedPayload{}), 6, now),
	}

	state := ProjectRun(1, now, events)

	if state.Status != RunStatusComplete {
		t.Fatalf("expected complete, got %s", state.Status)
	}
	if len(state.Executions) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(state.Executions))
	}
	if state.Executions[0].AgentName != "coder" {
		t.Fatalf("expected coder, got %s", state.Executions[0].AgentName)
	}
	if state.Executions[1].AgentName != "reviewer" {
		t.Fatalf("expected reviewer, got %s", state.Executions[1].AgentName)
	}
	if state.Executions[0].Status != ExecStatusCompleted {
		t.Fatalf("expected completed, got %s", state.Executions[0].Status)
	}
}

func TestProjectRunFailed(t *testing.T) {
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, time.Now()),
		withVersion(MustNewEvent(1, EventRunFailed, RunFailedPayload{Error: "boom"}), 2, time.Now()),
	}

	state := ProjectRun(1, time.Now(), events)

	if state.Status != RunStatusFailed {
		t.Fatalf("expected failed, got %s", state.Status)
	}
	if state.Error != "boom" {
		t.Fatalf("expected 'boom', got %s", state.Error)
	}
}

func TestProjectRunStuck(t *testing.T) {
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, time.Now()),
		withVersion(MustNewEvent(1, EventRunStuck, RunStuckPayload{Reason: "code blocked"}), 2, time.Now()),
	}

	state := ProjectRun(1, time.Now(), events)

	if state.Status != RunStatusStuck {
		t.Fatalf("expected stuck, got %s", state.Status)
	}
	if state.WaitingReason != "code blocked" {
		t.Fatalf("expected 'code blocked', got %s", state.WaitingReason)
	}
}

func TestProjectRunNeedsHuman(t *testing.T) {
	now := time.Now()
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, now),
		withVersion(MustNewEvent(1, EventAgentStarted, AgentStartedPayload{
			AgentName: "coder", CallIndex: 1, SessionID: "s1", PID: 100,
		}), 2, now),
		withVersion(MustNewEvent(1, EventAgentCompleted, AgentCompletedPayload{
			AgentName: "coder", CallIndex: 1,
			Signal: map[string]any{"status": "NEEDS_HUMAN", "reason": "help me"},
		}), 3, now),
		withVersion(MustNewEvent(1, EventRunWaitingHuman, RunWaitingHumanPayload{
			Reason: "help me", CallIndex: 1, SessionID: "s1",
		}), 4, now),
	}

	state := ProjectRun(1, now, events)

	if state.Status != RunStatusWaitingHuman {
		t.Fatalf("expected waiting_human, got %s", state.Status)
	}
	if state.WaitingReason != "help me" {
		t.Fatalf("expected 'help me', got %s", state.WaitingReason)
	}
	if state.WaitingSessionID != "s1" {
		t.Fatalf("expected session s1, got %s", state.WaitingSessionID)
	}
	// Execution should be waiting_human
	exec := state.GetExecutionByCallIndex(1)
	if exec == nil {
		t.Fatal("expected execution at call_index 1")
	}
	if exec.Status != ExecStatusWaitingHuman {
		t.Fatalf("expected waiting_human, got %s", exec.Status)
	}
}

func TestProjectRunNeedsHumanThenResume(t *testing.T) {
	now := time.Now()
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, now),
		withVersion(MustNewEvent(1, EventAgentStarted, AgentStartedPayload{
			AgentName: "coder", CallIndex: 1, SessionID: "s1", PID: 100,
		}), 2, now),
		withVersion(MustNewEvent(1, EventAgentCompleted, AgentCompletedPayload{
			AgentName: "coder", CallIndex: 1,
			Signal: map[string]any{"status": "NEEDS_HUMAN", "reason": "help"},
		}), 3, now),
		withVersion(MustNewEvent(1, EventRunWaitingHuman, RunWaitingHumanPayload{
			Reason: "help", CallIndex: 1, SessionID: "s1",
		}), 4, now),
		withVersion(MustNewEvent(1, EventHumanInputReceived, HumanInputReceivedPayload{
			CallIndex: 1, Signal: map[string]any{"status": "DONE", "summary": "fixed"},
		}), 5, now),
		withVersion(MustNewEvent(1, EventRunResumed, RunResumedPayload{}), 6, now),
		withVersion(MustNewEvent(1, EventRunCompleted, RunCompletedPayload{}), 7, now),
	}

	state := ProjectRun(1, now, events)

	if state.Status != RunStatusComplete {
		t.Fatalf("expected complete, got %s", state.Status)
	}
	exec := state.GetExecutionByCallIndex(1)
	if exec.Status != ExecStatusCompleted {
		t.Fatalf("expected completed, got %s", exec.Status)
	}
	if exec.Signal["status"] != "DONE" {
		t.Fatalf("expected DONE signal, got %v", exec.Signal["status"])
	}
}

func TestProjectRunKilled(t *testing.T) {
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, time.Now()),
		withVersion(MustNewEvent(1, EventRunKilled, RunKilledPayload{}), 2, time.Now()),
	}

	state := ProjectRun(1, time.Now(), events)

	if state.Status != RunStatusKilled {
		t.Fatalf("expected killed, got %s", state.Status)
	}
	if !state.Status.IsTerminal() {
		t.Fatal("killed should be terminal")
	}
}

func TestProjectRunStopped(t *testing.T) {
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, time.Now()),
		withVersion(MustNewEvent(1, EventRunStopped, RunStoppedPayload{Reason: "done"}), 2, time.Now()),
	}

	state := ProjectRun(1, time.Now(), events)

	if state.Status != RunStatusStuck {
		t.Fatalf("expected stuck (stopped maps to stuck), got %s", state.Status)
	}
}

func TestProjectRunDeleted(t *testing.T) {
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, time.Now()),
		withVersion(MustNewEvent(1, EventRunDeleted, RunDeletedPayload{}), 2, time.Now()),
	}

	state := ProjectRun(1, time.Now(), events)

	if state.Status != RunStatusDeleted {
		t.Fatalf("expected deleted, got %s", state.Status)
	}
}

func TestProjectRunLogMessages(t *testing.T) {
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, time.Now()),
		withVersion(MustNewEvent(1, EventLogMessage, LogMessagePayload{Message: "hello"}), 2, time.Now()),
		withVersion(MustNewEvent(1, EventLogMessage, LogMessagePayload{Message: "world"}), 3, time.Now()),
	}

	state := ProjectRun(1, time.Now(), events)

	if len(state.LogMessages) != 2 {
		t.Fatalf("expected 2 log messages, got %d", len(state.LogMessages))
	}
	if state.LogMessages[0].Message != "hello" {
		t.Fatalf("expected hello, got %s", state.LogMessages[0].Message)
	}
}

func TestProjectRunCheckpoint(t *testing.T) {
	now := time.Now()
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, now),
		withVersion(MustNewEvent(1, EventCheckpointStarted, CheckpointStartedPayload{
			CallIndex: 1, Message: "approve?", SessionID: "s1",
		}), 2, now),
		withVersion(MustNewEvent(1, EventCheckpointCompleted, CheckpointCompletedPayload{
			CallIndex: 1, Signal: map[string]any{"status": "CONTINUE"},
		}), 3, now),
		withVersion(MustNewEvent(1, EventRunCompleted, RunCompletedPayload{}), 4, now),
	}

	state := ProjectRun(1, now, events)

	if state.Status != RunStatusComplete {
		t.Fatalf("expected complete, got %s", state.Status)
	}
	if len(state.Executions) != 1 {
		t.Fatalf("expected 1 execution (checkpoint), got %d", len(state.Executions))
	}
	exec := state.Executions[0]
	if exec.AgentName != "_checkpoint" {
		t.Fatalf("expected _checkpoint, got %s", exec.AgentName)
	}
	if exec.Status != ExecStatusCompleted {
		t.Fatalf("expected completed, got %s", exec.Status)
	}
}

func TestProjectRunAgentFailed(t *testing.T) {
	now := time.Now()
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, now),
		withVersion(MustNewEvent(1, EventAgentStarted, AgentStartedPayload{
			AgentName: "coder", CallIndex: 1, SessionID: "s1", PID: 100,
		}), 2, now),
		withVersion(MustNewEvent(1, EventAgentFailed, AgentFailedPayload{
			AgentName: "coder", CallIndex: 1, Error: "exit 1", ExitCode: 1,
		}), 3, now),
	}

	state := ProjectRun(1, now, events)

	exec := state.GetExecutionByCallIndex(1)
	if exec == nil {
		t.Fatal("expected execution at call_index 1")
	}
	if exec.Status != ExecStatusFailed {
		t.Fatalf("expected failed, got %s", exec.Status)
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []RunStatus{RunStatusComplete, RunStatusFailed, RunStatusStuck, RunStatusKilled, RunStatusDeleted}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}

	nonTerminal := []RunStatus{RunStatusPending, RunStatusRunning, RunStatusWaitingHuman}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Fatalf("%s should NOT be terminal", s)
		}
	}
}

func TestActivePID(t *testing.T) {
	now := time.Now()
	events := []Event{
		withVersion(MustNewEvent(1, EventRunStarted, RunStartedPayload{WorkflowName: "test"}), 1, now),
		withVersion(MustNewEvent(1, EventAgentStarted, AgentStartedPayload{
			AgentName: "coder", CallIndex: 1, SessionID: "s1", PID: 42,
		}), 2, now),
	}

	state := ProjectRun(1, now, events)
	if state.ActivePID() != 42 {
		t.Fatalf("expected PID 42, got %d", state.ActivePID())
	}

	// After completion, no active PID
	events = append(events, withVersion(MustNewEvent(1, EventAgentCompleted, AgentCompletedPayload{
		AgentName: "coder", CallIndex: 1, Signal: map[string]any{"status": "DONE"},
	}), 3, now))

	state = ProjectRun(1, now, events)
	if state.ActivePID() != 0 {
		t.Fatalf("expected 0 after completion, got %d", state.ActivePID())
	}
}

func TestEmptyProjection(t *testing.T) {
	state := ProjectRun(1, time.Now(), nil)
	if state.Status != RunStatusPending {
		t.Fatalf("expected pending with no events, got %s", state.Status)
	}
	if state.Version != 0 {
		t.Fatalf("expected version 0, got %d", state.Version)
	}
}

// helper
func withVersion(e Event, version int, ts time.Time) Event {
	e.Version = version
	e.CreatedAt = ts
	return e
}
