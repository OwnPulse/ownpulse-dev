package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	StartedAt time.Time `json:"started_at"`
}

// SpawnSession launches a Claude Code session for a named repo/worktree.
func SpawnSession(cfg *config.WorkspaceConfig, repoName, worktree string, teams bool, dryRun bool) error {
	repo, err := findRepo(cfg.Repos, repoName)
	if err != nil {
		return err
	}

	sessionDir := filepath.Join(cfg.Workspace.CloneRoot, repo.Name)
	sessionName := repo.Name
	if worktree != "" {
		sessionDir = filepath.Join(cfg.Workspace.CloneRoot, repo.Name+"-"+worktree)
		sessionName = repo.Name + "/" + worktree
	}

	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		return fmt.Errorf("directory %s does not exist — run 'opdev setup' first", sessionDir)
	}

	env := os.Environ()
	if teams {
		env = append(env, "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
		fmt.Printf("%s Agent teams mode enabled\n", warn("!"))
	}

	fmt.Printf("%s Spawning Claude Code session: %s\n", info("→"), sessionName)
	fmt.Printf("    directory: %s\n", sessionDir)

	if dryRun {
		fmt.Printf("    %s (dry run — would exec: claude in %s)\n", warn("~"), sessionDir)
		return nil
	}

	// Launch claude in a new terminal tab/window if possible, otherwise foreground.
	cmd := exec.Command("claude")
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
		Worktree:  worktree,
		PID:       cmd.Process.Pid,
		Dir:       sessionDir,
		AgentMode: agentMode(teams),
		StartedAt: time.Now(),
	}

	if err := saveSession(cfg.Workspace.CloneRoot, session); err != nil {
		fmt.Printf("  %s could not save session state: %v\n", warn("!"), err)
	}

	fmt.Printf("  %s session started (PID %d)\n", ok("✓"), cmd.Process.Pid)
	return nil
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
	}

	state.Sessions = remaining
	return persistSessions(cloneRoot, state)
}

func saveSession(cloneRoot string, s Session) error {
	state, _ := loadSessions(cloneRoot)
	// Replace if same name exists.
	for i, existing := range state.Sessions {
		if existing.Name == s.Name {
			state.Sessions[i] = s
			return persistSessions(cloneRoot, state)
		}
	}
	state.Sessions = append(state.Sessions, s)
	return persistSessions(cloneRoot, state)
}

func loadSessions(cloneRoot string) (SessionState, error) {
	var state SessionState
	path := sessionsPath(cloneRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return state, nil // not an error — file may not exist yet
	}
	err = json.Unmarshal(data, &state)
	return state, err
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
	err = proc.Signal(os.Signal(nil))
	return err == nil
}

func agentMode(teams bool) string {
	if teams {
		return "teams"
	}
	return "solo"
}

func findRepo(repos []config.RepoConfig, name string) (config.RepoConfig, error) {
	for _, r := range repos {
		if strings.EqualFold(r.Name, name) {
			return r, nil
		}
	}
	return config.RepoConfig{}, fmt.Errorf("repo %q not found in workspace config", name)
}
