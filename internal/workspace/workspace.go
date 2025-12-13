package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Workspace struct {
	Path     string
	RepoPath string
}

type RunMetadata struct {
	RunID          int64    `json:"run_id"`
	SpecName       string   `json:"spec_name"`
	InitialPrompt  string   `json:"initial_prompt"`
	CurrentAgent   string   `json:"current_agent"`
	Iteration      int      `json:"iteration"`
	PreviousAgents []string `json:"previous_agents"`
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
		if err := w.createWorktree(sourceRepo); err != nil {
			return nil, err
		}
	} else {
		// Fall back to empty directory
		if err := os.MkdirAll(w.RepoPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create repo directory: %w", err)
		}
	}

	// Create .agents/ and .shop/ directories inside repo
	dirs := []string{
		filepath.Join(w.RepoPath, ".agents", "messages"),
		filepath.Join(w.RepoPath, ".agents", "signals"),
		filepath.Join(w.RepoPath, ".agents", "scratchpad"),
		filepath.Join(w.RepoPath, ".shop"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Write the skill file
	if err := w.writeSkillFile(); err != nil {
		return nil, err
	}

	return w, nil
}

func (w *Workspace) createWorktree(sourceRepo string) error {
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

	// Get current commit SHA (detached worktree at current HEAD)
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = absRepo
	shaOut, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	// Create detached worktree at current HEAD
	cmd = exec.Command("git", "worktree", "add", "--detach", w.RepoPath, sha)
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

func (w *Workspace) WriteRunMetadata(meta *RunMetadata) error {
	path := filepath.Join(w.RepoPath, ".shop", "run.json")

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal run metadata: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write run.json: %w", err)
	}

	return nil
}

func (w *Workspace) ReadSignal(agentName string) (map[string]any, error) {
	path := filepath.Join(w.RepoPath, ".agents", "signals", agentName+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("signal file not found for agent %s", agentName)
		}
		return nil, fmt.Errorf("failed to read signal file: %w", err)
	}

	var signal map[string]any
	if err := json.Unmarshal(data, &signal); err != nil {
		return nil, fmt.Errorf("failed to parse signal JSON: %w", err)
	}

	return signal, nil
}

func (w *Workspace) CreateAgentScratchpad(agentName string) error {
	path := filepath.Join(w.RepoPath, ".agents", "scratchpad", agentName)
	return os.MkdirAll(path, 0755)
}

func (w *Workspace) writeSkillFile() error {
	skillPath := filepath.Join(w.RepoPath, ".agents", "SKILL.md")
	return os.WriteFile(skillPath, []byte(skillContent), 0644)
}

const skillContent = `---
name: shop-protocol
description: Protocol for multi-agent orchestrated workflows. Use when .agents/ directory exists.
---

# Shop Workspace Protocol

You are one agent in a coordinated workflow. Other agents work on this
codebase before and after you.

## Reading Context

1. Check ` + "`" + `.shop/run.json` + "`" + ` for run metadata and your role
2. Read ` + "`" + `.agents/messages/*.md` + "`" + ` in order for notes from previous agents
3. Your predecessors may have left context about decisions or blockers

## Leaving Context for Next Agent

Write to ` + "`" + `.agents/messages/{NNN}-{your-role}.md` + "`" + `:
- Increment the number from the last message
- Be concise—what does the next agent need to know?
- Don't duplicate what's obvious from code or commits

## Signaling Completion

**IMPORTANT:** When your work is complete, write your decision to:
` + "`" + `.agents/signals/{your-role}.json` + "`" + `

Use the schema defined for your role. Example:
` + "```" + `json
{"status": "APPROVED", "summary": "Code looks good, no issues found"}
` + "```" + `

Valid status values depend on your role—check the orchestration spec.

## Private Workspace

Use ` + "`" + `.agents/scratchpad/{your-role}/` + "`" + ` for drafts, notes, or intermediate work.
No guarantee anyone reads this.

## Git Commits

Make atomic commits with clear messages. The commit history is part of
the communication trail. Don't squash—preserve the narrative.
`
