package main

import (
	"os"
	"testing"
)

// TestSignedManifestFromReleaseScriptVerifies proves the artifacts produced by
// ligand-x/scripts/sign_runtime_release.py are accepted by the launcher's own
// verifier. Without this the signer and the verifier can drift silently and the
// failure only shows up as "no user can install".
func TestSignedManifestFromReleaseScriptVerifies(t *testing.T) {
	dir := os.Getenv("LIGANDX_TEST_SIGNED_DIR")
	pub := os.Getenv("LIGANDX_TEST_PUBKEY")
	if dir == "" || pub == "" {
		t.Skip("set LIGANDX_TEST_SIGNED_DIR and LIGANDX_TEST_PUBKEY")
	}
	manifestBytes, err := os.ReadFile(dir + "/ligand-x-runtime-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	sigBytes, err := os.ReadFile(dir + "/ligand-x-runtime-manifest.sig")
	if err != nil {
		t.Fatal(err)
	}
	runtimeBundlePublicKeyB64 = pub

	manifest, err := verifyRuntimeBundleManifest(manifestBytes, sigBytes, "v2026.08.05")
	if err != nil {
		t.Fatalf("launcher rejected the signed manifest: %v", err)
	}
	if err := verifyRuntimeBundleFile(dir+"/ligand-x-runtime.zip", manifest); err != nil {
		t.Fatalf("launcher rejected the bundle against its manifest: %v", err)
	}

	// A tampered bundle must be caught.
	manifest.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := verifyRuntimeBundleFile(dir+"/ligand-x-runtime.zip", manifest); err == nil {
		t.Error("digest mismatch was not detected")
	}
	// A tampered manifest must fail the signature.
	manifestBytes[len(manifestBytes)-3] ^= 0xff
	if _, err := verifyRuntimeBundleManifest(manifestBytes, sigBytes, "v2026.08.05"); err == nil {
		t.Error("modified manifest passed signature verification")
	}
}
