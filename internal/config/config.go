package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config models workline.yml.
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
	RBAC struct {
		Roles                  map[string]RBACRole `yaml:"roles"`
		AttestationAuthorities map[string][]string `yaml:"attestation_authorities"`
	} `yaml:"rbac"`
}

type PolicyPreset struct {
	Require []string `yaml:"require"`
}

type RBACRole struct {
	Description string   `yaml:"description"`
	Permissions []string `yaml:"permissions"`
}

// Load reads and validates config from workspace.
func Load(workspace string) (*Config, error) {
	path := Path(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config %s not found; import with wl project config import --file <path>", path)
		}
		return nil, err
	}
	return FromYAML(data)
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
	if len(c.RBAC.Roles) > 0 {
		if _, ok := c.RBAC.Roles["owner"]; !ok {
			return fmt.Errorf("config.rbac.roles must include owner")
		}
		for roleID, role := range c.RBAC.Roles {
			if roleID == "" {
				return fmt.Errorf("config.rbac.roles contains empty role id")
			}
			for _, perm := range role.Permissions {
				if perm == "" {
					return fmt.Errorf("role %s has empty permission id", roleID)
				}
			}
		}
	}
	for kind, roles := range c.RBAC.AttestationAuthorities {
		if kind == "" {
			return fmt.Errorf("config.rbac.attestation_authorities has empty kind")
		}
		for _, roleID := range roles {
			if roleID == "" {
				return fmt.Errorf("attestation kind %s has empty role id", kind)
			}
			if len(c.RBAC.Roles) > 0 {
				if _, ok := c.RBAC.Roles[roleID]; !ok {
					return fmt.Errorf("attestation kind %s references unknown role %s", kind, roleID)
				}
			}
		}
	}
	return nil
}

// Path returns the config file path for a workspace.
func Path(workspace string) string {
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, "workline.yml")
}

// GenerateDefault returns default config YAML.
func GenerateDefault(projectID string) string {
	return fmt.Sprintf(defaultTemplate, projectID)
}

// LoadOptional returns nil,nil if the config file does not exist.
func LoadOptional(workspace string) (*Config, error) {
	path := Path(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return FromYAML(data)
}

// Default returns the default Config struct for a project.
func Default(projectID string) *Config {
	var cfg Config
	cfg.Project.ID = projectID
	cfg.Project.Kind = "software-project"
	_ = yaml.NewDecoder(bytes.NewBufferString(fmt.Sprintf(defaultTemplate, projectID))).Decode(&cfg)
	return &cfg
}

// FromYAML parses and validates config from raw YAML bytes.
func FromYAML(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config yaml: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// FromFile reads YAML config from the given path.
func FromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return FromYAML(data)
}

const defaultTemplate = `project:
  id: %s
  kind: software-project

attestations:
  catalog:
    requirements.accepted:
      description: "Team agreed on scope and requirements"
    design.reviewed:
      description: "Solution/design reviewed"
    scope.groomed:
      description: "Task is sized, dependencies known"
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
    workshop.discovery.completed:
      description: "Discovery workshop completed"
    workshop.problem_refinement.completed:
      description: "Problem refinement workshop completed"
    workshop.eventstorming.completed:
      description: "Event storming workshop completed"
    workshop.decision.completed:
      description: "Decision workshop completed"
    workshop.clarify.completed:
      description: "Clarification workshop completed"

policies:
  presets:
    ready:
      require: [requirements.accepted, design.reviewed, scope.groomed]

    done.standard:
      require: [ci.passed, review.approved, acceptance.passed]

    done.bugfix:
      require: [ci.passed, review.approved]

    low:
      require: [ci.passed, review.approved]

    medium:
      require: [ci.passed, review.approved]

    high:
      require: [ci.passed, review.approved, security.ok]

    workshop.discovery:
      require: [workshop.discovery.completed]

    workshop.problem_refinement:
      require: [workshop.problem_refinement.completed]

    workshop.eventstorming:
      require: [workshop.eventstorming.completed]

    workshop.decision:
      require: [workshop.decision.completed]

    workshop.clarify:
      require: [workshop.clarify.completed]

  defaults:
    task:
      feature: done.standard
      bug: done.bugfix
      technical: done.standard
      docs: low
      chore: low
      workshop: workshop.problem_refinement

    iteration:
      validation:
        require: iteration.approved
`
