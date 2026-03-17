package events

import (
	"os"
	"path/filepath"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateRunAndAppendEvents(t *testing.T) {
	s := tempStore(t)

	runID, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	if runID < 1 {
		t.Fatalf("expected positive run ID, got %d", runID)
	}

	e1 := MustNewEvent(runID, EventRunStarted, RunStartedPayload{
		WorkflowPath: "/path/to/wf.js",
		WorkflowName: "test",
		InitialPrompt: "hello",
		WorkspacePath: "/tmp/ws",
	})

	appended, err := s.AppendEvents(runID, 0, []Event{e1})
	if err != nil {
		t.Fatal(err)
	}
	if len(appended) != 1 {
		t.Fatalf("expected 1 appended event, got %d", len(appended))
	}
	if appended[0].Version != 1 {
		t.Fatalf("expected version 1, got %d", appended[0].Version)
	}

	// Append more
	e2 := MustNewEvent(runID, EventRunCompleted, RunCompletedPayload{})
	appended2, err := s.AppendEvents(runID, 1, []Event{e2})
	if err != nil {
		t.Fatal(err)
	}
	if appended2[0].Version != 2 {
		t.Fatalf("expected version 2, got %d", appended2[0].Version)
	}

	// Verify GetEvents
	events, err := s.GetEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].EventType != EventRunStarted || events[1].EventType != EventRunCompleted {
		t.Fatalf("unexpected event types: %s, %s", events[0].EventType, events[1].EventType)
	}
}

func TestVersionConflict(t *testing.T) {
	s := tempStore(t)

	runID, _ := s.CreateRun()

	e := MustNewEvent(runID, EventRunStarted, RunStartedPayload{WorkflowName: "test"})
	_, err := s.AppendEvents(runID, 0, []Event{e})
	if err != nil {
		t.Fatal(err)
	}

	// Try to append at wrong version
	e2 := MustNewEvent(runID, EventRunCompleted, RunCompletedPayload{})
	_, err = s.AppendEvents(runID, 0, []Event{e2})
	if err != ErrVersionConflict {
		t.Fatalf("expected ErrVersionConflict, got %v", err)
	}
}

func TestGetEventsSince(t *testing.T) {
	s := tempStore(t)

	runID, _ := s.CreateRun()

	e1 := MustNewEvent(runID, EventRunStarted, RunStartedPayload{WorkflowName: "test"})
	e2 := MustNewEvent(runID, EventAgentStarted, AgentStartedPayload{AgentName: "coder", CallIndex: 1})
	_, err := s.AppendEvents(runID, 0, []Event{e1, e2})
	if err != nil {
		t.Fatal(err)
	}

	events, err := s.GetEventsSince(runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event after version 1, got %d", len(events))
	}
	if events[0].EventType != EventAgentStarted {
		t.Fatalf("expected AgentStarted, got %s", events[0].EventType)
	}
}

func TestCommandCRUD(t *testing.T) {
	s := tempStore(t)

	runID, _ := s.CreateRun()

	// Submit
	err := s.SubmitCommand("cmd-1", runID, "StartRun", []byte(`{"workflow_name":"test"}`))
	if err != nil {
		t.Fatal(err)
	}

	// Get pending
	cmds, err := s.GetPendingCommands(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 pending command, got %d", len(cmds))
	}
	if cmds[0].CommandType != "StartRun" {
		t.Fatalf("expected StartRun, got %s", cmds[0].CommandType)
	}

	// Mark processed
	err = s.MarkCommandProcessed("cmd-1")
	if err != nil {
		t.Fatal(err)
	}

	cmds, _ = s.GetPendingCommands(runID)
	if len(cmds) != 0 {
		t.Fatalf("expected 0 pending after processing, got %d", len(cmds))
	}

	// Submit and fail
	err = s.SubmitCommand("cmd-2", runID, "KillRun", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	err = s.MarkCommandFailed("cmd-2", "run not active")
	if err != nil {
		t.Fatal(err)
	}

	cmds, _ = s.GetPendingCommands(runID)
	if len(cmds) != 0 {
		t.Fatalf("expected 0 pending after failure, got %d", len(cmds))
	}
}

func TestListRunIDs(t *testing.T) {
	s := tempStore(t)

	id1, _ := s.CreateRun()
	id2, _ := s.CreateRun()
	id3, _ := s.CreateRun()

	runs, err := s.ListRunIDs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	// Newest first (highest ID first since created_at has same precision)
	if runs[0].ID != id3 || runs[1].ID != id2 || runs[2].ID != id1 {
		t.Fatalf("expected order %d,%d,%d but got %d,%d,%d", id3, id2, id1, runs[0].ID, runs[1].ID, runs[2].ID)
	}
}

func TestProjectRunFromDB(t *testing.T) {
	s := tempStore(t)

	runID, _ := s.CreateRun()

	e1 := MustNewEvent(runID, EventRunStarted, RunStartedPayload{
		WorkflowName: "deploy",
		InitialPrompt: "deploy it",
	})
	e2 := MustNewEvent(runID, EventAgentStarted, AgentStartedPayload{
		AgentName: "coder", CallIndex: 1, SessionID: "sess-1", PID: 123,
	})
	e3 := MustNewEvent(runID, EventAgentCompleted, AgentCompletedPayload{
		AgentName: "coder", CallIndex: 1, Signal: map[string]any{"status": "DONE"},
	})
	e4 := MustNewEvent(runID, EventRunCompleted, RunCompletedPayload{})

	_, err := s.AppendEvents(runID, 0, []Event{e1, e2, e3, e4})
	if err != nil {
		t.Fatal(err)
	}

	state, err := s.ProjectRunFromDB(runID)
	if err != nil {
		t.Fatal(err)
	}

	if state.Status != RunStatusComplete {
		t.Fatalf("expected complete, got %s", state.Status)
	}
	if state.WorkflowName != "deploy" {
		t.Fatalf("expected deploy, got %s", state.WorkflowName)
	}
	if len(state.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(state.Executions))
	}
	if state.Executions[0].Status != ExecStatusCompleted {
		t.Fatalf("expected completed execution, got %s", state.Executions[0].Status)
	}
}

func TestStoreDBFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.DBPath() != dbPath {
		t.Fatalf("expected %s, got %s", dbPath, s.DBPath())
	}

	// Verify DB file exists
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}
