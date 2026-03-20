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

const tmuxSessionName = "opdev"

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
	TmuxWindow string  `json:"tmux_window,omitempty"` // tmux window name
	StartedAt time.Time `json:"started_at"`
}

// SessionOptions holds parameters for spawning a session.
type SessionOptions struct {
	RepoName       string
	Worktree       string
	Teams          bool
	DryRun         bool
	AgentsPath     string // resolved absolute path to agent definitions
	DangerousPerms bool   // pass --dangerously-skip-permissions to claude
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

	windowName := "workspace"
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

	env := sessionEnv(cfg, opts)
	claudeArgs := claudeArgs(opts)

	pid, err := launchInTmux(windowName, sessionDir, env, claudeArgs)
	if err != nil {
		return err
	}

	session := Session{
		Name:       windowName,
		RepoName:   "",
		PID:        pid,
		Dir:        sessionDir,
		AgentMode:  agentMode(opts.Teams),
		Managed:    false,
		TmuxWindow: windowName,
		StartedAt:  time.Now(),
	}

	if err := saveSession(cfg.Workspace.CloneRoot, session); err != nil {
		fmt.Printf("  %s could not save session state: %v\n", warn("!"), err)
	}

	fmt.Printf("  %s session started in tmux window '%s'\n", ok("✓"), windowName)
	return nil
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
		wtBranch := fmt.Sprintf("worktree/%s", opts.Worktree)
		if runSilent("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+wtBranch) == nil {
			startPoint = wtBranch
		}
	}

	// Window name for tmux: short and readable.
	windowName := repo.Name
	if opts.Worktree != "" {
		windowName = repo.Name + "-" + opts.Worktree
	}
	windowName = windowName + "-" + id[:4]

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

		if err := LinkAgents(sessionDir, repo.Agents, opts.AgentsPath, false); err != nil {
			fmt.Printf("  %s could not link agents: %v\n", warn("!"), err)
		}
	} else {
		fmt.Printf("    %s (dry run — would create worktree and exec claude)\n", warn("~"))
		return nil
	}

	env := sessionEnv(cfg, opts)
	claudeArgs := claudeArgs(opts)

	pid, err := launchInTmux(windowName, sessionDir, env, claudeArgs)
	if err != nil {
		return err
	}

	session := Session{
		Name:       sessionName,
		RepoName:   repo.Name,
		Worktree:   opts.Worktree,
		PID:        pid,
		Dir:        sessionDir,
		AgentMode:  agentMode(opts.Teams),
		Managed:    true,
		Branch:     branchName,
		TmuxWindow: windowName,
		StartedAt:  time.Now(),
	}

	if err := saveSession(cfg.Workspace.CloneRoot, session); err != nil {
		fmt.Printf("  %s could not save session state: %v\n", warn("!"), err)
	}

	fmt.Printf("  %s session started in tmux window '%s'\n", ok("✓"), windowName)
	return nil
}

// launchInTmux starts claude in a tmux window. Creates the tmux session if needed.
// Returns the PID of the tmux server (we track the window name for management).
func launchInTmux(windowName, dir string, env []string, args []string) (int, error) {
	// Build the claude command string for tmux send-keys.
	claudeCmd := "claude"
	for _, a := range args {
		claudeCmd += " " + a
	}

	// Build env export prefix.
	var envExports string
	for _, e := range env {
		// Only export our custom vars, not the full os.Environ().
		envExports += fmt.Sprintf("export %s; ", e)
	}

	fullCmd := envExports + "cd " + dir + " && " + claudeCmd

	// Check if our tmux session exists.
	tmuxExists := runSilent("tmux", "has-session", "-t", tmuxSessionName) == nil

	if !tmuxExists {
		// Create a new tmux session with this window.
		err := runSilent("tmux", "new-session", "-d", "-s", tmuxSessionName, "-n", windowName)
		if err != nil {
			return 0, fmt.Errorf("creating tmux session: %w — is tmux installed?", err)
		}
		// Send the command to the window.
		if err := runSilent("tmux", "send-keys", "-t", tmuxSessionName+":"+windowName, fullCmd, "Enter"); err != nil {
			return 0, fmt.Errorf("sending command to tmux: %w", err)
		}
	} else {
		// Create a new window in the existing session.
		if err := runSilent("tmux", "new-window", "-t", tmuxSessionName, "-n", windowName); err != nil {
			return 0, fmt.Errorf("creating tmux window: %w", err)
		}
		if err := runSilent("tmux", "send-keys", "-t", tmuxSessionName+":"+windowName, fullCmd, "Enter"); err != nil {
			return 0, fmt.Errorf("sending command to tmux: %w", err)
		}
	}

	// Get the PID of the pane's shell process.
	out, err := exec.Command("tmux", "list-panes", "-t", tmuxSessionName+":"+windowName, "-F", "#{pane_pid}").Output()
	if err != nil {
		return 0, nil // non-fatal — session is running, we just can't track the PID
	}
	pid := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid)

	// If we're not already attached and in an interactive terminal, attach.
	if os.Getenv("TMUX") == "" {
		fmt.Printf("  %s Attaching to tmux session '%s'...\n", info("→"), tmuxSessionName)
		attach := exec.Command("tmux", "attach-session", "-t", tmuxSessionName)
		attach.Stdin = os.Stdin
		attach.Stdout = os.Stdout
		attach.Stderr = os.Stderr
		_ = attach.Run()
	} else {
		// Already in tmux — switch to the new window.
		_ = runSilent("tmux", "select-window", "-t", tmuxSessionName+":"+windowName)
	}

	return pid, nil
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
		// Also check if the tmux window still exists.
		if s.TmuxWindow != "" && !alive {
			alive = runSilent("tmux", "has-session", "-t", tmuxSessionName+":"+s.TmuxWindow) == nil
		}
		status := ok("running")
		if !alive {
			status = warn("stopped")
		}
		mode := ""
		if s.AgentMode == "teams" {
			mode = fmt.Sprintf(" %s", info("[teams]"))
		}
		tmux := ""
		if s.TmuxWindow != "" {
			tmux = fmt.Sprintf(" %s", info("["+tmuxSessionName+":"+s.TmuxWindow+"]"))
		}
		fmt.Printf("  %-30s PID %-8s %s%s%s\n",
			s.Name,
			strconv.Itoa(s.PID),
			status,
			mode,
			tmux,
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
		// Kill the tmux window if it exists.
		if s.TmuxWindow != "" {
			_ = runSilent("tmux", "kill-window", "-t", tmuxSessionName+":"+s.TmuxWindow)
		}
		proc, err := os.FindProcess(s.PID)
		if err == nil {
			_ = proc.Kill()
		}
		fmt.Printf("  %s killed session %s (PID %d)\n", ok("✓"), s.Name, s.PID)
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
		isAlive := processAlive(s.PID)
		// Also check tmux window.
		if s.TmuxWindow != "" && !isAlive {
			isAlive = runSilent("tmux", "has-session", "-t", tmuxSessionName+":"+s.TmuxWindow) == nil
		}
		if isAlive {
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
		_ = os.RemoveAll(s.Dir)
	}
	if s.Branch != "" {
		_ = runSilent("git", "-C", repoDir, "branch", "-D", s.Branch)
	}
	fmt.Printf("  %s removed worktree %s\n", ok("✓"), s.Dir)
}

func sessionEnv(cfg *config.WorkspaceConfig, opts SessionOptions) []string {
	var env []string
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	if opts.Teams {
		env = append(env, "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
		fmt.Printf("  %s Agent teams mode enabled\n", warn("!"))
	}
	return env
}

func claudeArgs(opts SessionOptions) []string {
	var args []string
	if opts.DangerousPerms {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
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
	if pid == 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func agentMode(teams bool) string {
	if teams {
		return "teams"
	}
	return "solo"
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
