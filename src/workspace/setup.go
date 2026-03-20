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
	Container  bool     // true = Docker mode (default), false = local
	DryRun     bool
	AgentsPath string   // resolved absolute path to agent definitions
}

// Setup clones repos, creates worktrees, and links agent definitions.
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
		fmt.Printf("    %s already cloned, skipping\n", warn("~"))
	}

	// Create worktrees.
	for _, wt := range repo.Worktrees {
		wtDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name+"-"+wt)
		wtBranch := fmt.Sprintf("worktree/%s", wt)

		if _, err := os.Stat(wtDir); os.IsNotExist(err) {
			fmt.Printf("    creating worktree %s → %s\n", wt, wtDir)
			if !opts.DryRun {
				// Create the branch if it doesn't exist, then add worktree.
				branchExists := runSilent("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+wtBranch) == nil
				args := []string{"-C", repoDir, "worktree", "add", wtDir}
				if branchExists {
					args = append(args, wtBranch)
				} else {
					args = append(args, "-b", wtBranch)
				}
				if err := run("git", args...); err != nil {
					return fmt.Errorf("git worktree add %s: %w", wt, err)
				}
			}
		} else {
			fmt.Printf("    %s worktree %s already exists\n", warn("~"), wt)
		}
	}

	// Link agent definitions.
	if err := linkAgents(cfg, repo, opts); err != nil {
		return fmt.Errorf("linking agents: %w", err)
	}

	fmt.Printf("    %s done\n", ok("✓"))
	return nil
}

// linkAgents symlinks the relevant agent .md files into the repo's .claude/agents/ dir.
func linkAgents(cfg *config.WorkspaceConfig, repo config.RepoConfig, opts SetupOptions) error {
	if len(repo.Agents) == 0 {
		return nil
	}

	repoDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name)
	agentsDir := filepath.Join(repoDir, ".claude", "agents")

	if !opts.DryRun {
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			return err
		}
	}

	for _, agentName := range repo.Agents {
		src := filepath.Join(opts.AgentsPath, agentName+".md")
		dst := filepath.Join(agentsDir, agentName+".md")

		if _, err := os.Stat(src); os.IsNotExist(err) {
			fmt.Printf("    %s agent definition not found: %s\n", warn("!"), agentName)
			continue
		}

		if !opts.DryRun {
			// Remove existing symlink or file before (re)linking.
			_ = os.Remove(dst)
			if err := os.Symlink(src, dst); err != nil {
				return fmt.Errorf("symlink agent %s: %w", agentName, err)
			}
		}
		fmt.Printf("    linked agent: %s\n", agentName)
	}

	return nil
}

// Teardown removes worktrees and optionally the repo directories.
func Teardown(cfg *config.WorkspaceConfig, repos []string, removeRepos bool, dryRun bool) error {
	targets := filterRepos(cfg.Repos, repos)

	fmt.Printf("\n%s Tearing down workspace: %s\n\n", warn("→"), cfg.Workspace.Name)

	for _, repo := range targets {
		repoDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name)

		for _, wt := range repo.Worktrees {
			wtDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name+"-"+wt)
			fmt.Printf("  removing worktree %s\n", wtDir)
			if !dryRun {
				_ = run("git", "-C", repoDir, "worktree", "remove", "--force", wtDir)
				_ = os.RemoveAll(wtDir)
			}
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

// filterRepos returns only repos matching the given names, or all repos if names is empty.
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
