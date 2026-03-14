package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mpataki/shop/internal/commands"
	"github.com/mpataki/shop/internal/config"
	"github.com/mpataki/shop/internal/events"
	"github.com/mpataki/shop/internal/mcp"
	"github.com/mpataki/shop/internal/process"
	"github.com/mpataki/shop/internal/tui"
	"github.com/mpataki/shop/internal/workflow"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "shop",
		Short: "Claude Agent Orchestration System",
		Long:  "Shop coordinates multiple Claude Code agents through defined workflows.",
		RunE:  runTUI,
	}

	rootCmd.AddCommand(newRunCommand())
	rootCmd.AddCommand(newResumeCommand())
	rootCmd.AddCommand(newStatusCommand())
	rootCmd.AddCommand(newListCommand())
	rootCmd.AddCommand(newKillCommand())
	rootCmd.AddCommand(newDeleteCommand())
	rootCmd.AddCommand(newContinueCommand())
	rootCmd.AddCommand(newStopCommand())
	rootCmd.AddCommand(newMCPServerCommand())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	cfg, err := config.New()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := cfg.EnsureDataDir(); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	store, err := events.NewStore(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer store.Close()

	pm := process.NewCLIManager()
	proc := commands.NewProcessor(store, pm, cfg.WorkspacesDir())
	proc.Start()

	app := tui.NewApp(proc, store, cfg)
	p := tea.NewProgram(app, tea.WithAltScreen())

	_, err = p.Run()
	return err
}

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <workflow> <prompt>",
		Short: "Start a new run with a Lua workflow",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowName := args[0]
			prompt := args[1]
			noExec, _ := cmd.Flags().GetBool("no-exec")
			repoPath, _ := cmd.Flags().GetString("repo")

			cfg, err := config.New()
			if err != nil {
				return err
			}
			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := events.NewStore(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			workflowPath := findWorkflow(workflowName, cfg)
			if workflowPath == "" {
				return fmt.Errorf("workflow %q not found (looked in %s and %s)", workflowName, cfg.ProjectWorkflowDir, cfg.UserWorkflowDir)
			}

			if !workflow.IsWorkflow(workflowPath) {
				return fmt.Errorf("not a Lua workflow: %s", workflowPath)
			}

			// Create run
			runID, err := store.CreateRun()
			if err != nil {
				return fmt.Errorf("failed to create run: %w", err)
			}

			fmt.Printf("Created run #%d\n", runID)

			if noExec {
				fmt.Println("Skipping execution (--no-exec)")
				return nil
			}

			// Create processor and submit StartRun command
			pm := process.NewCLIManager()
			proc := commands.NewProcessor(store, pm, cfg.WorkspacesDir())

			startCmd, err := commands.NewCommand(runID, commands.CmdStartRun, commands.StartRunPayload{
				WorkflowPath:  workflowPath,
				WorkflowName:  workflowName,
				InitialPrompt: prompt,
				SourceRepo:    repoPath,
			})
			if err != nil {
				return err
			}

			if err := proc.SubmitCommand(startCmd); err != nil {
				return err
			}

			fmt.Printf("Executing workflow %q...\n", workflowName)

			// Wait for terminal state
			done := proc.ProcessRunSync(runID)
			<-done

			// Print final status
			state, err := store.ProjectRunFromDB(runID)
			if err != nil {
				return err
			}
			fmt.Printf("Run completed with status: %s\n", state.Status)
			if state.Error != "" {
				fmt.Printf("Error: %s\n", state.Error)
			}
			if state.Status == events.RunStatusWaitingHuman {
				fmt.Printf("Waiting: %s\n", state.WaitingReason)
				fmt.Printf("\nUse 'shop continue %d' to open the Claude session.\n", runID)
			}

			return nil
		},
	}

	cmd.Flags().Bool("no-exec", false, "Create run but don't execute")
	cmd.Flags().StringP("repo", "r", ".", "Source git repository for worktree (default: current directory)")
	return cmd
}

func findWorkflow(name string, cfg *config.Config) string {
	dirs := []string{cfg.ProjectWorkflowDir, cfg.UserWorkflowDir}

	for _, dir := range dirs {
		if strings.HasSuffix(name, ".lua") {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}

		path := filepath.Join(dir, name+".lua")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

func newResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume an interrupted run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			cfg, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			pm := process.NewCLIManager()
			proc := commands.NewProcessor(store, pm, cfg.WorkspacesDir())

			resumeCmd, err := commands.NewCommand(runID, commands.CmdResumeRun, commands.ResumeRunPayload{})
			if err != nil {
				return err
			}

			if err := proc.SubmitCommand(resumeCmd); err != nil {
				return err
			}

			fmt.Printf("Resuming run #%d\n", runID)

			done := proc.ProcessRunSync(runID)
			<-done

			state, _ := store.ProjectRunFromDB(runID)
			if state != nil {
				fmt.Printf("Run completed with status: %s\n", state.Status)
				if state.Error != "" {
					fmt.Printf("Error: %s\n", state.Error)
				}
			}
			return nil
		},
	}
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status <run-id>",
		Short: "Show run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			_, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			state, err := store.ProjectRunFromDB(runID)
			if err != nil {
				return fmt.Errorf("failed to get run: %w", err)
			}

			fmt.Printf("Run #%d: %s\n", state.ID, state.WorkflowName)
			fmt.Printf("Status: %s\n", state.Status)
			fmt.Printf("Prompt: %s\n", state.InitialPrompt)
			fmt.Printf("Workspace: %s\n", state.WorkspacePath)
			if state.WorkflowPath != "" {
				fmt.Printf("Workflow: %s\n", state.WorkflowPath)
			}
			if state.CurrentAgent != "" {
				fmt.Printf("Agent: %s\n", state.CurrentAgent)
			}

			if state.Status == events.RunStatusWaitingHuman {
				if state.WaitingSessionID != "" {
					fmt.Printf("Session: %s\n", state.WaitingSessionID)
				}
				if state.WaitingReason != "" {
					fmt.Printf("Reason: %s\n", state.WaitingReason)
				}
				fmt.Printf("\nUse 'shop continue %d' to open the Claude session.\n", state.ID)
			}

			if state.Error != "" {
				fmt.Printf("Error: %s\n", state.Error)
			}

			if len(state.Executions) > 0 {
				fmt.Println("\nExecutions:")
				for i, exec := range state.Executions {
					status := string(exec.Status)
					fmt.Printf("  [%d] %s [%s]\n", i+1, exec.AgentName, status)
				}
			}

			return nil
		},
	}
}

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			active, _ := cmd.Flags().GetBool("active")

			runs, err := store.ListRunIDs(20)
			if err != nil {
				return err
			}

			if len(runs) == 0 {
				fmt.Println("No runs found.")
				return nil
			}

			// Project each run
			type runEntry struct {
				state *events.RunState
			}
			var entries []runEntry

			for _, r := range runs {
				evts, err := store.GetEvents(r.ID)
				if err != nil {
					continue
				}
				state := events.ProjectRun(r.ID, r.CreatedAt, evts)

				if active {
					if state.Status != events.RunStatusRunning &&
						state.Status != events.RunStatusWaitingHuman &&
						state.Status != events.RunStatusPending {
						continue
					}
				}
				// Skip deleted runs
				if state.Status == events.RunStatusDeleted {
					continue
				}

				entries = append(entries, runEntry{state: state})
			}

			if len(entries) == 0 {
				if active {
					fmt.Println("No active runs found.")
				} else {
					fmt.Println("No runs found.")
				}
				return nil
			}

			fmt.Printf("%-4s %-15s %-14s %-12s %s\n", "ID", "WORKFLOW", "STATUS", "AGENT", "WAITING FOR")

			for _, e := range entries {
				s := e.state
				agent := s.CurrentAgent
				if agent == "" {
					agent = "-"
				}

				waitingFor := "-"
				if s.Status == events.RunStatusWaitingHuman && s.WaitingReason != "" {
					waitingFor = truncate(s.WaitingReason, 40)
				}

				fmt.Printf("%-4d %-15s %-14s %-12s %s\n",
					s.ID, truncate(s.WorkflowName, 15), string(s.Status), truncate(agent, 12), waitingFor)
			}

			return nil
		},
	}

	cmd.Flags().Bool("active", false, "Show only active runs (exclude completed/failed)")
	return cmd
}

func newKillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <run-id>",
		Short: "Kill a running run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			cfg, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			pm := process.NewCLIManager()
			proc := commands.NewProcessor(store, pm, cfg.WorkspacesDir())

			killCmd, err := commands.NewCommand(runID, commands.CmdKillRun, commands.KillRunPayload{})
			if err != nil {
				return err
			}

			if err := proc.SubmitCommand(killCmd); err != nil {
				return err
			}

			done := proc.ProcessRunSync(runID)
			<-done

			fmt.Printf("Killed run #%d\n", runID)
			return nil
		},
	}
}

func newDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <run-id>",
		Short: "Delete a run and its workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			cfg, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			pm := process.NewCLIManager()
			proc := commands.NewProcessor(store, pm, cfg.WorkspacesDir())

			delCmd, err := commands.NewCommand(runID, commands.CmdDeleteRun, commands.DeleteRunPayload{})
			if err != nil {
				return err
			}

			if err := proc.SubmitCommand(delCmd); err != nil {
				return err
			}

			done := proc.ProcessRunSync(runID)
			<-done

			fmt.Printf("Deleted run #%d\n", runID)
			return nil
		},
	}
}

func newContinueCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "continue <run-id>",
		Short: "Open Claude session for a waiting run",
		Long:  "Resume interaction with an agent that needs human input",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			_, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			state, err := store.ProjectRunFromDB(runID)
			if err != nil {
				return fmt.Errorf("failed to get run: %w", err)
			}

			if state.Status != events.RunStatusWaitingHuman {
				return fmt.Errorf("run %d is not waiting for human input (status: %s)", runID, state.Status)
			}

			if state.WaitingSessionID == "" {
				return fmt.Errorf("run %d has no session ID to resume", runID)
			}

			workDir := filepath.Join(state.WorkspacePath, "repo")

			fmt.Printf("Opening Claude session for: %s\n", state.CurrentAgent)
			fmt.Printf("Reason: %s\n\n", state.WaitingReason)

			claudeCmd := exec.Command("claude", "--resume", state.WaitingSessionID)
			claudeCmd.Dir = workDir
			claudeCmd.Stdin = os.Stdin
			claudeCmd.Stdout = os.Stdout
			claudeCmd.Stderr = os.Stderr

			if err := claudeCmd.Run(); err != nil {
				return fmt.Errorf("claude session failed: %w", err)
			}

			fmt.Println("\nClaude session ended.")
			fmt.Println("Run 'shop resume " + args[0] + "' to continue the workflow.")

			return nil
		},
	}
}

func newStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <run-id>",
		Short: "Stop a waiting run",
		Long:  "Mark a waiting run as stuck and stop waiting for human input",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid run ID: %w", err)
			}

			reason, _ := cmd.Flags().GetString("reason")

			cfg, store, err := openStore()
			if err != nil {
				return err
			}
			defer store.Close()

			pm := process.NewCLIManager()
			proc := commands.NewProcessor(store, pm, cfg.WorkspacesDir())

			stopCmd, err := commands.NewCommand(runID, commands.CmdStopRun, commands.StopRunPayload{Reason: reason})
			if err != nil {
				return err
			}

			if err := proc.SubmitCommand(stopCmd); err != nil {
				return err
			}

			done := proc.ProcessRunSync(runID)
			<-done

			if reason != "" {
				fmt.Printf("Run %d marked as stuck: %s\n", runID, reason)
			} else {
				fmt.Printf("Run %d marked as stuck\n", runID)
			}
			return nil
		},
	}

	cmd.Flags().String("reason", "", "Reason for stopping the run")
	return cmd
}

func newMCPServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "mcp-server",
		Short:  "Run the Shop MCP server (used internally)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath, _ := cmd.Flags().GetString("db")
			runID, _ := cmd.Flags().GetInt64("run-id")
			callIndex, _ := cmd.Flags().GetInt("call-index")

			// Legacy flag migration
			if callIndex == 0 {
				if execID, _ := cmd.Flags().GetInt64("execution-id"); execID > 0 {
					callIndex = int(execID) // best-effort fallback
				}
			}

			server := mcp.NewServer(dbPath, runID, callIndex)
			return server.Run()
		},
	}

	cmd.Flags().String("db", "", "Path to shop SQLite database")
	cmd.Flags().Int64("run-id", 0, "Run ID")
	cmd.Flags().Int("call-index", 0, "Call index for this agent execution")
	// Legacy flags
	cmd.Flags().String("agent", "", "Agent name (legacy, ignored)")
	cmd.Flags().MarkHidden("agent")
	cmd.Flags().Int64("execution-id", 0, "Execution ID (legacy, maps to call-index)")
	cmd.Flags().MarkHidden("execution-id")
	cmd.Flags().String("signal-dir", "", "")
	cmd.Flags().MarkHidden("signal-dir")
	return cmd
}

// helpers

func openStore() (*config.Config, *events.Store, error) {
	cfg, err := config.New()
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, nil, err
	}
	store, err := events.NewStore(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	return cfg, store, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
