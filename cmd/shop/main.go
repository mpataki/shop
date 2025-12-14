package main

import (
	"fmt"
	"os"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mpataki/shop/internal/config"
	"github.com/mpataki/shop/internal/orchestrator"
	"github.com/mpataki/shop/internal/spec"
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
	rootCmd.AddCommand(newStatusCommand())
	rootCmd.AddCommand(newListCommand())
	rootCmd.AddCommand(newKillCommand())
	rootCmd.AddCommand(newDeleteCommand())

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

	specs, err := spec.LoadAll([]string{cfg.ProjectSpecDir, cfg.UserSpecDir})
	if err != nil {
		return fmt.Errorf("failed to load specs: %w", err)
	}

	orch := orchestrator.New(store, cfg.WorkspacesDir())

	app := tui.NewApp(orch, specs)
	p := tea.NewProgram(app, tea.WithAltScreen())

	_, err = p.Run()
	return err
}

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <spec> <prompt>",
		Short: "Start a new run",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			specName := args[0]
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

			specs, err := spec.LoadAll([]string{cfg.ProjectSpecDir, cfg.UserSpecDir})
			if err != nil {
				return err
			}

			s, ok := specs[specName]
			if !ok {
				return fmt.Errorf("spec %q not found", specName)
			}

			orch := orchestrator.New(store, cfg.WorkspacesDir())

			run, err := orch.StartRun(s, prompt, repoPath)
			if err != nil {
				return fmt.Errorf("failed to start run: %w", err)
			}

			fmt.Printf("Created run #%d\n", run.ID)
			fmt.Printf("Workspace: %s\n", run.WorkspacePath)

			if noExec {
				fmt.Println("Skipping execution (--no-exec)")
				return nil
			}

			fmt.Printf("Executing with spec %q...\n", specName)
			if err := orch.Execute(run, s); err != nil {
				return fmt.Errorf("execution failed: %w", err)
			}

			fmt.Printf("Run completed with status: %s\n", run.Status)
			return nil
		},
	}

	cmd.Flags().Bool("no-exec", false, "Create run but don't execute")
	cmd.Flags().StringP("repo", "r", ".", "Source git repository for worktree (default: current directory)")
	return cmd
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

			fmt.Printf("Run #%d: %s\n", run.ID, run.SpecName)
			fmt.Printf("Status: %s\n", run.Status)
			fmt.Printf("Prompt: %s\n", run.InitialPrompt)
			fmt.Printf("Workspace: %s\n", run.WorkspacePath)
			if run.CurrentAgent != "" {
				fmt.Printf("Current Agent: %s\n", run.CurrentAgent)
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
					fmt.Printf("  %d. %s [%s]\n", exec.SequenceNum, exec.AgentName, status)
				}
			}

			return nil
		},
	}
}

func newListCommand() *cobra.Command {
	return &cobra.Command{
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

			runs, err := store.ListRuns(20)
			if err != nil {
				return err
			}

			if len(runs) == 0 {
				fmt.Println("No runs found.")
				return nil
			}

			for _, run := range runs {
				fmt.Printf("#%d %s [%s] %s\n",
					run.ID, run.SpecName, run.Status,
					truncate(run.InitialPrompt, 50))
			}

			return nil
		},
	}
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
