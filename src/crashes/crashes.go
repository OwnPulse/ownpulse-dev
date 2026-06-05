// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) OwnPulse Contributors

// Package crashes implements an App Store Connect API client for iOS crash
// diagnosis. It is a Go port of ops/asc_client.py from the ownpulse repo.
package crashes

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ascBaseURL       = "https://api.appstoreconnect.apple.com"
	ascHost          = "api.appstoreconnect.apple.com"
	ascAudience      = "appstoreconnect-v1"
	jwtExpirySeconds = 20 * 60
	sopsRelPath      = "secrets/ios/appstore-connect.sops.yaml"
)

// Credentials hold App Store Connect API credentials. KeyPEM never touches disk.
type Credentials struct {
	KeyID    string
	IssuerID string
	AppID    string
	KeyPEM   []byte
}

// CredentialOptions controls credential resolution. Provide either explicit
// KeyID/IssuerID/AppID/KeyPEM or a SOPSPath. Other fields are optional.
type CredentialOptions struct {
	KeyID    string
	IssuerID string
	AppID    string
	KeyPEM   []byte

	SOPSPath string

	// SopsRunner allows tests to stub out the `sops -d` invocation. Production
	// code leaves this nil — real exec.Command is used.
	SopsRunner SopsRunner
}

// SopsRunner runs `sops -d <path>` and returns stdout. It returns the wrapped
// exec.ExitError on non-zero exit; tests can return errSopsNotInstalled to
// mimic a missing binary.
type SopsRunner func(path string) (stdout []byte, err error)

// ErrSopsNotInstalled is returned (or wrapped) by SopsRunner when the `sops`
// binary cannot be found on PATH.
var ErrSopsNotInstalled = errors.New("sops not installed")

// HTTPGetter is a minimal HTTP GET function for dependency injection in tests.
type HTTPGetter func(url string, headers map[string]string) ([]byte, error)

// MissingCredentialsError is returned by ResolveCredentials when one or more
// credential fields could not be resolved from any source. Callers can use
// errors.As to detect this and add their own context (e.g. the workspace-config
// SOPS path opdev's main looked at).
type MissingCredentialsError struct {
	Fields []string
}

func (e *MissingCredentialsError) Error() string {
	return "credentials missing — " + strings.Join(e.Fields, ", ")
}

// ASCError represents a non-success App Store Connect response.
type ASCError struct {
	Status int
	URL    string
	Body   string
}

func (e *ASCError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500]
	}
	return fmt.Sprintf("ASC %d on %s: %s", e.Status, e.URL, body)
}

// --------------------------------------------------------------------------
// JWT
// --------------------------------------------------------------------------

func b64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// MintJWT mints an ES256 JWT for App Store Connect with a 20-minute expiry.
// The signature is the raw R||S concatenation (64 bytes), not DER.
func MintJWT(creds *Credentials) (string, error) {
	if creds == nil {
		return "", errors.New("nil credentials")
	}
	block, _ := pem.Decode(creds.KeyPEM)
	if block == nil {
		return "", errors.New("failed to load private key: no PEM block found")
	}

	ecKey, err := loadECPrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}

	if ecKey.Curve != elliptic.P256() {
		return "", errors.New("expected an EC P-256 private key (ES256)")
	}

	header := map[string]string{"alg": "ES256", "kid": creds.KeyID, "typ": "JWT"}
	now := time.Now().Unix()
	payload := map[string]interface{}{
		"iss": creds.IssuerID,
		"iat": now,
		"exp": now + jwtExpirySeconds,
		"aud": ascAudience,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	headerB64 := b64url(headerJSON)
	payloadB64 := b64url(payloadJSON)
	signingInput := headerB64 + "." + payloadB64

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, ecKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("ECDSA sign failed: %w", err)
	}

	// Raw R||S — each component padded to 32 bytes (P-256).
	rawSig := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(rawSig[32-len(rBytes):32], rBytes)
	copy(rawSig[64-len(sBytes):64], sBytes)

	return signingInput + "." + b64url(rawSig), nil
}

// loadECPrivateKey parses a DER-encoded private key, accepting either PKCS#8
// or SEC1 encoding. If both parsers fail, both errors are surfaced — the
// PKCS#8 error is wrapped (errors.Is/As works against it) and the SEC1 error
// is included in the message for human debugging.
func loadECPrivateKey(der []byte) (*ecdsa.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		ecKey, err2 := x509.ParseECPrivateKey(der)
		if err2 != nil {
			return nil, fmt.Errorf("failed to load private key (pkcs8: %w; sec1: %v)", err, err2)
		}
		return ecKey, nil
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected an EC P-256 private key (ES256), got %T", key)
	}
	return ecKey, nil
}

// --------------------------------------------------------------------------
// Credentials
// --------------------------------------------------------------------------

// sopsYAML mirrors the decrypted SOPS YAML document. Production uses
// `asc_api_key_b64` (base64-encoded .p8 contents); `key_pem` is kept as a
// fallback for ad-hoc / test files that store the PEM literally.
type sopsYAML struct {
	KeyID     string `yaml:"key_id"`
	IssuerID  string `yaml:"issuer_id"`
	AppID     string `yaml:"app_id"`
	KeyPEM    string `yaml:"key_pem"`         // legacy / test path: raw PEM block
	APIKeyB64 string `yaml:"asc_api_key_b64"` // production path: base64(.p8 contents)
}

// LoadCredentials resolves credentials from explicit fields, or by invoking
// `sops -d <SOPSPath>` and parsing the resulting YAML. The PEM is never written
// to disk.
func LoadCredentials(opts CredentialOptions) (*Credentials, error) {
	if opts.SOPSPath != "" {
		return loadFromSOPS(opts)
	}

	var missing []string
	if opts.KeyID == "" {
		missing = append(missing, "key_id")
	}
	if opts.IssuerID == "" {
		missing = append(missing, "issuer_id")
	}
	if opts.AppID == "" {
		missing = append(missing, "app_id")
	}
	if len(opts.KeyPEM) == 0 {
		missing = append(missing, "key_pem")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing credential fields: %s", strings.Join(missing, ", "))
	}

	return &Credentials{
		KeyID:    opts.KeyID,
		IssuerID: opts.IssuerID,
		AppID:    opts.AppID,
		KeyPEM:   opts.KeyPEM,
	}, nil
}

func loadFromSOPS(opts CredentialOptions) (*Credentials, error) {
	info, err := os.Stat(opts.SOPSPath)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("SOPS file not found: %s", opts.SOPSPath)
	}

	runner := opts.SopsRunner
	if runner == nil {
		runner = defaultSopsRunner
	}
	stdout, err := runner(opts.SOPSPath)
	if err != nil {
		if errors.Is(err, ErrSopsNotInstalled) {
			return nil, errors.New("'sops' not found in PATH; install with: brew install sops (or your platform equivalent)")
		}
		return nil, fmt.Errorf("sops -d failed: %w", err)
	}

	var doc sopsYAML
	if err := yaml.Unmarshal(stdout, &doc); err != nil {
		return nil, fmt.Errorf("parsing SOPS YAML: %w", err)
	}

	// Resolve PEM bytes. Production stores the .p8 as base64 in
	// `asc_api_key_b64`; legacy/test files use literal `key_pem`.
	// Base64 wins when both are present.
	var pemBytes []byte
	switch {
	case doc.APIKeyB64 != "":
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(doc.APIKeyB64))
		if err != nil {
			return nil, fmt.Errorf("decoding asc_api_key_b64 from SOPS YAML: %w", err)
		}
		pemBytes = decoded
	case doc.KeyPEM != "":
		pemBytes = []byte(doc.KeyPEM)
	}

	var missing []string
	if doc.KeyID == "" {
		missing = append(missing, "key_id")
	}
	if doc.IssuerID == "" {
		missing = append(missing, "issuer_id")
	}
	if doc.AppID == "" {
		missing = append(missing, "app_id")
	}
	if len(pemBytes) == 0 {
		missing = append(missing, "missing PEM: set either asc_api_key_b64 (base64) or key_pem (PEM literal) in the SOPS YAML")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("SOPS file missing required fields: %s", strings.Join(missing, ", "))
	}

	return &Credentials{
		KeyID:    doc.KeyID,
		IssuerID: doc.IssuerID,
		AppID:    doc.AppID,
		KeyPEM:   pemBytes,
	}, nil
}

func defaultSopsRunner(path string) ([]byte, error) {
	if _, err := exec.LookPath("sops"); err != nil {
		return nil, ErrSopsNotInstalled
	}
	cmd := exec.Command("sops", "-d", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops invocation failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout, nil
}

// --------------------------------------------------------------------------
// HTTP
// --------------------------------------------------------------------------

// bearerRedactRe matches `Bearer <token>` in error bodies so the token is
// stripped before the body is surfaced. Apple has historically echoed request
// headers in some error responses; this is a cheap defense.
var bearerRedactRe = regexp.MustCompile(`(?i)Bearer\s+\S+`)

func redactBearer(s string) string {
	return bearerRedactRe.ReplaceAllString(s, "Bearer [REDACTED]")
}

// TODO(opdev): thread context.Context through the network layer once we wire
// in a cancellation source above this CLI (signals, timeouts beyond per-request).
// For a one-shot CLI this is acceptable; refactor when we add long-running modes.

// DefaultHTTPGetter performs a real HTTPS GET. It is wired in by the CLI; tests
// inject a stub instead.
//
// Redirects are NOT followed: a 3xx hops away from the App Store Connect host
// would either silently leak the bearer token or, if we host-checked the new
// URL, generate confusing errors. The caller (ascGet) already pins the host.
func DefaultHTTPGetter(rawURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, &ASCError{Status: resp.StatusCode, URL: rawURL, Body: redactBearer(string(body))}
	}
	return body, nil
}

// assertASCHost pins outbound requests to https://api.appstoreconnect.apple.com.
//
// It enforces:
//   - scheme is exactly "https" (case-insensitive)
//   - hostname equals "api.appstoreconnect.apple.com" exactly (case-insensitive)
//     — guards against suffix attacks like "api.appstoreconnect.apple.com.evil.com"
//   - no userinfo segment ("https://user@evil.com/...") — even though url.Parse
//     would return the userinfo's host, we reject any URL that carries one
//   - non-empty hostname (rejects IP-literal-only or malformed inputs)
func assertASCHost(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &ASCError{Status: 0, URL: rawURL, Body: fmt.Sprintf("invalid URL: %v", err)}
	}
	if u.User != nil {
		return &ASCError{Status: 0, URL: rawURL, Body: "refusing URL with userinfo segment"}
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if scheme != "https" || host != ascHost {
		return &ASCError{Status: 0, URL: rawURL, Body: fmt.Sprintf("refusing to follow off-host URL: %s", rawURL)}
	}
	return nil
}

func ascGet(token, rawURL string, httpGet HTTPGetter) (map[string]interface{}, error) {
	return ascGetAccept(token, rawURL, "application/json", httpGet)
}

// ascGetAccept is ascGet with a caller-supplied Accept header. The
// perfPowerMetrics endpoint rejects "application/json" with a 406 and requires
// "application/vnd.apple.xcode-metrics+json"; everything else uses JSON.
func ascGetAccept(token, rawURL, accept string, httpGet HTTPGetter) (map[string]interface{}, error) {
	if err := assertASCHost(rawURL); err != nil {
		return nil, err
	}
	body, err := httpGet(rawURL, map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        accept,
	})
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return map[string]interface{}{}, nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parsing ASC response from %s: %w", rawURL, err)
	}
	return out, nil
}

// --------------------------------------------------------------------------
// Builds
// --------------------------------------------------------------------------

// Build mirrors the relevant subset of an App Store Connect build resource.
type Build struct {
	ID         string                 `json:"id"`
	Attributes map[string]interface{} `json:"attributes"`
	Raw        map[string]interface{} `json:"raw,omitempty"`
}

// ListBuilds returns builds for an app, newest first, optionally filtered to
// those uploaded since `since`. It paginates via `links.next`, asserting that
// every URL stays on the App Store Connect host with HTTPS.
func ListBuilds(token, appID string, since *time.Time, httpGet HTTPGetter) ([]Build, error) {
	next := fmt.Sprintf(
		"%s/v1/builds?filter%%5Bapp%%5D=%s&sort=-uploadedDate&limit=200",
		ascBaseURL, url.QueryEscape(appID),
	)

	var builds []Build
	for next != "" {
		payload, err := ascGet(token, next, httpGet)
		if err != nil {
			return nil, err
		}
		if data, ok := payload["data"].([]interface{}); ok {
			for _, item := range data {
				if obj, ok := item.(map[string]interface{}); ok {
					b := Build{Raw: obj}
					if id, ok := obj["id"].(string); ok {
						b.ID = id
					}
					if attrs, ok := obj["attributes"].(map[string]interface{}); ok {
						b.Attributes = attrs
					}
					builds = append(builds, b)
				}
			}
		}
		next = ""
		if links, ok := payload["links"].(map[string]interface{}); ok {
			if n, ok := links["next"].(string); ok && n != "" {
				next = n
			}
		}
	}

	if since == nil {
		return builds, nil
	}

	cutoff := since.UTC()
	filtered := make([]Build, 0, len(builds))
	for _, b := range builds {
		ts, ok := parseUploadedDate(b.Attributes)
		if !ok {
			continue
		}
		if !ts.Before(cutoff) {
			filtered = append(filtered, b)
		}
	}
	return filtered, nil
}

func parseUploadedDate(attrs map[string]interface{}) (time.Time, bool) {
	if attrs == nil {
		return time.Time{}, false
	}
	raw, ok := attrs["uploadedDate"].(string)
	if !ok || raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// --------------------------------------------------------------------------
// Crash feedback
// --------------------------------------------------------------------------

// Diagnostic is the normalized crash/feedback record returned to callers.
// All nullable fields are pointers so JSON output omits them cleanly via
// omitempty where appropriate.
type Diagnostic struct {
	BuildID     string                 `json:"build_id"`
	Signature   *string                `json:"signature"`
	Signal      *string                `json:"signal"`
	Stack       interface{}            `json:"stack"`
	OSVersion   *string                `json:"os_version"`
	Device      *string                `json:"device"`
	Count       *int                   `json:"count"`
	FirstSeen   *string                `json:"first_seen"`
	LastSeen    *string                `json:"last_seen"`
	TesterNotes *string                `json:"tester_notes,omitempty"`
	Raw         map[string]interface{} `json:"raw,omitempty"`
}

// Field mapping is provisional — Apple's perfPowerMetrics crash schema is not formally documented. Adjust when we see real responses.
func normalizeDiagnostic(raw map[string]interface{}, buildID string) Diagnostic {
	attrs, _ := raw["attributes"].(map[string]interface{})
	d := Diagnostic{BuildID: buildID, Raw: raw}

	d.Signature = firstString(attrs, "signature", "symbol")
	if d.Signature == nil {
		if id, ok := raw["id"].(string); ok && id != "" {
			s := id
			d.Signature = &s
		}
	}
	d.Signal = firstString(attrs, "signal", "exceptionType")
	if attrs != nil {
		for _, k := range []string{"stack", "callStack", "symbols"} {
			if v, ok := attrs[k]; ok && v != nil {
				d.Stack = v
				break
			}
		}
	}
	d.OSVersion = firstString(attrs, "osVersion", "platformVersion")
	d.Device = firstString(attrs, "deviceModel", "device")
	d.Count = firstInt(attrs, "count", "occurrences")
	d.FirstSeen = firstString(attrs, "firstSeen", "startDate")
	d.LastSeen = firstString(attrs, "lastSeen", "endDate")
	return d
}

func firstString(m map[string]interface{}, keys ...string) *string {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok && s != "" {
				cp := s
				return &cp
			}
		}
	}
	return nil
}

func firstInt(m map[string]interface{}, keys ...string) *int {
	if m == nil {
		return nil
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			n := int(t)
			return &n
		case int:
			n := t
			return &n
		case string:
			if n, err := strconv.Atoi(t); err == nil {
				return &n
			}
		}
	}
	return nil
}

// CrashFeedback fetches crash signatures plus tester notes for a build. 404 on
// either endpoint is swallowed with a stderr warning.
func CrashFeedback(token, buildID string, httpGet HTTPGetter) ([]Diagnostic, error) {
	var results []Diagnostic

	metricsURL := fmt.Sprintf("%s/v1/builds/%s/perfPowerMetrics", ascBaseURL, url.PathEscape(buildID))
	metrics, err := ascGetAccept(token, metricsURL, "application/vnd.apple.xcode-metrics+json", httpGet)
	if err != nil {
		var ascErr *ASCError
		if errors.As(err, &ascErr) && ascErr.Status == 404 {
			fmt.Fprintf(os.Stderr, "warning: perfPowerMetrics 404 for build %s\n", buildID)
		} else {
			return nil, err
		}
	} else if data, ok := metrics["data"].([]interface{}); ok {
		for _, item := range data {
			if obj, ok := item.(map[string]interface{}); ok {
				results = append(results, normalizeDiagnostic(obj, buildID))
			}
		}
	}

	locURL := fmt.Sprintf("%s/v1/builds/%s/betaBuildLocalizations", ascBaseURL, url.PathEscape(buildID))
	locs, err := ascGet(token, locURL, httpGet)
	if err != nil {
		var ascErr *ASCError
		if errors.As(err, &ascErr) && ascErr.Status == 404 {
			fmt.Fprintf(os.Stderr, "warning: betaBuildLocalizations 404 for build %s\n", buildID)
		} else {
			return nil, err
		}
	} else if data, ok := locs["data"].([]interface{}); ok {
		for _, item := range data {
			obj, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			attrs, _ := obj["attributes"].(map[string]interface{})
			notes := firstString(attrs, "whatsNew")
			if notes == nil {
				continue
			}
			results = append(results, Diagnostic{
				BuildID:     buildID,
				TesterNotes: notes,
				Raw:         obj,
			})
		}
	}

	return results, nil
}

// --------------------------------------------------------------------------
// Diagnose orchestration
// --------------------------------------------------------------------------

var durationRe = regexp.MustCompile(`^(\d+)([smhdw])$`)

// ParseSince parses a duration spec like "24h" or "7d", or an ISO 8601
// timestamp, into a UTC time.Time relative to time.Now().
func ParseSince(spec string) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	if m := durationRe.FindStringSubmatch(spec); m != nil {
		n, _ := strconv.Atoi(m[1])
		var unit time.Duration
		switch m[2] {
		case "s":
			unit = time.Second
		case "m":
			unit = time.Minute
		case "h":
			unit = time.Hour
		case "d":
			unit = 24 * time.Hour
		case "w":
			unit = 7 * 24 * time.Hour
		}
		return time.Now().UTC().Add(-time.Duration(n) * unit), nil
	}
	normalized := spec
	if strings.HasSuffix(normalized, "Z") {
		normalized = strings.TrimSuffix(normalized, "Z") + "+00:00"
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05-07:00"} {
		if t, err := time.Parse(layout, normalized); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: use e.g. 24h, 7d, or ISO 8601", spec)
}

// DiagnoseOptions wires the diagnose CLI flags to the orchestrator.
type DiagnoseOptions struct {
	AppID      string // override; empty means use creds.AppID
	Since      *time.Time
	BuildVer   string // filter to a specific build version
	DeviceID   string // accepted but Apple-side-limited; warning emitted
	SignalName string // client-side filter
	HTTPGet    HTTPGetter
}

// DiagnoseResult is the output of Diagnose — a flat list of diagnostics grouped
// for rendering.
type DiagnoseResult struct {
	Builds      []Build      `json:"builds"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Diagnose mints a JWT, lists builds (applying Since + BuildVer filters),
// pulls crash feedback for each, applies the Signal filter, and returns a
// DiagnoseResult.
func Diagnose(creds *Credentials, opts DiagnoseOptions) (DiagnoseResult, error) {
	var result DiagnoseResult
	if opts.HTTPGet == nil {
		opts.HTTPGet = DefaultHTTPGetter
	}

	token, err := MintJWT(creds)
	if err != nil {
		return result, err
	}

	appID := opts.AppID
	if appID == "" {
		appID = creds.AppID
	}

	builds, err := ListBuilds(token, appID, opts.Since, opts.HTTPGet)
	if err != nil {
		return result, err
	}

	if opts.BuildVer != "" {
		filtered := make([]Build, 0, len(builds))
		for _, b := range builds {
			if v, ok := b.Attributes["version"].(string); ok && v == opts.BuildVer {
				filtered = append(filtered, b)
			}
		}
		builds = filtered
	}

	if opts.DeviceID != "" {
		fmt.Fprintln(os.Stderr, "warning: --device filter is Apple-side-only and limited in Phase 1.")
	}

	result.Builds = builds
	for _, b := range builds {
		if b.ID == "" {
			continue
		}
		entries, err := CrashFeedback(token, b.ID, opts.HTTPGet)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: crash_feedback(%s) failed: %v\n", b.ID, err)
			continue
		}
		result.Diagnostics = append(result.Diagnostics, entries...)
	}

	if opts.SignalName != "" {
		needle := strings.ToLower(opts.SignalName)
		filtered := make([]Diagnostic, 0, len(result.Diagnostics))
		for _, d := range result.Diagnostics {
			if d.Signal != nil && strings.Contains(strings.ToLower(*d.Signal), needle) {
				filtered = append(filtered, d)
			}
		}
		result.Diagnostics = filtered
	}

	return result, nil
}

// --------------------------------------------------------------------------
// Rendering
// --------------------------------------------------------------------------

// RenderTable writes a human-readable grouped table of diagnostics to w.
func RenderTable(w io.Writer, result DiagnoseResult) {
	entries := result.Diagnostics
	if len(entries) == 0 {
		fmt.Fprintln(w, "(no crash entries returned)")
		return
	}

	type group struct {
		sig   string
		items []Diagnostic
	}
	groupMap := map[string]*group{}
	var order []string
	for _, e := range entries {
		sig := "(no signature)"
		if e.Signature != nil && *e.Signature != "" {
			sig = *e.Signature
		}
		if _, ok := groupMap[sig]; !ok {
			groupMap[sig] = &group{sig: sig}
			order = append(order, sig)
		}
		groupMap[sig].items = append(groupMap[sig].items, e)
	}
	sort.Strings(order) // deterministic output

	cols := []string{"signature", "signal", "count", "last_seen", "os", "device"}
	widths := map[string]int{}
	for _, c := range cols {
		widths[c] = len(c)
	}
	type row struct {
		fields map[string]string
		items  []Diagnostic
	}
	rows := make([]row, 0, len(order))
	for _, sig := range order {
		g := groupMap[sig]
		first := g.items[0]
		total := 0
		for _, it := range g.items {
			if it.Count != nil {
				total += *it.Count
			}
		}
		if total == 0 {
			total = len(g.items)
		}
		fields := map[string]string{
			"signature": truncate(g.sig, 60),
			"signal":    strOrEmpty(first.Signal),
			"count":     strconv.Itoa(total),
			"last_seen": strOrEmpty(first.LastSeen),
			"os":        strOrEmpty(first.OSVersion),
			"device":    strOrEmpty(first.Device),
		}
		for _, c := range cols {
			if l := len(fields[c]); l > widths[c] {
				widths[c] = l
			}
		}
		rows = append(rows, row{fields: fields, items: g.items})
	}

	pad := func(s string, n int) string {
		if len(s) >= n {
			return s
		}
		return s + strings.Repeat(" ", n-len(s))
	}
	fmtRow := func(get func(string) string) string {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = pad(get(c), widths[c])
		}
		return strings.Join(parts, " | ")
	}

	fmt.Fprintln(w, fmtRow(func(c string) string { return c }))
	sepParts := make([]string, len(cols))
	for i, c := range cols {
		sepParts[i] = strings.Repeat("-", widths[c])
	}
	fmt.Fprintln(w, strings.Join(sepParts, "-+-"))
	for _, r := range rows {
		fmt.Fprintln(w, fmtRow(func(c string) string { return r.fields[c] }))
		for _, item := range r.items {
			if item.Stack != nil {
				switch s := item.Stack.(type) {
				case []interface{}:
					for _, line := range s {
						fmt.Fprintf(w, "    %v\n", line)
					}
				case string:
					for _, line := range strings.Split(s, "\n") {
						fmt.Fprintf(w, "    %s\n", line)
					}
				}
			}
			if item.TesterNotes != nil {
				for _, line := range strings.Split(*item.TesterNotes, "\n") {
					fmt.Fprintf(w, "    [tester] %s\n", line)
				}
			}
		}
		fmt.Fprintln(w)
	}
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --------------------------------------------------------------------------
// Defaults
// --------------------------------------------------------------------------

// SOPSRelPath is the conventional path inside an ownpulse-infra checkout to
// the App Store Connect credentials YAML. Exported so callers (e.g. opdev's
// main package, which owns workspace-config lookups) can build the absolute
// path themselves.
const SOPSRelPath = sopsRelPath

// ResolveCredentials resolves credentials field-by-field, with precedence:
//
//  1. Explicit field on `explicit` (e.g. --key-id flag, --app-id flag)
//  2. Environment variable (ASC_KEY_ID, ASC_ISSUER_ID, ASC_APP_ID, ASC_KEY_PEM)
//  3. Field loaded from the SOPS-decrypted YAML at `explicit.SOPSPath`
//
// Each field is resolved independently — passing only --key-id with the rest
// in SOPS produces a merged result rather than silently dropping the flag.
// Returns an error naming every field that could not be filled from any
// source.
//
// This function is workspace-config-agnostic: the SOPS path must be supplied
// by the caller. opdev's main package owns the flag/env/workspace.toml
// resolution and passes the final path in.
//
// Note: there is no --key-pem flag (and intentionally so — PEM contents on the
// command line leak via ps/shell history). Only env or SOPS can supply KeyPEM.
func ResolveCredentials(explicit CredentialOptions) (*Credentials, error) {
	// Attempt to load SOPS values up front so we can fall back to them on a
	// per-field basis. If SOPS isn't asked for or fails, leave sopsCreds nil
	// and report whatever sopsErr we got only if we end up *needing* a SOPS
	// field that wasn't otherwise provided.
	var (
		sopsCreds *Credentials
		sopsErr   error
	)
	sopsPath := explicit.SOPSPath
	if sopsPath != "" {
		sopsCreds, sopsErr = LoadCredentials(CredentialOptions{
			SOPSPath:   sopsPath,
			SopsRunner: explicit.SopsRunner,
		})
	}

	pick := func(flag, env string, fromSOPS func(*Credentials) string) string {
		if flag != "" {
			return flag
		}
		if v := os.Getenv(env); v != "" {
			return v
		}
		if sopsCreds != nil {
			return fromSOPS(sopsCreds)
		}
		return ""
	}
	pickBytes := func(flag []byte, env string, fromSOPS func(*Credentials) []byte) []byte {
		if len(flag) > 0 {
			return flag
		}
		if v := os.Getenv(env); v != "" {
			return []byte(v)
		}
		if sopsCreds != nil {
			return fromSOPS(sopsCreds)
		}
		return nil
	}

	keyID := pick(explicit.KeyID, "ASC_KEY_ID", func(c *Credentials) string { return c.KeyID })
	issuerID := pick(explicit.IssuerID, "ASC_ISSUER_ID", func(c *Credentials) string { return c.IssuerID })
	appID := pick(explicit.AppID, "ASC_APP_ID", func(c *Credentials) string { return c.AppID })
	keyPEM := pickBytes(explicit.KeyPEM, "ASC_KEY_PEM", func(c *Credentials) []byte { return c.KeyPEM })

	var missing []string
	if keyID == "" {
		missing = append(missing, "key_id (flag --key-id / env ASC_KEY_ID / SOPS)")
	}
	if issuerID == "" {
		missing = append(missing, "issuer_id (flag --issuer-id / env ASC_ISSUER_ID / SOPS)")
	}
	if appID == "" {
		missing = append(missing, "app_id (flag --app-id / env ASC_APP_ID / SOPS)")
	}
	if len(keyPEM) == 0 {
		missing = append(missing, "key_pem (env ASC_KEY_PEM / SOPS)")
	}

	if len(missing) > 0 {
		if sopsErr != nil {
			return nil, fmt.Errorf("credentials missing — %s; (SOPS load also failed: %w)",
				strings.Join(missing, ", "), sopsErr)
		}
		return nil, &MissingCredentialsError{Fields: missing}
	}

	return &Credentials{
		KeyID:    keyID,
		IssuerID: issuerID,
		AppID:    appID,
		KeyPEM:   keyPEM,
	}, nil
}
