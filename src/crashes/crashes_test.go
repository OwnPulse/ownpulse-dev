// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) OwnPulse Contributors

package crashes

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func genECKeyPEM(t *testing.T) ([]byte, *ecdsa.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return pem.EncodeToMemory(block), &priv.PublicKey
}

func genRSAKeyPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal RSA: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func b64urlDecode(t *testing.T, s string) []byte {
	t.Helper()
	out, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode %q: %v", s, err)
	}
	return out
}

// ---------------------------------------------------------------------------
// JWT
// ---------------------------------------------------------------------------

func TestMintJWT_HeaderShape(t *testing.T) {
	pemBytes, _ := genECKeyPEM(t)
	tok, err := MintJWT(&Credentials{KeyID: "MYKEYID", IssuerID: "iss", KeyPEM: pemBytes})
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token parts = %d, want 3", len(parts))
	}
	var hdr map[string]string
	if err := json.Unmarshal(b64urlDecode(t, parts[0]), &hdr); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr["alg"] != "ES256" {
		t.Errorf("alg = %q, want ES256", hdr["alg"])
	}
	if hdr["kid"] != "MYKEYID" {
		t.Errorf("kid = %q, want MYKEYID", hdr["kid"])
	}
	if hdr["typ"] != "JWT" {
		t.Errorf("typ = %q, want JWT", hdr["typ"])
	}
}

func TestMintJWT_PayloadShape(t *testing.T) {
	pemBytes, _ := genECKeyPEM(t)
	before := time.Now().Unix()
	tok, err := MintJWT(&Credentials{KeyID: "K", IssuerID: "issuer-uuid", KeyPEM: pemBytes})
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}
	after := time.Now().Unix()

	parts := strings.Split(tok, ".")
	var payload map[string]interface{}
	if err := json.Unmarshal(b64urlDecode(t, parts[1]), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload["iss"] != "issuer-uuid" {
		t.Errorf("iss = %v", payload["iss"])
	}
	if payload["aud"] != "appstoreconnect-v1" {
		t.Errorf("aud = %v", payload["aud"])
	}
	iat, _ := payload["iat"].(float64)
	exp, _ := payload["exp"].(float64)
	if int64(iat) < before || int64(iat) > after {
		t.Errorf("iat %v not in [%d,%d]", iat, before, after)
	}
	if int64(exp-iat) != 1200 {
		t.Errorf("exp-iat = %v, want 1200", exp-iat)
	}
}

func TestMintJWT_SignatureVerifies(t *testing.T) {
	pemBytes, pub := genECKeyPEM(t)
	tok, err := MintJWT(&Credentials{KeyID: "K", IssuerID: "I", KeyPEM: pemBytes})
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}
	parts := strings.Split(tok, ".")
	sig := b64urlDecode(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature len = %d, want 64 (raw R||S)", len(sig))
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	signingInput := []byte(parts[0] + "." + parts[1])
	digest := sha256.Sum256(signingInput)
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("ecdsa.Verify failed against minted signature")
	}
}

func TestMintJWT_RejectsRSA(t *testing.T) {
	rsaPEM := genRSAKeyPEM(t)
	_, err := MintJWT(&Credentials{KeyID: "K", IssuerID: "I", KeyPEM: rsaPEM})
	if err == nil {
		t.Fatal("expected error minting JWT from RSA key, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListBuilds
// ---------------------------------------------------------------------------

// stubResponse pairs a body with an optional error (used to simulate 404s etc.).
type stubResponse struct {
	body []byte
	err  error
}

// stubGetter returns canned responses in order. Each call consumes one entry.
type stubGetter struct {
	t         *testing.T
	calls     []string
	responses []stubResponse
	idx       int
}

func (s *stubGetter) get(url string, _ map[string]string) ([]byte, error) {
	s.calls = append(s.calls, url)
	if s.idx >= len(s.responses) {
		s.t.Fatalf("unexpected call #%d to %s", s.idx+1, url)
	}
	r := s.responses[s.idx]
	s.idx++
	return r.body, r.err
}

// newStub builds a stubGetter from raw byte bodies (no errors).
func newStub(t *testing.T, bodies ...[]byte) *stubGetter {
	resps := make([]stubResponse, len(bodies))
	for i, b := range bodies {
		resps[i] = stubResponse{body: b}
	}
	return &stubGetter{t: t, responses: resps}
}

func TestListBuilds_SinceFilter(t *testing.T) {
	now := time.Now().UTC()
	body, _ := json.Marshal(map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"id":         "recent",
				"attributes": map[string]interface{}{"uploadedDate": now.Add(-1 * time.Hour).Format(time.RFC3339)},
			},
			map[string]interface{}{
				"id":         "old1",
				"attributes": map[string]interface{}{"uploadedDate": now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)},
			},
			map[string]interface{}{
				"id":         "old2",
				"attributes": map[string]interface{}{"uploadedDate": now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)},
			},
		},
		"links": map[string]interface{}{"next": nil},
	})
	stub := newStub(t, body)
	since := now.Add(-24 * time.Hour)
	builds, err := ListBuilds("tok", "app-1", &since, stub.get)
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(builds) != 1 || builds[0].ID != "recent" {
		t.Fatalf("got %+v, want [recent]", builds)
	}
	if len(stub.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(stub.calls))
	}
}

func TestListBuilds_PaginationFollowsNext(t *testing.T) {
	page1, _ := json.Marshal(map[string]interface{}{
		"data":  []interface{}{map[string]interface{}{"id": "a", "attributes": map[string]interface{}{}}},
		"links": map[string]interface{}{"next": "https://api.appstoreconnect.apple.com/v1/builds?cursor=2"},
	})
	page2, _ := json.Marshal(map[string]interface{}{
		"data":  []interface{}{map[string]interface{}{"id": "b", "attributes": map[string]interface{}{}}},
		"links": map[string]interface{}{"next": nil},
	})
	stub := newStub(t, page1, page2)
	builds, err := ListBuilds("tok", "app-1", nil, stub.get)
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(builds) != 2 || builds[0].ID != "a" || builds[1].ID != "b" {
		t.Fatalf("builds = %+v", builds)
	}
	if len(stub.calls) != 2 {
		t.Errorf("calls = %d, want 2", len(stub.calls))
	}
}

func TestListBuilds_RejectsOffHost(t *testing.T) {
	page1, _ := json.Marshal(map[string]interface{}{
		"data":  []interface{}{},
		"links": map[string]interface{}{"next": "https://evil.com/v1/builds?cursor=2"},
	})
	stub := newStub(t, page1)
	_, err := ListBuilds("tok", "app-1", nil, stub.get)
	if err == nil {
		t.Fatal("expected off-host rejection, got nil")
	}
	var ascErr *ASCError
	if !errors.As(err, &ascErr) {
		t.Fatalf("expected *ASCError, got %T: %v", err, err)
	}
}

func TestListBuilds_RejectsHTTPScheme(t *testing.T) {
	page1, _ := json.Marshal(map[string]interface{}{
		"data":  []interface{}{},
		"links": map[string]interface{}{"next": "http://api.appstoreconnect.apple.com/v1/builds?cursor=2"},
	})
	stub := newStub(t, page1)
	_, err := ListBuilds("tok", "app-1", nil, stub.get)
	if err == nil {
		t.Fatal("expected http-scheme rejection, got nil")
	}
	var ascErr *ASCError
	if !errors.As(err, &ascErr) {
		t.Fatalf("expected *ASCError, got %T", err)
	}
}

// TestAssertASCHost_Vectors locks the host-pin invariants against common
// bypass patterns. Both accept-cases and reject-cases share one table.
func TestAssertASCHost_Vectors(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantError bool
	}{
		{"canonical", "https://api.appstoreconnect.apple.com/v1/builds", false},
		{"canonical with query", "https://api.appstoreconnect.apple.com/v1/builds?cursor=x", false},
		{"mixed case host", "https://API.AppStoreConnect.apple.com/v1/builds", false},
		{"mixed case scheme", "HTTPS://api.appstoreconnect.apple.com/v1/builds", false},

		{"plain http", "http://api.appstoreconnect.apple.com/v1/builds", true},
		{"suffix attack", "https://api.appstoreconnect.apple.com.evil.com/v1/builds", true},
		{"prefix attack", "https://evil-api.appstoreconnect.apple.com/v1/builds", true},
		{"userinfo override", "https://api.appstoreconnect.apple.com@evil.com/v1/builds", true},
		{"userinfo with apple host", "https://user:pass@api.appstoreconnect.apple.com/v1/builds", true},
		{"embedded at ambiguity", "https://api.appstoreconnect.apple.com#@evil.com/v1/builds", false}, // fragment, host pin still wins
		{"ip literal", "https://17.0.0.1/v1/builds", true},
		{"file scheme", "file:///etc/passwd", true},
		{"empty", "", true},
		{"garbage", "::::not a url", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			err := assertASCHost(c.url)
			if c.wantError && err == nil {
				t.Fatalf("assertASCHost(%q) = nil, want error", c.url)
			}
			if !c.wantError && err != nil {
				t.Fatalf("assertASCHost(%q) = %v, want nil", c.url, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LoadCredentials
// ---------------------------------------------------------------------------

func TestLoadCredentials_ExplicitFields(t *testing.T) {
	creds, err := LoadCredentials(CredentialOptions{
		KeyID:    "K",
		IssuerID: "I",
		AppID:    "A",
		KeyPEM:   []byte("PEM-BYTES"),
	})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.KeyID != "K" || creds.IssuerID != "I" || creds.AppID != "A" || string(creds.KeyPEM) != "PEM-BYTES" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestLoadCredentials_MissingFieldsError(t *testing.T) {
	_, err := LoadCredentials(CredentialOptions{KeyID: "K"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"issuer_id", "app_id", "key_pem"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestLoadCredentials_SOPSPath(t *testing.T) {
	tmp := t.TempDir() + "/fake.yaml"
	if err := writeFile(tmp, "stub"); err != nil {
		t.Fatal(err)
	}
	yamlDoc := "key_id: KEYID123\n" +
		"issuer_id: 11111111-2222-3333-4444-555555555555\n" +
		"app_id: '1234567890'\n" +
		"key_pem: |\n" +
		"  -----BEGIN PRIVATE KEY-----\n" +
		"  FAKE\n" +
		"  -----END PRIVATE KEY-----\n"

	creds, err := LoadCredentials(CredentialOptions{
		SOPSPath: tmp,
		SopsRunner: func(path string) ([]byte, error) {
			if path != tmp {
				t.Errorf("path = %s", path)
			}
			return []byte(yamlDoc), nil
		},
	})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.KeyID != "KEYID123" {
		t.Errorf("key_id = %q", creds.KeyID)
	}
	if creds.IssuerID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("issuer = %q", creds.IssuerID)
	}
	if creds.AppID != "1234567890" {
		t.Errorf("app_id = %q", creds.AppID)
	}
	if !strings.Contains(string(creds.KeyPEM), "BEGIN PRIVATE KEY") {
		t.Errorf("key_pem missing BEGIN marker: %q", string(creds.KeyPEM))
	}
}

func TestLoadCredentials_SOPS_Base64Encoded(t *testing.T) {
	// Production path: SOPS stores the .p8 contents as base64 under
	// asc_api_key_b64. LoadCredentials must decode it into raw PEM bytes.
	const testPEM = "-----BEGIN PRIVATE KEY-----\nFAKE-CONTENTS\n-----END PRIVATE KEY-----\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(testPEM))

	tmp := t.TempDir() + "/fake.yaml"
	if err := writeFile(tmp, "stub"); err != nil {
		t.Fatal(err)
	}
	yamlDoc := "key_id: KEYID123\n" +
		"issuer_id: 11111111-2222-3333-4444-555555555555\n" +
		"app_id: '1234567890'\n" +
		"asc_api_key_b64: " + b64 + "\n"

	creds, err := LoadCredentials(CredentialOptions{
		SOPSPath:   tmp,
		SopsRunner: func(path string) ([]byte, error) { return []byte(yamlDoc), nil },
	})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if string(creds.KeyPEM) != testPEM {
		t.Fatalf("KeyPEM = %q, want decoded PEM %q", string(creds.KeyPEM), testPEM)
	}
}

func TestLoadCredentials_SOPS_PrefersBase64OverPEM(t *testing.T) {
	// When both fields are present, asc_api_key_b64 must win — it's the
	// production schema, so any legacy key_pem residue is stale.
	const winnerPEM = "-----BEGIN PRIVATE KEY-----\nWINNER\n-----END PRIVATE KEY-----\n"
	const loserPEM = "-----BEGIN PRIVATE KEY-----\nLOSER\n-----END PRIVATE KEY-----\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(winnerPEM))

	tmp := t.TempDir() + "/fake.yaml"
	if err := writeFile(tmp, "stub"); err != nil {
		t.Fatal(err)
	}
	// Note: key_pem is a YAML literal block so it survives intact for the
	// comparison below.
	yamlDoc := "key_id: K\n" +
		"issuer_id: I\n" +
		"app_id: A\n" +
		"asc_api_key_b64: " + b64 + "\n" +
		"key_pem: |\n  " + strings.ReplaceAll(loserPEM, "\n", "\n  ")

	creds, err := LoadCredentials(CredentialOptions{
		SOPSPath:   tmp,
		SopsRunner: func(path string) ([]byte, error) { return []byte(yamlDoc), nil },
	})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if string(creds.KeyPEM) != winnerPEM {
		t.Fatalf("KeyPEM = %q, want base64-decoded winner %q", string(creds.KeyPEM), winnerPEM)
	}
}

func TestLoadCredentials_SOPSMissing(t *testing.T) {
	tmp := t.TempDir() + "/fake.yaml"
	if err := writeFile(tmp, "stub"); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCredentials(CredentialOptions{
		SOPSPath:   tmp,
		SopsRunner: func(path string) ([]byte, error) { return nil, ErrSopsNotInstalled },
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "sops") || !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error missing install hint: %v", err)
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

// ---------------------------------------------------------------------------
// ResolveCredentials — field-by-field precedence
// ---------------------------------------------------------------------------

func TestResolveCredentials_FieldByFieldPrecedence(t *testing.T) {
	// Clear any inherited env so we start from a known baseline.
	clearAllASCEnv := func(t *testing.T) {
		t.Helper()
		for _, k := range []string{"ASC_KEY_ID", "ASC_ISSUER_ID", "ASC_APP_ID", "ASC_KEY_PEM", "OWNPULSE_INFRA_PATH"} {
			t.Setenv(k, "")
		}
	}

	// Pre-baked SOPS stub used by the cases that exercise the SOPS layer.
	sopsYAML := "key_id: sops-kid\n" +
		"issuer_id: sops-iss\n" +
		"app_id: sops-app\n" +
		"key_pem: sops-pem\n"
	sopsRunner := func(_ string) ([]byte, error) { return []byte(sopsYAML), nil }
	makeSOPSFile := func(t *testing.T) string {
		t.Helper()
		path := t.TempDir() + "/fake.yaml"
		if err := writeFile(path, "stub"); err != nil {
			t.Fatal(err)
		}
		return path
	}

	type want struct {
		KeyID, IssuerID, AppID, KeyPEM string
	}

	t.Run("all flags win", func(t *testing.T) {
		clearAllASCEnv(t)
		// Even if env + SOPS would also supply values, flags win on all four.
		t.Setenv("ASC_KEY_ID", "env-kid")
		t.Setenv("ASC_ISSUER_ID", "env-iss")
		t.Setenv("ASC_APP_ID", "env-app")
		t.Setenv("ASC_KEY_PEM", "env-pem")
		creds, err := ResolveCredentials(CredentialOptions{
			KeyID: "flag-kid", IssuerID: "flag-iss", AppID: "flag-app",
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		// KeyPEM has no flag → env wins.
		got := want{creds.KeyID, creds.IssuerID, creds.AppID, string(creds.KeyPEM)}
		if got != (want{"flag-kid", "flag-iss", "flag-app", "env-pem"}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("env wins when no flags", func(t *testing.T) {
		clearAllASCEnv(t)
		t.Setenv("ASC_KEY_ID", "env-kid")
		t.Setenv("ASC_ISSUER_ID", "env-iss")
		t.Setenv("ASC_APP_ID", "env-app")
		t.Setenv("ASC_KEY_PEM", "env-pem")
		creds, err := ResolveCredentials(CredentialOptions{})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		got := want{creds.KeyID, creds.IssuerID, creds.AppID, string(creds.KeyPEM)}
		if got != (want{"env-kid", "env-iss", "env-app", "env-pem"}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("sops wins when no flags or env", func(t *testing.T) {
		clearAllASCEnv(t)
		creds, err := ResolveCredentials(CredentialOptions{
			SOPSPath:   makeSOPSFile(t),
			SopsRunner: sopsRunner,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		got := want{creds.KeyID, creds.IssuerID, creds.AppID, string(creds.KeyPEM)}
		if got != (want{"sops-kid", "sops-iss", "sops-app", "sops-pem"}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("flag for one field + env for the rest", func(t *testing.T) {
		clearAllASCEnv(t)
		t.Setenv("ASC_ISSUER_ID", "env-iss")
		t.Setenv("ASC_APP_ID", "env-app")
		t.Setenv("ASC_KEY_PEM", "env-pem")
		creds, err := ResolveCredentials(CredentialOptions{KeyID: "flag-kid"})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		got := want{creds.KeyID, creds.IssuerID, creds.AppID, string(creds.KeyPEM)}
		if got != (want{"flag-kid", "env-iss", "env-app", "env-pem"}) {
			t.Fatalf("got %+v — flag should override one field without dropping the env-sourced rest", got)
		}
	})

	t.Run("flag + env + sops merged across all fields", func(t *testing.T) {
		clearAllASCEnv(t)
		t.Setenv("ASC_APP_ID", "env-app")
		creds, err := ResolveCredentials(CredentialOptions{
			KeyID:      "flag-kid",
			SOPSPath:   makeSOPSFile(t),
			SopsRunner: sopsRunner,
			// IssuerID + KeyPEM should come from SOPS, AppID from env.
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		got := want{creds.KeyID, creds.IssuerID, creds.AppID, string(creds.KeyPEM)}
		if got != (want{"flag-kid", "sops-iss", "env-app", "sops-pem"}) {
			t.Fatalf("got %+v — expected mixed sources", got)
		}
	})

	t.Run("missing field across all sources errors with field name", func(t *testing.T) {
		clearAllASCEnv(t)
		// KeyPEM has no flag, no env, no SOPS file → must error and name key_pem.
		_, err := ResolveCredentials(CredentialOptions{
			KeyID: "k", IssuerID: "i", AppID: "a",
		})
		if err == nil {
			t.Fatal("expected error for missing key_pem")
		}
		if !strings.Contains(err.Error(), "key_pem") {
			t.Errorf("error should name key_pem, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// CrashFeedback — 404 swallowed
// ---------------------------------------------------------------------------

func TestCrashFeedback_Swallows404(t *testing.T) {
	// Both endpoints return 404 → expect no error, empty result.
	stub := &stubGetter{
		t: t,
		responses: []stubResponse{
			{err: &ASCError{Status: 404, URL: "https://api.appstoreconnect.apple.com/v1/builds/xx/perfPowerMetrics", Body: "not found"}},
			{err: &ASCError{Status: 404, URL: "https://api.appstoreconnect.apple.com/v1/builds/xx/betaBuildLocalizations", Body: "not found"}},
		},
	}
	out, err := CrashFeedback("tok", "xx", stub.get)
	if err != nil {
		t.Fatalf("CrashFeedback returned error on 404: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty slice on double-404, got %d entries", len(out))
	}
}

func TestCrashFeedback_PropagatesNon404(t *testing.T) {
	stub := &stubGetter{
		t: t,
		responses: []stubResponse{
			{err: &ASCError{Status: 500, URL: "x", Body: "internal"}},
		},
	}
	_, err := CrashFeedback("tok", "xx", stub.get)
	if err == nil {
		t.Fatal("expected 500 to surface, got nil")
	}
}

// ---------------------------------------------------------------------------
// Diagnose — signal filter
// ---------------------------------------------------------------------------

func TestDiagnose_SignalFilter(t *testing.T) {
	pemBytes, _ := genECKeyPEM(t)
	now := time.Now().UTC()

	buildsPage, _ := json.Marshal(map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"id":         "b1",
				"attributes": map[string]interface{}{"uploadedDate": now.Add(-1 * time.Hour).Format(time.RFC3339)},
			},
		},
		"links": map[string]interface{}{"next": nil},
	})
	perfPage, _ := json.Marshal(map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"id":         "c1",
				"attributes": map[string]interface{}{"signature": "sig-A", "signal": "SIGSEGV"},
			},
			map[string]interface{}{
				"id":         "c2",
				"attributes": map[string]interface{}{"signature": "sig-B", "signal": "SIGABRT"},
			},
			map[string]interface{}{
				"id":         "c3",
				"attributes": map[string]interface{}{"signature": "sig-C", "signal": "EXC_BAD_ACCESS"},
			},
		},
	})
	locPage, _ := json.Marshal(map[string]interface{}{"data": []interface{}{}})

	stub := newStub(t, buildsPage, perfPage, locPage)

	result, err := Diagnose(
		&Credentials{KeyID: "K", IssuerID: "I", AppID: "A", KeyPEM: pemBytes},
		DiagnoseOptions{SignalName: "segv", HTTPGet: stub.get},
	)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic after SIGSEGV filter, got %d", len(result.Diagnostics))
	}
	if got := result.Diagnostics[0].Signal; got == nil || *got != "SIGSEGV" {
		t.Fatalf("signal = %v, want SIGSEGV", got)
	}
}

// ---------------------------------------------------------------------------
// DefaultHTTPGetter — redirects are not followed
// ---------------------------------------------------------------------------

func TestDefaultHTTPGetter_DoesNotFollowRedirects(t *testing.T) {
	// Stand up two test servers: src returns a 302 to dst; dst would set a
	// magic body. If the client follows the redirect, we'd see dst's body.
	// We expect either an error or the 302 surfaced through our ASCError path.
	dst := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("FOLLOWED-REDIRECT"))
	}))
	defer dst.Close()

	src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", dst.URL)
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("redirect-body"))
	}))
	defer src.Close()

	// Trust the test servers' self-signed certs.
	prev := http.DefaultTransport
	http.DefaultTransport = src.Client().Transport
	defer func() { http.DefaultTransport = prev }()

	_, err := DefaultHTTPGetter(src.URL, nil)
	if err == nil {
		t.Fatal("expected 302 to surface as error (redirect not followed)")
	}
	var ascErr *ASCError
	if !errors.As(err, &ascErr) {
		t.Fatalf("expected *ASCError on 302, got %T: %v", err, err)
	}
	if ascErr.Status != http.StatusFound {
		t.Fatalf("status = %d, want 302", ascErr.Status)
	}
	if strings.Contains(ascErr.Body, "FOLLOWED-REDIRECT") {
		t.Fatal("body contains downstream content — redirect WAS followed")
	}
}

// ---------------------------------------------------------------------------
// redactBearer
// ---------------------------------------------------------------------------

func TestRedactBearer(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Authorization: Bearer abc.def.ghi", "Authorization: Bearer [REDACTED]"},
		{"bearer XYZ", "Bearer [REDACTED]"},
		{"Bearer\tmy-token here", "Bearer [REDACTED] here"},
		{"no token here", "no token here"},
	}
	for _, c := range cases {
		if got := redactBearer(c.in); got != c.want {
			t.Errorf("redactBearer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
