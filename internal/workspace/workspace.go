package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Workspace struct {
	Path     string
	RepoPath string
}

func Create(baseDir string, runID int64, sourceRepo string) (*Workspace, error) {
	path := filepath.Join(baseDir, fmt.Sprintf("run-%d", runID))

	w := &Workspace{
		Path:     path,
		RepoPath: filepath.Join(path, "repo"),
	}

	// Create base workspace directory
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create workspace directory: %w", err)
	}

	// Create repo via git worktree if source repo provided
	if sourceRepo != "" {
		if err := w.createWorktree(sourceRepo, runID); err != nil {
			return nil, err
		}
	} else {
		// Fall back to empty directory
		if err := os.MkdirAll(w.RepoPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create repo directory: %w", err)
		}
	}

	// Scratchpad lives as a sibling to repo (keeps worktree clean)
	if err := os.MkdirAll(filepath.Join(path, "scratchpad"), 0755); err != nil {
		return nil, fmt.Errorf("failed to create scratchpad directory: %w", err)
	}

	return w, nil
}

func (w *Workspace) createWorktree(sourceRepo string, runID int64) error {
	// Resolve to absolute path
	absRepo, err := filepath.Abs(sourceRepo)
	if err != nil {
		return fmt.Errorf("failed to resolve repo path: %w", err)
	}

	// Verify it's a git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = absRepo
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s is not a git repository", absRepo)
	}

	// Create a new branch for this run
	branchName := fmt.Sprintf("shop/run-%d", runID)

	// Create worktree with new branch at current HEAD
	cmd = exec.Command("git", "worktree", "add", "-b", branchName, w.RepoPath)
	cmd.Dir = absRepo
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create worktree: %s", string(output))
	}

	return nil
}

func Open(baseDir string, runID int64) (*Workspace, error) {
	path := filepath.Join(baseDir, fmt.Sprintf("run-%d", runID))

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("workspace for run %d does not exist", runID)
	}

	return &Workspace{
		Path:     path,
		RepoPath: filepath.Join(path, "repo"),
	}, nil
}

func (w *Workspace) CreateAgentScratchpad(agentName string) error {
	return os.MkdirAll(filepath.Join(w.Path, "scratchpad", agentName), 0755)
}

func (w *Workspace) ScratchpadPath(agentName string) string {
	return filepath.Join(w.Path, "scratchpad", agentName)
}

func (w *Workspace) MCPConfigPath() string {
	return filepath.Join(w.Path, "mcp.json")
}
