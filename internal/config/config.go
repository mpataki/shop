package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	DataDir            string
	DBPath             string
	UserWorkflowDir    string
	ProjectWorkflowDir string
}

func New() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dataDir := getEnv("SHOP_DATA_DIR", filepath.Join(homeDir, ".shop"))

	c := &Config{
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "shop.db"),
		UserWorkflowDir:    filepath.Join(dataDir, "workflows"),
		ProjectWorkflowDir: ".shop/workflows",
	}

	return c, nil
}

func (c *Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(c.UserWorkflowDir, 0755); err != nil {
		return err
	}
	return nil
}

func (c *Config) WorkspacesDir() string {
	return filepath.Join(c.DataDir, "workspaces")
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// WorkflowInfo contains metadata about a discovered workflow
type WorkflowInfo struct {
	Name   string // Display name (filename without .lua)
	Path   string // Full path to the workflow file
	Source string // "project" or "user"
}

// ListWorkflows returns all available workflows from both project and user directories
func (c *Config) ListWorkflows() ([]WorkflowInfo, error) {
	var workflows []WorkflowInfo

	// Check project directory first (.shop/workflows/)
	if entries, err := os.ReadDir(c.ProjectWorkflowDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if filepath.Ext(name) == ".lua" {
				workflows = append(workflows, WorkflowInfo{
					Name:   name[:len(name)-4], // Remove .lua extension
					Path:   filepath.Join(c.ProjectWorkflowDir, name),
					Source: "project",
				})
			}
		}
	}

	// Check user directory (~/.shop/workflows/)
	if entries, err := os.ReadDir(c.UserWorkflowDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if filepath.Ext(name) == ".lua" {
				// Skip if already added from project dir
				baseName := name[:len(name)-4]
				exists := false
				for _, w := range workflows {
					if w.Name == baseName {
						exists = true
						break
					}
				}
				if !exists {
					workflows = append(workflows, WorkflowInfo{
						Name:   baseName,
						Path:   filepath.Join(c.UserWorkflowDir, name),
						Source: "user",
					})
				}
			}
		}
	}

	return workflows, nil
}
