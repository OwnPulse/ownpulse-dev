package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ownpulse/ownpulse-dev/src/config"
)

func TestResolveRepo_ByName(t *testing.T) {
	cfg := &config.WorkspaceConfig{
		Workspace: config.WorkspaceMeta{CloneRoot: "/tmp/ws"},
		Repos: []config.RepoConfig{
			{Name: "app"},
			{Name: "infra"},
		},
	}

	dir, name, err := resolveRepo(cfg, "infra")
	if err != nil {
		t.Fatal(err)
	}
	if name != "infra" {
		t.Errorf("name = %q, want %q", name, "infra")
	}
	if dir != "/tmp/ws/infra" {
		t.Errorf("dir = %q, want %q", dir, "/tmp/ws/infra")
	}
}

func TestResolveRepo_CaseInsensitive(t *testing.T) {
	cfg := &config.WorkspaceConfig{
		Workspace: config.WorkspaceMeta{CloneRoot: "/tmp/ws"},
		Repos:     []config.RepoConfig{{Name: "OwnPulse"}},
	}

	_, name, err := resolveRepo(cfg, "ownpulse")
	if err != nil {
		t.Fatal(err)
	}
	if name != "OwnPulse" {
		t.Errorf("name = %q, want %q", name, "OwnPulse")
	}
}

func TestResolveRepo_NotFound(t *testing.T) {
	cfg := &config.WorkspaceConfig{
		Workspace: config.WorkspaceMeta{CloneRoot: "/tmp/ws"},
		Repos:     []config.RepoConfig{{Name: "app"}},
	}

	_, _, err := resolveRepo(cfg, "nope")
	if err == nil {
		t.Error("expected error for unknown repo")
	}
}

func TestResolveRepo_FromCwd(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "app")
	subDir := filepath.Join(repoDir, "backend", "src")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.WorkspaceConfig{
		Workspace: config.WorkspaceMeta{CloneRoot: dir},
		Repos:     []config.RepoConfig{{Name: "app"}},
	}

	// Save and restore cwd.
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	os.Chdir(subDir)

	_, name, err := resolveRepo(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "app" {
		t.Errorf("name = %q, want %q", name, "app")
	}
}

func TestShortID(t *testing.T) {
	id, err := shortID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 8 {
		t.Errorf("id length = %d, want 8", len(id))
	}
}

func TestAppendEnv_New(t *testing.T) {
	env := []string{"A=1", "B=2"}
	got := appendEnv(env, "C=3")
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestAppendEnv_Replace(t *testing.T) {
	env := []string{"A=1", "PWD=/old"}
	got := appendEnv(env, "PWD=/new")
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	if got[1] != "PWD=/new" {
		t.Errorf("PWD = %q, want %q", got[1], "PWD=/new")
	}
}
