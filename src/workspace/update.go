package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	releaseAPI = "https://api.github.com/repos/OwnPulse/ownpulse-dev/releases/latest"
	binaryName = "opdev"
)

// UpdateOptions controls the update command.
type UpdateOptions struct {
	DryRun bool
}

// Update downloads the latest release from GitHub and replaces the current binary.
func Update(currentVersion string, opts UpdateOptions) error {
	fmt.Printf("\n%s Checking for updates...\n", info("→"))

	release, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("fetching latest release: %w", err)
	}

	fmt.Printf("  current: %s\n", currentVersion)
	fmt.Printf("  latest:  %s\n", release.TagName)

	if release.TagName == currentVersion || "v"+currentVersion == release.TagName {
		fmt.Printf("\n  %s already up to date\n\n", ok("✓"))
		return nil
	}

	assetName := expectedAssetName()
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no release asset found for %s (looked for %s)", runtime.GOOS+"/"+runtime.GOARCH, assetName)
	}

	if opts.DryRun {
		fmt.Printf("  would download %s\n", assetName)
		fmt.Printf("  would install to %s\n\n", installPath())
		return nil
	}

	// Download to temp file
	fmt.Printf("  downloading %s...\n", assetName)
	tmpFile, err := download(downloadURL)
	if err != nil {
		return fmt.Errorf("downloading release: %w", err)
	}
	defer os.Remove(tmpFile)

	// Install
	dest := installPath()
	fmt.Printf("  installing to %s...\n", dest)
	if err := install(tmpFile, dest); err != nil {
		return fmt.Errorf("installing: %w", err)
	}

	fmt.Printf("\n  %s updated to %s\n\n", ok("✓"), release.TagName)
	return nil
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func fetchLatestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", releaseAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

func expectedAssetName() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("%s-%s-%s", binaryName, os, arch)
}

func installPath() string {
	// Use the path of the currently running binary if possible.
	if exe, err := os.Executable(); err == nil {
		resolved, err := filepath.EvalSymlinks(exe)
		if err == nil {
			return resolved
		}
		return exe
	}
	return "/usr/local/bin/" + binaryName
}

func download(url string) (string, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "opdev-update-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}

	return tmp.Name(), nil
}

func install(src, dest string) error {
	// Check if we need sudo
	destDir := filepath.Dir(dest)
	testFile := filepath.Join(destDir, ".opdev-write-test")
	needsSudo := false
	if f, err := os.Create(testFile); err != nil {
		needsSudo = true
	} else {
		f.Close()
		os.Remove(testFile)
	}

	if needsSudo {
		fmt.Printf("  (requires sudo for %s)\n", destDir)
		if err := runCmd("sudo", "install", "-m", "0755", src, dest); err != nil {
			return err
		}
	} else {
		// Direct copy + chmod
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0755); err != nil {
			return err
		}
	}

	// Verify
	out, err := exec.Command(dest, "--version").Output()
	if err != nil {
		return fmt.Errorf("installed binary failed version check: %w", err)
	}
	fmt.Printf("  verified: %s", strings.TrimSpace(string(out)))
	return nil
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
