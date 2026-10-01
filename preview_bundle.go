package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// verifySelectedRuntimePreview is called on verified, extracted bundle bytes
// before activation and again before starting the opt-in service group.
func verifySelectedRuntimePreview(root, selection string) error {
	labels, err := splitPreviewLabels(selection)
	if err != nil {
		return err
	}
	if _, err := previewServiceGroups(selection, "unused"); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		return err
	}
	header := "# Preview bundles: " + strings.Join(labels, ",") + "\n"
	if len(labels) == 0 {
		if strings.HasPrefix(string(data), "# Preview bundles:") {
			return fmt.Errorf("preview runtime requires an explicitly selected preview launcher")
		}
		return nil
	}
	if !strings.HasPrefix(string(data), header) || !strings.Contains(string(data), "\n  worker-proto:\n") {
		return fmt.Errorf("protein mutation preview runtime is not installed: install the signed proto-mutation bundle using LIGANDX_RUNTIME_BUNDLE_URL")
	}
	return nil
}

func proteinModelCacheAvailable(path string) error {
	return preparedProteinRuntimeAvailable(map[string]string{"PROTO_MODEL_CACHE_HOST": path})
}

// This host check verifies the reviewed inventory and asset bytes. Worker CUDA,
// task registration and freshly observed environment identity remain worker
// readiness checks; a launcher check never declares scientific qualification.
func preparedProteinRuntimeAvailable(values map[string]string) error {
	unavailable := func(reason string) error {
		return fmt.Errorf("protein mutation prepared assets unavailable: %s. A healthy worker is not model readiness", reason)
	}
	manifestPath := strings.TrimSpace(values["PROTO_RUNTIME_MANIFEST_HOST"])
	digestPath := strings.TrimSpace(values["PROTO_RUNTIME_MANIFEST_SHA256_HOST"])
	home := strings.TrimSpace(values["PROTO_HOME_HOST"])
	cache := strings.TrimSpace(values["PROTO_MODEL_CACHE_HOST"])
	if manifestPath == "" || digestPath == "" || home == "" || cache == "" {
		return unavailable("configure PROTO_HOME_HOST, PROTO_MODEL_CACHE_HOST and approved manifest paths")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return unavailable("manifest missing")
	}
	expected, err := os.ReadFile(digestPath)
	actual := sha256.Sum256(data)
	fields := strings.Fields(string(expected))
	if err != nil || len(fields) == 0 || fields[0] != hex.EncodeToString(actual[:]) {
		return unavailable("manifest digest differs from the approved inventory")
	}
	type asset struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	type model struct {
		Files           []asset           `json:"files"`
		Interpreter     string            `json:"interpreter"`
		ToolInterpreter string            `json:"tool_interpreter"`
		Identity        map[string]string `json:"identity"`
	}
	var document struct {
		Schema  string           `json:"schema"`
		Fixture bool             `json:"fixture"`
		ESM2    map[string]model `json:"esm2"`
		FAMPNN  map[string]model `json:"fampnn"`
	}
	if err := json.Unmarshal(data, &document); err != nil || document.Schema != "protein_tools_assets/v1" || document.Fixture {
		return unavailable("invalid production manifest")
	}
	models := []model{}
	for _, checkpoint := range []string{"esm2_t6_8M_UR50D", "esm2_t33_650M_UR50D"} {
		row, ok := document.ESM2[checkpoint]
		if !ok {
			return unavailable("checkpoint inventory missing: " + checkpoint)
		}
		models = append(models, row)
	}
	row, ok := document.FAMPNN["0.3_cath"]
	if !ok {
		return unavailable("FAMPNN inventory missing")
	}
	models = append(models, row)
	containerHome := strings.TrimSpace(values["PROTO_HOME"])
	if containerHome == "" {
		containerHome = "/opt/proto"
	}
	containerCache := strings.TrimSpace(values["PROTO_MODEL_CACHE"])
	if containerCache == "" {
		containerCache = "/models/proto"
	}
	if !filepath.IsAbs(containerHome) || !filepath.IsAbs(containerCache) {
		return unavailable("container runtime roots must be absolute")
	}
	mapPath := func(path string) (string, error) {
		for _, pair := range [][2]string{{containerHome, home}, {containerCache, cache}} {
			relative, err := filepath.Rel(pair[0], filepath.Clean(path))
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
				return filepath.Join(pair[1], relative), nil
			}
		}
		return "", unavailable("manifest contains an asset outside controlled runtime roots")
	}
	verify := func(path, digest string) error {
		hostPath, err := mapPath(path)
		if err != nil {
			return err
		}
		file, err := os.Open(hostPath)
		if err != nil {
			return unavailable("missing asset: " + path)
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return unavailable("unreadable asset: " + path)
		}
		if len(digest) != 64 || hex.EncodeToString(hash.Sum(nil)) != digest {
			return unavailable("asset hash mismatch: " + path)
		}
		return nil
	}
	for _, row := range models {
		if len(row.Files) == 0 || row.Identity["proto_commit"] == "" {
			return unavailable("incomplete prepared inventory")
		}
		if err := verify(row.Interpreter, row.Identity["driver_interpreter_sha256"]); err != nil {
			return err
		}
		if err := verify(row.ToolInterpreter, row.Identity["interpreter_sha256"]); err != nil {
			return err
		}
		for _, file := range row.Files {
			if err := verify(file.Path, file.SHA256); err != nil {
				return err
			}
		}
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
