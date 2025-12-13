package storage

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/mpataki/shop/internal/models"
	_ "modernc.org/sqlite"
)

type Storage struct {
	db *sql.DB
}

func New(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	s := &Storage{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
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
		spec_name TEXT NOT NULL,
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

	return nil
}

func (s *Storage) CreateRun(run *models.Run) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO runs (initial_prompt, spec_name, workspace_path, status, current_agent)
		 VALUES (?, ?, ?, ?, ?)`,
		run.InitialPrompt, run.SpecName, run.WorkspacePath, run.Status, run.CurrentAgent,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Storage) GetRun(id int64) (*models.Run, error) {
	row := s.db.QueryRow(
		`SELECT id, created_at, completed_at, initial_prompt, spec_name, workspace_path, status, current_agent
		 FROM runs WHERE id = ?`, id,
	)

	var run models.Run
	var completedAt sql.NullTime
	var currentAgent sql.NullString

	err := row.Scan(
		&run.ID, &run.CreatedAt, &completedAt, &run.InitialPrompt,
		&run.SpecName, &run.WorkspacePath, &run.Status, &currentAgent,
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

	return &run, nil
}

func (s *Storage) UpdateRun(run *models.Run) error {
	_, err := s.db.Exec(
		`UPDATE runs SET completed_at = ?, status = ?, current_agent = ?, workspace_path = ? WHERE id = ?`,
		run.CompletedAt, run.Status, run.CurrentAgent, run.WorkspacePath, run.ID,
	)
	return err
}

func (s *Storage) ListRuns(limit int) ([]*models.Run, error) {
	rows, err := s.db.Query(
		`SELECT id, created_at, completed_at, initial_prompt, spec_name, workspace_path, status, current_agent
		 FROM runs ORDER BY created_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*models.Run
	for rows.Next() {
		var run models.Run
		var completedAt sql.NullTime
		var currentAgent sql.NullString

		err := rows.Scan(
			&run.ID, &run.CreatedAt, &completedAt, &run.InitialPrompt,
			&run.SpecName, &run.WorkspacePath, &run.Status, &currentAgent,
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

		runs = append(runs, &run)
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
		`INSERT INTO executions (run_id, agent_name, claude_session_id, status, exit_code, started_at, completed_at, output_signal, sequence_num, pid)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		exec.RunID, exec.AgentName, exec.ClaudeSessionID, exec.Status,
		exec.ExitCode, exec.StartedAt, exec.CompletedAt, signalJSON, exec.SequenceNum, exec.PID,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Storage) GetExecutionsForRun(runID int64) ([]*models.Execution, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, agent_name, claude_session_id, status, exit_code, started_at, completed_at, output_signal, sequence_num, pid
		 FROM executions WHERE run_id = ? ORDER BY sequence_num`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []*models.Execution
	for rows.Next() {
		var exec models.Execution
		var sessionID, signalJSON sql.NullString
		var exitCode, pid sql.NullInt64
		var startedAt, completedAt sql.NullTime

		err := rows.Scan(
			&exec.ID, &exec.RunID, &exec.AgentName, &sessionID, &exec.Status,
			&exitCode, &startedAt, &completedAt, &signalJSON, &exec.SequenceNum, &pid,
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

		execs = append(execs, &exec)
	}

	return execs, rows.Err()
}

func (s *Storage) GetRunningExecutionForRun(runID int64) (*models.Execution, error) {
	execs, err := s.GetExecutionsForRun(runID)
	if err != nil {
		return nil, err
	}
	for _, exec := range execs {
		if exec.Status == models.ExecStatusRunning {
			return exec, nil
		}
	}
	return nil, nil
}

func (s *Storage) UpdateExecutionPID(execID int64, pid int) error {
	_, err := s.db.Exec(`UPDATE executions SET pid = ? WHERE id = ?`, pid, execID)
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

// Helper to format time for display
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
