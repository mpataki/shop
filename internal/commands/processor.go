package commands

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/mpataki/shop/internal/events"
	"github.com/mpataki/shop/internal/process"
)

// Processor handles commands for all runs.
type Processor struct {
	store          *events.Store
	processManager process.Manager
	workspacesDir  string

	mu          sync.Mutex
	activeRuns  map[int64]chan struct{} // notify channels per run
	subscribers []chan events.Event     // fan-out event subscribers
}

// NewProcessor creates a command processor.
func NewProcessor(store *events.Store, pm process.Manager, workspacesDir string) *Processor {
	return &Processor{
		store:          store,
		processManager: pm,
		workspacesDir:  workspacesDir,
		activeRuns:     make(map[int64]chan struct{}),
	}
}

// Subscribe returns a new channel that receives all events emitted by the processor.
// Each subscriber gets its own channel (fan-out). Channels are buffered and
// events are dropped if a subscriber falls behind.
func (p *Processor) Subscribe() <-chan events.Event {
	ch := make(chan events.Event, 64)
	p.mu.Lock()
	p.subscribers = append(p.subscribers, ch)
	p.mu.Unlock()
	return ch
}

// emit sends an event to all subscribers without blocking.
func (p *Processor) emit(e events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range p.subscribers {
		select {
		case ch <- e:
		default:
		}
	}
}

// SubmitCommand creates a command and notifies the run's processor goroutine.
func (p *Processor) SubmitCommand(cmd Command) error {
	if err := p.store.SubmitCommand(cmd.ID, cmd.RunID, string(cmd.Type), cmd.Payload); err != nil {
		return err
	}
	p.notifyRun(cmd.RunID)
	return nil
}

// notifyRun signals the run's goroutine that a new command is available.
func (p *Processor) notifyRun(runID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.activeRuns[runID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Start performs startup recovery and begins processing.
func (p *Processor) Start() error {
	// Process any pending commands from before crash
	cmds, err := p.store.GetAllPendingCommands()
	if err != nil {
		return fmt.Errorf("get pending commands: %w", err)
	}

	// Group by run
	byRun := make(map[int64][]events.CommandRow)
	for _, c := range cmds {
		byRun[c.RunID] = append(byRun[c.RunID], c)
	}

	for runID := range byRun {
		p.ensureRunGoroutine(runID)
	}

	return nil
}

// ensureRunGoroutine starts a goroutine for a run if one isn't already active.
func (p *Processor) ensureRunGoroutine(runID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.activeRuns[runID]; ok {
		return
	}
	ch := make(chan struct{}, 1)
	p.activeRuns[runID] = ch
	go p.processRun(runID, ch)
}

// ProcessRunSync starts a goroutine for the run and returns a channel that
// closes when the run reaches a terminal state or the goroutine exits.
func (p *Processor) ProcessRunSync(runID int64) <-chan struct{} {
	done := make(chan struct{})
	p.mu.Lock()
	if _, ok := p.activeRuns[runID]; ok {
		p.mu.Unlock()
		// Already active — wait for terminal
		go func() {
			defer close(done)
			p.waitForTerminal(runID)
		}()
		return done
	}
	ch := make(chan struct{}, 1)
	p.activeRuns[runID] = ch
	p.mu.Unlock()

	go func() {
		defer close(done)
		p.processRun(runID, ch)
	}()

	return done
}

// waitForTerminal polls until the run is in a terminal state.
func (p *Processor) waitForTerminal(runID int64) {
	// Subscribe to events and wait for terminal
	sub := p.Subscribe()
	for e := range sub {
		if e.RunID != runID {
			continue
		}
		state, err := p.store.ProjectRunFromDB(runID)
		if err != nil {
			return
		}
		if state.Status.IsTerminal() || state.Status == events.RunStatusWaitingHuman {
			return
		}
	}
}

// processRun is the per-run goroutine. It processes commands until
// there are no more pending commands for this run.
func (p *Processor) processRun(runID int64, notify chan struct{}) {
	defer func() {
		p.mu.Lock()
		delete(p.activeRuns, runID)
		p.mu.Unlock()
	}()

	for {
		cmds, err := p.store.GetPendingCommands(runID)
		if err != nil {
			log.Printf("processor: error getting commands for run %d: %v", runID, err)
			return
		}

		if len(cmds) == 0 {
			// Wait for notification or exit
			select {
			case _, ok := <-notify:
				if !ok {
					return
				}
				continue
			}
		}

		for _, cmd := range cmds {
			if err := p.handleCommand(runID, cmd); err != nil {
				log.Printf("processor: error handling %s for run %d: %v", cmd.CommandType, runID, err)
				p.store.MarkCommandFailed(cmd.ID, err.Error())
			} else {
				p.store.MarkCommandProcessed(cmd.ID)
			}
		}

		// Check if run is terminal — if so, exit goroutine
		state, err := p.store.ProjectRunFromDB(runID)
		if err != nil {
			return
		}
		if state.Status.IsTerminal() || state.Status == events.RunStatusWaitingHuman {
			return
		}
	}
}

func (p *Processor) handleCommand(runID int64, cmd events.CommandRow) error {
	cmdType := CommandType(cmd.CommandType)
	switch cmdType {
	case CmdStartRun:
		return p.handleStartRun(runID, cmd)
	case CmdExecuteWorkflow:
		return p.handleExecuteWorkflow(runID, cmd)
	case CmdReportSignal:
		return p.handleReportSignal(runID, cmd)
	case CmdResumeRun:
		return p.handleResumeRun(runID, cmd)
	case CmdKillRun:
		return p.handleKillRun(runID, cmd)
	case CmdStopRun:
		return p.handleStopRun(runID, cmd)
	case CmdDeleteRun:
		return p.handleDeleteRun(runID, cmd)
	case CmdProvideHumanInput:
		return p.handleProvideHumanInput(runID, cmd)
	default:
		return fmt.Errorf("unknown command type: %s", cmdType)
	}
}

// appendEvents appends events with optimistic locking + retry, then emits to channel.
func (p *Processor) appendEvents(runID int64, evts []events.Event) ([]events.Event, error) {
	for attempt := 0; attempt < 3; attempt++ {
		info, err := p.store.GetRun(runID)
		if err != nil {
			return nil, err
		}
		appended, err := p.store.AppendEvents(runID, info.Version, evts)
		if err == events.ErrVersionConflict {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range appended {
			p.emit(e)
		}
		return appended, nil
	}
	return nil, fmt.Errorf("version conflict after 3 retries")
}

// drainPendingCommands processes all pending commands for a run (used during agent execution).
func (p *Processor) drainPendingCommands(runID int64) error {
	cmds, err := p.store.GetPendingCommands(runID)
	if err != nil {
		return err
	}
	for _, cmd := range cmds {
		cmdType := CommandType(cmd.CommandType)
		// Only drain signal reports during agent execution
		if cmdType == CmdReportSignal {
			if err := p.handleReportSignal(runID, cmd); err != nil {
				p.store.MarkCommandFailed(cmd.ID, err.Error())
			} else {
				p.store.MarkCommandProcessed(cmd.ID)
			}
		}
	}
	return nil
}

// submitInternalCommand submits a command within the processor (for chaining).
func (p *Processor) submitInternalCommand(runID int64, cmdType CommandType, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.store.SubmitCommand(events.NewID(), runID, string(cmdType), data)
}
