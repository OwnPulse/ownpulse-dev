package workspace

import (
	"encoding/json"
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

	// Generate .claude/settings.json (teams env var).
	if err := generateSettings(repoDir, opts.DryRun); err != nil {
		fmt.Printf("    %s could not generate settings: %v\n", warn("!"), err)
	}

	// Generate .claude/CLAUDE.md (workspace context).
	if err := generateCLAUDEmd(cfg, repo, repoDir, opts.DryRun); err != nil {
		fmt.Printf("    %s could not generate CLAUDE.md: %v\n", warn("!"), err)
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

// generateSettings writes .claude/settings.json with agent teams enabled.
// If the file already exists, it merges into the existing content.
func generateSettings(repoDir string, dryRun bool) error {
	path := filepath.Join(repoDir, ".claude", "settings.json")

	settings := map[string]interface{}{}

	// Read existing if present — preserve user settings.
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	// Ensure env map exists and set teams flag.
	env, ok := settings["env"].(map[string]interface{})
	if !ok {
		env = map[string]interface{}{}
	}
	env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] = "1"
	settings["env"] = env

	if dryRun {
		fmt.Printf("    would generate .claude/settings.json\n")
		return nil
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// generateCLAUDEmd writes .claude/CLAUDE.md with workspace context for the lead session.
// Always overwrites — this is a generated file.
func generateCLAUDEmd(cfg *config.WorkspaceConfig, repo config.RepoConfig, repoDir string, dryRun bool) error {
	path := filepath.Join(repoDir, ".claude", "CLAUDE.md")

	if dryRun {
		fmt.Printf("    would generate .claude/CLAUDE.md\n")
		return nil
	}

	var b strings.Builder
	b.WriteString("<!-- Generated by opdev setup — do not edit manually -->\n")
	b.WriteString("# OwnPulse Workspace\n\n")
	b.WriteString("This repo is part of the OwnPulse workspace managed by opdev.\n\n")

	b.WriteString("## Repos\n")
	for _, r := range cfg.Repos {
		b.WriteString(fmt.Sprintf("- **%s** — %s\n", r.Name, r.Description))
	}

	b.WriteString("\n## Agents in this repo\n")
	for _, a := range repo.Agents {
		b.WriteString(fmt.Sprintf("- %s\n", a))
	}

	b.WriteString("\n## Worktree policy\n")
	b.WriteString("Every agent that modifies code creates a git worktree before starting.\n")
	b.WriteString("The primary checkout stays on main as a clean reference.\n")

	b.WriteString("\n## Agent teams\n")
	b.WriteString("This workspace has agent teams enabled. When you need parallel work,\n")
	b.WriteString("spawn teammates using the available agents. Each teammate creates its\n")
	b.WriteString("own worktree and works independently.\n")

	b.WriteString("\n## Default workflow\n")
	b.WriteString("When given a feature or task:\n")
	b.WriteString("1. Break it into the smallest parallel units that make sense.\n")
	b.WriteString("2. Spawn the relevant agents as teammates — each handles its slice end-to-end ")
	b.WriteString("(design, implement, test).\n")
	b.WriteString("3. After teammates finish, review their work yourself:\n")
	b.WriteString("   - Correctness: does it do what was asked?\n")
	b.WriteString("   - Security: injection, auth bypass, secret leakage, OWASP top 10.\n")
	b.WriteString("   - Simplicity: no over-engineering, no unnecessary abstractions.\n")
	b.WriteString("4. Fix issues or ask teammates to revise before reporting done.\n")
	b.WriteString("\nKeep implementations minimal. Prefer fewer files, less indirection, and no ")
	b.WriteString("speculative features. If it can be three lines instead of a helper, use three lines.\n")

	return os.WriteFile(path, []byte(b.String()), 0644)
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
