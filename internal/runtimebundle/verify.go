// Package runtimebundle finds, verifies and installs signed Ligand-X runtime
// bundles: the release listing and signed release index, the signed bundle
// manifest, bounded downloads, safe extraction and staged activation.
package runtimebundle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"ligandx-launcher/internal/envfile"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"
)

// Policy carries the build-time inputs that release verification and download
// checks are made against. The launcher builds one from its ldflags-injected
// values (see runtimePolicy in package main); tests build their own.
type Policy struct {
	// PublicKeyB64 is the base64 raw Ed25519 key that signs runtime manifests
	// and release indexes. Empty fails closed: nothing verifies.
	PublicKeyB64 string
	// LauncherVersion is compared against each release's minimum launcher.
	LauncherVersion string
	// AllowLocalSources permits file paths and file:// URLs as bundle
	// sources. Public builds leave it false.
	AllowLocalSources bool
}

// RuntimeRelease is a launcher-safe entry from the signed stable release
// index. The backend re-resolves the selected version from the authenticated
// index; it never accepts a URL or image tag supplied by the UI.
type Release struct {
	Version           string   `json:"version"`
	PublishedAt       string   `json:"publishedAt"`
	Status            string   `json:"status"`
	Summary           string   `json:"summary"`
	Recommended       bool     `json:"recommended"`
	Compatible        bool     `json:"compatible"`
	Compatibility     string   `json:"compatibility"`
	DownloadBytes     int64    `json:"downloadBytes"`
	RebuiltComponents []string `json:"rebuiltComponents"`
	RollbackSafeFrom  []string `json:"rollbackSafeFrom"`
	MinimumLauncher   string   `json:"minimumLauncherVersion"`
	BundleURL         string   `json:"bundleUrl"`
}

type ReleaseIndex struct {
	Schema    string    `json:"schema"`
	IssuedAt  string    `json:"issued_at"`
	ExpiresAt string    `json:"expires_at"`
	Releases  []Release `json:"releases"`

	// Warning carries a non-fatal defect found while validating the index, for
	// display alongside the release list. Never serialised: it describes this
	// verification, not the signed document.
	Warning string `json:"-"`
}

const DefaultBundleURL = "https://github.com/kon-218/ligand-x-launcher/releases/latest/download/ligand-x-runtime.zip"

const AssetName = "ligand-x-runtime.zip"

// The listing endpoint, unlike /releases/latest, returns pre-releases and does
// not depend on GitHub's "Latest" pointer -- which has been wrong here before.
const releasesListAPIURL = "https://api.github.com/repos/kon-218/ligand-x-launcher/releases?per_page=50"

const ManifestAssetName = "ligand-x-runtime-manifest.json"

const SignatureAssetName = "ligand-x-runtime-manifest.sig"

const IndexAssetName = "ligand-x-release-index.json"

const IndexSignatureAssetName = "ligand-x-release-index.sig"

const MaxDownloadBytes int64 = 256 * 1024 * 1024

const MaxExpandedBytes uint64 = 1024 * 1024 * 1024

const MaxFiles = 128

type Artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type WindowsSigning struct {
	Authenticode bool   `json:"authenticode"`
	Evidence     string `json:"evidence"`
}

type MacOSSigning struct {
	DeveloperID bool   `json:"developer_id"`
	Notarized   bool   `json:"notarized"`
	Evidence    string `json:"evidence"`
}

type PlatformSigning struct {
	Windows WindowsSigning `json:"windows"`
	MacOS   MacOSSigning   `json:"macos"`
}

type Manifest struct {
	Schema          string              `json:"schema"`
	Version         string              `json:"version"`
	Asset           string              `json:"asset"`
	SHA256          string              `json:"sha256"`
	Size            int64               `json:"size"`
	IssuedAt        string              `json:"issued_at"`
	ExpiresAt       string              `json:"expires_at"`
	GitCommit       string              `json:"git_commit"`
	Artifacts       map[string]Artifact `json:"artifacts,omitempty"`
	PlatformSigning PlatformSigning     `json:"platform_signing,omitempty"`
}

func CompanionAssetURL(bundleURL, assetName string) (string, error) {
	parsed, err := url.Parse(bundleURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		return filepath.Join(filepath.Dir(bundleURL), assetName), nil
	}
	parsed.Path = pathpkg.Join(pathpkg.Dir(parsed.Path), assetName)
	parsed.RawPath = ""
	return parsed.String(), nil
}

func DecodePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
	}
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid runtime bundle public key")
	}
	return ed25519.PublicKey(raw), nil
}

func (p Policy) VerifyManifest(manifestBytes, signatureBytes []byte, expectedTag string) (Manifest, error) {
	publicKey, err := DecodePublicKey(p.PublicKeyB64)
	if err != nil {
		return Manifest{}, fmt.Errorf("runtime release trust root is not configured: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureBytes)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Manifest{}, fmt.Errorf("invalid runtime manifest signature encoding")
	}
	if !ed25519.Verify(publicKey, manifestBytes, signature) {
		return Manifest{}, fmt.Errorf("runtime manifest signature verification failed")
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("invalid runtime manifest: %w", err)
	}
	if manifest.Schema != "ligandx-runtime-manifest/1" || manifest.Asset != AssetName {
		return Manifest{}, fmt.Errorf("unsupported runtime manifest")
	}
	if !envfile.IsPinnedVersion(manifest.Version) || (expectedTag != "" && manifest.Version != expectedTag) {
		return Manifest{}, fmt.Errorf("runtime manifest version mismatch")
	}
	if manifest.Size <= 0 || manifest.Size > MaxDownloadBytes {
		return Manifest{}, fmt.Errorf("runtime bundle size is outside allowed bounds")
	}
	runtimeArtifact, ok := manifest.Artifacts[AssetName]
	if !ok || runtimeArtifact.SHA256 != manifest.SHA256 || runtimeArtifact.Size != manifest.Size {
		return Manifest{}, fmt.Errorf("runtime artifact is missing from signed release manifest")
	}
	for name, artifact := range manifest.Artifacts {
		if name == "" || artifact.Size <= 0 || len(artifact.SHA256) != 64 {
			return Manifest{}, fmt.Errorf("invalid signed artifact metadata")
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			return Manifest{}, fmt.Errorf("invalid signed artifact digest")
		}
	}
	if len(manifest.SHA256) != 64 {
		return Manifest{}, fmt.Errorf("invalid runtime bundle digest")
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return Manifest{}, fmt.Errorf("invalid runtime bundle digest")
	}
	issuedAt, err := time.Parse(time.RFC3339, manifest.IssuedAt)
	if err != nil || issuedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return Manifest{}, fmt.Errorf("invalid runtime manifest issuance time")
	}
	expiresAt, err := time.Parse(time.RFC3339, manifest.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || time.Now().UTC().After(expiresAt) {
		return Manifest{}, fmt.Errorf("runtime manifest is expired or has invalid expiry")
	}
	if len(manifest.GitCommit) != 40 {
		return Manifest{}, fmt.Errorf("invalid runtime manifest source commit")
	}
	if _, err := hex.DecodeString(manifest.GitCommit); err != nil {
		return Manifest{}, fmt.Errorf("invalid runtime manifest source commit")
	}
	return manifest, nil
}

func (p Policy) VerifyReleaseIndex(indexBytes, signatureBytes []byte) (ReleaseIndex, error) {
	publicKey, err := DecodePublicKey(p.PublicKeyB64)
	if err != nil {
		return ReleaseIndex{}, fmt.Errorf("runtime release trust root is not configured: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureBytes)))
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, indexBytes, signature) {
		return ReleaseIndex{}, fmt.Errorf("release index signature verification failed")
	}
	var index ReleaseIndex
	decoder := json.NewDecoder(bytes.NewReader(indexBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return ReleaseIndex{}, fmt.Errorf("invalid release index: %w", err)
	}
	if index.Schema != "ligandx-release-index/1" || len(index.Releases) == 0 {
		return ReleaseIndex{}, fmt.Errorf("unsupported or empty release index")
	}
	issuedAt, err := time.Parse(time.RFC3339, index.IssuedAt)
	if err != nil || issuedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return ReleaseIndex{}, fmt.Errorf("invalid release index issuance time")
	}
	expiresAt, err := time.Parse(time.RFC3339, index.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || time.Now().UTC().After(expiresAt) {
		return ReleaseIndex{}, fmt.Errorf("release index is expired or has invalid expiry")
	}
	seen := map[string]bool{}
	recommended := 0
	for i := range index.Releases {
		release := &index.Releases[i]
		if !envfile.IsPinnedVersion(release.Version) || seen[release.Version] {
			return ReleaseIndex{}, fmt.Errorf("invalid or duplicate release version")
		}
		seen[release.Version] = true
		if release.Status != "supported" && release.Status != "deprecated" && release.Status != "revoked" {
			return ReleaseIndex{}, fmt.Errorf("invalid status for release %s", release.Version)
		}
		if _, err := p.ApprovedDownloadURL(release.BundleURL); err != nil {
			return ReleaseIndex{}, fmt.Errorf("release %s has invalid bundle URL: %w", release.Version, err)
		}
		if release.DownloadBytes <= 0 || release.DownloadBytes > MaxDownloadBytes {
			return ReleaseIndex{}, fmt.Errorf("release %s has invalid download size", release.Version)
		}
		for _, sourceVersion := range release.RollbackSafeFrom {
			if !envfile.IsPinnedVersion(sourceVersion) {
				return ReleaseIndex{}, fmt.Errorf("release %s has invalid rollback source", release.Version)
			}
		}
		if release.Recommended {
			recommended++
		}
		compatible := release.Status != "revoked"
		message := "Compatible"
		if release.MinimumLauncher != "" {
			comparison, comparable := CompareVersions(p.LauncherVersion, release.MinimumLauncher)
			if !comparable || comparison < 0 {
				compatible = false
				message = "Requires launcher " + release.MinimumLauncher + " or newer"
			}
		}
		if release.Status == "revoked" {
			compatible = false
			message = "This release has been revoked"
		}
		release.Compatible = compatible
		release.Compatibility = message
	}
	// A wrong recommendation count is a presentation defect, not an authenticity
	// one: the signature above already proved the document is ours. Failing here
	// used to hide every release and leave no way to install anything, which is
	// exactly what v2026.08.15-rc.8 did to the version picker by shipping an
	// index with no recommendation and being pinned as "Latest". Clear the
	// ambiguous flags, say so, and still show the list.
	switch {
	case recommended == 0:
		index.Warning = "This release index does not mark a recommended version. " +
			"Choose one explicitly, or check for a newer release."
	case recommended > 1:
		for i := range index.Releases {
			index.Releases[i].Recommended = false
		}
		index.Warning = "This release index marks more than one version as recommended, " +
			"so none is shown as recommended. Choose one explicitly."
	}
	return index, nil
}

func VerifyFile(path string, manifest Manifest) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != manifest.Size {
		return fmt.Errorf("runtime bundle size mismatch")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256) {
		return fmt.Errorf("runtime bundle digest mismatch")
	}
	return nil
}
