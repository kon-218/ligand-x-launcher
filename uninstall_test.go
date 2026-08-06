package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
)

func composeContainer(id, project, service string) container.Summary {
	return container.Summary{ID: id, Labels: map[string]string{
		"com.docker.compose.project": project,
		"com.docker.compose.service": service,
	}}
}

// Uninstall must never be confusable with "delete anything containing 'ligand'".
// A project is ours only once a service we actually ship is found inside it.
func TestLigandxComposeProjectsRequiresAKnownService(t *testing.T) {
	containers := []container.Summary{
		composeContainer("a", "ligand-x", "gateway"),
		composeContainer("b", "my-ligand-notes", "web"), // name matches, service does not
		composeContainer("c", "unrelated", "postgres"),
		{ID: "d", Labels: map[string]string{}}, // no compose labels at all
	}
	got := ligandxComposeProjects(containers)
	if !got["ligand-x"] {
		t.Error("our own project was not identified")
	}
	if got["my-ligand-notes"] {
		t.Error(`a user project named "my-ligand-notes" was claimed as ours`)
	}
	if got["unrelated"] {
		t.Error("an unrelated project was claimed as ours")
	}
}

// Once a project is confirmed, every container in it goes — including services
// we do not enumerate, like celery-beat.
func TestSelectProjectContainersTakesUnknownServicesInOurProject(t *testing.T) {
	containers := []container.Summary{
		composeContainer("a", "ligand-x", "gateway"),
		composeContainer("b", "ligand-x", "celery-beat"),
		composeContainer("c", "unrelated", "postgres"),
	}
	projects := ligandxComposeProjects(containers)
	got := selectProjectContainers(containers, projects)
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

func TestSelectLigandxVolumesUsesLabelsAndRefusesLooseNameMatches(t *testing.T) {
	projects := map[string]bool{"ligand-x": true}
	volumes := []volume.Volume{
		{Name: "ligand-x_postgres_data", Labels: map[string]string{"com.docker.compose.project": "ligand-x"}},
		{Name: "ligand-x_docking_outputs"}, // unlabelled, prefix of a confirmed project
		{Name: "my-ligand-data"},           // unlabelled, merely contains "ligand"
		{Name: "other_postgres_data", Labels: map[string]string{"com.docker.compose.project": "other"}}, // labelled, not ours
		{Name: "someones_backup"},
	}
	got := selectLigandxVolumes(volumes, projects)
	want := []string{"ligand-x_docking_outputs", "ligand-x_postgres_data"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

func TestSelectLigandxNetworksSkipsForeignProjects(t *testing.T) {
	projects := map[string]bool{"ligand-x": true}
	networks := []network.Summary{
		{Network: network.Network{ID: "n1", Name: "ligand-x_default",
			Labels: map[string]string{"com.docker.compose.project": "ligand-x"}}},
		{Network: network.Network{ID: "n2", Name: "other_default",
			Labels: map[string]string{"com.docker.compose.project": "other"}}},
		{Network: network.Network{ID: "n3", Name: "bridge"}},
	}
	got := selectLigandxNetworks(networks, projects)
	if want := []string{"n1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

// Images carry no compose project, so repository prefix is the only safe signal.
// A substring match on "ligand" would delete a user's own image.
func TestSelectLigandxImagesMatchesRepositoryPrefixOnly(t *testing.T) {
	prefixes := ligandxImageRepoPrefixes("ghcr.io/kon-218/ligand-x-pro")
	images := []image.Summary{
		{ID: "sha-1", RepoTags: []string{"ghcr.io/kon-218/ligand-x/gateway:v2026.08.05"}},
		{ID: "sha-2", RepoTags: []string{"ghcr.io/kon-218/ligand-x-pro/qc:v2026.06.21"}},
		{ID: "sha-3", RepoTags: []string{"postgres:16-alpine"}},
		{ID: "sha-4", RepoTags: []string{"mycorp/ligand-x-fork:dev"}}, // contains our name, not our repo
		{ID: "sha-5", RepoTags: []string{"<none>:<none>"}},
	}
	got := selectLigandxImages(images, prefixes)
	if want := []string{"sha-1", "sha-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

func TestSelectLigandxImagesDeduplicatesMultiTaggedImage(t *testing.T) {
	images := []image.Summary{{ID: "sha-1", RepoTags: []string{
		"ghcr.io/kon-218/ligand-x/gateway:v2026.08.05",
		"ghcr.io/kon-218/ligand-x/gateway:latest",
	}}}
	got := selectLigandxImages(images, ligandxImageRepoPrefixes(""))
	if want := []string{"sha-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

// A custom LIGANDX_PRO_IMAGE_PREFIX must be swept too, or a self-hosted Pro
// registry's images survive the uninstall.
func TestLigandxImageRepoPrefixesIncludesCustomProPrefix(t *testing.T) {
	got := ligandxImageRepoPrefixes("registry.internal/ligandx-pro")
	found := false
	for _, p := range got {
		if p == "registry.internal/ligandx-pro/" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom Pro prefix missing from %v", got)
	}
}

// The guard that keeps a misconfigured LIGANDX_RUNTIME_DIR from turning
// uninstall into rm -rf on something important.
func TestSafeToRemoveTreeRejectsDangerousPaths(t *testing.T) {
	home, err := filepathAbsHome()
	if err != nil {
		t.Skip("no home directory")
	}
	refuse := []string{"", "relative/path", string(filepath.Separator), home}
	for _, p := range refuse {
		if safeToRemoveTree(p) {
			t.Errorf("safeToRemoveTree(%q) = true, want false", p)
		}
	}
	allow := filepath.Join(home, ".config", "ligandx-launcher")
	if !safeToRemoveTree(allow) {
		t.Errorf("safeToRemoveTree(%q) = false, want true", allow)
	}
}

// Wails binds every exported method on App, so Uninstall must be inert without
// the confirmation phrase — no Docker call, no file removed.
func TestUninstallRefusesWithoutConfirmation(t *testing.T) {
	app := NewApp()
	app.projectPath = t.TempDir()
	// Never exercise an accepted phrase here: a successful Uninstall would
	// delete the real ~/.config/ligandx-launcher of whoever runs the tests.
	for _, phrase := range []string{"", "uninstall", "yes", "Uninstall", "UNINSTAL"} {
		report, err := app.Uninstall(UninstallOptions{Confirm: phrase})
		if err == nil {
			t.Errorf("Uninstall(%q) was allowed to proceed", phrase)
		}
		if report.Complete || len(report.Steps) > 0 {
			t.Errorf("Uninstall(%q) did work before refusing: %+v", phrase, report)
		}
	}
}

func filepathAbsHome() (string, error) {
	return os.UserHomeDir()
}
