package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/ownpulse/ownpulse-dev/src/config"
)

// SessionState is persisted to .ownpulse-dev/sessions.json in the clone root.
type SessionState struct {
	Sessions []Session `json:"sessions"`
}

type Session struct {
	Name      string    `json:"name"`
	RepoName  string    `json:"repo_name"`
	Worktree  string    `json:"worktree,omitempty"`
	PID       int       `json:"pid"`
	Dir       string    `json:"dir"`
	AgentMode string    `json:"agent_mode"` // "solo" or "teams"
	Managed   bool      `json:"managed"`    // true = session created this worktree, should clean up
	Branch    string    `json:"branch"`     // branch created for this session worktree
	StartedAt time.Time `json:"started_at"`
}

// SessionOptions holds parameters for spawning a session.
type SessionOptions struct {
	RepoName        string
	Worktree        string
	Teams           bool
	DryRun          bool
	AgentsPath      string // resolved absolute path to agent definitions
	DangerousPerms  bool   // pass --dangerously-skip-permissions to claude
}

// SpawnWorkspaceSession launches a Claude Code session in the workspace root (clone_root)
// with all agents from all repos linked. Use this for cross-cutting features.
func SpawnWorkspaceSession(cfg *config.WorkspaceConfig, opts SessionOptions) error {
	sessionDir := cfg.Workspace.CloneRoot

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}

	// Collect all unique agents across all repos.
	agentSet := make(map[string]bool)
	for _, repo := range cfg.Repos {
		for _, a := range repo.Agents {
			agentSet[a] = true
		}
	}
	var allAgents []string
	for a := range agentSet {
		allAgents = append(allAgents, a)
	}

	fmt.Printf("%s Launching workspace session with %d agents\n", info("→"), len(allAgents))
	fmt.Printf("    directory: %s\n", sessionDir)

	if !opts.DryRun {
		if err := LinkAgents(sessionDir, allAgents, opts.AgentsPath, false); err != nil {
			fmt.Printf("  %s could not link agents: %v\n", warn("!"), err)
		}
	} else {
		for _, a := range allAgents {
			fmt.Printf("    would link agent: %s\n", a)
		}
		fmt.Printf("    %s (dry run — would exec claude in %s)\n", warn("~"), sessionDir)
		return nil
	}

	// Build environment.
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	if opts.Teams {
		env = append(env, "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
		fmt.Printf("  %s Agent teams mode enabled\n", warn("!"))
	}

	cmd := claudeCmd(opts)
	cmd.Dir = sessionDir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude: %w — is Claude Code installed?", err)
	}

	session := Session{
		Name:      "workspace",
		RepoName:  "",
		PID:       cmd.Process.Pid,
		Dir:       sessionDir,
		AgentMode: agentMode(opts.Teams),
		Managed:   false, // workspace dir is not a managed worktree
		StartedAt: time.Now(),
	}

	if err := saveSession(cfg.Workspace.CloneRoot, session); err != nil {
		fmt.Printf("  %s could not save session state: %v\n", warn("!"), err)
	}

	fmt.Printf("  %s session started (PID %d)\n", ok("✓"), cmd.Process.Pid)
	return cmd.Wait()
}

// SpawnSession creates an isolated git worktree and launches a Claude Code session in it.
func SpawnSession(cfg *config.WorkspaceConfig, opts SessionOptions) error {
	repo, err := findRepo(cfg.Repos, opts.RepoName)
	if err != nil {
		return err
	}

	repoDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name)
	if !opts.DryRun {
		if _, err := os.Stat(repoDir); os.IsNotExist(err) {
			return fmt.Errorf("repo directory %s does not exist — run 'opdev setup' first", repoDir)
		}
	}

	// Generate a short random session ID.
	id, err := sessionID()
	if err != nil {
		return fmt.Errorf("generating session ID: %w", err)
	}

	// Build session name and worktree path.
	var sessionName string
	var sessionDir string
	if opts.Worktree != "" {
		sessionName = fmt.Sprintf("%s/%s-%s", repo.Name, opts.Worktree, id)
		sessionDir = filepath.Join(cfg.Workspace.CloneRoot, fmt.Sprintf("%s-%s-session-%s", repo.Name, opts.Worktree, id))
	} else {
		sessionName = fmt.Sprintf("%s/%s", repo.Name, id)
		sessionDir = filepath.Join(cfg.Workspace.CloneRoot, fmt.Sprintf("%s-session-%s", repo.Name, id))
	}

	branchName := fmt.Sprintf("session/%s", id)

	// Determine the start point for the worktree.
	startPoint := ""
	if opts.Worktree != "" {
		// Branch off the named worktree's branch.
		wtBranch := fmt.Sprintf("worktree/%s", opts.Worktree)
		if runSilent("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+wtBranch) == nil {
			startPoint = wtBranch
		}
	}

	fmt.Printf("%s Creating session worktree: %s\n", info("→"), sessionName)
	fmt.Printf("    directory: %s\n", sessionDir)
	fmt.Printf("    branch:    %s\n", branchName)

	if !opts.DryRun {
		args := []string{"-C", repoDir, "worktree", "add", sessionDir, "-b", branchName}
		if startPoint != "" {
			args = append(args, startPoint)
		}
		if err := run("git", args...); err != nil {
			return fmt.Errorf("creating session worktree: %w", err)
		}

		// Link agents into the session worktree.
		if err := LinkAgents(sessionDir, repo.Agents, opts.AgentsPath, false); err != nil {
			fmt.Printf("  %s could not link agents: %v\n", warn("!"), err)
		}
	} else {
		fmt.Printf("    %s (dry run — would create worktree and exec claude)\n", warn("~"))
		return nil
	}

	// Build environment.
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	if opts.Teams {
		env = append(env, "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
		fmt.Printf("  %s Agent teams mode enabled\n", warn("!"))
	}

	// Launch claude.
	cmd := claudeCmd(opts)
	cmd.Dir = sessionDir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude: %w — is Claude Code installed? (npm install -g @anthropic-ai/claude-code)", err)
	}

	session := Session{
		Name:      sessionName,
		RepoName:  repo.Name,
		Worktree:  opts.Worktree,
		PID:       cmd.Process.Pid,
		Dir:       sessionDir,
		AgentMode: agentMode(opts.Teams),
		Managed:   true,
		Branch:    branchName,
		StartedAt: time.Now(),
	}

	if err := saveSession(cfg.Workspace.CloneRoot, session); err != nil {
		fmt.Printf("  %s could not save session state: %v\n", warn("!"), err)
	}

	fmt.Printf("  %s session started (PID %d)\n", ok("✓"), cmd.Process.Pid)

	// Wait for claude to exit — it needs the foreground TTY.
	return cmd.Wait()
}

// ListSessions prints all tracked sessions and whether their processes are still running.
func ListSessions(cfg *config.WorkspaceConfig) error {
	state, err := loadSessions(cfg.Workspace.CloneRoot)
	if err != nil || len(state.Sessions) == 0 {
		fmt.Printf("No active sessions tracked.\n")
		return nil
	}

	bold := color.New(color.Bold).SprintFunc()
	fmt.Printf("\n%s\n\n", bold("Claude Code Sessions"))

	for _, s := range state.Sessions {
		alive := processAlive(s.PID)
		status := ok("running")
		if !alive {
			status = warn("stopped")
		}
		mode := ""
		if s.AgentMode == "teams" {
			mode = fmt.Sprintf(" %s", info("[teams]"))
		}
		fmt.Printf("  %-30s PID %-8s %s%s\n",
			s.Name,
			strconv.Itoa(s.PID),
			status,
			mode,
		)
		fmt.Printf("  %-30s %s\n\n", "", s.Dir)
	}

	return nil
}

// KillSessions stops tracked sessions by name or all if name is empty.
// Managed session worktrees are removed.
func KillSessions(cloneRoot, name string) error {
	state, err := loadSessions(cloneRoot)
	if err != nil {
		return err
	}

	var remaining []Session
	for _, s := range state.Sessions {
		if name != "" && s.Name != name {
			remaining = append(remaining, s)
			continue
		}
		proc, err := os.FindProcess(s.PID)
		if err == nil {
			_ = proc.Kill()
			fmt.Printf("  %s killed session %s (PID %d)\n", ok("✓"), s.Name, s.PID)
		}
		if s.Managed {
			removeSessionWorktree(cloneRoot, s)
		}
	}

	state.Sessions = remaining
	return persistSessions(cloneRoot, state)
}

// CleanupSessions removes stopped sessions and their managed worktrees.
func CleanupSessions(cloneRoot string, dryRun bool) error {
	state, err := loadSessions(cloneRoot)
	if err != nil {
		return err
	}

	var alive []Session
	cleaned := 0
	for _, s := range state.Sessions {
		if processAlive(s.PID) {
			alive = append(alive, s)
			continue
		}
		fmt.Printf("  %s removing dead session %s\n", info("→"), s.Name)
		if !dryRun && s.Managed {
			removeSessionWorktree(cloneRoot, s)
		}
		cleaned++
	}

	if cleaned == 0 {
		fmt.Printf("No dead sessions to clean up.\n")
		return nil
	}

	if !dryRun {
		state.Sessions = alive
		if err := persistSessions(cloneRoot, state); err != nil {
			return err
		}
	}
	fmt.Printf("  %s cleaned up %d session(s)\n", ok("✓"), cleaned)
	return nil
}

func removeSessionWorktree(cloneRoot string, s Session) {
	repoDir := filepath.Join(cloneRoot, s.RepoName)
	if err := runSilent("git", "-C", repoDir, "worktree", "remove", "--force", s.Dir); err != nil {
		// Worktree remove can fail if dir is already gone; force-remove the directory.
		_ = os.RemoveAll(s.Dir)
	}
	// Clean up the branch.
	if s.Branch != "" {
		_ = runSilent("git", "-C", repoDir, "branch", "-D", s.Branch)
	}
	fmt.Printf("  %s removed worktree %s\n", ok("✓"), s.Dir)
}

func saveSession(cloneRoot string, s Session) error {
	state, _ := loadSessions(cloneRoot)
	state.Sessions = append(state.Sessions, s)
	return persistSessions(cloneRoot, state)
}

func loadSessions(cloneRoot string) (SessionState, error) {
	var state SessionState
	path := sessionsPath(cloneRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("reading sessions file: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("parsing sessions file %s: %w", path, err)
	}
	return state, nil
}

func persistSessions(cloneRoot string, state SessionState) error {
	path := sessionsPath(cloneRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func sessionsPath(cloneRoot string) string {
	return filepath.Join(cloneRoot, ".ownpulse-dev", "sessions.json")
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds; send signal 0 to check.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func agentMode(teams bool) string {
	if teams {
		return "teams"
	}
	return "solo"
}

func claudeCmd(opts SessionOptions) *exec.Cmd {
	var args []string
	if opts.DangerousPerms {
		args = append(args, "--dangerously-skip-permissions")
	}
	return exec.Command("claude", args...)
}

func sessionID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func findRepo(repos []config.RepoConfig, name string) (config.RepoConfig, error) {
	for _, r := range repos {
		if strings.EqualFold(r.Name, name) {
			return r, nil
		}
	}
	return config.RepoConfig{}, fmt.Errorf("repo %q not found in workspace config", name)
}
