package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteMCPConfig writes mcp.json to the workspace root.
func WriteMCPConfig(workspacePath, dbPath string, runID int64, callIndex int, statuses []string) error {
	shopBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find shop binary: %w", err)
	}
	shopBin, err = filepath.EvalSymlinks(shopBin)
	if err != nil {
		return fmt.Errorf("resolve shop binary path: %w", err)
	}

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"shop": map[string]any{
				"command": shopBin,
				"args": mcpServerArgs(dbPath, runID, callIndex, statuses),
			},
		},
	}

	data, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(workspacePath, "mcp.json"), data, 0644)
}

func mcpServerArgs(dbPath string, runID int64, callIndex int, statuses []string) []string {
	args := []string{
		"mcp-server",
		"--db", dbPath,
		"--run-id", fmt.Sprintf("%d", runID),
		"--call-index", fmt.Sprintf("%d", callIndex),
	}
	if len(statuses) > 0 {
		args = append(args, "--statuses", strings.Join(statuses, ","))
	}
	return args
}
