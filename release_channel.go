package main

import (
	"fmt"
	"strconv"
	"strings"
)

// isPrereleaseVersion reports whether a release version names a pre-release,
// e.g. v2026.08.15-rc.9.
//
// The version string is the signal rather than GitHub's `prerelease` flag,
// deliberately. That flag has already proved unreliable here: publishing
// v2026.08.15-rc.8 as "Latest" silently un-marked it as a pre-release, because
// GitHub does not allow the latest release to be one. The version inside the
// signed index cannot drift that way.
func isPrereleaseVersion(version string) bool {
	return strings.Contains(strings.TrimSpace(version), "-")
}

// filterReleasesForChannel drops pre-releases unless the user has opted into
// them. The currently installed version is always kept, so a user already
// running a pre-release can still see, re-select and roll back from it after
// turning the toggle off.
func filterReleasesForChannel(releases []RuntimeRelease, includePrereleases bool, installed string) []RuntimeRelease {
	if includePrereleases {
		return releases
	}
	installed = strings.TrimSpace(installed)
	filtered := make([]RuntimeRelease, 0, len(releases))
	for _, release := range releases {
		if !isPrereleaseVersion(release.Version) || release.Version == installed {
			filtered = append(filtered, release)
		}
	}
	return filtered
}

// githubAsset and githubRelease mirror the fields this launcher reads from the
// GitHub releases list endpoint.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Prerelease bool          `json:"prerelease"`
	Draft      bool          `json:"draft"`
	Assets     []githubAsset `json:"assets"`
}

// selectReleaseAssets picks the newest release carrying every wanted asset,
// walking the listing in the order GitHub returns it (newest first).
//
// This replaces the /releases/latest shortcut, which cannot see pre-releases at
// all and follows a pointer that has proved wrong in practice: v2026.08.15-rc.8
// was published as "Latest", which both hid the newer rc.9 and, because GitHub
// forbids a latest pre-release, quietly cleared rc.8's own pre-release flag.
//
// A release missing one of the assets is skipped rather than fatal, so releases
// published before an asset existed do not block the search.
func selectReleaseAssets(releases []githubRelease, includePrereleases bool, wanted []string) (map[string]string, string, error) {
	for _, release := range releases {
		if release.Draft {
			continue
		}
		// Both signals must agree before a release is treated as stable: the tag
		// is authoritative when GitHub's flag has been cleared by promotion.
		if !includePrereleases && (release.Prerelease || isPrereleaseVersion(releaseVersionFromTag(release.TagName))) {
			continue
		}

		found := make(map[string]string, len(wanted))
		for _, asset := range release.Assets {
			for _, name := range wanted {
				if asset.Name == name && strings.TrimSpace(asset.BrowserDownloadURL) != "" {
					found[name] = asset.BrowserDownloadURL
				}
			}
		}
		if len(found) == len(wanted) {
			return found, strings.TrimSpace(release.TagName), nil
		}
	}

	channel := "stable"
	if includePrereleases {
		channel = "stable or pre-release"
	}
	return nil, "", fmt.Errorf("no %s release provides %s", channel, strings.Join(wanted, ", "))
}

// releaseVersionFromTag strips the launcher's tag prefix, e.g.
// launcher-v2026.08.15-rc.9 -> v2026.08.15-rc.9.
func releaseVersionFromTag(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "launcher-")
}

// includePrereleases reports whether this install has opted into release
// candidates. Unreadable config means stable, the safe default.
func (a *App) includePrereleases() bool {
	config, err := a.GetLauncherConfig()
	if err != nil {
		return false
	}
	return config.ShowPrereleases
}

// GetShowPrereleases reports the current release channel for the UI toggle.
func (a *App) GetShowPrereleases() bool {
	return a.includePrereleases()
}

// SetShowPrereleases switches the release channel and persists it.
func (a *App) SetShowPrereleases(enabled bool) error {
	config, err := a.GetLauncherConfig()
	if err != nil {
		config = LauncherConfig{ConfigVersion: 1}
	}
	config.ShowPrereleases = enabled
	return a.SaveLauncherConfig(config)
}

// prereleaseOrdinal returns the trailing number of a pre-release suffix, e.g.
// v2026.08.15-rc.10 -> 10. Zero when there is no numeric part.
func prereleaseOrdinal(version string) int {
	_, suffix, found := strings.Cut(strings.TrimSpace(version), "-")
	if !found {
		return 0
	}
	digits := ""
	for i := len(suffix) - 1; i >= 0; i-- {
		if suffix[i] < '0' || suffix[i] > '9' {
			break
		}
		digits = string(suffix[i]) + digits
	}
	ordinal, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return ordinal
}

// compareReleasesNewestFirst orders releases for the version picker, newest at
// the top.
//
// numericReleaseVersion deliberately drops the pre-release suffix, so every rc
// of a version compares equal to that version's stable release and to every
// other rc of it. Those ties are broken here: a stable release outranks its own
// candidates, and candidates are ordered by their number -- which a string
// compare gets backwards, sorting "rc.9" above "rc.10".
func compareReleasesNewestFirst(left, right RuntimeRelease) int {
	comparison, comparable := compareReleaseVersions(left.Version, right.Version)
	if !comparable {
		return strings.Compare(right.Version, left.Version)
	}
	if comparison != 0 {
		return -comparison
	}

	leftPre, rightPre := isPrereleaseVersion(left.Version), isPrereleaseVersion(right.Version)
	if leftPre != rightPre {
		if leftPre {
			return 1
		}
		return -1
	}
	if leftPre && rightPre {
		if ordinal := prereleaseOrdinal(right.Version) - prereleaseOrdinal(left.Version); ordinal != 0 {
			return ordinal
		}
	}
	return strings.Compare(right.Version, left.Version)
}
