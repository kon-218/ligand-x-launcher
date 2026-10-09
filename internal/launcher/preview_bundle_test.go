package launcher

import (
	"os"
	"strings"
	"testing"
)

func TestStableGroupsExcludeProteinPilot(t *testing.T) {
	previous := enabledPreviewBundles
	enabledPreviewBundles = ""
	t.Cleanup(func() { enabledPreviewBundles = previous })

	groups := NewApp().GetServiceGroups()
	if len(groups) != 8 {
		t.Fatalf("stable launcher has %d groups, want 8", len(groups))
	}
	for _, group := range groups {
		if group.ID == "protein-mutation" || strings.Contains(group.Name, "Protein mutation") {
			t.Fatalf("stable group list includes the pilot: %+v", group)
		}
	}
}

func TestProtoMutationBundleAddsFreeWorker(t *testing.T) {
	previous := enabledPreviewBundles
	enabledPreviewBundles = "proto-mutation"
	t.Cleanup(func() { enabledPreviewBundles = previous })

	groups := NewApp().GetServiceGroups()
	if len(groups) != 9 {
		t.Fatalf("preview launcher has %d groups, want 9", len(groups))
	}
	var pilot *ServiceGroup
	for i := range groups {
		if groups[i].ID == "protein-mutation" {
			pilot = &groups[i]
		}
	}
	if pilot == nil {
		t.Fatal("protein-mutation group missing")
	}
	if pilot.Name != "Protein mutation pilot" || pilot.Edition != "free" || pilot.DefaultOn || pilot.Required || pilot.Locked {
		t.Fatalf("pilot group shape = %+v", *pilot)
	}
	if len(pilot.Services) != 1 || pilot.Services[0] != "worker-proto" {
		t.Fatalf("services = %v", pilot.Services)
	}
	if len(pilot.Images) != 1 || !strings.Contains(pilot.Images[0], "ghcr.io/kon-218/ligand-x/worker-proto:") {
		t.Fatalf("image = %v, want public worker-proto", pilot.Images)
	}
	if !strings.Contains(pilot.Description, "proto-gpu") || !strings.Contains(pilot.Description, "not model readiness") {
		t.Fatalf("description = %q", pilot.Description)
	}
	if !gpuRequiredRuntime["worker-proto"] {
		t.Fatal("worker-proto must be hardware-gated")
	}
}

func TestUnknownPreviewBundleFailsClosed(t *testing.T) {
	groups, err := previewServiceGroups("not-a-bundle", "v1.0.0")
	if err == nil || !strings.Contains(err.Error(), "unknown preview selection") {
		t.Fatalf("err = %v, groups = %v", err, groups)
	}
	previous := enabledPreviewBundles
	enabledPreviewBundles = "not-a-bundle"
	t.Cleanup(func() { enabledPreviewBundles = previous })
	stable := NewApp().GetServiceGroups()
	if len(stable) != 8 {
		t.Fatalf("unknown label changed the stable list to %d groups", len(stable))
	}
}

func TestPreviewEnvFollowsGroupSelection(t *testing.T) {
	off, err := previewEnvUpdates("proto-mutation", nil)
	if err != nil {
		t.Fatal(err)
	}
	if off["LIGANDX_ENABLE_PREVIEW_MODULES"] != "0" || off["LIGANDX_PROTO_PILOT_ENABLED"] != "0" {
		t.Fatalf("unselected pilot env = %v", off)
	}
	on, err := previewEnvUpdates("proto-mutation", []string{"protein-mutation"})
	if err != nil {
		t.Fatal(err)
	}
	if on["LIGANDX_ENABLE_PREVIEW_MODULES"] != "1" || on["LIGANDX_PROTO_PILOT_ENABLED"] != "1" {
		t.Fatalf("selected pilot env = %v", on)
	}
	if on["PROTO_HOME"] != "/opt/proto" || on["PROTO_MODEL_CACHE"] != "/models/proto" {
		t.Fatalf("model controls = %v", on)
	}
	stable, err := previewEnvUpdates("", []string{"protein-mutation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stable) != 0 {
		t.Fatalf("stable build must not write pilot flags, got %v", stable)
	}
}

func TestProteinPilotAssetsMissing(t *testing.T) {
	if err := proteinModelCacheAvailable(""); err == nil || !strings.Contains(err.Error(), "not model readiness") {
		t.Fatalf("empty cache err = %v", err)
	}
	if err := proteinModelCacheAvailable(t.TempDir()); err == nil {
		t.Fatal("empty directory was treated as prepared assets")
	}
	cache := t.TempDir()
	if err := os.WriteFile(cache+"/weights.bin", []byte("pinned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proteinModelCacheAvailable(cache); err == nil {
		t.Fatal("arbitrary weights file was treated as approved prepared assets")
	}
}

func TestPublicFrontendContainsProteinPilotOptIn(t *testing.T) {
	data, err := os.ReadFile("../../frontend-public/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, needle := range []string{
		"Protein mutation pilot",
		"LIGANDX_ENABLE_PREVIEW_MODULES",
		"LIGANDX_PROTO_PILOT_ENABLED",
		"not model readiness",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("public frontend missing %q", needle)
		}
	}
}
