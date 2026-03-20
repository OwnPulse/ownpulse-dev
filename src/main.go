package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ownpulse/ownpulse-dev/src/config"
	"github.com/ownpulse/ownpulse-dev/src/workspace"
)

var (
	configPath  string
	overlayPath string
	dryRun      bool
)

func main() {
	root := &cobra.Command{
		Use:   "opdev",
		Short: "OwnPulse developer workspace tool",
		Long: `opdev bootstraps and manages the OwnPulse development workspace.

It clones repos, creates git worktrees, links Claude Code agent definitions,
and manages Claude Code session state.

Configuration is driven by workspace.toml. For hosted/private workspaces,
create a workspace.override.toml alongside it — see config/workspace.override.toml.example.`,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "path to workspace.toml (default: ./config/workspace.toml)")
	root.PersistentFlags().StringVar(&overlayPath, "overlay", "", "path to workspace.override.toml (default: auto-detected alongside config)")
	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "print what would happen without making changes")

	root.AddCommand(
		setupCmd(),
		sessionCmd(),
		statusCmd(),
		teardownCmd(),
		listCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// setupCmd clones repos, creates worktrees, links agents.
func setupCmd() *cobra.Command {
	var repos []string
	var local bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Bootstrap the workspace — clone repos, create worktrees, link agents",
		Example: `  opdev setup                        # set up all repos
  opdev setup --repos ownpulse        # set up one repo
  opdev setup --local                 # skip Docker, use local toolchain
  opdev setup --overlay ./workspace.override.toml`,
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
				Container:  !local,
				DryRun:     dryRun,
				AgentsPath: agentsPath,
			})
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repos", nil, "comma-separated repo names to set up (default: all)")
	cmd.Flags().BoolVar(&local, "local", false, "use local toolchain instead of Docker containers")
	return cmd
}

// sessionCmd spawns a Claude Code session for a repo or worktree.
func sessionCmd() *cobra.Command {
	var worktree string
	var teams bool

	cmd := &cobra.Command{
		Use:   "session <repo>",
		Short: "Spawn a Claude Code session for a repo or worktree",
		Example: `  opdev session ownpulse                     # session in main repo dir
  opdev session ownpulse --worktree backend  # session in backend worktree
  opdev session ownpulse --teams             # enable experimental agent teams`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return workspace.SpawnSession(cfg, args[0], worktree, teams, dryRun)
		},
	}

	cmd.Flags().StringVar(&worktree, "worktree", "", "worktree name to use as the session directory")
	cmd.Flags().BoolVar(&teams, "teams", false, "set CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 for this session")
	return cmd
}

// statusCmd shows all tracked sessions.
func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show status of tracked Claude Code sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return workspace.ListSessions(cfg)
		},
	}
}

// teardownCmd removes worktrees and optionally repos.
func teardownCmd() *cobra.Command {
	var repos []string
	var removeRepos bool
	var killSessions bool

	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove worktrees and optionally repo directories",
		Example: `  opdev teardown                     # remove all worktrees
  opdev teardown --repos ownpulse    # remove worktrees for one repo
  opdev teardown --remove-repos      # also delete repo directories
  opdev teardown --kill-sessions     # kill tracked Claude Code sessions first`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if killSessions {
				if err := workspace.KillSessions(cfg.Workspace.CloneRoot, ""); err != nil {
					fmt.Fprintf(os.Stderr, "%s failed to kill sessions: %v\n", color.YellowString("!"), err)
				}
			}

			return workspace.Teardown(cfg, repos, removeRepos, dryRun)
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repos", nil, "repos to tear down (default: all)")
	cmd.Flags().BoolVar(&removeRepos, "remove-repos", false, "delete repo directories (not just worktrees)")
	cmd.Flags().BoolVar(&killSessions, "kill-sessions", false, "kill tracked Claude Code sessions before teardown")
	return cmd
}

// listCmd prints repos and agents from the merged config.
func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List repos and agents defined in the workspace config",
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
					fmt.Printf("    agents: %s\n", cyan(joinStrings(r.Agents)))
				}
				if len(r.Worktrees) > 0 {
					fmt.Printf("    worktrees: %s\n", joinStrings(r.Worktrees))
				}
				fmt.Println()
			}
			return nil
		},
	}
}

// loadConfig finds and loads the workspace config with any overlay.
func loadConfig() (*config.WorkspaceConfig, error) {
	base := configPath
	if base == "" {
		// Auto-detect: look for config/workspace.toml relative to binary location,
		// then relative to cwd.
		candidates := []string{
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
			return nil, fmt.Errorf("could not find workspace.toml — use --config to specify its path")
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
	abs, err := filepath.Abs(agentsPath)
	if err != nil {
		return "", fmt.Errorf("resolving agents path: %w", err)
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return "", fmt.Errorf("agents directory not found at %s", abs)
	}
	return abs, nil
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
