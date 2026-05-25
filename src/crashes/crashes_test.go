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

// stubGetter returns canned JSON responses keyed by URL substring. If multiple
// responses are configured (sequence mode), it returns them in order.
type stubGetter struct {
	t         *testing.T
	calls     []string
	responses [][]byte
	idx       int
}

func (s *stubGetter) get(url string, _ map[string]string) ([]byte, error) {
	s.calls = append(s.calls, url)
	if s.idx >= len(s.responses) {
		s.t.Fatalf("unexpected call #%d to %s", s.idx+1, url)
	}
	body := s.responses[s.idx]
	s.idx++
	return body, nil
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
	stub := &stubGetter{t: t, responses: [][]byte{body}}
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
	stub := &stubGetter{t: t, responses: [][]byte{page1, page2}}
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
	stub := &stubGetter{t: t, responses: [][]byte{page1}}
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
	stub := &stubGetter{t: t, responses: [][]byte{page1}}
	_, err := ListBuilds("tok", "app-1", nil, stub.get)
	if err == nil {
		t.Fatal("expected http-scheme rejection, got nil")
	}
	var ascErr *ASCError
	if !errors.As(err, &ascErr) {
		t.Fatalf("expected *ASCError, got %T", err)
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
