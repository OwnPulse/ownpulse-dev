package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// WorkspaceConfig is the merged result of workspace.toml + any override file.
type WorkspaceConfig struct {
	Workspace WorkspaceMeta       `toml:"workspace"`
	Agents    AgentsConfig        `toml:"agents"`
	Env       map[string]string   `toml:"env"`
	Repos     []RepoConfig        `toml:"repo"`

	configDir string // base dir for resolving relative paths (set during Load)
}

// ConfigDir returns the directory containing the base config file.
func (c *WorkspaceConfig) ConfigDir() string { return c.configDir }

type WorkspaceMeta struct {
	Name          string `toml:"name"`
	DefaultOrg    string `toml:"default_org"`
	DefaultBranch string `toml:"default_branch"`
	CloneRoot     string `toml:"clone_root"`
}

type AgentsConfig struct {
	DefinitionsPath string `toml:"definitions_path"`
}

type RepoConfig struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Org         string   `toml:"org"`
	Visibility  string   `toml:"visibility"`
	Branch      string   `toml:"branch"`
	Agents      []string `toml:"agents"`
	Worktrees   []string `toml:"worktrees"`
}

// overlayFile is the raw structure of an override TOML — additive repos + overrides.
type overlayFile struct {
	Workspace     WorkspaceMeta     `toml:"workspace"`
	Env           map[string]string `toml:"env"`
	Repos         []RepoConfig      `toml:"repo"`
	RepoOverrides []RepoConfig      `toml:"repo_override"`
}

// Load reads the base workspace.toml and, if present, merges an override file on top.
// overlayPath may be empty — if so, only the base file is loaded.
func Load(basePath, overlayPath string) (*WorkspaceConfig, error) {
	base, err := loadBase(basePath)
	if err != nil {
		return nil, fmt.Errorf("loading base config: %w", err)
	}

	base.configDir = filepath.Dir(basePath)

	// Resolve defaults: repos inherit org and branch from workspace meta if not set.
	for i := range base.Repos {
		if base.Repos[i].Org == "" {
			base.Repos[i].Org = base.Workspace.DefaultOrg
		}
		if base.Repos[i].Branch == "" {
			base.Repos[i].Branch = base.Workspace.DefaultBranch
		}
	}

	// Expand clone_root ~ if present.
	base.Workspace.CloneRoot = expandHome(base.Workspace.CloneRoot)

	if overlayPath == "" {
		return base, nil
	}

	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		return base, nil
	}

	if err := applyOverlay(base, overlayPath); err != nil {
		return nil, fmt.Errorf("applying overlay %s: %w", overlayPath, err)
	}

	return base, nil
}

// DefaultOverlayPath returns the conventional override path next to the base file.
func DefaultOverlayPath(basePath string) string {
	dir := filepath.Dir(basePath)
	return filepath.Join(dir, "workspace.override.toml")
}

func loadBase(path string) (*WorkspaceConfig, error) {
	var cfg WorkspaceConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyOverlay(base *WorkspaceConfig, path string) error {
	var overlay overlayFile
	if _, err := toml.DecodeFile(path, &overlay); err != nil {
		return err
	}

	// Merge workspace meta — only non-zero fields override.
	if overlay.Workspace.Name != "" {
		base.Workspace.Name = overlay.Workspace.Name
	}
	if overlay.Workspace.DefaultOrg != "" {
		base.Workspace.DefaultOrg = overlay.Workspace.DefaultOrg
	}
	if overlay.Workspace.DefaultBranch != "" {
		base.Workspace.DefaultBranch = overlay.Workspace.DefaultBranch
	}
	if overlay.Workspace.CloneRoot != "" {
		base.Workspace.CloneRoot = expandHome(overlay.Workspace.CloneRoot)
	}

	// Merge env — overlay keys win.
	if base.Env == nil {
		base.Env = make(map[string]string)
	}
	for k, v := range overlay.Env {
		base.Env[k] = v
	}

	// Append additional repos from overlay.
	for _, r := range overlay.Repos {
		if r.Org == "" {
			r.Org = base.Workspace.DefaultOrg
		}
		if r.Branch == "" {
			r.Branch = base.Workspace.DefaultBranch
		}
		base.Repos = append(base.Repos, r)
	}

	// Apply repo overrides — matched by name, non-zero fields win.
	for _, override := range overlay.RepoOverrides {
		for i, repo := range base.Repos {
			if repo.Name == override.Name {
				if override.Org != "" {
					base.Repos[i].Org = override.Org
				}
				if override.Branch != "" {
					base.Repos[i].Branch = override.Branch
				}
				if override.Description != "" {
					base.Repos[i].Description = override.Description
				}
				if len(override.Agents) > 0 {
					base.Repos[i].Agents = override.Agents
				}
				break
			}
		}
	}

	return nil
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
