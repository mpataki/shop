package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mpataki/shop/internal/config"
	shopLua "github.com/mpataki/shop/internal/lua"
	"github.com/mpataki/shop/internal/mcp"
	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/orchestrator"
	"github.com/mpataki/shop/internal/storage"
	"github.com/mpataki/shop/internal/tui"
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

	store, err := storage.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer store.Close()

	orch := orchestrator.New(store, cfg.WorkspacesDir())

	app := tui.NewApp(orch, cfg)
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

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			// Find workflow
			workflowPath := findWorkflow(workflowName, cfg)
			if workflowPath == "" {
				return fmt.Errorf("workflow %q not found (looked in %s and %s)", workflowName, cfg.ProjectWorkflowDir, cfg.UserWorkflowDir)
			}

			return runWorkflow(orch, workflowPath, workflowName, prompt, repoPath, noExec)
		},
	}

	cmd.Flags().Bool("no-exec", false, "Create run but don't execute")
	cmd.Flags().StringP("repo", "r", ".", "Source git repository for worktree (default: current directory)")
	return cmd
}

// findWorkflow looks for a Lua workflow file in the standard locations
func findWorkflow(name string, cfg *config.Config) string {
	// Check project directory first (.shop/workflows/)
	dirs := []string{cfg.ProjectWorkflowDir, cfg.UserWorkflowDir}

	for _, dir := range dirs {
		// Try exact name if it ends with .lua
		if strings.HasSuffix(name, ".lua") {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}

		// Try adding .lua extension
		path := filepath.Join(dir, name+".lua")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// runWorkflow runs a Lua workflow
func runWorkflow(orch *orchestrator.Orchestrator, workflowPath, workflowName, prompt, repoPath string, noExec bool) error {
	// Verify it's a valid Lua workflow
	if !shopLua.IsWorkflow(workflowPath) {
		return fmt.Errorf("not a Lua workflow: %s", workflowPath)
	}

	run, err := orch.StartRun(workflowPath, workflowName, prompt, repoPath)
	if err != nil {
		return fmt.Errorf("failed to start run: %w", err)
	}

	fmt.Printf("Created run #%d\n", run.ID)
	fmt.Printf("Workspace: %s\n", run.WorkspacePath)
	fmt.Printf("Workflow: %s\n", workflowPath)

	if noExec {
		fmt.Println("Skipping execution (--no-exec)")
		return nil
	}

	fmt.Printf("Executing workflow %q...\n", workflowName)
	if err := orch.Execute(run); err != nil {
		// Re-fetch run to get updated status
		run, _ = orch.GetRun(run.ID)
		if run != nil {
			fmt.Printf("Run completed with status: %s\n", run.Status)
			if run.Error != "" {
				fmt.Printf("Error: %s\n", run.Error)
			}
		}
		return fmt.Errorf("execution failed: %w", err)
	}

	// Re-fetch run to get updated status
	run, _ = orch.GetRun(run.ID)
	if run != nil {
		fmt.Printf("Run completed with status: %s\n", run.Status)
	}
	return nil
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

			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			run, err := orch.GetRun(runID)
			if err != nil {
				return fmt.Errorf("failed to get run: %w", err)
			}

			fmt.Printf("Resuming run #%d\n", runID)
			fmt.Printf("Workflow: %s\n", run.WorkflowPath)

			if err := orch.Resume(runID); err != nil {
				// Re-fetch run to get updated status
				run, _ = orch.GetRun(runID)
				if run != nil {
					fmt.Printf("Run completed with status: %s\n", run.Status)
					if run.Error != "" {
						fmt.Printf("Error: %s\n", run.Error)
					}
				}
				return fmt.Errorf("resume failed: %w", err)
			}

			// Re-fetch run to get updated status
			run, _ = orch.GetRun(runID)
			if run != nil {
				fmt.Printf("Run completed with status: %s\n", run.Status)
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

			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			run, err := store.GetRun(runID)
			if err != nil {
				return fmt.Errorf("failed to get run: %w", err)
			}

			fmt.Printf("Run #%d: %s\n", run.ID, run.WorkflowName)
			fmt.Printf("Status: %s\n", run.Status)
			fmt.Printf("Prompt: %s\n", run.InitialPrompt)
			fmt.Printf("Workspace: %s\n", run.WorkspacePath)
			if run.WorkflowPath != "" {
				fmt.Printf("Workflow: %s\n", run.WorkflowPath)
			}
			if run.CurrentAgent != "" {
				fmt.Printf("Agent: %s\n", run.CurrentAgent)
			}

			// Show waiting information for waiting_human status
			if run.Status == models.RunStatusWaitingHuman {
				if run.WaitingSessionID != "" {
					fmt.Printf("Session: %s\n", run.WaitingSessionID)
				}
				if run.WaitingReason != "" {
					fmt.Printf("Reason: %s\n", run.WaitingReason)
				}
				fmt.Printf("Waiting since: %s\n", formatTimeAgo(run.CreatedAt))
				fmt.Printf("\nUse 'shop continue %d' to open the Claude session.\n", run.ID)
			}

			if run.Error != "" {
				fmt.Printf("Error: %s\n", run.Error)
			}

			execs, err := store.GetExecutionsForRun(runID)
			if err != nil {
				return err
			}

			if len(execs) > 0 {
				fmt.Println("\nExecutions:")
				for _, exec := range execs {
					status := string(exec.Status)
					if exec.ExitCode != nil {
						status += fmt.Sprintf(" (exit %d)", *exec.ExitCode)
					}
					// Show call_index for Lua workflows
					if exec.CallIndex > 0 {
						fmt.Printf("  [%d] %s [%s]\n", exec.CallIndex, exec.AgentName, status)
					} else {
						fmt.Printf("  %d. %s [%s]\n", exec.SequenceNum, exec.AgentName, status)
					}
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
			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			active, _ := cmd.Flags().GetBool("active")

			runs, err := store.ListRuns(20)
			if err != nil {
				return err
			}

			if len(runs) == 0 {
				fmt.Println("No runs found.")
				return nil
			}

			// Filter to active runs if requested
			if active {
				var activeRuns []*models.Run
				for _, run := range runs {
					if run.Status == models.RunStatusRunning ||
						run.Status == models.RunStatusWaitingHuman ||
						run.Status == models.RunStatusPending {
						activeRuns = append(activeRuns, run)
					}
				}
				runs = activeRuns
			}

			if len(runs) == 0 {
				fmt.Println("No active runs found.")
				return nil
			}

			// Print header
			fmt.Printf("%-4s %-15s %-14s %-12s %s\n", "ID", "WORKFLOW", "STATUS", "AGENT", "WAITING FOR")

			for _, run := range runs {
				status := string(run.Status)
				agent := run.CurrentAgent
				if agent == "" {
					agent = "-"
				}

				waitingFor := "-"
				if run.Status == models.RunStatusWaitingHuman && run.WaitingReason != "" {
					waitingFor = truncate(run.WaitingReason, 40)
				}

				fmt.Printf("%-4d %-15s %-14s %-12s %s\n",
					run.ID, truncate(run.WorkflowName, 15), status, truncate(agent, 12), waitingFor)
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

			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			if err := orch.KillRun(runID); err != nil {
				return fmt.Errorf("failed to kill run: %w", err)
			}

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

			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			if err := orch.DeleteRun(runID); err != nil {
				return fmt.Errorf("failed to delete run: %w", err)
			}

			fmt.Printf("Deleted run #%d\n", runID)
			return nil
		},
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatTimeAgo(t time.Time) string {
	return storage.FormatTimeAgo(t)
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

			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			// Get run details to show context
			run, err := orch.GetRun(runID)
			if err != nil {
				return fmt.Errorf("failed to get run: %w", err)
			}

			sessionID, workDir, err := orch.ContinueRun(runID)
			if err != nil {
				return err
			}

			fmt.Printf("Opening Claude session for: %s\n", run.CurrentAgent)
			fmt.Printf("Reason: %s\n\n", run.WaitingReason)

			// Resume the Claude session
			claudeCmd := exec.Command("claude", "--resume", sessionID)
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

			cfg, err := config.New()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store, err := storage.New(cfg.DBPath)
			if err != nil {
				return err
			}
			defer store.Close()

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			if err := orch.StopRun(runID, reason); err != nil {
				return fmt.Errorf("failed to stop run: %w", err)
			}

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
			agent, _ := cmd.Flags().GetString("agent")
			dbPath, _ := cmd.Flags().GetString("db")
			runID, _ := cmd.Flags().GetInt64("run-id")
			execID, _ := cmd.Flags().GetInt64("execution-id")

			if agent == "" {
				return fmt.Errorf("--agent is required")
			}

			server := mcp.NewServer(agent, dbPath, runID, execID)
			return server.Run()
		},
	}

	cmd.Flags().String("agent", "", "Agent name")
	cmd.Flags().String("db", "", "Path to shop SQLite database")
	cmd.Flags().Int64("run-id", 0, "Run ID")
	cmd.Flags().Int64("execution-id", 0, "Execution ID")
	// Legacy flag — accepted but ignored (sessions created before migration may pass it)
	cmd.Flags().String("signal-dir", "", "")
	cmd.Flags().MarkHidden("signal-dir")
	return cmd
}
