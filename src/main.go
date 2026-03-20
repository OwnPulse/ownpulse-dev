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
