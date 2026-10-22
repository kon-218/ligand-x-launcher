package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

const bioemuManifest = `{
  "schema": "ligandx-module-manifest/1",
  "modules": [
    {"id": "docking", "name": "Molecular Docking", "edition": "free", "entitlement_id": "docking",
     "maturity": "stable", "services": ["docking"]},
    {"id": "bioemu", "name": "BioEmu Ensembles", "edition": "pro", "entitlement_id": "bioemu",
     "maturity": "preview", "preview": {
       "status": "Samples protein-monomer ensembles; screening only.",
       "known_limits": ["Monomers only"], "compose_profile": "preview-bioemu",
       "services": ["bioemu-outputs-init", "bioemu", "worker-bioemu"],
       "images": ["ligand-x-pro/bioemu", "ligand-x-pro/worker-bioemu"],
       "requires_gpu": true, "ships_dormant": true}},
    {"id": "protein_tools", "name": "Protein mutation pilot", "edition": "free", "entitlement_id": "protein_tools",
     "maturity": "preview", "preview": {
       "status": "Scores substitutions.", "known_limits": [], "compose_profile": "preview-protein-tools",
       "services": ["worker-proto"], "images": ["ligand-x/worker-proto"],
       "requires_gpu": true, "ships_dormant": false}}
  ]
}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "modules.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestOlderRuntimeWithoutManifestOffersNoPreviews(t *testing.T) {
	manifest, err := loadRuntimeModuleManifest(t.TempDir())
	if err != nil || len(manifest.dormantPreviews()) != 0 {
		t.Fatalf("manifest = %+v, err = %v", manifest, err)
	}
}

func TestUnknownManifestSchemaIsRefused(t *testing.T) {
	if _, err := loadRuntimeModuleManifest(writeManifest(t, `{"schema": "other/1"}`)); err == nil {
		t.Fatal("an unknown schema must not be read as an empty preview list")
	}
}

func TestOnlyDormantPreviewsBecomeGroups(t *testing.T) {
	manifest, err := loadRuntimeModuleManifest(writeManifest(t, bioemuManifest))
	if err != nil {
		t.Fatal(err)
	}
	groups := runtimePreviewGroups(manifest, "v1.0.0", "v1.0.1", "ghcr.io/kon-218/ligand-x-pro",
		func(string) bool { return true }, true)
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	group := groups[0]
	if group.ID != "preview-bioemu" || group.ModuleID != "bioemu" || group.Maturity != "preview" {
		t.Fatalf("group identity = %+v", group)
	}
	if group.Locked || group.DefaultOn || group.Required {
		t.Fatalf("a licensed preview on a GPU host must be optional and unlocked: %+v", group)
	}
	if group.Images[0] != "ghcr.io/kon-218/ligand-x-pro/bioemu:v1.0.1" {
		t.Fatalf("Pro preview images follow PRO_VERSION: %v", group.Images)
	}
	if len(group.Services) != 3 || group.KnownLimits[0] != "Monomers only" {
		t.Fatalf("services/limits = %v / %v", group.Services, group.KnownLimits)
	}
}

func TestPreviewGroupsLockForLicenceThenGPU(t *testing.T) {
	manifest, _ := loadRuntimeModuleManifest(writeManifest(t, bioemuManifest))
	unlicensed := runtimePreviewGroups(manifest, "v1", "v1", "p", func(string) bool { return false }, true)[0]
	if !unlicensed.Locked || unlicensed.Licensed || unlicensed.LockedReason != "no_entitlement" {
		t.Fatalf("unlicensed = %+v", unlicensed)
	}
	noGPU := runtimePreviewGroups(manifest, "v1", "v1", "p", func(string) bool { return true }, false)[0]
	if !noGPU.Locked || !noGPU.Licensed || noGPU.LockedReason != "no_gpu" {
		t.Fatalf("no GPU = %+v", noGPU)
	}
}

func TestSelectedPreviewModulesSkipsLockedAndUnselectedGroups(t *testing.T) {
	groups := []ServiceGroup{
		{ID: "core"},
		{ID: "preview-bioemu", ModuleID: "bioemu"},
		{ID: "protein-mutation", ModuleID: "protein_tools"},
		{ID: "preview-other", ModuleID: "other", Locked: true},
	}
	got := selectedPreviewModules(groups, []string{"core", "preview-other", "protein-mutation", "preview-bioemu"})
	if got != "bioemu,protein_tools" {
		t.Fatalf("LIGANDX_PREVIEW_MODULES = %q", got)
	}
	if selectedPreviewModules(groups, nil) != "" {
		t.Fatal("nothing selected must turn every preview off")
	}
}

func TestServiceGroupsIncludeTheInstalledRuntimesPreviews(t *testing.T) {
	t.Setenv("LIGANDX_LAUNCHER_CONFIG_DIR", t.TempDir())
	app := NewApp()
	app.projectPath = writeManifest(t, bioemuManifest)
	var found *ServiceGroup
	groups := app.GetServiceGroups()
	for i := range groups {
		if groups[i].ID == "preview-bioemu" {
			found = &groups[i]
		}
	}
	if found == nil {
		t.Fatalf("preview-bioemu missing from %d groups", len(groups))
	}
	if !found.Locked {
		t.Fatalf("without a licence naming bioemu the preview must stay locked: %+v", *found)
	}
}
