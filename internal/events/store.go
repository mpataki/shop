package events

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database for event sourcing.
type Store struct {
	db     *sql.DB
	dbPath string
}

// NewStore opens (or creates) the event-sourced database.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")

	s := &Store{db: db, dbPath: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) DBPath() string { return s.dbPath }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		version INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id INTEGER NOT NULL,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		version INTEGER NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(run_id, version)
	);

	CREATE INDEX IF NOT EXISTS idx_events_run ON events(run_id, version);

	CREATE TABLE IF NOT EXISTS commands (
		id TEXT PRIMARY KEY,
		run_id INTEGER NOT NULL,
		command_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'pending',
		error TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		processed_at TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_commands_pending ON commands(status, created_at)
		WHERE status = 'pending';
	CREATE INDEX IF NOT EXISTS idx_commands_run ON commands(run_id);
	`

	_, err := s.db.Exec(schema)
	return err
}

// CreateRun inserts a new run row and returns its ID.
func (s *Store) CreateRun() (int64, error) {
	result, err := s.db.Exec(`INSERT INTO runs (version) VALUES (0)`)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// AppendEvents atomically appends events to a run's event stream.
// Returns ErrVersionConflict if the expected version doesn't match.
func (s *Store) AppendEvents(runID int64, expectedVersion int, newEvents []Event) ([]Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Check current version
	var currentVersion int
	err = tx.QueryRow(`SELECT version FROM runs WHERE id = ?`, runID).Scan(&currentVersion)
	if err != nil {
		return nil, fmt.Errorf("run %d not found: %w", runID, err)
	}
	if currentVersion != expectedVersion {
		return nil, ErrVersionConflict
	}

	now := time.Now()
	result := make([]Event, len(newEvents))
	for i, e := range newEvents {
		version := expectedVersion + i + 1
		res, err := tx.Exec(
			`INSERT INTO events (run_id, event_type, payload, version, created_at) VALUES (?, ?, ?, ?, ?)`,
			runID, string(e.EventType), string(e.Payload), version, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert event (version %d): %w", version, err)
		}
		id, _ := res.LastInsertId()
		result[i] = Event{
			ID:        id,
			RunID:     runID,
			EventType: e.EventType,
			Payload:   e.Payload,
			Version:   version,
			CreatedAt: now,
		}
	}

	newVersion := expectedVersion + len(newEvents)
	_, err = tx.Exec(`UPDATE runs SET version = ? WHERE id = ?`, newVersion, runID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

// ErrVersionConflict is returned when optimistic locking fails.
var ErrVersionConflict = fmt.Errorf("version conflict")

// GetEvents returns all events for a run in version order.
func (s *Store) GetEvents(runID int64) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, event_type, payload, version, created_at FROM events WHERE run_id = ? ORDER BY version`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// GetEventsSince returns events for a run after the given version.
func (s *Store) GetEventsSince(runID int64, afterVersion int) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, event_type, payload, version, created_at FROM events WHERE run_id = ? AND version > ? ORDER BY version`,
		runID, afterVersion,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.RunID, &e.EventType, &payload, &e.Version, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}
	return events, rows.Err()
}

// RunInfo holds the minimal run row data.
type RunInfo struct {
	ID        int64
	CreatedAt time.Time
	Version   int
}

// GetRun returns the run row.
func (s *Store) GetRun(id int64) (*RunInfo, error) {
	var r RunInfo
	err := s.db.QueryRow(`SELECT id, created_at, version FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.CreatedAt, &r.Version)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRunIDs returns all run IDs ordered by creation time (newest first).
func (s *Store) ListRunIDs(limit int) ([]RunInfo, error) {
	rows, err := s.db.Query(`SELECT id, created_at, version FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []RunInfo
	for rows.Next() {
		var r RunInfo
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.Version); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ProjectRunFromDB loads events and projects state for a run.
func (s *Store) ProjectRunFromDB(runID int64) (*RunState, error) {
	info, err := s.GetRun(runID)
	if err != nil {
		return nil, err
	}
	events, err := s.GetEvents(runID)
	if err != nil {
		return nil, err
	}
	return ProjectRun(info.ID, info.CreatedAt, events), nil
}

// ── Command CRUD ──────────────────────────────────────────────────────────────

// SubmitCommand inserts a new command into the commands table.
func (s *Store) SubmitCommand(id string, runID int64, cmdType string, payload json.RawMessage) error {
	_, err := s.db.Exec(
		`INSERT INTO commands (id, run_id, command_type, payload, status) VALUES (?, ?, ?, ?, 'pending')`,
		id, runID, cmdType, string(payload),
	)
	return err
}

// CommandRow represents a row from the commands table.
type CommandRow struct {
	ID          string
	RunID       int64
	CommandType string
	Payload     json.RawMessage
	Status      string
	Error       *string
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

// GetPendingCommands returns pending commands for a run, ordered by creation time.
func (s *Store) GetPendingCommands(runID int64) ([]CommandRow, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, command_type, payload, status, error, created_at, processed_at
		 FROM commands WHERE run_id = ? AND status = 'pending' ORDER BY created_at`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommands(rows)
}

// GetAllPendingCommands returns all pending commands across all runs.
func (s *Store) GetAllPendingCommands() ([]CommandRow, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, command_type, payload, status, error, created_at, processed_at
		 FROM commands WHERE status = 'pending' ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommands(rows)
}

func scanCommands(rows *sql.Rows) ([]CommandRow, error) {
	var cmds []CommandRow
	for rows.Next() {
		var c CommandRow
		var payload string
		var errStr sql.NullString
		var processedAt sql.NullTime
		if err := rows.Scan(&c.ID, &c.RunID, &c.CommandType, &payload, &c.Status, &errStr, &c.CreatedAt, &processedAt); err != nil {
			return nil, err
		}
		c.Payload = json.RawMessage(payload)
		if errStr.Valid {
			c.Error = &errStr.String
		}
		if processedAt.Valid {
			c.ProcessedAt = &processedAt.Time
		}
		cmds = append(cmds, c)
	}
	return cmds, rows.Err()
}

// MarkCommandProcessed marks a command as processed.
func (s *Store) MarkCommandProcessed(id string) error {
	_, err := s.db.Exec(
		`UPDATE commands SET status = 'processed', processed_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
	)
	return err
}

// MarkCommandFailed marks a command as failed with an error message.
func (s *Store) MarkCommandFailed(id string, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE commands SET status = 'failed', error = ?, processed_at = CURRENT_TIMESTAMP WHERE id = ?`,
		errMsg, id,
	)
	return err
}

// FormatTimeAgo formats a time as a human-readable relative string.
func FormatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}
