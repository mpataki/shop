package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mpataki/shop/internal/models"
	"gopkg.in/yaml.v3"
)

func Parse(path string) (*models.Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read spec file: %w", err)
	}

	var spec models.Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("failed to parse spec YAML: %w", err)
	}

	if spec.Settings == nil {
		spec.Settings = &models.Settings{
			MaxIterations:     10,
			WorkspaceTemplate: "git",
		}
	}

	return &spec, nil
}

func LoadAll(dirs []string) (map[string]*models.Spec, error) {
	specs := make(map[string]*models.Spec)

	for _, dir := range dirs {
		if err := loadFromDir(dir, specs); err != nil {
			// Skip directories that don't exist
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
	}

	return specs, nil
}

func loadFromDir(dir string, specs map[string]*models.Spec) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)
		spec, err := Parse(path)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}

		// Use spec name from file, or filename without extension
		specName := spec.Name
		if specName == "" {
			specName = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		}

		specs[specName] = spec
	}

	return nil
}

func Validate(spec *models.Spec) error {
	if spec.Name == "" {
		return fmt.Errorf("spec must have a name")
	}

	if spec.Start == "" {
		return fmt.Errorf("spec must have a start agent")
	}

	if len(spec.Agents) == 0 {
		return fmt.Errorf("spec must define at least one agent")
	}

	if _, ok := spec.Agents[spec.Start]; !ok {
		return fmt.Errorf("start agent %q not found in agents", spec.Start)
	}

	for _, t := range spec.Transitions {
		if t.From == "" {
			return fmt.Errorf("transition must have a 'from' field")
		}
		if t.To == "" {
			return fmt.Errorf("transition must have a 'to' field")
		}

		// from must be a defined agent
		if _, ok := spec.Agents[t.From]; !ok {
			return fmt.Errorf("transition from agent %q not found in agents", t.From)
		}

		// to must be a defined agent, or END/STUCK
		if t.To != "END" && t.To != "STUCK" {
			if _, ok := spec.Agents[t.To]; !ok {
				return fmt.Errorf("transition to agent %q not found in agents", t.To)
			}
		}
	}

	return nil
}
