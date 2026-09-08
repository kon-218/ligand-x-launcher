package main

import "fmt"

func agentSetupGPUShortWarning(selectedGroups []string) string {
	if gpuShortImageOverride(selectedGroups, "ghcr.io/example/ligand-x-pro", "v0.0.0") != "" {
		return "Pro gpu-short jobs (ADMET, Boltz-2, RBFE mapping preview) can run with the selected service groups."
	}
	return "WARNING: ADMET, Boltz-2, and RBFE mapping preview templates require the admet, boltz2, or free-energy service groups. Without them, jobs enqueue on the public gpu-short worker and fail at dispatch."
}

func buildAgentSetupInstructions(mcpConfigJSON, expiresAt string, selectedGroups []string, executionEnabled bool) string {
	gpuShortNote := agentSetupGPUShortWarning(selectedGroups)
	authority := "This is a planning-only connection: do not submit, cancel, approve, or execute calculations."
	if executionEnabled {
		authority = "This connection may execute calculations only after their exact plan hash is approved in the Ligand-X application; it cannot approve its own plans."
	}
	return fmt.Sprintf(`Install this MCP configuration in your coding assistant:

%s

Client notes:
- Claude Code / Claude Desktop: merge the mcpServers block into your Claude MCP settings.
- Codex: merge the same block into the Codex MCP servers configuration.
- Cursor: merge into .cursor/mcp.json (or Cursor Settings → MCP). Prefer a user-level
  config; do not commit workspace credentials into a shared repository.

Workflow:
1. Call ligandx_capabilities first. Use only curated templates from ligandx_list_templates.
2. Discover or create a project with ligandx_projects / ligandx_create_project.
3. Validate inputs with ligandx_validate_template, then create the same immutable plan the browser uses with ligandx_create_plan_from_template. Do not invent workflow graphs or generic job parameters.
4. Review and approve the exact plan hash in Ligand-X; the assistant cannot approve it.
5. If this connection has execution authority, call ligandx_execute_job_plan after Ligand-X reports that exact template-backed plan as approved, then poll with ligandx_get_job using next_poll_seconds from each response.
6. Continue docking poses into MD only with the docking-pose-to-md template and the opaque pose handle from a completed docking result. Raw structures, paths, archives, logs, and coordinates are browser-only.
7. Preview modules (QMMM, kinetics) are not available through MCP.

Security warning (lethal trifecta): do not combine this Ligand-X MCP server in the same
assistant session with other MCP servers that can read private files or browse the open
web while scientific workspace content and optional remote egress are in play. Prefer a
dedicated assistant session that only connects to Ligand-X. PDB/template fetch may send
identifiers off-host when remote egress is enabled in Ligand-X.

Pro jobs require a valid licence. %s

%s
This workspace access expires at %s.`, mcpConfigJSON, gpuShortNote, authority, expiresAt)
}
