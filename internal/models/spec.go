package models

type Spec struct {
	Name        string               `yaml:"name"`
	Description string               `yaml:"description"`
	Start       string               `yaml:"start"`
	Agents      map[string]*AgentDef `yaml:"agents"`
	Transitions []*Transition        `yaml:"transitions"`
	Settings    *Settings            `yaml:"settings"`
}

type AgentDef struct {
	OutputSchema map[string]*FieldDef `yaml:"output_schema"`
}

type FieldDef struct {
	Type     string   `yaml:"type"`
	Values   []string `yaml:"values,omitempty"`
	Optional bool     `yaml:"optional,omitempty"`
}

type Transition struct {
	From string         `yaml:"from"`
	To   string         `yaml:"to"`
	When map[string]any `yaml:"when,omitempty"`
}

type Settings struct {
	MaxIterations     int    `yaml:"max_iterations"`
	WorkspaceTemplate string `yaml:"workspace_template"`
}
