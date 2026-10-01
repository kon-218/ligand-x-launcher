package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

//go:embed scripts/preview_bundles.json
var previewBundlesJSON []byte

// enabledPreviewBundles is empty in stable builds. A public build can set
// -X main.enabledPreviewBundles=proto-mutation. Unknown labels add no group.
var enabledPreviewBundles string

type previewBundle struct {
	GroupID         string            `json:"group_id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Services        []string          `json:"services"`
	ImageRepository string            `json:"image_repository"`
	ImageName       string            `json:"image_name"`
	Queue           string            `json:"queue"`
	Edition         string            `json:"edition"`
	RequiresGPU     bool              `json:"requires_gpu"`
	CatalogID       string            `json:"catalog_id"`
	Env             map[string]string `json:"env"`
}

type previewAllowlist struct {
	Schema  string                   `json:"schema"`
	Bundles map[string]previewBundle `json:"bundles"`
}

func loadPreviewAllowlist() (previewAllowlist, error) {
	var document previewAllowlist
	if err := json.Unmarshal(previewBundlesJSON, &document); err != nil {
		return document, err
	}
	if document.Schema != "ligandx-launcher-preview-bundles/1" || len(document.Bundles) == 0 {
		return document, fmt.Errorf("unsupported preview bundle allowlist")
	}
	return document, nil
}

func splitPreviewLabels(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var labels []string
	for _, part := range strings.Split(raw, ",") {
		label := strings.TrimSpace(part)
		if label == "" {
			return nil, fmt.Errorf("unknown preview selection: empty label")
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func validatePreviewBundle(label string, bundle previewBundle) error {
	if label != "proto-mutation" {
		return fmt.Errorf("unknown preview selection: %s", label)
	}
	if bundle.GroupID != "protein-mutation" || bundle.Name != "Protein mutation pilot" {
		return fmt.Errorf("proto-mutation group identity drifted")
	}
	if bundle.Edition != "free" || !bundle.RequiresGPU || bundle.Queue != "proto-gpu" {
		return fmt.Errorf("proto-mutation must stay an optional Free GPU preview on proto-gpu")
	}
	if bundle.CatalogID != "public/worker-proto" || bundle.ImageName != "worker-proto" {
		return fmt.Errorf("proto-mutation image must be public/worker-proto")
	}
	if len(bundle.Services) != 1 || bundle.Services[0] != "worker-proto" {
		return fmt.Errorf("proto-mutation must select only worker-proto")
	}
	if bundle.Env["LIGANDX_ENABLE_PREVIEW_MODULES"] != "1" || bundle.Env["LIGANDX_PROTO_PILOT_ENABLED"] != "1" {
		return fmt.Errorf("proto-mutation must enable both operator flags")
	}
	return nil
}

func previewServiceGroups(raw, version string) ([]ServiceGroup, error) {
	labels, err := splitPreviewLabels(raw)
	if err != nil || len(labels) == 0 {
		return nil, err
	}
	document, err := loadPreviewAllowlist()
	if err != nil {
		return nil, err
	}
	groups := make([]ServiceGroup, 0, len(labels))
	for _, label := range labels {
		bundle, ok := document.Bundles[label]
		if !ok {
			return nil, fmt.Errorf("unknown preview selection: %s", label)
		}
		if err := validatePreviewBundle(label, bundle); err != nil {
			return nil, err
		}
		groups = append(groups, ServiceGroup{
			ID:          bundle.GroupID,
			Name:        bundle.Name,
			Description: bundle.Description,
			Services:    append([]string(nil), bundle.Services...),
			Images: []string{
				imageRef(bundle.ImageRepository+"/"+bundle.ImageName, version),
			},
			Required:  false,
			DefaultOn: false,
			Edition:   bundle.Edition,
			Licensed:  true,
			Locked:    false,
		})
	}
	return groups, nil
}

func previewEnvUpdates(raw string, selectedGroupIDs []string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	groups, err := previewServiceGroups(raw, "unused")
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(selectedGroupIDs))
	for _, id := range selectedGroupIDs {
		selected[id] = true
	}
	document, err := loadPreviewAllowlist()
	if err != nil {
		return nil, err
	}
	updates := map[string]string{
		"LIGANDX_ENABLE_PREVIEW_MODULES": "0",
		"LIGANDX_PROTO_PILOT_ENABLED":    "0",
	}
	for _, group := range groups {
		if !selected[group.ID] {
			continue
		}
		for _, bundle := range document.Bundles {
			if bundle.GroupID != group.ID {
				continue
			}
			for key, value := range bundle.Env {
				updates[key] = value
			}
		}
	}
	return updates, nil
}

func proteinModelCacheAvailable(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("protein mutation model assets are unavailable: set LIGANDX_PROTO_MODEL_CACHE to a prepared model directory. A healthy worker is not model readiness")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("protein mutation model assets are unavailable at %s. A healthy worker is not model readiness", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) == 0 {
		return fmt.Errorf("protein mutation model assets are unavailable at %s. A healthy worker is not model readiness", path)
	}
	return nil
}

func selectedGroup(groupIDs []string, id string) bool {
	for _, groupID := range groupIDs {
		if groupID == id {
			return true
		}
	}
	return false
}
