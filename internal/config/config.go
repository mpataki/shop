package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	DataDir       string
	DBPath        string
	UserSpecDir   string
	ProjectSpecDir string
}

func New() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dataDir := getEnv("SHOP_DATA_DIR", filepath.Join(homeDir, ".shop"))

	c := &Config{
		DataDir:        dataDir,
		DBPath:         filepath.Join(dataDir, "shop.db"),
		UserSpecDir:    filepath.Join(dataDir, "specs"),
		ProjectSpecDir: ".shop/specs",
	}

	return c, nil
}

func (c *Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(c.UserSpecDir, 0755); err != nil {
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
