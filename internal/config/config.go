package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config models workline.yml.
type Config struct {
	Project struct {
		ID             string                       `yaml:"id"`
		TaskTypes      map[string]TaskTypeConfig    `yaml:"task_types"`
		IterationTypes map[string]IterationTypeSpec `yaml:"iteration_types"`
		Attestations   []AttestationConfig          `yaml:"attestations"`
		RBAC           RBACConfig                   `yaml:"rbac"`
	} `yaml:"project"`
	Webhooks []WebhookConfig `yaml:"webhooks"`
}

type TaskTypeConfig struct {
	Policies map[string]PolicyRule `yaml:"policies"`
}

type IterationTypeSpec struct {
	Policies map[string]PolicyRule `yaml:"policies"`
}

type PolicyRule struct {
	All []string `yaml:"all"`
}

type AttestationConfig struct {
	ID          string `yaml:"id"`
	Category    string `yaml:"category"`
	Description string `yaml:"description"`
}

type RBACConfig struct {
	Permissions map[string][]string `yaml:"permissions"`
	Roles       map[string]RBACRole `yaml:"roles"`
}

type RBACRole struct {
	Description string   `yaml:"description"`
	Grants      []string `yaml:"grants"`
	CanAttest   []string `yaml:"can_attest"`
}

type WebhookConfig struct {
	URL            string   `yaml:"url"`
	Events         []string `yaml:"events"`
	Secret         string   `yaml:"secret"`
	Enabled        *bool    `yaml:"enabled"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
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
	if len(c.Project.TaskTypes) == 0 {
		return fmt.Errorf("config.project.task_types is required")
	}
	attestationKinds := c.attestationKinds()
	for id, tt := range c.Project.TaskTypes {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("config.project.task_types contains empty type id")
		}
		if len(tt.Policies) == 0 {
			return fmt.Errorf("task type %s has no policies", id)
		}
		for policyName, rule := range tt.Policies {
			if strings.TrimSpace(policyName) == "" {
				return fmt.Errorf("task type %s has empty policy name", id)
			}
			for _, kind := range rule.All {
				if kind == "" {
					return fmt.Errorf("task type %s policy %s has empty attestation kind", id, policyName)
				}
				if len(attestationKinds) > 0 && !attestationKinds[kind] {
					return fmt.Errorf("task type %s policy %s requires unknown attestation kind %s", id, policyName, kind)
				}
			}
		}
	}
	for id, it := range c.Project.IterationTypes {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("config.project.iteration_types contains empty type id")
		}
		for policyName, rule := range it.Policies {
			if strings.TrimSpace(policyName) == "" {
				return fmt.Errorf("iteration type %s has empty policy name", id)
			}
			for _, kind := range rule.All {
				if kind == "" {
					return fmt.Errorf("iteration type %s policy %s has empty attestation kind", id, policyName)
				}
				if len(attestationKinds) > 0 && !attestationKinds[kind] {
					return fmt.Errorf("iteration type %s policy %s requires unknown attestation kind %s", id, policyName, kind)
				}
			}
		}
	}
	if len(c.Project.Attestations) > 0 {
		seen := map[string]bool{}
		for _, att := range c.Project.Attestations {
			if strings.TrimSpace(att.ID) == "" {
				return fmt.Errorf("config.project.attestations contains empty id")
			}
			if seen[att.ID] {
				return fmt.Errorf("duplicate attestation id %s", att.ID)
			}
			seen[att.ID] = true
		}
	}
	if len(c.Project.RBAC.Roles) > 0 {
		if len(c.Project.RBAC.Permissions) == 0 {
			return fmt.Errorf("config.project.rbac.permissions is required when roles are defined")
		}
		if _, ok := c.Project.RBAC.Roles["owner"]; !ok {
			return fmt.Errorf("config.project.rbac.roles must include owner")
		}
		for roleID, role := range c.Project.RBAC.Roles {
			if roleID == "" {
				return fmt.Errorf("config.project.rbac.roles contains empty role id")
			}
			for _, grant := range role.Grants {
				if grant == "" {
					return fmt.Errorf("role %s has empty grant id", roleID)
				}
				if len(c.Project.RBAC.Permissions) > 0 {
					if _, ok := c.Project.RBAC.Permissions[grant]; !ok {
						return fmt.Errorf("role %s references unknown permission set %s", roleID, grant)
					}
				}
			}
			for _, kind := range role.CanAttest {
				if kind == "" {
					return fmt.Errorf("role %s has empty attestation kind", roleID)
				}
				if len(attestationKinds) > 0 && !attestationKinds[kind] {
					return fmt.Errorf("role %s references unknown attestation kind %s", roleID, kind)
				}
			}
		}
	}
	for i, hook := range c.Webhooks {
		if hook.Enabled != nil && !*hook.Enabled {
			continue
		}
		if strings.TrimSpace(hook.URL) == "" {
			return fmt.Errorf("config.webhooks[%d].url is required", i)
		}
		for _, evt := range hook.Events {
			if strings.TrimSpace(evt) == "" {
				return fmt.Errorf("config.webhooks[%d] has empty event type", i)
			}
		}
	}
	return nil
}

func (c *Config) attestationKinds() map[string]bool {
	kinds := map[string]bool{}
	for _, att := range c.Project.Attestations {
		id := strings.TrimSpace(att.ID)
		if id == "" {
			continue
		}
		kinds[id] = true
	}
	return kinds
}

func defaultTaskTypes() map[string]bool {
	types := []string{"technical", "feature", "bug", "docs", "chore", "workshop", "plan"}
	allowed := make(map[string]bool, len(types))
	for _, taskType := range types {
		allowed[taskType] = true
	}
	return allowed
}

// AllowedTaskTypes returns the task types for this config (defaults when unset).
func (c *Config) AllowedTaskTypes() map[string]bool {
	if len(c.Project.TaskTypes) == 0 {
		return defaultTaskTypes()
	}
	allowed := make(map[string]bool, len(c.Project.TaskTypes))
	for taskType := range c.Project.TaskTypes {
		tt := strings.TrimSpace(taskType)
		if tt == "" {
			continue
		}
		allowed[tt] = true
	}
	return allowed
}

// TaskPolicy returns the policy rule for a task type and policy name.
func (c *Config) TaskPolicy(taskType, policyName string) (PolicyRule, bool) {
	tt, ok := c.Project.TaskTypes[taskType]
	if !ok {
		return PolicyRule{}, false
	}
	rule, ok := tt.Policies[policyName]
	return rule, ok
}

// DefaultTaskPolicyName returns the default policy name for a task type.
func (c *Config) DefaultTaskPolicyName(taskType string) string {
	tt, ok := c.Project.TaskTypes[taskType]
	if !ok || len(tt.Policies) == 0 {
		return ""
	}
	if _, ok := tt.Policies["done"]; ok {
		return "done"
	}
	names := make([]string, 0, len(tt.Policies))
	for name := range tt.Policies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0]
}

// IterationValidationPolicy returns the attestation kinds required for validation.
func (c *Config) IterationValidationPolicy() []string {
	if len(c.Project.IterationTypes) == 0 {
		return nil
	}
	if it, ok := c.Project.IterationTypes["standard"]; ok {
		if rule, ok := it.Policies["validation"]; ok {
			return rule.All
		}
	}
	names := make([]string, 0, len(c.Project.IterationTypes))
	for name := range c.Project.IterationTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	it := c.Project.IterationTypes[names[0]]
	if rule, ok := it.Policies["validation"]; ok {
		return rule.All
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
  task_types:
    feature:
      policies:
        ready:
          all: [requirements.accepted, design.reviewed, scope.groomed]
        done:
          all: [ci.passed, review.approved, acceptance.passed]
    bug:
      policies:
        done:
          all: [ci.passed, review.approved]
    technical:
      policies:
        done:
          all: [ci.passed, review.approved, acceptance.passed]
    docs:
      policies:
        done:
          all: [ci.passed, review.approved]
    chore:
      policies:
        done:
          all: [ci.passed, review.approved]
    workshop:
      policies:
        discovery:
          all: [workshop.discovery.completed]
        problem_refinement:
          all: [workshop.problem_refinement.completed]
        eventstorming:
          all: [workshop.eventstorming.completed]
        decision:
          all: [workshop.decision.completed]
        clarify:
          all: [workshop.clarify.completed]
    plan:
      policies:
        done:
          all: [planning.approved]
  iteration_types:
    standard:
      policies:
        validation:
          all: [iteration.approved]
  attestations:
    - id: requirements.accepted
      category: requirements
      description: "Team agreed on scope and requirements"
    - id: design.reviewed
      category: design
      description: "Solution/design reviewed"
    - id: scope.groomed
      category: planning
      description: "Task is sized, dependencies known"
    - id: ci.passed
      category: delivery
      description: "CI pipeline completed successfully"
    - id: review.approved
      category: delivery
      description: "Code review approved"
    - id: acceptance.passed
      category: delivery
      description: "Acceptance criteria validated"
    - id: security.ok
      category: security
      description: "Security checks passed"
    - id: iteration.approved
      category: iteration
      description: "Iteration approved"
    - id: workshop.discovery.completed
      category: workshop
      description: "Discovery workshop completed"
    - id: workshop.problem_refinement.completed
      category: workshop
      description: "Problem refinement workshop completed"
    - id: workshop.eventstorming.completed
      category: workshop
      description: "Event storming workshop completed"
    - id: workshop.decision.completed
      category: workshop
      description: "Decision workshop completed"
    - id: workshop.clarify.completed
      category: workshop
      description: "Clarification workshop completed"
    - id: planning.approved
      category: planning
      description: "Planning approved"
    - id: init.check
      category: system
      description: "Initial project check"
  rbac:
    permissions:
      project.viewer:
        - project.list
        - project.read
        - project.config.read
        - project.status.read
        - project.events.read
      project.admin:
        - project.create
        - project.update
        - project.delete
      task.viewer:
        - task.list
        - task.read
        - task.next
        - task.tree
        - task.validation.read
      task.writer:
        - task.create
        - task.update
        - task.claim
        - task.release
      task.executor:
        - task.done
      iteration.viewer:
        - iteration.list
      iteration.writer:
        - iteration.create
        - iteration.list
        - iteration.set_status
      decision.writer:
        - decision.create
      attestation.viewer:
        - attestation.list
      attestation.writer:
        - attestation.add
        - attestation.list
      rbac.admin:
        - rbac.manage
      force.use:
        - force.use
    roles:
      owner:
        description: "Project owner"
        grants:
          - project.viewer
          - project.admin
          - task.viewer
          - task.writer
          - task.executor
          - iteration.viewer
          - iteration.writer
          - decision.writer
          - attestation.writer
          - rbac.admin
          - force.use
        can_attest:
          - ci.passed
          - review.approved
          - acceptance.passed
          - security.ok
          - iteration.approved
          - init.check
          - workshop.discovery.completed
          - workshop.problem_refinement.completed
          - workshop.eventstorming.completed
          - workshop.decision.completed
          - workshop.clarify.completed
          - planning.approved
      planner:
        description: "Plans work and creates backlog"
        grants:
          - project.viewer
          - task.viewer
          - task.writer
          - iteration.viewer
          - iteration.writer
          - decision.writer
          - attestation.viewer
        can_attest:
          - workshop.discovery.completed
          - workshop.problem_refinement.completed
          - workshop.eventstorming.completed
          - workshop.decision.completed
          - workshop.clarify.completed
      executor:
        description: "Executes tasks and updates status"
        grants:
          - project.viewer
          - task.viewer
          - task.writer
          - task.executor
          - iteration.viewer
          - iteration.writer
          - attestation.writer
        can_attest:
          - ci.passed
      reviewer:
        description: "Reviews work and approves gates"
        grants:
          - project.viewer
          - task.viewer
          - iteration.viewer
          - attestation.writer
        can_attest:
          - review.approved
          - acceptance.passed
          - iteration.approved
          - planning.approved
      dev:
        description: "Developer"
        grants:
          - project.viewer
          - task.viewer
          - task.writer
          - task.executor
          - iteration.viewer
          - attestation.viewer
      security:
        description: "Security"
        grants:
          - project.viewer
          - task.viewer
          - iteration.viewer
          - attestation.writer
        can_attest:
          - security.ok
      observer:
        description: "Read-only observer"
        grants:
          - project.viewer
          - task.viewer
          - iteration.viewer
          - attestation.viewer
`
