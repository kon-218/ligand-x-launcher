package launcher

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FEAT-27: preview modules come from the installed runtime, not from this
// binary. The runtime bundle carries Core's generated module manifest
// (modules.json) next to docker-compose.yml, and every preview it lists as
// ships_dormant has its services in the bundle under its own Compose profile.
// A stable launcher can therefore offer a new preview without a new release.

const moduleManifestSchema = "ligandx-module-manifest/1"

// previewModulesEnv is the one switch the gateway reads to turn previews on.
const previewModulesEnv = "LIGANDX_PREVIEW_MODULES"

// previewGroupPrefix keeps preview group IDs apart from the compiled-in groups.
const previewGroupPrefix = "preview-"

type modulePreview struct {
	Status         string   `json:"status"`
	KnownLimits    []string `json:"known_limits"`
	ComposeProfile string   `json:"compose_profile"`
	Services       []string `json:"services"`
	Images         []string `json:"images"`
	RequiresGPU    bool     `json:"requires_gpu"`
	ShipsDormant   bool     `json:"ships_dormant"`
}

type moduleRow struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Edition       string         `json:"edition"`
	EntitlementID string         `json:"entitlement_id"`
	Maturity      string         `json:"maturity"`
	Preview       *modulePreview `json:"preview"`
}

type moduleManifest struct {
	Schema  string      `json:"schema"`
	Modules []moduleRow `json:"modules"`
}

// loadRuntimeModuleManifest reads modules.json from an installed runtime. An
// older runtime without one has no previews to offer, which is not an error.
func loadRuntimeModuleManifest(root string) (moduleManifest, error) {
	var manifest moduleManifest
	data, err := os.ReadFile(filepath.Join(root, "modules.json"))
	if errors.Is(err, os.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Schema != moduleManifestSchema {
		return moduleManifest{}, errors.New("unsupported module manifest schema")
	}
	return manifest, nil
}

// dormantPreviews are the previews this runtime can turn on.
func (m moduleManifest) dormantPreviews() []moduleRow {
	var rows []moduleRow
	for _, row := range m.Modules {
		if row.Maturity == "preview" && row.Preview != nil && row.Preview.ShipsDormant && len(row.Preview.Services) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// previewImageRef maps a manifest image ("ligand-x-pro/bioemu") to the
// reference this install pulls, using the same prefix and tag as Compose.
func previewImageRef(image, version, proVersion, proPrefix string) string {
	if name, ok := strings.CutPrefix(image, "ligand-x-pro/"); ok {
		return imageRef(proPrefix+"/"+name, proVersion)
	}
	return imageRef("ghcr.io/kon-218/"+image, version)
}

// runtimePreviewGroups builds one optional, off-by-default service group per
// dormant preview. A Pro preview is locked until the licence names its
// entitlement; one that needs a GPU is locked on a host without one.
func runtimePreviewGroups(manifest moduleManifest, version, proVersion, proPrefix string,
	hasEntitlement func(string) bool, gpuUsable bool) []ServiceGroup {
	var groups []ServiceGroup
	for _, row := range manifest.dormantPreviews() {
		images := make([]string, 0, len(row.Preview.Images))
		for _, image := range row.Preview.Images {
			images = append(images, previewImageRef(image, version, proVersion, proPrefix))
		}
		group := ServiceGroup{
			ID:          previewGroupPrefix + row.ID,
			Name:        row.Name,
			Description: row.Preview.Status,
			Services:    append([]string(nil), row.Preview.Services...),
			Images:      images,
			Edition:     row.Edition,
			Entitlement: row.EntitlementID,
			Licensed:    true,
			Maturity:    "preview",
			ModuleID:    row.ID,
			KnownLimits: append([]string(nil), row.Preview.KnownLimits...),
			RequiresGPU: row.Preview.RequiresGPU,
		}
		if group.Edition == "pro" {
			group.RegistryAuthImages = append([]string(nil), images...)
			group.Licensed = hasEntitlement(row.EntitlementID)
		}
		switch {
		case !group.Licensed:
			group.Locked, group.LockedReason = true, "no_entitlement"
		case group.RequiresGPU && !gpuUsable:
			group.Locked, group.LockedReason = true, "no_gpu"
		}
		groups = append(groups, group)
	}
	return groups
}

// selectedPreviewModules is the value of LIGANDX_PREVIEW_MODULES for this
// selection: every selected, unlocked preview group, sorted.
func selectedPreviewModules(groups []ServiceGroup, selectedGroupIDs []string) string {
	selected := make(map[string]bool, len(selectedGroupIDs))
	for _, id := range selectedGroupIDs {
		selected[id] = true
	}
	var modules []string
	for _, group := range groups {
		if group.ModuleID != "" && !group.Locked && selected[group.ID] {
			modules = append(modules, group.ModuleID)
		}
	}
	sort.Strings(modules)
	return strings.Join(sortedUnique(modules), ",")
}
