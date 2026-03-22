package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ownpulse/ownpulse-dev/src/config"
)

// SessionOptions controls how a session is created and launched.
type SessionOptions struct {
	RepoName       string // which repo (empty = detect from cwd)
	Name           string // session name (used in branch + worktree dir)
	DryRun         bool
	NoLaunch       bool   // create worktree but don't launch claude
	SkipPermissions bool  // pass --dangerously-skip-permissions to claude (default true)
}

// Session creates an isolated worktree and launches Claude Code in it.
func Session(cfg *config.WorkspaceConfig, opts SessionOptions) error {
	repoDir, repoName, err := resolveRepo(cfg, opts.RepoName)
	if err != nil {
		return err
	}

	name := opts.Name
	if name == "" {
		id, err := shortID()
		if err != nil {
			return fmt.Errorf("generating session id: %w", err)
		}
		name = id
	}

	worktreeDir := filepath.Join(filepath.Dir(repoDir), "worktrees", fmt.Sprintf("%s-%s", repoName, name))
	branch := fmt.Sprintf("work/%s", name)

	fmt.Printf("\n%s Creating session for %s\n\n", info("→"), repoName)
	fmt.Printf("  worktree: %s\n", worktreeDir)
	fmt.Printf("  branch:   %s\n", branch)

	if opts.DryRun {
		fmt.Printf("\n  %s dry run — no changes made\n", warn("~"))
		return nil
	}

	// Create parent directory.
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return fmt.Errorf("creating worktrees dir: %w", err)
	}

	// Create worktree. Try new branch first; if it exists, check it out.
	if err := run("git", "-C", repoDir, "worktree", "add", worktreeDir, "-b", branch); err != nil {
		// Branch might already exist — try without -b.
		if err2 := run("git", "-C", repoDir, "worktree", "add", worktreeDir, branch); err2 != nil {
			return fmt.Errorf("git worktree add: %w (also tried existing branch: %v)", err, err2)
		}
	}

	fmt.Printf("\n  %s worktree ready\n", ok("✓"))

	if opts.NoLaunch {
		fmt.Printf("\n  cd %s && claude\n\n", worktreeDir)
		return nil
	}

	// Launch claude in the worktree via exec (replaces this process).
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		// Can't find claude — just print instructions.
		fmt.Printf("\n  claude not found in PATH — run manually:\n")
		fmt.Printf("  cd %s && claude\n\n", worktreeDir)
		return nil
	}

	args := []string{"claude"}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	sessionName := fmt.Sprintf("%s/%s", repoName, name)
	args = append(args, "--name", sessionName)

	fmt.Printf("  %s launching claude\n\n", info("→"))

	// Replace this process with claude.
	return syscall.Exec(claudePath, args, appendEnv(os.Environ(), "PWD="+worktreeDir))
}

// ListSessions lists active worktrees for a repo.
func ListSessions(cfg *config.WorkspaceConfig, repoName string) error {
	repoDir, name, err := resolveRepo(cfg, repoName)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s Worktrees for %s\n\n", info("→"), name)

	cmd := exec.Command("git", "-C", repoDir, "worktree", "list")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CleanSessions removes worktrees that are no longer needed.
func CleanSessions(cfg *config.WorkspaceConfig, repoName string, dryRun bool) error {
	repoDir, name, err := resolveRepo(cfg, repoName)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s Pruning worktrees for %s\n", info("→"), name)

	if dryRun {
		fmt.Printf("  %s dry run\n", warn("~"))
		return nil
	}

	return run("git", "-C", repoDir, "worktree", "prune")
}

// resolveRepo finds the repo directory, either from the name or by detecting cwd.
func resolveRepo(cfg *config.WorkspaceConfig, name string) (string, string, error) {
	if name != "" {
		for _, r := range cfg.Repos {
			if strings.EqualFold(r.Name, name) {
				return filepath.Join(cfg.Workspace.CloneRoot, r.Name), r.Name, nil
			}
		}
		return "", "", fmt.Errorf("repo %q not found in workspace config", name)
	}

	// Detect from cwd. Resolve symlinks to handle macOS /private/var vs /var.
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	cwdReal, _ := filepath.EvalSymlinks(cwd)

	for _, r := range cfg.Repos {
		repoDir := filepath.Join(cfg.Workspace.CloneRoot, r.Name)
		repoDirReal, _ := filepath.EvalSymlinks(repoDir)
		if strings.HasPrefix(cwdReal, repoDirReal) || strings.HasPrefix(cwd, repoDir) {
			return repoDir, r.Name, nil
		}
	}

	return "", "", fmt.Errorf("could not detect repo from cwd %s — use --repo", cwd)
}

func shortID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func appendEnv(env []string, kv string) []string {
	key := strings.SplitN(kv, "=", 2)[0] + "="
	for i, e := range env {
		if strings.HasPrefix(e, key) {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}
