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

	// Link agent definitions — all agents go to every repo.
	if err := LinkAgents(repoDir, opts.AgentsPath, opts.DryRun); err != nil {
		return fmt.Errorf("linking agents: %w", err)
	}

	// Generate .claude/settings.json (teams env var).
	if err := generateSettings(repoDir, opts.DryRun); err != nil {
		fmt.Printf("    %s could not generate settings: %v\n", warn("!"), err)
	}

	// Generate .claude/CLAUDE.md (workspace context).
	if err := generateCLAUDEmd(cfg, repo, repoDir, opts.AgentsPath, opts.DryRun); err != nil {
		fmt.Printf("    %s could not generate CLAUDE.md: %v\n", warn("!"), err)
	}

	fmt.Printf("    %s done\n", ok("✓"))
	return nil
}

// LinkAgents symlinks all agent .md files from agentsPath into targetDir's .claude/agents/ dir.
// Every repo gets every agent so sessions can spawn cross-repo work.
func LinkAgents(targetDir string, agentsPath string, dryRun bool) error {
	entries, err := os.ReadDir(agentsPath)
	if err != nil {
		return fmt.Errorf("reading agents directory %s: %w", agentsPath, err)
	}

	agentsDir := filepath.Join(targetDir, ".claude", "agents")

	if !dryRun {
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			return err
		}
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		src := filepath.Join(agentsPath, entry.Name())
		dst := filepath.Join(agentsDir, entry.Name())

		if !dryRun {
			_ = os.Remove(dst)
			if err := os.Symlink(src, dst); err != nil {
				return fmt.Errorf("symlink agent %s: %w", entry.Name(), err)
			}
		}
		agentName := strings.TrimSuffix(entry.Name(), ".md")
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
func generateCLAUDEmd(cfg *config.WorkspaceConfig, repo config.RepoConfig, repoDir string, agentsPath string, dryRun bool) error {
	path := filepath.Join(repoDir, ".claude", "CLAUDE.md")

	if dryRun {
		fmt.Printf("    would generate .claude/CLAUDE.md\n")
		return nil
	}

	var b strings.Builder
	b.WriteString("<!-- Generated by opdev setup — edit the template in ownpulse-dev/src/workspace/setup.go -->\n")
	b.WriteString("# OwnPulse — Agent Conventions\n\n")

	// Hard rules — these MUST be at the top so every session sees them immediately.
	b.WriteString("## Hard Rules (violations = immediate revert)\n\n")
	b.WriteString("1. **Everything is IaC.** Never run ad-hoc `kubectl`, `helm install`, `tofu apply`,\n")
	b.WriteString("   SSH commands, or any manual infrastructure changes. Every change is a committed\n")
	b.WriteString("   change to OpenTofu, Helm values, Ansible playbooks, or GitHub Actions. If something\n")
	b.WriteString("   is broken, fix the code and let the pipeline apply it.\n")
	b.WriteString("2. **No telemetry, analytics, or third-party data egress** without explicit user consent.\n")
	b.WriteString("3. **No health data in logs, error messages, or crash reports.**\n")
	b.WriteString("4. **Never skip or disable tests to make CI pass.** Fix the underlying problem.\n")
	b.WriteString("5. **Secrets in SOPS + age only.** Never commit plaintext secrets.\n")
	b.WriteString("6. **Self-hosting must work.** Every feature must work with `helm upgrade --install`\n")
	b.WriteString("   and Postgres. No required cloud services.\n")
	b.WriteString("7. **Do not modify files outside your assigned area** without flagging it.\n")
	b.WriteString("8. **Review before committing.** Run code-review, test-review, and security-review\n")
	b.WriteString("   agents on all changes before committing. Run principles-guardian on any change\n")
	b.WriteString("   that touches data collection, export, sharing, or external integrations. Run\n")
	b.WriteString("   arch-review on plans before starting implementation. test-review is mandatory\n")
	b.WriteString("   on every PR — no exceptions.\n")
	b.WriteString("9. **Update docs with every feature.** If a change affects user-visible behavior,\n")
	b.WriteString("   update `userdocs/` (user-facing docs) and/or `docs/` (developer docs) in the\n")
	b.WriteString("   same PR. New API endpoints need `docs/architecture/api.md` updates. New user\n")
	b.WriteString("   features need a `userdocs/` page or section update. Docs and code must be\n")
	b.WriteString("   consistent — if they contradict each other, that is a bug.\n\n")

	b.WriteString("## Repos\n")
	for _, r := range cfg.Repos {
		b.WriteString(fmt.Sprintf("- **%s** — %s\n", r.Name, r.Description))
	}

	b.WriteString("\n## CI/CD\n")
	b.WriteString("Prefer shared actions from `OwnPulse/gh-actions` over inline workflow steps.\n")
	b.WriteString("Reference as: `uses: OwnPulse/gh-actions/<action-name>@v1`\n")

	b.WriteString("\n## Agents\n\n")

	// Discover all agents from the definitions directory.
	allAgents := []string{}
	if entries, err := os.ReadDir(agentsPath); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				allAgents = append(allAgents, strings.TrimSuffix(entry.Name(), ".md"))
			}
		}
	}

	// Categorize agents into write and read-only for clarity.
	writeAgents := []string{}
	reviewAgents := []string{}
	reviewSet := map[string]bool{
		"code-review":        true,
		"test-review":        true,
		"security-review":    true,
		"principles-guardian": true,
		"arch-review":        true,
		"feature-plan":       true,
	}
	for _, a := range allAgents {
		if reviewSet[a] {
			reviewAgents = append(reviewAgents, a)
		} else {
			writeAgents = append(writeAgents, a)
		}
	}
	if len(writeAgents) > 0 {
		b.WriteString("**Write agents** (spawn with `isolation: \"worktree\"`):\n")
		for _, a := range writeAgents {
			b.WriteString(fmt.Sprintf("- %s\n", a))
		}
	}
	if len(reviewAgents) > 0 {
		if len(writeAgents) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("**Review agents** (read-only, no worktree needed):\n")
		for _, a := range reviewAgents {
			b.WriteString(fmt.Sprintf("- %s\n", a))
		}
	}

	b.WriteString("\n## Agent Teams\n\n")
	b.WriteString("You are the lead session — you orchestrate, delegate, and review.\n\n")
	b.WriteString("**Use a team** for tasks touching multiple areas, parallel work, or non-trivial\n")
	b.WriteString("changes. **Do it directly** for single-file fixes and quick questions.\n\n")

	b.WriteString("### Workflow\n\n")
	b.WriteString("1. Plan: for new features, run feature-plan first — it clarifies user experience\n")
	b.WriteString("   before technical details. Then run arch-review on the resulting plan.\n")
	b.WriteString("   For smaller tasks, break into parallel units directly.\n")
	b.WriteString("2. For cross-cutting features, define the API contract first (endpoint path,\n")
	b.WriteString("   request/response types, error codes), then spawn agents in parallel.\n")
	b.WriteString("3. Spawn write agents with `isolation: \"worktree\"` — each gets an isolated\n")
	b.WriteString("   git copy automatically. Do NOT create worktrees manually.\n")
	b.WriteString("4. Update docs: if the change affects user-visible behavior, spawn the userdocs\n")
	b.WriteString("   agent to update `userdocs/`. Update `docs/architecture/api.md` for new/changed\n")
	b.WriteString("   endpoints. Update `docs/guides/self-hosting.md` for new env vars or services.\n")
	b.WriteString("5. **Before committing**, run review agents on the results:\n")
	b.WriteString("   - code-review — always\n")
	b.WriteString("   - test-review — always (this is the test coverage gate — missing tests block merge)\n")
	b.WriteString("   - security-review — always for auth, crypto, API, or data changes\n")
	b.WriteString("   - principles-guardian — for data collection, export, sharing, integrations\n")
	b.WriteString("6. Fix issues flagged by reviewers. **All must-fix items from test-review must be\n")
	b.WriteString("   resolved before committing** — test gaps are never deferred. Ask write agents\n")
	b.WriteString("   to add missing tests, then re-run test-review to confirm.\n")
	b.WriteString("7. **Clean up after yourself.** When a task is complete (committed or abandoned),\n")
	b.WriteString("   remove worktrees and branches that are no longer needed. Run `opdev clean --all`\n")
	b.WriteString("   or manually `git worktree remove <path>` + `git branch -d <branch>`. Agent\n")
	b.WriteString("   worktrees (`.claude/worktrees/agent-*`) accumulate fast — don't leave them.\n\n")

	b.WriteString("Keep implementations minimal. Prefer fewer files, less indirection, and no\n")
	b.WriteString("speculative features. If three lines work, don't write a helper.\n")

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
