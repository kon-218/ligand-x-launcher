package license

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeRegistryAuth(t *testing.T) {
	encoded, err := EncodeRegistryAuth(RegistryCredentials{
		Host:     "ghcr.io",
		Username: "oauth2",
		Token:    "short-lived-token",
	})
	if err != nil {
		t.Fatalf("encodeRegistryAuth failed: %v", err)
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("encoded auth is not base64url: %v", err)
	}
	if !strings.Contains(string(raw), "short-lived-token") {
		t.Fatalf("encoded auth missing token: %s", string(raw))
	}
}

// TestEmbeddedPublicKeyMatchesPemFile prevents drift between the launcher's
// compiled-in public key and the canonical PEM under lib/licensing/. If
// either is rotated alone, every signed license silently fails verification
// at one verifier or the other.
func TestEmbeddedPublicKeyMatchesPemFile(t *testing.T) {
	publicRoot := os.Getenv("LIGANDX_PUBLIC_ROOT")
	if publicRoot == "" {
		publicRoot = filepath.Join("..", "..", "..", "ligand-x")
	}
	pemPath := filepath.Join(publicRoot, "lib", "licensing", "public_key.pem")
	onDisk, err := os.ReadFile(pemPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("cross-repository validation skipped: ligand-x is unavailable; embedded public-key drift was not verified (set LIGANDX_PUBLIC_ROOT to its checkout)")
		}
		t.Fatalf("read %s: %v", pemPath, err)
	}
	// Compare PEM blocks structurally — trailing whitespace differences in
	// the source files are not meaningful, but key bytes must match.
	diskBlock, _ := pem.Decode(onDisk)
	embedBlock, _ := pem.Decode([]byte(PublicKeyPEM))
	if diskBlock == nil || embedBlock == nil {
		t.Fatalf("failed to PEM-decode launcher (%v) or %s (%v)", embedBlock, pemPath, diskBlock)
	}
	if !bytes.Equal(diskBlock.Bytes, embedBlock.Bytes) {
		t.Fatalf("public key drift between launcher embed and %s", pemPath)
	}
}

// signTestLicense produces a signed bundle for the table-driven verifier
// tests. Uses a fresh keypair per call so production keys are never needed.
func signTestLicense(t *testing.T, payload map[string]interface{}) (bundleBytes []byte, publicPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	canonical, err := canonicalPayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	sig := ed25519.Sign(priv, canonical)

	bundle := map[string]interface{}{
		"schema":    "ligandx-license/1",
		"algorithm": "Ed25519",
		"payload":   payload,
		"signature": base64.StdEncoding.EncodeToString(sig),
	}
	bundleBytes, err = json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	pubDer, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	publicPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer})
	return bundleBytes, publicPEM
}

func TestVerifyLicenseValid(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-1",
		"entitlements": []interface{}{"qc", "admet"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"customer":     map[string]interface{}{"name": "Acme"},
	})
	got, err := VerifyWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid || got.Edition != "pro" {
		t.Fatalf("expected valid pro, got %+v", got)
	}
	if got.CustomerName != "Acme" {
		t.Fatalf("expected customer Acme, got %q", got.CustomerName)
	}
	if !got.HasEntitlement("qc") || got.HasEntitlement("boltz2") {
		t.Fatalf("entitlement check wrong: %+v", got.Entitlements)
	}
}

func TestVerifyLicenseRejectsTamperedPayload(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-2",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	tampered := bytes.Replace(bundle, []byte(`"qc"`), []byte(`"boltz2"`), 1)
	got, _ := VerifyWithPublicKey(tampered, pub)
	if got.Valid {
		t.Fatalf("expected tampered payload to fail, got valid")
	}
	if got.Reason != "invalid_signature" {
		t.Fatalf("expected invalid_signature, got %q", got.Reason)
	}
}

func TestVerifyLicenseRejectsWrongKey(t *testing.T) {
	bundle, _ := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-3",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	_, otherPub := signTestLicense(t, map[string]interface{}{"edition": "pro"})
	got, _ := VerifyWithPublicKey(bundle, otherPub)
	if got.Valid {
		t.Fatalf("expected verification under wrong key to fail")
	}
}

func TestVerifyLicenseExpiredNoGrace(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-4",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, _ := VerifyWithPublicKey(bundle, pub)
	if got.Valid {
		t.Fatalf("expected expired license to be invalid")
	}
	if got.Reason != "license_expired" {
		t.Fatalf("expected license_expired, got %q", got.Reason)
	}
}

func TestVerifyLicenseExpiredWithinGrace(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-5",
		"entitlements": []interface{}{"qc"},
		"expires_at":   time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		"grace_until":  time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, err := VerifyWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid {
		t.Fatalf("expected grace-period license to be valid, got %+v", got)
	}
}

func TestVerifyLicenseUnknownEntitlement(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-6",
		"entitlements": []interface{}{"definitely-not-a-real-module"},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, _ := VerifyWithPublicKey(bundle, pub)
	if got.Valid {
		t.Fatalf("expected unknown entitlement to invalidate license")
	}
	if got.Reason != "unknown_entitlement" {
		t.Fatalf("expected unknown_entitlement, got %q", got.Reason)
	}
}

func TestVerifyLicenseProRequiresEntitlements(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":      "pro",
		"license_id":   "LX-TEST-7",
		"entitlements": []interface{}{},
		"expires_at":   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, _ := VerifyWithPublicKey(bundle, pub)
	if got.Valid {
		t.Fatalf("expected empty Pro entitlements to invalidate")
	}
	if got.Reason != "pro_license_requires_entitlements" {
		t.Fatalf("got reason %q", got.Reason)
	}
}

func TestVerifyLicenseAcademicGrantsAll(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":    "academic",
		"license_id": "LX-TEST-8",
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	})
	got, err := VerifyWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid || got.Edition != "academic" {
		t.Fatalf("expected academic valid, got %+v", got)
	}
	for entitlement := range proEntitlements {
		if !got.HasEntitlement(entitlement) {
			t.Fatalf("academic should grant %q", entitlement)
		}
	}
}

func TestVerifyLicenseWithVersionRangeGreaterThan(t *testing.T) {
	bundle, pub := signTestLicense(t, map[string]interface{}{
		"edition":       "academic",
		"license_id":    "LX-TEST-HTML-ESCAPE",
		"expires_at":    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"version_range": ">=0.0.0",
	})
	got, err := VerifyWithPublicKey(bundle, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Valid || got.Reason != "ok" {
		t.Fatalf("expected version_range license to verify, got %+v", got)
	}
}

func TestHasEntitlementSemantics(t *testing.T) {
	free := Summary{Edition: "free", Valid: true}
	if free.HasEntitlement("qc") {
		t.Fatal("free should not have qc")
	}

	expired := Summary{Edition: "pro", Valid: false, Entitlements: []string{"qc"}}
	if expired.HasEntitlement("qc") {
		t.Fatal("invalid pro should not have entitlement")
	}

	pro := Summary{Edition: "pro", Valid: true, Entitlements: []string{"qc"}}
	if !pro.HasEntitlement("qc") || pro.HasEntitlement("boltz2") {
		t.Fatal("pro entitlement scoping wrong")
	}

	academic := Summary{Edition: "academic", Valid: true}
	if !academic.HasEntitlement("anything") {
		t.Fatal("academic should grant any pro entitlement")
	}
}

func TestRegistryCredentialsFromLicenseRequiresValidSignedBridgeMode(t *testing.T) {
	validPayload := map[string]interface{}{
		"edition":       "academic",
		"license_id":    "LX-BRIDGE-TEST",
		"expires_at":    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"registry_mode": "bridge",
		"registry": map[string]interface{}{
			"host": "ghcr.io", "username": "reader", "token": "tok",
		},
	}
	bundle, pub := signTestLicense(t, validPayload)
	creds, ok := RegistryCredentialsFromData(bundle, pub)
	if !ok {
		t.Fatal("expected valid signed bridge credentials to be accepted")
	}
	if creds.Host != "ghcr.io" || creds.Username != "reader" || creds.Token != "tok" {
		t.Fatalf("unexpected bridge credentials: %+v", creds)
	}

	withoutMode := map[string]interface{}{}
	for key, value := range validPayload {
		withoutMode[key] = value
	}
	delete(withoutMode, "registry_mode")
	bundle, pub = signTestLicense(t, withoutMode)
	if _, ok := RegistryCredentialsFromData(bundle, pub); ok {
		t.Fatal("expected credentials without registry_mode=bridge to be ignored")
	}

	bundle, pub = signTestLicense(t, validPayload)
	tampered := bytes.Replace(bundle, []byte(`"tok"`), []byte(`"other"`), 1)
	if _, ok := RegistryCredentialsFromData(tampered, pub); ok {
		t.Fatal("expected tampered bridge credentials to be rejected")
	}

	expiredPayload := map[string]interface{}{}
	for key, value := range validPayload {
		expiredPayload[key] = value
	}
	expiredPayload["expires_at"] = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	bundle, pub = signTestLicense(t, expiredPayload)
	if _, ok := RegistryCredentialsFromData(bundle, pub); ok {
		t.Fatal("expected expired bridge credentials to be rejected")
	}

	wrongHostPayload := map[string]interface{}{}
	for key, value := range validPayload {
		wrongHostPayload[key] = value
	}
	wrongHostPayload["registry"] = map[string]interface{}{
		"host": "example.com", "username": "reader", "token": "tok",
	}
	bundle, pub = signTestLicense(t, wrongHostPayload)
	if _, ok := RegistryCredentialsFromData(bundle, pub); ok {
		t.Fatal("expected non-GHCR bridge credentials to be rejected")
	}
}

func TestRegistryTokenResponseMustBeShortLivedAndExactlyScoped(t *testing.T) {
	repositories := []string{"ghcr.io/kon-218/ligand-x-pro/admet"}
	valid := RegistryTokenResponse{
		Host:         "ghcr.io",
		Username:     "oauth2",
		Token:        "short-lived-token",
		ExpiresAt:    time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
		Repositories: repositories,
	}
	if _, err := ValidateRegistryTokenResponse(valid, repositories); err != nil {
		t.Fatalf("valid scoped token response rejected: %v", err)
	}
	wrongScope := valid
	wrongScope.Repositories = []string{"ghcr.io/kon-218/ligand-x-pro/qc"}
	if _, err := ValidateRegistryTokenResponse(wrongScope, repositories); err == nil {
		t.Fatal("unexpected repository scope was accepted")
	}
	expired := valid
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := ValidateRegistryTokenResponse(expired, repositories); err == nil {
		t.Fatal("expired registry token was accepted")
	}
	wrongHost := valid
	wrongHost.Host = "registry.attacker.example"
	if _, err := ValidateRegistryTokenResponse(wrongHost, repositories); err == nil {
		t.Fatal("non-GHCR registry token was accepted")
	}
}
