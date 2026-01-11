package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"workline/internal/app"
	"workline/internal/config"
	"workline/internal/db"
	"workline/internal/domain"
	"workline/internal/engine"
	"workline/internal/migrate"
	"workline/internal/repo"
	"workline/internal/server"
)

var rootCmd = &cobra.Command{
	Use:   "wl",
	Short: "Workline CLI",
	Long: `Workline tracks project work with attestations and policy-driven validation.
Core concepts (kid-friendly):
- Why it matters: attestations are proof stickers and policies are the rules; together they stop "done" from being just a checkbox and keep quality consistent without nagging.
- Workspace: your .workline toy box with only the database; configs are stored in the DB and imported explicitly.
- Project: the one big game inside that box that owns all tasks, iterations, and evidence.
- Policies: presets say what proof a task needs (required attestation kinds); task types map to presets by default.
- Definition of Ready (DoR): proof stickers that say a task is ready to start (requirements accepted, design reviewed, scope groomed).
- Definition of Done (DoD): proof stickers that say a task is truly done (tests passed, review approved, acceptance checked); enforced by presets per task type.
- Tasks: work items with parents/deps/leases; statuses go planned -> in_progress -> review -> done (rejected/canceled are exits).
- Iterations: smaller adventures that move pending -> running -> delivered -> validated/rejected; validation can require a catalog attestation.
- Attestations: proof stickers like ci.passed or review.approved that satisfy policies.
- Leases: temporary "I’m working on this" tags (wl task claim/release).
- Event log: diary of changes, view with 'wl log tail'.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		workspace := viper.GetString("workspace")
		if _, err := db.EnsureWorkspace(workspace); err != nil {
			return err
		}
		return nil
	},
}

func main() {
	cobra.OnInitialize(initConfig)
	addPersistentFlags()
	registerCommands()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

func initConfig() {
	viper.SetEnvPrefix("WORKLINE")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
}

func addPersistentFlags() {
	rootCmd.PersistentFlags().StringP("workspace", "w", ".", "workspace directory")
	rootCmd.PersistentFlags().Bool("json", false, "output JSON")
	rootCmd.PersistentFlags().String("actor-id", "local-user", "actor identifier")
	rootCmd.PersistentFlags().Bool("force", false, "force operation")
	rootCmd.PersistentFlags().String("project", "", "project id (overrides config default)")
	_ = viper.BindPFlag("workspace", rootCmd.PersistentFlags().Lookup("workspace"))
	_ = viper.BindPFlag("json", rootCmd.PersistentFlags().Lookup("json"))
	_ = viper.BindPFlag("actor-id", rootCmd.PersistentFlags().Lookup("actor-id"))
	_ = viper.BindPFlag("force", rootCmd.PersistentFlags().Lookup("force"))
	_ = viper.BindPFlag("project", rootCmd.PersistentFlags().Lookup("project"))
}

func registerCommands() {
	rootCmd.AddCommand(projectCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(taskCmd())
	rootCmd.AddCommand(iterationCmd())
	rootCmd.AddCommand(decisionCmd())
	rootCmd.AddCommand(attestCmd())
	rootCmd.AddCommand(logCmd())
	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(rbacCmd())
}

func projectCmd() *cobra.Command {
	prj := &cobra.Command{Use: "project", Short: "Manage projects"}
	prj.AddCommand(projectListCmd())
	prj.AddCommand(projectCreateCmd())
	prj.AddCommand(projectShowCmd())
	prj.AddCommand(projectUpdateCmd())
	prj.AddCommand(projectDeleteCmd())
	prj.AddCommand(projectConfigCmd())
	prj.AddCommand(projectUseCmd())
	return prj
}

func projectListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRepo(cmd.Context(), func(ctx context.Context, r repo.Repo) error {
				items, err := r.ListProjects(ctx)
				if err != nil {
					return err
				}
				return printJSONOrTable(items)
			})
		},
	}
	return cmd
}

func projectCreateCmd() *cobra.Command {
	var id, desc string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id required")
			}
			workspace := viper.GetString("workspace")
			if _, err := db.EnsureWorkspace(workspace); err != nil {
				return err
			}
			conn, err := db.Open(db.Config{Workspace: workspace})
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := migrate.Migrate(conn); err != nil {
				return err
			}
			cfg := config.Default(id)
			e := engine.New(conn, cfg)
			p, err := e.InitProject(cmd.Context(), id, desc, viper.GetString("actor-id"))
			if err != nil {
				return err
			}
			if err := e.Repo.UpsertProjectConfig(cmd.Context(), id, cfg); err != nil {
				return err
			}
			return printJSONOrTable(p)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "project id")
	cmd.Flags().StringVar(&desc, "description", "", "description")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func projectShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := viper.GetString("project")
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if target == "" {
					target = e.Config.Project.ID
				}
				p, err := e.Repo.GetProject(ctx, target)
				if err != nil {
					return err
				}
				return printJSONOrTable(p)
			})
		},
	}
	return cmd
}

func projectUpdateCmd() *cobra.Command {
	var status string
	var description string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := viper.GetString("project")
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if target == "" {
					target = e.Config.Project.ID
				}
				var descPtr *string
				if cmd.Flags().Changed("description") {
					descPtr = &description
				}
				if err := e.Repo.UpdateProject(ctx, target, status, descPtr); err != nil {
					return err
				}
				p, err := e.Repo.GetProject(ctx, target)
				if err != nil {
					return err
				}
				return printJSONOrTable(p)
			})
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "status (active, paused, archived)")
	cmd.Flags().StringVar(&description, "description", "", "description")
	return cmd
}

func projectDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := viper.GetString("project")
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if target == "" {
					target = e.Config.Project.ID
				}
				return e.Repo.DeleteProject(ctx, target)
			})
		},
	}
	return cmd
}

func projectUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use <id>",
		Short: "Set current project for this workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := strings.TrimSpace(args[0])
			if projectID == "" {
				return fmt.Errorf("project id is required")
			}
			workspace := viper.GetString("workspace")
			if err := setEnvValue(filepath.Join(workspace, ".env"), "WORKLINE_DEFAULT_PROJECT", projectID); err != nil {
				return err
			}
			fmt.Printf("Set WORKLINE_DEFAULT_PROJECT=%s in %s/.env\n", projectID, workspace)
			return nil
		},
	}
	return cmd
}

func projectConfigCmd() *cobra.Command {
	cfg := &cobra.Command{
		Use:   "config",
		Short: "Manage project config",
	}
	cfg.AddCommand(projectConfigShowCmd())
	cfg.AddCommand(projectConfigImportCmd())
	return cfg
}

func projectConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show project config stored in DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return printJSONOrTable(e.Config)
			})
		},
	}
	return cmd
}

func projectConfigImportCmd() *cobra.Command {
	var filePath string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import project config from YAML into the DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			cfg, err := config.FromYAML(data)
			if err != nil {
				return err
			}
			projectID := cfg.Project.ID
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if projectID == "" {
					projectID = e.Config.Project.ID
				}
				if err := e.Repo.UpsertProjectConfig(ctx, projectID, cfg); err != nil {
					return err
				}
				return printJSONOrTable(cfg)
			})
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "path to YAML config")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func statusCmd() *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show project status",
		Long:  "See the scoreboard for your project: current iteration, task counts, and overall project state.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				projectID = strings.TrimSpace(projectID)
				if projectID == "" {
					projectID = e.Config.Project.ID
				}
				p, err := e.Repo.GetProject(ctx, projectID)
				if err != nil {
					return err
				}
				counts, err := e.Repo.CountTasksByStatus(ctx, projectID)
				if err != nil {
					return err
				}
				running, err := e.Repo.LatestRunningIteration(ctx, projectID)
				if err != nil {
					return err
				}
				out := map[string]any{
					"project_id":  p.ID,
					"status":      p.Status,
					"iteration":   running,
					"task_counts": counts,
				}
				if viper.GetBool("json") {
					return printJSON(out)
				}
				fmt.Printf("Project: %s (%s)\n", p.ID, p.Status)
				if running != nil {
					fmt.Printf("Running iteration: %s - %s\n", running.ID, running.Goal)
				} else {
					fmt.Println("Running iteration: none")
				}
				fmt.Println("Tasks:")
				for status, c := range counts {
					fmt.Printf("  %s: %d\n", status, c)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project id")
	return cmd
}

func taskCmd() *cobra.Command {
	task := &cobra.Command{
		Use:   "task",
		Short: "Manage tasks",
		Long:  "Tasks are the work items (features, bugs, docs). They flow planned -> in_progress -> review -> done, can depend on each other, and may need proof stickers per policy. Leases prevent two people doing the same task at once.",
	}
	task.AddCommand(taskCreateCmd())
	task.AddCommand(taskListCmd())
	task.AddCommand(taskGetCmd())
	task.AddCommand(taskUpdateCmd())
	task.AddCommand(taskDoneCmd())
	task.AddCommand(taskClaimCmd())
	task.AddCommand(taskReleaseCmd())
	task.AddCommand(taskTreeCmd())
	return task
}

func taskCreateCmd() *cobra.Command {
	var opts engine.TaskCreateOptions
	var requires []string
	var dependsOn []string
	var policy string
	var priority int
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a task",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ActorID = viper.GetString("actor-id")
			opts.RequiredKinds = requires
			opts.DependsOn = dependsOn
			opts.PolicyPreset = policy
			if cmd.Flags().Changed("priority") {
				opts.Priority = &priority
			}
			if cmd.Flags().Changed("require") {
				opts.PolicyOverride = true
			}
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if opts.ProjectID == "" {
					opts.ProjectID = e.Config.Project.ID
				}
				t, err := e.CreateTask(ctx, opts)
				if err != nil {
					return err
				}
				return printJSONOrTable(t)
			})
		},
	}
	cmd.Flags().StringVar(&opts.ID, "id", "", "task id (optional, deterministic UUID if omitted)")
	cmd.Flags().StringVar(&opts.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&opts.IterationID, "iteration", "", "iteration id")
	cmd.Flags().StringVar(&opts.ParentID, "parent", "", "parent task id")
	cmd.Flags().StringVar(&opts.Type, "type", "technical", "task type")
	cmd.Flags().StringVar(&opts.Title, "title", "", "title")
	cmd.Flags().StringVar(&opts.Description, "description", "", "description")
	cmd.Flags().StringArrayVar(&dependsOn, "depends-on", []string{}, "dependency task id (repeatable)")
	cmd.Flags().StringVar(&opts.AssigneeID, "assignee-id", "", "assignee id")
	cmd.Flags().IntVar(&priority, "priority", 0, "priority (lower is higher)")
	cmd.Flags().StringVar(&opts.PolicyPreset, "policy", "", "policy preset to apply (defaults use config mapping by task type)")
	cmd.Flags().StringArrayVar(&requires, "require", []string{}, "required attestation kind (repeatable)")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func taskListCmd() *cobra.Command {
	var f repo.TaskFilters
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if f.ProjectID == "" {
					f.ProjectID = e.Config.Project.ID
				}
				tasks, err := e.Repo.ListTasks(ctx, f)
				if err != nil {
					return err
				}
				if viper.GetBool("json") {
					return printJSON(tasks)
				}
				tw := table.NewWriter()
				tw.SetOutputMirror(os.Stdout)
				tw.AppendHeader(table.Row{"ID", "Title", "Status", "Assignee", "Iteration"})
				for _, t := range tasks {
					assignee := ""
					if t.AssigneeID != nil {
						assignee = *t.AssigneeID
					}
					iter := ""
					if t.IterationID != nil {
						iter = *t.IterationID
					}
					tw.AppendRow(table.Row{t.ID, t.Title, t.Status, assignee, iter})
				}
				tw.Render()
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&f.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&f.Status, "status", "", "status filter")
	cmd.Flags().StringVar(&f.Iteration, "iteration", "", "iteration filter")
	cmd.Flags().StringVar(&f.Parent, "parent", "", "parent task id")
	cmd.Flags().StringVar(&f.AssigneeID, "assignee-id", "", "assignee filter")
	return cmd
}

func taskGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				t, err := e.Repo.GetTask(ctx, id)
				if err != nil {
					return err
				}
				return printJSONOrTable(t)
			})
		},
	}
	return cmd
}

func taskUpdateCmd() *cobra.Command {
	var opts engine.TaskUpdateOptions
	var addDeps, removeDeps, requires []string
	var setParent string
	var workOutcomes string
	var assign string
	var setPolicy string
	var priority int
	var clearPriority bool
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ID = args[0]
			opts.ActorID = viper.GetString("actor-id")
			opts.AddDeps = addDeps
			opts.RemoveDeps = removeDeps
			opts.RequiredKinds = requires
			opts.SetParent = optionalString(setParent)
			opts.SetWorkOutcomes = optionalString(workOutcomes)
			opts.Assign = optionalString(assign)
			opts.PolicyPreset = setPolicy
			opts.AssignProvided = cmd.Flags().Changed("assign")
			opts.ParentProvided = cmd.Flags().Changed("set-parent")
			opts.WorkOutcomesSet = cmd.Flags().Changed("set-work-outcomes-json")
			if cmd.Flags().Changed("priority") || clearPriority {
				opts.PriorityProvided = true
				if clearPriority {
					opts.ClearPriority = true
				} else {
					opts.SetPriority = &priority
				}
			}
			opts.RequiredKindsSet = cmd.Flags().Changed("require")
			if opts.WorkOutcomesSet && opts.SetWorkOutcomes == nil {
				opts.ClearWorkOutcomes = true
			}
			if cmd.Flags().Changed("require") {
				opts.PolicyOverride = true
			}
			opts.Force = viper.GetBool("force")
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				t, err := e.UpdateTask(ctx, opts)
				if err != nil {
					return err
				}
				return printJSONOrTable(t)
			})
		},
	}
	cmd.Flags().StringVar(&opts.Status, "status", "", "new status")
	cmd.Flags().StringVar(&assign, "assign", "", "set assignee id (empty clears)")
	cmd.Flags().StringArrayVar(&addDeps, "add-depends-on", []string{}, "add dependency")
	cmd.Flags().StringArrayVar(&removeDeps, "remove-depends-on", []string{}, "remove dependency")
	cmd.Flags().StringVar(&setParent, "set-parent", "", "set parent task id (empty for none)")
	cmd.Flags().StringVar(&workOutcomes, "set-work-outcomes-json", "", "set work outcomes JSON")
	cmd.Flags().IntVar(&priority, "priority", 0, "priority (lower is higher)")
	cmd.Flags().BoolVar(&clearPriority, "clear-priority", false, "clear priority")
	cmd.Flags().StringVar(&opts.PolicyPreset, "set-policy", "", "apply policy preset to task")
	cmd.Flags().StringArrayVar(&requires, "require", []string{}, "required attestation kind")
	return cmd
}

func taskDoneCmd() *cobra.Command {
	var workOutcomes string
	cmd := &cobra.Command{
		Use:   "done <id>",
		Short: "Complete task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if workOutcomes == "" {
				return fmt.Errorf("--work-outcomes-json required")
			}
			id := args[0]
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				t, err := e.TaskDone(ctx, id, workOutcomes, viper.GetString("actor-id"), viper.GetBool("force"))
				if err != nil {
					return err
				}
				return printJSONOrTable(t)
			})
		},
	}
	cmd.Flags().StringVar(&workOutcomes, "work-outcomes-json", "", "work outcomes JSON")
	return cmd
}

func taskClaimCmd() *cobra.Command {
	var leaseSeconds int
	cmd := &cobra.Command{
		Use:   "claim <id>",
		Short: "Claim task lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				lease, err := e.ClaimLease(ctx, id, viper.GetString("actor-id"), leaseSeconds)
				if err != nil {
					return err
				}
				return printJSONOrTable(lease)
			})
		},
	}
	cmd.Flags().IntVar(&leaseSeconds, "lease-seconds", 900, "lease duration seconds")
	return cmd
}

func taskReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release <id>",
		Short: "Release lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return e.ReleaseLease(ctx, id, viper.GetString("actor-id"))
			})
		},
	}
	return cmd
}

func taskTreeCmd() *cobra.Command {
	var iteration, status string
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Show task tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				tasks, err := e.Repo.ListTasks(ctx, repo.TaskFilters{ProjectID: e.Config.Project.ID, Iteration: iteration, Status: status})
				if err != nil {
					return err
				}
				nodes := map[string][]domain.Task{}
				var roots []domain.Task
				for _, t := range tasks {
					if t.ParentID != nil {
						nodes[*t.ParentID] = append(nodes[*t.ParentID], t)
					} else {
						roots = append(roots, t)
					}
				}
				if viper.GetBool("json") {
					type Node struct {
						Task     domain.Task `json:"task"`
						Children []Node      `json:"children,omitempty"`
					}
					var build func(t domain.Task) Node
					build = func(t domain.Task) Node {
						children := nodes[t.ID]
						var childNodes []Node
						for _, c := range children {
							childNodes = append(childNodes, build(c))
						}
						return Node{Task: t, Children: childNodes}
					}
					var treeNodes []Node
					for _, r := range roots {
						treeNodes = append(treeNodes, build(r))
					}
					return printJSON(treeNodes)
				}
				for _, r := range roots {
					printTaskTree(r, nodes, "", true)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&iteration, "iteration", "", "iteration filter")
	cmd.Flags().StringVar(&status, "status", "", "status filter")
	return cmd
}

func iterationCmd() *cobra.Command {
	iter := &cobra.Command{
		Use:   "iteration",
		Short: "Manage iterations",
		Long:  "Iterations are mini-adventures for the project: pending -> running -> delivered -> validated/rejected. Validation can require a specific attestation kind from config.",
	}
	iter.AddCommand(iterationCreateCmd())
	iter.AddCommand(iterationListCmd())
	iter.AddCommand(iterationStatusCmd())
	return iter
}

func iterationCreateCmd() *cobra.Command {
	var it domain.Iteration
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create iteration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if it.ProjectID == "" {
					it.ProjectID = e.Config.Project.ID
				}
				it.Status = "pending"
				res, err := e.CreateIteration(ctx, it, viper.GetString("actor-id"))
				if err != nil {
					return err
				}
				return printJSONOrTable(res)
			})
		},
	}
	cmd.Flags().StringVar(&it.ID, "id", "", "iteration id")
	cmd.Flags().StringVar(&it.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&it.Goal, "goal", "", "goal")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("goal")
	return cmd
}

func iterationListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List iterations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				items, err := e.Repo.ListIterations(ctx, e.Config.Project.ID)
				if err != nil {
					return err
				}
				return printJSONOrTable(items)
			})
		},
	}
	return cmd
}

func iterationStatusCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "set-status <id>",
		Short: "Update iteration status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				it, err := e.SetIterationStatus(ctx, id, status, viper.GetString("actor-id"), viper.GetBool("force"))
				if err != nil {
					return err
				}
				return printJSONOrTable(it)
			})
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "new status")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

func configCmd() *cobra.Command {
	cfg := &cobra.Command{
		Use:   "config",
		Short: "Inspect project config",
		Long:  "Config is the rulebook (stored in DB): project id/kind, attestation catalog, and policy presets/defaults that decide which proof is needed. Import from workline.yml if desired.",
	}
	cfg.AddCommand(configShowCmd())
	cfg.AddCommand(configValidateCmd())
	return cfg
}

func configShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show loaded config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return printJSONOrTable(e.Config)
			})
		},
	}
	return cmd
}

func configValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate stored config",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return e.Config.Validate()
			})
			if viper.GetBool("json") {
				return printJSON(map[string]any{"ok": err == nil, "error": fmt.Sprint(err)})
			}
			if err != nil {
				return err
			}
			fmt.Println("config OK")
			return nil
		},
	}
	return cmd
}

func decisionCmd() *cobra.Command {
	dec := &cobra.Command{
		Use:   "decision",
		Short: "Manage decisions",
		Long:  "Decisions capture the important choices, who decided, and why—so future you knows the reasoning.",
	}
	dec.AddCommand(decisionCreateCmd())
	return dec
}

func decisionCreateCmd() *cobra.Command {
	var d domain.Decision
	var rationale []string
	var alternatives []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create decision",
		RunE: func(cmd *cobra.Command, args []string) error {
			d.RationaleJSON = toJSONArray(rationale)
			d.AlternativesJSON = toJSONArray(alternatives)
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if d.ProjectID == "" {
					d.ProjectID = e.Config.Project.ID
				}
				res, err := e.CreateDecision(ctx, d, viper.GetString("actor-id"))
				if err != nil {
					return err
				}
				return printJSONOrTable(res)
			})
		},
	}
	cmd.Flags().StringVar(&d.ID, "id", "", "decision id")
	cmd.Flags().StringVar(&d.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&d.Title, "title", "", "title")
	cmd.Flags().StringVar(&d.Decision, "decision", "", "decision text")
	cmd.Flags().StringArrayVar(&rationale, "rationale", []string{}, "rationale entries")
	cmd.Flags().StringArrayVar(&alternatives, "alternatives", []string{}, "alternative entries")
	cmd.Flags().StringVar(&d.ContextJSON, "context-json", "", "context JSON")
	cmd.Flags().StringVar(&d.DeciderID, "decider-id", "", "decider id")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("decision")
	_ = cmd.MarkFlagRequired("decider-id")
	return cmd
}

func attestCmd() *cobra.Command {
	a := &cobra.Command{
		Use:   "attest",
		Short: "Manage attestations",
		Long:  "Attestations are proof stickers (ci.passed, review.approved, acceptance.passed, etc.) attached to tasks or iterations. Policies check these before letting work finish.",
	}
	a.AddCommand(attestAddCmd())
	a.AddCommand(attestListCmd())
	return a
}

func attestAddCmd() *cobra.Command {
	var att domain.Attestation
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add attestation",
		RunE: func(cmd *cobra.Command, args []string) error {
			att.ActorID = viper.GetString("actor-id")
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if att.ProjectID == "" {
					att.ProjectID = e.Config.Project.ID
				}
				res, err := e.AddAttestation(ctx, att, viper.GetString("actor-id"))
				if err != nil {
					return err
				}
				return printJSONOrTable(res)
			})
		},
	}
	cmd.Flags().StringVar(&att.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&att.EntityKind, "entity-kind", "", "entity kind")
	cmd.Flags().StringVar(&att.EntityID, "entity-id", "", "entity id")
	cmd.Flags().StringVar(&att.Kind, "kind", "", "attestation kind")
	cmd.Flags().StringVar(&att.PayloadJSON, "payload-json", "", "payload JSON")
	_ = cmd.MarkFlagRequired("entity-kind")
	_ = cmd.MarkFlagRequired("entity-id")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func attestListCmd() *cobra.Command {
	var f repo.AttestationFilters
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List attestations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				if f.ProjectID == "" {
					f.ProjectID = e.Config.Project.ID
				}
				items, err := e.Repo.ListAttestations(ctx, f)
				if err != nil {
					return err
				}
				return printJSONOrTable(items)
			})
		},
	}
	cmd.Flags().StringVar(&f.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&f.EntityKind, "entity-kind", "", "entity kind filter")
	cmd.Flags().StringVar(&f.EntityID, "entity-id", "", "entity id filter")
	cmd.Flags().StringVar(&f.Kind, "kind", "", "kind filter")
	return cmd
}

func logCmd() *cobra.Command {
	log := &cobra.Command{
		Use:   "log",
		Short: "Event log",
		Long:  "The diary of everything that happened: task changes, policy applications, leases, and more.",
	}
	log.AddCommand(logTailCmd())
	return log
}

func rbacCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rbac",
		Short: "RBAC management",
	}
	cmd.AddCommand(rbacWhoamiCmd())
	cmd.AddCommand(rbacGrantCmd())
	cmd.AddCommand(rbacRevokeCmd())
	cmd.AddCommand(rbacAllowAttCmd())
	cmd.AddCommand(rbacDenyAttCmd())
	cmd.AddCommand(rbacBootstrapCmd())
	return cmd
}

func rbacWhoamiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show current actor roles and permissions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				who, err := e.WhoAmI(ctx, e.Config.Project.ID, viper.GetString("actor-id"))
				if err != nil {
					return err
				}
				return printJSONOrTable(who)
			})
		},
	}
	return cmd
}

func rbacGrantCmd() *cobra.Command {
	var target, role string
	cmd := &cobra.Command{
		Use:   "grant-role",
		Short: "Grant role to actor",
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" || role == "" {
				return fmt.Errorf("--actor and --role required")
			}
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return e.GrantRole(ctx, e.Config.Project.ID, viper.GetString("actor-id"), target, role)
			})
		},
	}
	cmd.Flags().StringVar(&target, "actor", "", "actor id")
	cmd.Flags().StringVar(&role, "role", "", "role id")
	return cmd
}

func rbacRevokeCmd() *cobra.Command {
	var target, role string
	cmd := &cobra.Command{
		Use:   "revoke-role",
		Short: "Revoke role from actor",
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" || role == "" {
				return fmt.Errorf("--actor and --role required")
			}
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return e.RevokeRole(ctx, e.Config.Project.ID, viper.GetString("actor-id"), target, role)
			})
		},
	}
	cmd.Flags().StringVar(&target, "actor", "", "actor id")
	cmd.Flags().StringVar(&role, "role", "", "role id")
	return cmd
}

func rbacAllowAttCmd() *cobra.Command {
	var role, kind string
	cmd := &cobra.Command{
		Use:   "allow-attestation",
		Short: "Allow role to issue attestation kind",
		RunE: func(cmd *cobra.Command, args []string) error {
			if role == "" || kind == "" {
				return fmt.Errorf("--role and --kind required")
			}
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return e.AllowAttestationRole(ctx, e.Config.Project.ID, viper.GetString("actor-id"), kind, role)
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "role id")
	cmd.Flags().StringVar(&kind, "kind", "", "attestation kind")
	return cmd
}

func rbacDenyAttCmd() *cobra.Command {
	var role, kind string
	cmd := &cobra.Command{
		Use:   "deny-attestation",
		Short: "Remove role attestation authority",
		RunE: func(cmd *cobra.Command, args []string) error {
			if role == "" || kind == "" {
				return fmt.Errorf("--role and --kind required")
			}
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				return e.DenyAttestationRole(ctx, e.Config.Project.ID, viper.GetString("actor-id"), kind, role)
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "role id")
	cmd.Flags().StringVar(&kind, "kind", "", "attestation kind")
	return cmd
}

func rbacBootstrapCmd() *cobra.Command {
	var target, role string
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap an actor role without RBAC checks (dev only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" || role == "" {
				return fmt.Errorf("--actor and --role required")
			}
			projectID := strings.TrimSpace(viper.GetString("project"))
			if projectID == "" {
				return fmt.Errorf("project not specified; use --project or set WORKLINE_DEFAULT_PROJECT (wl project use <id>)")
			}
			return withRepo(cmd.Context(), func(ctx context.Context, r repo.Repo) error {
				if _, err := r.GetProject(ctx, projectID); err != nil {
					return err
				}
				cfg, cfgErr := r.GetProjectConfig(ctx, projectID)
				tx, err := r.DB.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if cfgErr == nil && cfg != nil {
					if roleDef, ok := cfg.RBAC.Roles[role]; ok {
						if err := r.InsertRole(ctx, tx, role, roleDef.Description); err != nil {
							return err
						}
						for _, perm := range roleDef.Permissions {
							if err := r.InsertPermission(ctx, tx, perm, ""); err != nil {
								return err
							}
							if err := r.AddRolePermission(ctx, tx, role, perm); err != nil {
								return err
							}
						}
					} else {
						if err := r.InsertRole(ctx, tx, role, ""); err != nil {
							return err
						}
					}
				} else {
					if err := r.InsertRole(ctx, tx, role, ""); err != nil {
						return err
					}
				}
				if err := r.EnsureActor(ctx, tx, target, time.Now().UTC().Format(time.RFC3339)); err != nil {
					return err
				}
				if err := r.AssignRole(ctx, tx, projectID, target, role); err != nil {
					return err
				}
				return tx.Commit()
			})
		},
	}
	cmd.Flags().StringVar(&target, "actor", "", "actor id")
	cmd.Flags().StringVar(&role, "role", "", "role id")
	return cmd
}

func serveCmd() *cobra.Command {
	var addr, basePath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start HTTP API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace := viper.GetString("workspace")
			if _, err := db.EnsureWorkspace(workspace); err != nil {
				return err
			}
			conn, err := db.Open(db.Config{Workspace: workspace})
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := migrate.Migrate(conn); err != nil {
				return err
			}
			r := repo.Repo{DB: conn}
			_, cfg, err := app.ResolveProjectAndConfig(cmd.Context(), workspace, viper.GetString("project"), viper.GetString("actor-id"), r)
			if err != nil {
				return err
			}
			e := engine.New(conn, cfg)
			authCfg := server.AuthConfig{JWTSecret: os.Getenv("WORKLINE_JWT_SECRET")}
			if authCfg.JWTSecret == "" {
				return fmt.Errorf("WORKLINE_JWT_SECRET is required for bearer auth")
			}
			handler, err := server.New(server.Config{Engine: e, BasePath: basePath, Auth: authCfg})
			if err != nil {
				return err
			}
			srv := &http.Server{Addr: addr, Handler: handler}
			go func() {
				<-cmd.Context().Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(ctx)
			}()
			fmt.Printf("Serving Workline API on http://%s%s (OpenAPI at /openapi.json, Swagger UI at /docs)\n", addr, basePath)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "listen address")
	cmd.Flags().StringVar(&basePath, "base-path", "/v0", "API base path")
	return cmd
}

func logTailCmd() *cobra.Command {
	var n int
	var evtType, entityKind, entityID string
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Tail events",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEngine(cmd.Context(), func(ctx context.Context, e engine.Engine) error {
				events, err := e.Repo.LatestEvents(ctx, n, e.Config.Project.ID, evtType, entityKind, entityID)
				if err != nil {
					return err
				}
				return printJSONOrTable(events)
			})
		},
	}
	cmd.Flags().IntVar(&n, "n", 20, "number of events")
	cmd.Flags().StringVar(&evtType, "type", "", "event type filter")
	cmd.Flags().StringVar(&entityKind, "entity-kind", "", "entity kind")
	cmd.Flags().StringVar(&entityID, "entity-id", "", "entity id")
	return cmd
}

// --- helpers ---

func withEngine(ctx context.Context, fn func(context.Context, engine.Engine) error) error {
	workspace := viper.GetString("workspace")
	conn, err := db.Open(db.Config{Workspace: workspace})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := migrate.Migrate(conn); err != nil {
		return err
	}
	r := repo.Repo{DB: conn}
	_, cfg, err := app.ResolveProjectAndConfig(ctx, workspace, viper.GetString("project"), viper.GetString("actor-id"), r)
	if err != nil {
		return err
	}
	e := engine.New(conn, cfg)
	return fn(ctx, e)
}

func withRepo(ctx context.Context, fn func(context.Context, repo.Repo) error) error {
	workspace := viper.GetString("workspace")
	conn, err := db.Open(db.Config{Workspace: workspace})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := migrate.Migrate(conn); err != nil {
		return err
	}
	r := repo.Repo{DB: conn}
	return fn(ctx, r)
}

func printJSONOrTable(v any) error {
	if viper.GetBool("json") {
		return printJSON(v)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func toJSONArray(items []string) string {
	b, _ := json.Marshal(items)
	return string(b)
}

func setEnvValue(path, key, value string) error {
	var lines []string
	seen := false
	f, err := os.Open(path)
	if err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, key+"=") {
				lines = append(lines, fmt.Sprintf("%s=%s", key, value))
				seen = true
			} else {
				lines = append(lines, line)
			}
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			return err
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	if !seen {
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
	}
	content := strings.Join(lines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func printTaskTree(t domain.Task, children map[string][]domain.Task, prefix string, last bool) {
	connector := "├── "
	newPrefix := prefix + "│   "
	if last {
		connector = "└── "
		newPrefix = prefix + "    "
	}
	fmt.Printf("%s%s%s [%s]\n", prefix, connector, t.Title, t.Status)
	for i, c := range children[t.ID] {
		printTaskTree(c, children, newPrefix, i == len(children[t.ID])-1)
	}
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
