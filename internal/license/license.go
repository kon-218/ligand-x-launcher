// Package license verifies signed Ligand-X licence bundles against the
// embedded Ed25519 public key and derives GHCR registry credentials from a
// verified licence or a token-broker response.
package license

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Summary struct {
	Edition      string   `json:"edition"`
	LicenseID    string   `json:"licenseId"`
	CustomerName string   `json:"customerName"`
	ExpiresAt    string   `json:"expiresAt"`
	GraceUntil   string   `json:"graceUntil"`
	Entitlements []string `json:"entitlements"`
	Valid        bool     `json:"valid"`
	Reason       string   `json:"reason"`
}

type bundle struct {
	Schema    string                 `json:"schema"`
	Algorithm string                 `json:"algorithm"`
	Payload   map[string]interface{} `json:"payload"`
	Signature string                 `json:"signature"`
}

type RegistryCredentials struct {
	Host     string
	Username string
	Token    string
}

type RegistryTokenRequest struct {
	LicenseID    string   `json:"license_id"`
	Groups       []string `json:"groups"`
	Repositories []string `json:"repositories"`
	Entitlements []string `json:"entitlements"`
	MachineID    string   `json:"machine_id"`
	Version      string   `json:"version"`
}

type RegistryTokenResponse struct {
	Host          string   `json:"host"`
	Username      string   `json:"username"`
	Token         string   `json:"token"`
	IdentityToken string   `json:"identity_token"`
	RegistryToken string   `json:"registry_token"`
	ExpiresAt     string   `json:"expires_at"`
	Repositories  []string `json:"repositories"`
}

var proEntitlements = map[string]bool{
	"admet":       true,
	"qc":          true,
	"boltz2":      true,
	"free-energy": true,
	"reinvent":    true,
}

const PublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAcKQKljOJr+vNjOKVewo7sDMaguZUqIJVhYZDgDhnUlE=
-----END PUBLIC KEY-----`

func Verify(data []byte) (Summary, error) {
	return VerifyWithPublicKey(data, []byte(PublicKeyPEM))
}

func canonicalPayload(payload map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func VerifyWithPublicKey(data []byte, publicKeyPEM []byte) (Summary, error) {
	var bundle bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_license_json"}, err
	}
	if bundle.Algorithm != "Ed25519" {
		return Summary{Edition: "free", Valid: false, Reason: "unsupported_algorithm"}, nil
	}

	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_public_key"}, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_public_key"}, err
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_public_key_type"}, nil
	}

	canonical, err := canonicalPayload(bundle.Payload)
	if err != nil {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_payload"}, err
	}
	signature, err := base64.StdEncoding.DecodeString(bundle.Signature)
	if err != nil {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_signature_encoding"}, err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return Summary{Edition: "free", Valid: false, Reason: "invalid_signature"}, nil
	}

	return summarize(bundle.Payload), nil
}

func summarize(payload map[string]interface{}) Summary {
	edition, _ := payload["edition"].(string)
	entitlements := stringSlice(payload["entitlements"])
	if edition == "academic" {
		entitlements = []string{"admet", "boltz2", "free-energy", "qc", "reinvent"}
	}

	status := Summary{
		Edition:      edition,
		LicenseID:    stringValue(payload["license_id"]),
		ExpiresAt:    stringValue(payload["expires_at"]),
		GraceUntil:   stringValue(payload["grace_until"]),
		Entitlements: entitlements,
		Valid:        true,
		Reason:       "ok",
	}
	if customer, ok := payload["customer"].(map[string]interface{}); ok {
		status.CustomerName = stringValue(customer["name"])
	}
	if edition != "academic" && edition != "pro" {
		status.Edition = "free"
		status.Valid = false
		status.Reason = "invalid_edition"
		return status
	}
	if edition == "pro" && len(entitlements) == 0 {
		status.Edition = "free"
		status.Valid = false
		status.Reason = "pro_license_requires_entitlements"
		return status
	}
	for _, entitlement := range entitlements {
		if !proEntitlements[entitlement] {
			status.Edition = "free"
			status.Valid = false
			status.Reason = "unknown_entitlement"
			return status
		}
	}

	now := time.Now().UTC()
	if status.ExpiresAt != "" {
		if expiresAt, err := time.Parse(time.RFC3339, status.ExpiresAt); err == nil && now.After(expiresAt) {
			if status.GraceUntil == "" {
				status.Edition = "free"
				status.Valid = false
				status.Reason = "license_expired"
				return status
			}
			if graceUntil, err := time.Parse(time.RFC3339, status.GraceUntil); err != nil || now.After(graceUntil) {
				status.Edition = "free"
				status.Valid = false
				status.Reason = "license_expired"
				return status
			}
		}
	}

	return status
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func stringSlice(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func RegistryCredentialsFromData(data, publicKeyPEM []byte) (RegistryCredentials, bool) {
	status, err := VerifyWithPublicKey(data, publicKeyPEM)
	if err != nil || !status.Valid {
		return RegistryCredentials{}, false
	}

	var bundle bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return RegistryCredentials{}, false
	}
	// Bridge registry credentials embedded in the license are an offline /
	// airgap fallback. They MUST be opt-in via an explicit signed claim and
	// are accepted only after the complete certificate verifies above.
	if mode := stringValue(bundle.Payload["registry_mode"]); mode != "bridge" {
		return RegistryCredentials{}, false
	}
	registry, ok := bundle.Payload["registry"].(map[string]interface{})
	if !ok {
		return RegistryCredentials{}, false
	}
	creds := RegistryCredentials{
		Host:     strings.ToLower(strings.TrimSpace(stringValue(registry["host"]))),
		Username: strings.TrimSpace(stringValue(registry["username"])),
		Token:    strings.TrimSpace(stringValue(registry["token"])),
	}
	if creds.Host != "ghcr.io" || creds.Username == "" || creds.Token == "" {
		return RegistryCredentials{}, false
	}
	return creds, true
}

func ValidateRegistryTokenResponse(tokenResp RegistryTokenResponse, repositories []string) (RegistryCredentials, error) {
	secret := tokenResp.Token
	if secret == "" {
		secret = tokenResp.IdentityToken
	}
	if secret == "" {
		secret = tokenResp.RegistryToken
	}
	creds := RegistryCredentials{
		Host:     stringValueOrDefault(tokenResp.Host, "ghcr.io"),
		Username: stringValueOrDefault(tokenResp.Username, "oauth2"),
		Token:    secret,
	}
	expiresAt, expiryErr := time.Parse(time.RFC3339, tokenResp.ExpiresAt)
	now := time.Now().UTC()
	if expiryErr != nil || now.After(expiresAt) || expiresAt.After(now.Add(15*time.Minute+30*time.Second)) {
		return RegistryCredentials{}, fmt.Errorf("registry token broker returned an invalid expiry")
	}
	if !slices.Equal(tokenResp.Repositories, repositories) {
		return RegistryCredentials{}, fmt.Errorf("registry token broker returned an unexpected repository scope")
	}
	if creds.Host != "ghcr.io" || creds.Token == "" {
		return RegistryCredentials{}, fmt.Errorf("registry token broker response did not include valid GHCR credentials")
	}
	return creds, nil
}

func stringValueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func EncodeRegistryAuth(creds RegistryCredentials) (string, error) {
	if creds.Host == "" || creds.Token == "" {
		return "", nil
	}
	payload := map[string]string{
		"username":      creds.Username,
		"password":      creds.Token,
		"serveraddress": creds.Host,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

func (s Summary) HasEntitlement(entitlement string) bool {
	if entitlement == "" {
		return true
	}
	if s.Valid && s.Edition == "academic" {
		return true
	}
	if !s.Valid || s.Edition != "pro" {
		return false
	}
	for _, candidate := range s.Entitlements {
		if candidate == entitlement {
			return true
		}
	}
	return false
}
