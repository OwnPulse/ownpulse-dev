// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) OwnPulse Contributors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownpulse/ownpulse-dev/src/crashes"
)

// writeTempWorkspaceConfig creates a minimal workspace.toml in tmpDir whose
// clone_root points at cloneRoot, with an ownpulse-infra repo registered.
// Returns the absolute path to the generated workspace.toml.
func writeTempWorkspaceConfig(t *testing.T, tmpDir, cloneRoot string, includeInfra bool) string {
	t.Helper()
	repoBlock := ""
	if includeInfra {
		repoBlock = `
[[repo]]
name = "ownpulse-infra"
description = "infra"
visibility = "private"
`
	}
	doc := "[workspace]\nname = \"test\"\ndefault_org = \"test\"\ndefault_branch = \"main\"\nclone_root = \"" + cloneRoot + "\"\n\n[agents]\ndefinitions_path = \"./agents\"\n" + repoBlock
	path := filepath.Join(tmpDir, "workspace.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	// loadConfig also tries to resolve agents_path; create an agents dir so
	// later code that may stat it succeeds. (resolveSOPSPath itself doesn't,
	// but Load doesn't fail on missing agents either — guard anyway.)
	_ = os.MkdirAll(filepath.Join(tmpDir, "agents"), 0o755)
	return path
}

func TestResolveSOPSPath_FlagWins(t *testing.T) {
	t.Setenv("OWNPULSE_INFRA_PATH", "/should/be/ignored")
	got := resolveSOPSPath("/explicit/file.yaml")
	if got.resolved != "/explicit/file.yaml" {
		t.Fatalf("resolved = %q, want explicit path", got.resolved)
	}
	if got.source != "flag" {
		t.Errorf("source = %q, want flag", got.source)
	}
}

func TestResolveSOPSPath_EnvBeatsConfig(t *testing.T) {
	// Set up a workspace config that WOULD be used if the env var weren't set,
	// then assert the env var wins.
	tmp := t.TempDir()
	cloneRoot := filepath.Join(tmp, "checkouts")
	if err := os.MkdirAll(filepath.Join(cloneRoot, "ownpulse-infra", "secrets", "ios"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTempWorkspaceConfig(t, tmp, cloneRoot, true)
	t.Setenv("OPDEV_CONFIG", cfgPath)
	t.Setenv("OWNPULSE_INFRA_PATH", "/from/env")

	got := resolveSOPSPath("")
	want := filepath.Join("/from/env", crashes.SOPSRelPath)
	if got.resolved != want {
		t.Fatalf("resolved = %q, want %q", got.resolved, want)
	}
	if got.source != "env" {
		t.Errorf("source = %q, want env", got.source)
	}
}

func TestResolveSOPSPath_WorkspaceConfigPath(t *testing.T) {
	// Lay out a fake clone_root/ownpulse-infra/secrets/ios/appstore-connect.sops.yaml
	// and assert resolveSOPSPath finds it via the workspace config.
	tmp := t.TempDir()
	cloneRoot := filepath.Join(tmp, "checkouts")
	infraDir := filepath.Join(cloneRoot, "ownpulse-infra")
	secretDir := filepath.Join(infraDir, "secrets", "ios")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(secretDir, "appstore-connect.sops.yaml")
	if err := os.WriteFile(secretPath, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeTempWorkspaceConfig(t, tmp, cloneRoot, true)
	t.Setenv("OPDEV_CONFIG", cfgPath)
	t.Setenv("OWNPULSE_INFRA_PATH", "")

	got := resolveSOPSPath("")
	if got.resolved != secretPath {
		t.Fatalf("resolved = %q, want %q", got.resolved, secretPath)
	}
	if got.source != "workspace-config" {
		t.Errorf("source = %q, want workspace-config", got.source)
	}
}

func TestResolveSOPSPath_ConfigMissingRepo(t *testing.T) {
	// Workspace config exists but doesn't register ownpulse-infra.
	tmp := t.TempDir()
	cloneRoot := filepath.Join(tmp, "checkouts")
	if err := os.MkdirAll(cloneRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTempWorkspaceConfig(t, tmp, cloneRoot, false)
	t.Setenv("OPDEV_CONFIG", cfgPath)
	t.Setenv("OWNPULSE_INFRA_PATH", "")

	got := resolveSOPSPath("")
	if got.resolved != "" {
		t.Fatalf("resolved = %q, want empty", got.resolved)
	}
	if !strings.Contains(got.configProblem, "not registered") {
		t.Errorf("configProblem = %q, want mention of 'not registered'", got.configProblem)
	}
}

func TestResolveSOPSPath_ConfigCheckoutMissing(t *testing.T) {
	// Workspace config registers ownpulse-infra but the secrets file doesn't
	// exist on disk. resolveSOPSPath should report the expected path and not
	// claim success.
	tmp := t.TempDir()
	cloneRoot := filepath.Join(tmp, "checkouts")
	if err := os.MkdirAll(cloneRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTempWorkspaceConfig(t, tmp, cloneRoot, true)
	t.Setenv("OPDEV_CONFIG", cfgPath)
	t.Setenv("OWNPULSE_INFRA_PATH", "")

	got := resolveSOPSPath("")
	if got.resolved != "" {
		t.Fatalf("resolved = %q, want empty (file missing)", got.resolved)
	}
	wantAttempt := filepath.Join(cloneRoot, "ownpulse-infra", crashes.SOPSRelPath)
	if got.configAttempt != wantAttempt {
		t.Errorf("configAttempt = %q, want %q", got.configAttempt, wantAttempt)
	}
	if got.configProblem == "" {
		t.Error("configProblem should describe the missing file")
	}
}
