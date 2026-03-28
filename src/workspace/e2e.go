package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ownpulse/ownpulse-dev/src/config"
)

const (
	e2eClusterName = "ownpulse-local"
	e2eNamespace   = "ownpulse"
	e2eTestEmail   = "test@localhost"
	e2eTestPass    = "localdevpassword1"

	pgRelease  = "pg"
	apiRelease = "api"
	webRelease = "web"

	pgFullname  = "pg-postgres"
	apiFullname = "api-ownpulse-api"

	// k3d context name — k3d always creates contexts with this prefix.
	k3dContext = "k3d-" + e2eClusterName
)

// E2EOptions controls the e2e subcommand behavior.
type E2EOptions struct {
	DryRun bool
}

// e2eKubeconfig returns the path to the k3d-specific kubeconfig.
// All kubectl and helm calls MUST use this — never the default context.
func e2eKubeconfig() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".k3d", "kubeconfig-"+e2eClusterName+".yaml")
}

// ensureLocalCluster verifies the k3d cluster exists and its kubeconfig is
// available. Returns an error if the cluster is not running — this prevents
// any command from accidentally targeting a production cluster.
func ensureLocalCluster() error {
	if !clusterExists() {
		return fmt.Errorf("k3d cluster '%s' is not running — run 'opdev e2e up' first", e2eClusterName)
	}
	kc := e2eKubeconfig()
	if _, err := os.Stat(kc); os.IsNotExist(err) {
		// Cluster exists but kubeconfig was cleaned up — regenerate it.
		return runSilent("k3d", "kubeconfig", "get", e2eClusterName, "--output", kc)
	}
	return nil
}

// kubectl runs a kubectl command against the local k3d cluster ONLY.
func kubectl(args ...string) error {
	full := append([]string{"--kubeconfig", e2eKubeconfig()}, args...)
	return run("kubectl", full...)
}

// kubectlSilent runs kubectl without printing output.
func kubectlSilent(args ...string) error {
	full := append([]string{"--kubeconfig", e2eKubeconfig()}, args...)
	return runSilent(full[0], full[1:]...)
}

// helm runs a helm command against the local k3d cluster ONLY.
func helmCmd(args ...string) error {
	full := append([]string{"--kubeconfig", e2eKubeconfig()}, args...)
	return run("helm", full...)
}

// E2EUp creates the k3d cluster, builds images, deploys via Helm, and seeds data.
func E2EUp(cfg *config.WorkspaceConfig, opts E2EOptions) error {
	repoDir, err := ownpulseRepoDir(cfg)
	if err != nil {
		return err
	}

	steps := []struct {
		name string
		fn   func(string, bool) error
	}{
		{"Create k3d cluster", e2eCreateCluster},
		{"Create secrets", e2eCreateSecrets},
		{"Build images", e2eBuildImages},
		{"Deploy services", e2eDeploy},
		{"Seed test data", e2eSeedData},
	}

	for _, step := range steps {
		fmt.Printf("\n%s %s\n", info("→"), step.name)
		if err := step.fn(repoDir, opts.DryRun); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}

	e2ePrintSummary()
	return nil
}

// E2EBuild rebuilds images and redeploys.
func E2EBuild(cfg *config.WorkspaceConfig, opts E2EOptions) error {
	if !opts.DryRun {
		if err := ensureLocalCluster(); err != nil {
			return err
		}
	}

	repoDir, err := ownpulseRepoDir(cfg)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s Rebuilding images\n", info("→"))
	if err := e2eBuildImages(repoDir, opts.DryRun); err != nil {
		return err
	}

	fmt.Printf("\n%s Redeploying services\n", info("→"))
	if err := e2eDeploy(repoDir, opts.DryRun); err != nil {
		return err
	}

	fmt.Printf("\n%s Rebuild complete\n", ok("✓"))
	return nil
}

// E2ESeed re-seeds test data.
func E2ESeed(_ *config.WorkspaceConfig, opts E2EOptions) error {
	if !opts.DryRun {
		if err := ensureLocalCluster(); err != nil {
			return err
		}
	}

	fmt.Printf("\n%s Seeding test data\n", info("→"))
	return e2eSeedData("", opts.DryRun)
}

// E2EStatus shows the current state of the local cluster.
func E2EStatus(_ *config.WorkspaceConfig, _ E2EOptions) error {
	if err := ensureLocalCluster(); err != nil {
		return err
	}

	fmt.Printf("\n%s Pods:\n", info("→"))
	_ = kubectl("-n", e2eNamespace, "get", "pods", "-o", "wide")

	fmt.Printf("\n%s Services:\n", info("→"))
	_ = kubectl("-n", e2eNamespace, "get", "svc")

	fmt.Printf("\n%s Ingresses:\n", info("→"))
	_ = kubectl("-n", e2eNamespace, "get", "ingress")

	fmt.Println()
	if healthCheck("http://api.localhost:8080/api/v1/health") {
		fmt.Printf("  %s API is healthy\n", ok("✓"))
	} else {
		fmt.Printf("  %s API is not reachable at http://api.localhost:8080\n", fail("✗"))
	}

	if healthCheck("http://app.localhost:8080") {
		fmt.Printf("  %s Web is healthy\n", ok("✓"))
	} else {
		fmt.Printf("  %s Web is not reachable at http://app.localhost:8080\n", fail("✗"))
	}

	return nil
}

// E2ETeardown deletes the k3d cluster.
func E2ETeardown(_ *config.WorkspaceConfig, opts E2EOptions) error {
	fmt.Printf("\n%s Deleting cluster '%s'\n", warn("→"), e2eClusterName)
	if opts.DryRun {
		fmt.Printf("  %s dry run — no changes made\n", warn("~"))
		return nil
	}
	if err := run("k3d", "cluster", "delete", e2eClusterName); err != nil {
		return err
	}
	// Clean up kubeconfig
	os.Remove(e2eKubeconfig())
	fmt.Printf("  %s Cluster deleted\n", ok("✓"))
	return nil
}

// --- internal steps ---

func ownpulseRepoDir(cfg *config.WorkspaceConfig) (string, error) {
	for _, r := range cfg.Repos {
		if r.Name == "ownpulse" {
			dir := filepath.Join(cfg.Workspace.CloneRoot, r.Name)
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return "", fmt.Errorf("ownpulse repo not found at %s — run opdev setup first", dir)
			}
			return dir, nil
		}
	}
	return "", fmt.Errorf("ownpulse repo not in workspace config")
}

func e2eCreateCluster(_ string, dryRun bool) error {
	if clusterExists() {
		fmt.Printf("  %s cluster '%s' already exists\n", warn("~"), e2eClusterName)
		// Ensure kubeconfig exists
		kc := e2eKubeconfig()
		if _, err := os.Stat(kc); os.IsNotExist(err) {
			os.MkdirAll(filepath.Dir(kc), 0755)
			_ = runSilent("k3d", "kubeconfig", "get", e2eClusterName, "--output", kc)
		}
		return nil
	}
	if dryRun {
		fmt.Printf("  would create k3d cluster '%s'\n", e2eClusterName)
		return nil
	}
	if err := run("k3d", "cluster", "create", e2eClusterName,
		"--port", "8080:80@loadbalancer",
		"--wait"); err != nil {
		return err
	}

	// Write dedicated kubeconfig — never pollute the default context.
	kc := e2eKubeconfig()
	os.MkdirAll(filepath.Dir(kc), 0755)
	if err := runSilent("k3d", "kubeconfig", "get", e2eClusterName, "--output", kc); err != nil {
		return fmt.Errorf("writing kubeconfig: %w", err)
	}

	fmt.Printf("  %s cluster created (kubeconfig: %s)\n", ok("✓"), kc)
	return nil
}

func clusterExists() bool {
	out, err := exec.Command("k3d", "cluster", "list", "-o", "json").Output()
	if err != nil {
		return false
	}
	var clusters []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(out, &clusters) != nil {
		return false
	}
	for _, c := range clusters {
		if c.Name == e2eClusterName {
			return true
		}
	}
	return false
}

func e2eCreateSecrets(_ string, dryRun bool) error {
	if dryRun {
		fmt.Printf("  would create namespace and secrets\n")
		return nil
	}

	kc := e2eKubeconfig()

	// Create namespace (idempotent via apply).
	createNs := exec.Command("kubectl", "--kubeconfig", kc, "create", "namespace", e2eNamespace, "--dry-run=client", "-o", "yaml")
	applyNs := exec.Command("kubectl", "--kubeconfig", kc, "apply", "-f", "-")
	pipe, _ := createNs.StdoutPipe()
	applyNs.Stdin = pipe
	applyNs.Stdout = os.Stdout
	applyNs.Stderr = os.Stderr
	_ = createNs.Start()
	_ = applyNs.Start()
	_ = createNs.Wait()
	_ = applyNs.Wait()

	// Postgres secret.
	if err := applySecret(kc, e2eNamespace, "ownpulse-postgres-secrets", map[string]string{
		"POSTGRES_PASSWORD": "devpassword",
		"POSTGRES_USER":     "postgres",
		"POSTGRES_DB":       "ownpulse",
	}); err != nil {
		return fmt.Errorf("postgres secret: %w", err)
	}

	// API secret.
	dbURL := fmt.Sprintf("postgres://postgres:devpassword@%s:5432/ownpulse?sslmode=disable", pgFullname)
	if err := applySecret(kc, e2eNamespace, "ownpulse-api-secret", map[string]string{
		"DATABASE_URL":   dbURL,
		"JWT_SECRET":     "dev-only-change-me",
		"ENCRYPTION_KEY": "0000000000000000000000000000000000000000000000000000000000000000",
	}); err != nil {
		return fmt.Errorf("api secret: %w", err)
	}

	fmt.Printf("  %s secrets created\n", ok("✓"))
	return nil
}

func applySecret(kubeconfig, namespace, name string, data map[string]string) error {
	args := []string{"--kubeconfig", kubeconfig, "create", "secret", "generic", name, "-n", namespace, "--dry-run=client", "-o", "yaml"}
	for k, v := range data {
		args = append(args, fmt.Sprintf("--from-literal=%s=%s", k, v))
	}

	create := exec.Command("kubectl", args...)
	apply := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
	pipe, _ := create.StdoutPipe()
	apply.Stdin = pipe
	apply.Stderr = os.Stderr
	_ = create.Start()
	_ = apply.Start()
	_ = create.Wait()
	return apply.Wait()
}

func e2eBuildImages(repoDir string, dryRun bool) error {
	if dryRun {
		fmt.Printf("  would build ownpulse-api:local and ownpulse-web:local\n")
		return nil
	}

	fmt.Printf("  building API image...\n")
	if err := run("docker", "build", "-t", "ownpulse-api:local",
		"-f", filepath.Join(repoDir, "backend", "Dockerfile"), repoDir); err != nil {
		return fmt.Errorf("docker build api: %w", err)
	}

	fmt.Printf("  building web image...\n")
	webDir := filepath.Join(repoDir, "web")
	if err := run("docker", "build", "-t", "ownpulse-web:local",
		"-f", filepath.Join(webDir, "Dockerfile"), webDir); err != nil {
		return fmt.Errorf("docker build web: %w", err)
	}

	fmt.Printf("  importing images into k3d...\n")
	if err := run("k3d", "image", "import",
		"ownpulse-api:local", "ownpulse-web:local",
		"-c", e2eClusterName); err != nil {
		return fmt.Errorf("k3d image import: %w", err)
	}

	fmt.Printf("  %s images built and imported\n", ok("✓"))
	return nil
}

func e2eDeploy(repoDir string, dryRun bool) error {
	helmDir := filepath.Join(repoDir, "helm")
	kc := e2eKubeconfig()

	if dryRun {
		fmt.Printf("  would deploy postgres, api, web via Helm\n")
		return nil
	}

	// Postgres
	fmt.Printf("  deploying PostgreSQL...\n")
	if err := helmCmd("upgrade", "--install", pgRelease,
		filepath.Join(helmDir, "postgres"),
		"-n", e2eNamespace,
		"-f", filepath.Join(helmDir, "postgres", "values-local.yaml")); err != nil {
		return fmt.Errorf("helm postgres: %w", err)
	}

	fmt.Printf("  waiting for PostgreSQL...\n")
	if err := kubectl("-n", e2eNamespace, "rollout", "status",
		fmt.Sprintf("statefulset/%s", pgFullname), "--timeout=120s"); err != nil {
		return fmt.Errorf("postgres rollout: %w", err)
	}

	// API (migration job runs as pre-install hook)
	fmt.Printf("  deploying API (with migration)...\n")
	if err := helmCmd("upgrade", "--install", apiRelease,
		filepath.Join(helmDir, "api"),
		"-n", e2eNamespace,
		"-f", filepath.Join(helmDir, "api", "values-local.yaml")); err != nil {
		return fmt.Errorf("helm api: %w", err)
	}

	// Wait for migration job
	fmt.Printf("  waiting for migration...\n")
	_ = kubectlSilent("-n", e2eNamespace, "wait", "--for=condition=complete",
		"job", "-l", "app.kubernetes.io/name=ownpulse-api-migrate", "--timeout=120s")

	fmt.Printf("  waiting for API...\n")
	if err := kubectl("-n", e2eNamespace, "rollout", "status",
		fmt.Sprintf("deployment/%s", apiFullname), "--timeout=120s"); err != nil {
		return fmt.Errorf("api rollout: %w", err)
	}

	// Web
	fmt.Printf("  deploying web...\n")
	if err := helmCmd("upgrade", "--install", webRelease,
		filepath.Join(helmDir, "web"),
		"-n", e2eNamespace,
		"-f", filepath.Join(helmDir, "web", "values-local.yaml")); err != nil {
		return fmt.Errorf("helm web: %w", err)
	}

	fmt.Printf("  waiting for web...\n")
	if err := kubectl("-n", e2eNamespace, "rollout", "status",
		"deployment/web-ownpulse-web", "--timeout=60s"); err != nil {
		return fmt.Errorf("web rollout: %w", err)
	}

	// Ignore unused variable
	_ = kc

	fmt.Printf("  %s all services deployed\n", ok("✓"))
	return nil
}

func e2eSeedData(_ string, dryRun bool) error {
	if dryRun {
		fmt.Printf("  would seed test data for %s\n", e2eTestEmail)
		return nil
	}

	apiURL := "http://api.localhost:8080"

	// Wait for API health
	fmt.Printf("  waiting for API...\n")
	for i := 0; i < 30; i++ {
		if healthCheck(apiURL + "/api/v1/health") {
			break
		}
		if i == 29 {
			return fmt.Errorf("API not reachable after 30 attempts")
		}
		time.Sleep(2 * time.Second)
	}

	// Register (ignore errors — user may already exist)
	fmt.Printf("  registering test user (%s)...\n", e2eTestEmail)
	_ = postJSON(apiURL+"/api/v1/auth/register", map[string]string{
		"email":    e2eTestEmail,
		"password": e2eTestPass,
	}, "")

	// Login
	fmt.Printf("  logging in...\n")
	loginResp := postJSON(apiURL+"/api/v1/auth/login", map[string]string{
		"email":    e2eTestEmail,
		"password": e2eTestPass,
	}, "")

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(loginResp, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return fmt.Errorf("failed to get access token: %s", string(loginResp))
	}
	token := tokenResp.AccessToken

	// Seed weight data (15 points over 90 days)
	fmt.Printf("  seeding weight data...\n")
	daysAgo := []int{90, 80, 70, 60, 50, 40, 30, 25, 20, 15, 10, 7, 5, 2, 0}
	for _, d := range daysAgo {
		ts := time.Now().AddDate(0, 0, -d).UTC().Format(time.RFC3339)
		weight := 83.5 - float64(d)*0.015 + math.Sin(float64(d)/10)*0.4
		_ = postJSON(apiURL+"/api/v1/health-records", map[string]interface{}{
			"source":      "manual",
			"record_type": "body_mass",
			"value":       math.Round(weight*10) / 10,
			"unit":        "kg",
			"start_time":  ts,
		}, token)
	}

	// Seed resting heart rate (10 points)
	fmt.Printf("  seeding heart rate data...\n")
	for d := 9; d >= 0; d-- {
		ts := time.Now().AddDate(0, 0, -d).UTC().Format(time.RFC3339)
		hr := 62 + (d%5)*2 // deterministic variation
		_ = postJSON(apiURL+"/api/v1/health-records", map[string]interface{}{
			"source":      "manual",
			"record_type": "resting_heart_rate",
			"value":       hr,
			"unit":        "bpm",
			"start_time":  ts,
		}, token)
	}

	fmt.Printf("  %s test data seeded\n", ok("✓"))
	return nil
}

// --- helpers ---

func healthCheck(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func postJSON(url string, body interface{}, bearerToken string) []byte {
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return buf.Bytes()
}

func e2ePrintSummary() {
	fmt.Printf("\n%s Local E2E environment ready!\n\n", ok("✓"))
	fmt.Printf("  Web app:   http://app.localhost:8080\n")
	fmt.Printf("  API:       http://api.localhost:8080/api/v1/health\n")
	fmt.Printf("  Metrics:   kubectl --kubeconfig %s -n %s port-forward svc/%s 9090:9090\n", e2eKubeconfig(), e2eNamespace, apiFullname)
	fmt.Printf("             then: curl http://localhost:9090/metrics\n\n")
	fmt.Printf("  Test user: %s / %s\n\n", e2eTestEmail, e2eTestPass)
	fmt.Printf("  Rebuild:   opdev e2e build\n")
	fmt.Printf("  Re-seed:   opdev e2e seed\n")
	fmt.Printf("  Status:    opdev e2e status\n")
	fmt.Printf("  Teardown:  opdev e2e teardown\n\n")
}
