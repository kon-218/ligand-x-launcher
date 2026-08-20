package main

import "strings"

// proGPUShortGroups lists the service groups that submit work to the gpu-short
// queue whose task implementations ship only in the Pro worker image.
//
// gpu-short is the one queue that mixes editions: md_optimize and workflow_run
// are free, while admet_predict, boltz_predict, boltz_batch and
// rbfe_mapping_preview are Pro. The public worker image
// (ghcr.io/kon-218/ligand-x/worker-gpu-short) runs the free pair and needs no
// registry credentials, so it is the default. Selecting any group below swaps
// in the Pro superset image, which does require credentials.
//
// Keep this in step with lib/tasks/celery_app.py's task_routes in the core
// repo: a new Pro task routed to gpu-short means the group that submits it
// belongs here. tests/test_gpu_short_queue_editions.py fails the core build if
// that mapping drifts.
var proGPUShortGroups = map[string]bool{
	"admet":       true, // ligandx_tasks.admet_predict
	"boltz2":      true, // ligandx_tasks.boltz_predict, boltz_batch
	"free-energy": true, // ligandx_tasks.rbfe_mapping_preview
}

// gpuShortImageOverride returns the value LIGANDX_GPU_SHORT_IMAGE must hold for
// this selection, or "" when the public default in docker-compose.yml applies.
//
// The result is a complete image reference rather than a registry prefix on
// purpose: VERSION and PRO_VERSION are pinned independently, so anything that
// substitutes only the registry can pair a Pro tag with the public repository
// and fail with "manifest unknown".
func gpuShortImageOverride(groupIDs []string, proPrefix, proVersion string) string {
	for _, id := range groupIDs {
		if proGPUShortGroups[id] {
			return imageRef(proPrefix+"/worker-gpu-short", proVersion)
		}
	}
	return ""
}

// productionProVersion resolves the tag compose would use for a Pro image,
// mirroring ${PRO_VERSION:-${VERSION:-latest}} in docker-compose.yml.
//
// productionImageSettings deliberately does not do this: it reports VERSION for
// the public images. Pro images carry their own pin, and the two are set to
// different values in the shipped template.
func (a *App) productionProVersion() string {
	content, err := a.GetEnvContent("prod")
	if err != nil {
		return "latest"
	}
	parsed := parseEnvFile(content)
	if proVersion := strings.TrimSpace(parsed["PRO_VERSION"]); proVersion != "" {
		return proVersion
	}
	if version := strings.TrimSpace(parsed["VERSION"]); version != "" {
		return version
	}
	return "latest"
}
