package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/ownpulse/ownpulse-dev/src/config"
)

var (
	ok   = color.New(color.FgGreen).SprintFunc()
	info = color.New(color.FgCyan).SprintFunc()
	warn = color.New(color.FgYellow).SprintFunc()
	fail = color.New(color.FgRed).SprintFunc()
)

// SetupOptions controls what setup does.
type SetupOptions struct {
	Repos      []string // empty = all repos in config
	DryRun     bool
	AgentsPath string // resolved absolute path to agent definitions
}

// Setup clones repos and links agent definitions.
func Setup(cfg *config.WorkspaceConfig, opts SetupOptions) error {
	repos := filterRepos(cfg.Repos, opts.Repos)
	if len(repos) == 0 {
		return fmt.Errorf("no matching repos found")
	}

	fmt.Printf("\n%s Setting up workspace: %s\n\n", info("→"), cfg.Workspace.Name)

	for _, repo := range repos {
		if err := setupRepo(cfg, repo, opts); err != nil {
			fmt.Printf("  %s %s: %v\n", fail("✗"), repo.Name, err)
			continue
		}
	}

	fmt.Printf("\n%s Workspace ready at %s\n", ok("✓"), cfg.Workspace.CloneRoot)
	fmt.Printf("  cd into any repo and run claude — agents are linked.\n")
	fmt.Printf("  Agents create their own worktrees for isolation.\n\n")
	return nil
}

func setupRepo(cfg *config.WorkspaceConfig, repo config.RepoConfig, opts SetupOptions) error {
	repoDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name)
	cloneURL := fmt.Sprintf("git@github.com:%s/%s.git", repo.Org, repo.Name)

	fmt.Printf("  %s %s (%s)\n", info("→"), repo.Name, repo.Description)

	// Clone if not already present.
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		fmt.Printf("    cloning %s...\n", cloneURL)
		if !opts.DryRun {
			if err := run("git", "clone", cloneURL, repoDir); err != nil {
				return fmt.Errorf("git clone: %w", err)
			}
		}
	} else {
		fmt.Printf("    %s already cloned\n", warn("~"))
	}

	// Link agent definitions.
	if err := LinkAgents(repoDir, repo.Agents, opts.AgentsPath, opts.DryRun); err != nil {
		return fmt.Errorf("linking agents: %w", err)
	}

	fmt.Printf("    %s done\n", ok("✓"))
	return nil
}

// LinkAgents symlinks agent .md files into targetDir's .claude/agents/ dir.
func LinkAgents(targetDir string, agentNames []string, agentsPath string, dryRun bool) error {
	if len(agentNames) == 0 {
		return nil
	}

	agentsDir := filepath.Join(targetDir, ".claude", "agents")

	if !dryRun {
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			return err
		}
	}

	for _, agentName := range agentNames {
		src := filepath.Join(agentsPath, agentName+".md")
		dst := filepath.Join(agentsDir, agentName+".md")

		if _, err := os.Stat(src); os.IsNotExist(err) {
			fmt.Printf("    %s agent definition not found: %s\n", warn("!"), agentName)
			continue
		}

		if !dryRun {
			_ = os.Remove(dst)
			if err := os.Symlink(src, dst); err != nil {
				return fmt.Errorf("symlink agent %s: %w", agentName, err)
			}
		}
		fmt.Printf("    linked agent: %s\n", agentName)
	}

	return nil
}

// Teardown removes repo directories.
func Teardown(cfg *config.WorkspaceConfig, repos []string, removeRepos bool, dryRun bool) error {
	targets := filterRepos(cfg.Repos, repos)

	fmt.Printf("\n%s Tearing down workspace: %s\n\n", warn("→"), cfg.Workspace.Name)

	for _, repo := range targets {
		repoDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name)

		// Prune any worktrees (agent-created or otherwise).
		if !dryRun {
			_ = runSilent("git", "-C", repoDir, "worktree", "prune")
		}

		if removeRepos {
			fmt.Printf("  removing repo %s\n", repoDir)
			if !dryRun {
				_ = os.RemoveAll(repoDir)
			}
		}
	}

	fmt.Printf("\n%s Teardown complete\n", ok("✓"))
	return nil
}

func filterRepos(all []config.RepoConfig, names []string) []config.RepoConfig {
	if len(names) == 0 {
		return all
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[strings.ToLower(n)] = true
	}
	var out []config.RepoConfig
	for _, r := range all {
		if nameSet[strings.ToLower(r.Name)] {
			out = append(out, r)
		}
	}
	return out
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}
