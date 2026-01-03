package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config models proofline.yml.
type Config struct {
	Project struct {
		ID   string `yaml:"id"`
		Kind string `yaml:"kind"`
	} `yaml:"project"`
	Attestations struct {
		Catalog map[string]struct {
			Description string `yaml:"description"`
		} `yaml:"catalog"`
	} `yaml:"attestations"`
	Policies struct {
		Presets  map[string]PolicyPreset `yaml:"presets"`
		Defaults struct {
			Task      map[string]string `yaml:"task"`
			Iteration struct {
				Validation struct {
					Require string `yaml:"require"`
				} `yaml:"validation"`
			} `yaml:"iteration"`
		} `yaml:"defaults"`
	} `yaml:"policies"`
}

type PolicyPreset struct {
	Mode      string   `yaml:"mode"`
	Require   []string `yaml:"require"`
	Threshold *int     `yaml:"threshold"`
}

var allowedModes = map[string]struct{}{
	"none":      {},
	"all":       {},
	"any":       {},
	"threshold": {},
}

// Load reads and validates config from workspace.
func Load(workspace string) (*Config, error) {
	path := Path(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config %s not found; run pl init", path)
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config yaml: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate ensures the config meets required structure.
func (c *Config) Validate() error {
	if c.Project.ID == "" {
		return fmt.Errorf("config.project.id is required")
	}
	if c.Project.Kind != "software-project" {
		return fmt.Errorf("config.project.kind must be 'software-project'")
	}
	if c.Policies.Presets == nil {
		return fmt.Errorf("config.policies.presets is required")
	}
	for name, preset := range c.Policies.Presets {
		if _, ok := allowedModes[preset.Mode]; !ok {
			return fmt.Errorf("preset %s has invalid mode %s", name, preset.Mode)
		}
		if preset.Mode == "threshold" {
			if preset.Threshold == nil {
				return fmt.Errorf("preset %s threshold required for mode threshold", name)
			}
		}
		for _, req := range preset.Require {
			if req == "" {
				return fmt.Errorf("preset %s has empty attestation kind", name)
			}
			if len(c.Attestations.Catalog) > 0 {
				if _, ok := c.Attestations.Catalog[req]; !ok {
					return fmt.Errorf("preset %s requires unknown attestation kind %s", name, req)
				}
			}
		}
	}
	if c.Policies.Defaults.Task == nil {
		return fmt.Errorf("config.policies.defaults.task is required")
	}
	for taskType, preset := range c.Policies.Defaults.Task {
		if preset == "" {
			return fmt.Errorf("default policy for task type %s is empty", taskType)
		}
		if _, ok := c.Policies.Presets[preset]; !ok {
			return fmt.Errorf("default task preset %s for type %s not defined", preset, taskType)
		}
	}
	requiredKind := c.Policies.Defaults.Iteration.Validation.Require
	if requiredKind != "" && len(c.Attestations.Catalog) > 0 {
		if _, ok := c.Attestations.Catalog[requiredKind]; !ok {
			return fmt.Errorf("iteration validation requires unknown attestation kind %s", requiredKind)
		}
	}
	return nil
}

// Path returns the config file path for a workspace.
func Path(workspace string) string {
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".proofline", "proofline.yml")
}

// GenerateDefault returns default config YAML.
func GenerateDefault(projectID string) string {
	return fmt.Sprintf(defaultTemplate, projectID)
}

const defaultTemplate = `project:
  id: %s
  kind: software-project

attestations:
  catalog:
    ci.passed:
      description: "CI pipeline completed successfully"
    review.approved:
      description: "Code review approved"
    acceptance.passed:
      description: "Acceptance criteria validated"
    security.ok:
      description: "Security checks passed"
    iteration.approved:
      description: "Iteration approved"

policies:
  presets:
    low:
      mode: any
      require: [ci.passed, review.approved]

    medium:
      mode: all
      require: [ci.passed, review.approved]

    high:
      mode: all
      require: [ci.passed, review.approved, security.ok]

    feature:
      mode: all
      require: [ci.passed, review.approved, acceptance.passed]

    bug:
      mode: all
      require: [ci.passed, review.approved]

    technical:
      mode: all
      require: [ci.passed, review.approved]

  defaults:
    task:
      feature: feature
      bug: bug
      technical: technical
      docs: low
      chore: low

    iteration:
      validation:
        require: iteration.approved
`
