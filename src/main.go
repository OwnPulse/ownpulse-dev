package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ownpulse/ownpulse-dev/src/config"
	"github.com/ownpulse/ownpulse-dev/src/workspace"
)

var version = "dev"

var (
	configPath  string
	overlayPath string
	dryRun      bool
)

func main() {
	root := &cobra.Command{
		Use:   "opdev",
		Short: "OwnPulse developer workspace tool",
		Long: `opdev sets up the OwnPulse development workspace.

It clones repos and links Claude Code agent definitions so you can
cd into any repo and run claude.

Configuration is driven by workspace.toml. For hosted/private workspaces,
create a workspace.override.toml alongside it.`,
		Version: version,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "path to workspace.toml")
	root.PersistentFlags().StringVar(&overlayPath, "overlay", "", "path to workspace.override.toml")
	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "print what would happen without making changes")

	root.AddCommand(
		setupCmd(),
		teardownCmd(),
		listCmd(),
		sessionCmd(),
		cleanCmd(),
		e2eCmd(),
		updateCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func setupCmd() *cobra.Command {
	var repos []string

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Clone repos and link agent definitions",
		Example: `  opdev setup
  opdev setup --repos ownpulse`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			agentsPath, err := resolveAgentsPath(cfg)
			if err != nil {
				return err
			}

			return workspace.Setup(cfg, workspace.SetupOptions{
				Repos:      repos,
				DryRun:     dryRun,
				AgentsPath: agentsPath,
			})
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repos", nil, "repos to set up (default: all)")
	return cmd
}

func teardownCmd() *cobra.Command {
	var repos []string
	var removeRepos bool

	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove repo directories and prune worktrees",
		Example: `  opdev teardown
  opdev teardown --repos ownpulse
  opdev teardown --remove-repos`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return workspace.Teardown(cfg, repos, removeRepos, dryRun)
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repos", nil, "repos to tear down (default: all)")
	cmd.Flags().BoolVar(&removeRepos, "remove-repos", false, "delete repo directories too")
	return cmd
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List repos and agents from the workspace config",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			bold := color.New(color.Bold).SprintFunc()
			cyan := color.New(color.FgCyan).SprintFunc()

			fmt.Printf("\n%s  (%s)\n\n", bold(cfg.Workspace.Name), cfg.Workspace.CloneRoot)
			for _, r := range cfg.Repos {
				vis := color.GreenString("public")
				if r.Visibility == "private" {
					vis = color.YellowString("private")
				}
				fmt.Printf("  %s  [%s]  %s/%s@%s\n", bold(r.Name), vis, r.Org, r.Name, r.Branch)
				fmt.Printf("    %s\n", r.Description)
				if len(r.Agents) > 0 {
					fmt.Printf("    agents: %s\n", cyan(strings.Join(r.Agents, ", ")))
				}
				fmt.Println()
			}
			return nil
		},
	}
}

func loadConfig() (*config.WorkspaceConfig, error) {
	base := configPath
	if base == "" {
		base = os.Getenv("OPDEV_CONFIG")
	}
	if base == "" {
		home, _ := os.UserHomeDir()
		candidates := []string{
			filepath.Join(home, ".config", "ownpulse", "workspace.toml"),
			"config/workspace.toml",
			"workspace.toml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				base = c
				break
			}
		}
		if base == "" {
			return nil, fmt.Errorf("could not find workspace.toml — use --config or set OPDEV_CONFIG")
		}
	}

	base, _ = filepath.Abs(base)

	overlay := overlayPath
	if overlay == "" {
		overlay = config.DefaultOverlayPath(base)
	}

	return config.Load(base, overlay)
}

func sessionCmd() *cobra.Command {
	var repo string
	var noLaunch bool
	var dangerousPermissions bool

	cmd := &cobra.Command{
		Use:   "session [name]",
		Short: "Create an isolated worktree and launch Claude Code",
		Long: `Creates a git worktree for an isolated Claude Code session.

Each session gets its own working copy so multiple sessions can run
in parallel without stomping on each other. Claude Code is launched
with --dangerously-skip-permissions by default.

If no name is given, a random ID is used.`,
		Example: `  opdev session backend-auth
  opdev session --repo ownpulse backend-auth
  opdev session --no-launch backend-auth
  opdev session --safe backend-auth`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			name := ""
			if len(args) > 0 {
				name = args[0]
			}

			return workspace.Session(cfg, workspace.SessionOptions{
				RepoName:        repo,
				Name:            name,
				DryRun:          dryRun,
				NoLaunch:        noLaunch,
				SkipPermissions: !dangerousPermissions,
			})
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repo name (default: detect from cwd)")
	cmd.Flags().BoolVar(&noLaunch, "no-launch", false, "create worktree but don't launch claude")
	cmd.Flags().BoolVar(&dangerousPermissions, "safe", false, "launch claude without --dangerously-skip-permissions")
	return cmd
}

func cleanCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Prune stale worktrees",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return workspace.CleanSessions(cfg, repo, dryRun)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repo name (default: detect from cwd)")
	return cmd
}

func e2eCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "e2e [up|build|seed|status|teardown]",
		Short: "Manage a local k3d cluster for end-to-end testing",
		Long: `Creates and manages a local k3d Kubernetes cluster that mirrors
production. Builds Docker images, deploys via Helm, and seeds test data.

Requires: docker, k3d, kubectl, helm`,
		Example: `  opdev e2e             # full setup: cluster + build + deploy + seed
  opdev e2e up          # same as above
  opdev e2e build       # rebuild images and redeploy
  opdev e2e seed        # re-seed test data only
  opdev e2e status      # show pods, services, health checks
  opdev e2e teardown    # delete the cluster`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"up", "build", "seed", "status", "teardown"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			opts := workspace.E2EOptions{DryRun: dryRun}
			sub := "up"
			if len(args) > 0 {
				sub = args[0]
			}

			switch sub {
			case "up":
				return workspace.E2EUp(cfg, opts)
			case "build":
				return workspace.E2EBuild(cfg, opts)
			case "seed":
				return workspace.E2ESeed(cfg, opts)
			case "status":
				return workspace.E2EStatus(cfg, opts)
			case "teardown":
				return workspace.E2ETeardown(cfg, opts)
			default:
				return fmt.Errorf("unknown e2e subcommand: %s", sub)
			}
		},
	}
	return cmd
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update opdev to the latest release",
		Long: `Downloads the latest opdev release from GitHub and replaces the
current binary. Supports macOS (arm64, amd64) and Linux (arm64, amd64).

May require sudo if installed to a system directory like /usr/local/bin.`,
		Example: `  opdev update
  opdev update --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return workspace.Update(version, workspace.UpdateOptions{DryRun: dryRun})
		},
	}
}

func resolveAgentsPath(cfg *config.WorkspaceConfig) (string, error) {
	agentsPath := cfg.Agents.DefinitionsPath
	if agentsPath == "" {
		agentsPath = "./agents"
	}
	if !filepath.IsAbs(agentsPath) {
		agentsPath = filepath.Join(cfg.ConfigDir(), agentsPath)
	}
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		return "", fmt.Errorf("agents directory not found at %s", agentsPath)
	}
	return agentsPath, nil
}
