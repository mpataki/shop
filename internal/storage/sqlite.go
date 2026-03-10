package storage

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/mpataki/shop/internal/models"
	_ "modernc.org/sqlite"
)

type Storage struct {
	db     *sql.DB
	dbPath string
}

func New(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// WAL mode allows concurrent reads + one writer without SQLITE_BUSY
	// busy_timeout retries for 5s instead of failing immediately on lock contention
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	s := &Storage{db: db, dbPath: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Storage) DBPath() string {
	return s.dbPath
}

func (s *Storage) Close() error {
	return s.db.Close()
}

func (s *Storage) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		completed_at TIMESTAMP,
		initial_prompt TEXT NOT NULL,
		workflow_name TEXT NOT NULL,
		workspace_path TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		current_agent TEXT
	);

	CREATE TABLE IF NOT EXISTS executions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id INTEGER NOT NULL REFERENCES runs(id),
		agent_name TEXT NOT NULL,
		claude_session_id TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		exit_code INTEGER,
		started_at TIMESTAMP,
		completed_at TIMESTAMP,
		output_signal TEXT,
		sequence_num INTEGER NOT NULL,
		pid INTEGER,
		UNIQUE(run_id, sequence_num)
	);

	CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);
	CREATE INDEX IF NOT EXISTS idx_executions_run ON executions(run_id);
	`

	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Migration: add pid column if it doesn't exist
	s.db.Exec(`ALTER TABLE executions ADD COLUMN pid INTEGER`)

	// Migration: add Lua workflow support columns
	s.db.Exec(`ALTER TABLE runs ADD COLUMN workflow_path TEXT`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN error TEXT`)
	s.db.Exec(`ALTER TABLE executions ADD COLUMN call_index INTEGER`)
	s.db.Exec(`ALTER TABLE executions ADD COLUMN prompt TEXT`)

	// Create index for Lua call_index lookups
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_executions_call_index ON executions(run_id, call_index)`)

	// Migration: add human interaction columns
	s.db.Exec(`ALTER TABLE runs ADD COLUMN waiting_reason TEXT`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN waiting_session_id TEXT`)

	// Migration: add model column to executions
	s.db.Exec(`ALTER TABLE executions ADD COLUMN model TEXT`)

	return nil
}

// scanRun scans a Run from a row, reducing boilerplate across queries.
func scanRun(scanner interface{ Scan(...any) error }) (*models.Run, error) {
	var run models.Run
	var completedAt sql.NullTime
	var currentAgent, workflowPath, runError, waitingReason, waitingSessionID sql.NullString

	err := scanner.Scan(
		&run.ID, &run.CreatedAt, &completedAt, &run.InitialPrompt,
		&run.WorkflowName, &run.WorkspacePath, &run.Status, &currentAgent,
		&workflowPath, &runError, &waitingReason, &waitingSessionID,
	)
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	if currentAgent.Valid {
		run.CurrentAgent = currentAgent.String
	}
	if workflowPath.Valid {
		run.WorkflowPath = workflowPath.String
	}
	if runError.Valid {
		run.Error = runError.String
	}
	if waitingReason.Valid {
		run.WaitingReason = waitingReason.String
	}
	if waitingSessionID.Valid {
		run.WaitingSessionID = waitingSessionID.String
	}

	return &run, nil
}

const runColumns = `id, created_at, completed_at, initial_prompt, workflow_name, workspace_path, status, current_agent, workflow_path, error, waiting_reason, waiting_session_id`

// scanExecution scans an Execution from a row, reducing boilerplate across queries.
func scanExecution(scanner interface{ Scan(...any) error }) (*models.Execution, error) {
	var exec models.Execution
	var sessionID, signalJSON, prompt, model sql.NullString
	var exitCode, pid, callIndex sql.NullInt64
	var startedAt, completedAt sql.NullTime

	err := scanner.Scan(
		&exec.ID, &exec.RunID, &exec.AgentName, &sessionID, &exec.Status,
		&exitCode, &startedAt, &completedAt, &signalJSON, &exec.SequenceNum,
		&pid, &callIndex, &prompt, &model,
	)
	if err != nil {
		return nil, err
	}

	if sessionID.Valid {
		exec.ClaudeSessionID = sessionID.String
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		exec.ExitCode = &code
	}
	if startedAt.Valid {
		exec.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		exec.CompletedAt = &completedAt.Time
	}
	if signalJSON.Valid {
		var signal map[string]any
		if err := json.Unmarshal([]byte(signalJSON.String), &signal); err == nil {
			exec.OutputSignal = signal
		}
	}
	if pid.Valid {
		p := int(pid.Int64)
		exec.PID = &p
	}
	if callIndex.Valid {
		exec.CallIndex = int(callIndex.Int64)
	}
	if prompt.Valid {
		exec.Prompt = prompt.String
	}
	if model.Valid {
		exec.Model = model.String
	}

	return &exec, nil
}

const execColumns = `id, run_id, agent_name, claude_session_id, status, exit_code, started_at, completed_at, output_signal, sequence_num, pid, call_index, prompt, model`

func (s *Storage) CreateRun(run *models.Run) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO runs (initial_prompt, workflow_name, workspace_path, status, current_agent, workflow_path, error, waiting_reason, waiting_session_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.InitialPrompt, run.WorkflowName, run.WorkspacePath, run.Status, run.CurrentAgent, run.WorkflowPath, run.Error,
		run.WaitingReason, run.WaitingSessionID,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Storage) GetRun(id int64) (*models.Run, error) {
	row := s.db.QueryRow(`SELECT `+runColumns+` FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *Storage) UpdateRun(run *models.Run) error {
	_, err := s.db.Exec(
		`UPDATE runs SET completed_at = ?, status = ?, current_agent = ?, workspace_path = ?, workflow_path = ?, error = ?, waiting_reason = ?, waiting_session_id = ? WHERE id = ?`,
		run.CompletedAt, run.Status, run.CurrentAgent, run.WorkspacePath, run.WorkflowPath, run.Error, run.WaitingReason, run.WaitingSessionID, run.ID,
	)
	return err
}

func (s *Storage) ListRuns(limit int) ([]*models.Run, error) {
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*models.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	return runs, rows.Err()
}

func (s *Storage) CreateExecution(exec *models.Execution) (int64, error) {
	var signalJSON *string
	if exec.OutputSignal != nil {
		data, err := json.Marshal(exec.OutputSignal)
		if err != nil {
			return 0, err
		}
		str := string(data)
		signalJSON = &str
	}

	result, err := s.db.Exec(
		`INSERT INTO executions (`+execColumns[4:]+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		exec.RunID, exec.AgentName, exec.ClaudeSessionID, exec.Status,
		exec.ExitCode, exec.StartedAt, exec.CompletedAt, signalJSON, exec.SequenceNum, exec.PID, exec.CallIndex, exec.Prompt, exec.Model,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Storage) GetExecutionsForRun(runID int64) ([]*models.Execution, error) {
	rows, err := s.db.Query(
		`SELECT `+execColumns+` FROM executions WHERE run_id = ? ORDER BY sequence_num`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []*models.Execution
	for rows.Next() {
		exec, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		execs = append(execs, exec)
	}

	return execs, rows.Err()
}

func (s *Storage) GetRunningExecutionForRun(runID int64) (*models.Execution, error) {
	row := s.db.QueryRow(
		`SELECT `+execColumns+` FROM executions WHERE run_id = ? AND status = ? LIMIT 1`,
		runID, models.ExecStatusRunning,
	)
	exec, err := scanExecution(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return exec, err
}

func (s *Storage) UpdateExecutionPID(execID int64, pid int) error {
	_, err := s.db.Exec(`UPDATE executions SET pid = ? WHERE id = ?`, pid, execID)
	return err
}

func (s *Storage) UpdateExecutionSessionID(execID int64, sessionID string) error {
	_, err := s.db.Exec(`UPDATE executions SET claude_session_id = ? WHERE id = ?`, sessionID, execID)
	return err
}

func (s *Storage) UpdateExecution(exec *models.Execution) error {
	var signalJSON *string
	if exec.OutputSignal != nil {
		data, err := json.Marshal(exec.OutputSignal)
		if err != nil {
			return err
		}
		str := string(data)
		signalJSON = &str
	}

	_, err := s.db.Exec(
		`UPDATE executions SET claude_session_id = ?, status = ?, exit_code = ?, started_at = ?, completed_at = ?, output_signal = ?
		 WHERE id = ?`,
		exec.ClaudeSessionID, exec.Status, exec.ExitCode, exec.StartedAt, exec.CompletedAt, signalJSON, exec.ID,
	)
	return err
}

func (s *Storage) DeleteRun(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM executions WHERE run_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM runs WHERE id = ?`, id); err != nil {
		return err
	}

	return tx.Commit()
}

// FormatTimeAgo formats a time as a human-readable relative string.
func FormatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return time.Duration(d.Minutes()).String() + "m ago"
	case d < 24*time.Hour:
		return time.Duration(d.Hours()).String() + "h ago"
	default:
		return t.Format("Jan 2")
	}
}

// GetExecutionByCallIndex gets an execution by run_id and call_index (for Lua workflows)
func (s *Storage) GetExecutionByCallIndex(runID int64, callIndex int) (*models.Execution, error) {
	row := s.db.QueryRow(
		`SELECT `+execColumns+` FROM executions WHERE run_id = ? AND call_index = ?`, runID, callIndex,
	)
	exec, err := scanExecution(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return exec, nil
}

// InvalidateExecutionsAfterIndex marks all executions after callIndex as failed (for Lua workflow recovery)
func (s *Storage) InvalidateExecutionsAfterIndex(runID int64, callIndex int) error {
	_, err := s.db.Exec(
		`UPDATE executions SET status = ? WHERE run_id = ? AND call_index > ?`,
		models.ExecStatusFailed, runID, callIndex,
	)
	return err
}

// GetWaitingExecutionForRun returns the execution that is waiting for human input
func (s *Storage) GetWaitingExecutionForRun(runID int64) (*models.Execution, error) {
	row := s.db.QueryRow(
		`SELECT `+execColumns+` FROM executions WHERE run_id = ? AND status = ? LIMIT 1`,
		runID, models.ExecStatusWaitingHuman,
	)
	exec, err := scanExecution(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return exec, err
}

// GetExecution fetches a single execution by ID.
func (s *Storage) GetExecution(id int64) (*models.Execution, error) {
	row := s.db.QueryRow(`SELECT `+execColumns+` FROM executions WHERE id = ?`, id)
	exec, err := scanExecution(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return exec, err
}

// UpdateExecutionSignal sets the output_signal for an execution (called by MCP server).
func (s *Storage) UpdateExecutionSignal(execID int64, signal map[string]any) error {
	data, err := json.Marshal(signal)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE executions SET output_signal = ? WHERE id = ?`, string(data), execID)
	return err
}

// ListWaitingRuns returns runs that are waiting for human input
func (s *Storage) ListWaitingRuns() ([]*models.Run, error) {
	rows, err := s.db.Query(
		`SELECT `+runColumns+` FROM runs WHERE status = ? ORDER BY created_at DESC`,
		models.RunStatusWaitingHuman,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*models.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	return runs, rows.Err()
}
