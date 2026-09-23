package runtimebundle

import (
	"encoding/json"
	"fmt"
	"ligandx-launcher/internal/envfile"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// fetchReleaseListing queries the GitHub releases API to find the
// download URL of the runtime bundle asset attached to the latest release.
// GitHub's /releases/latest/download/<asset> redirect is unreliable on some
// Windows HTTP clients, so we resolve the concrete asset URL explicitly.
func fetchReleaseListing() ([]GitHubRelease, error) {
	req, err := http.NewRequest(http.MethodGet, releasesListAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ligand-x-launcher")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases API returned HTTP %d", resp.StatusCode)
	}

	var releases []GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub releases response: %w", err)
	}
	return releases, nil
}

// resolveReleaseAssets finds the newest release on the requested channel that
// carries every wanted asset.
func ResolveReleaseAssets(includePrereleases bool, wanted ...string) (map[string]string, string, error) {
	releases, err := fetchReleaseListing()
	if err != nil {
		return nil, "", err
	}
	return SelectReleaseAssets(releases, includePrereleases, wanted)
}

func ResolveBundleURL(includePrereleases bool) (string, string, error) {
	assets, tag, err := ResolveReleaseAssets(includePrereleases, AssetName)
	if err != nil {
		return "", "", err
	}
	return assets[AssetName], tag, nil
}

func numericVersion(version string) ([3]int, bool) {
	var parsed [3]int
	base := strings.TrimPrefix(strings.TrimSpace(version), "v")
	base = strings.SplitN(base, "-", 2)[0]
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return parsed, false
	}
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return parsed, false
		}
		parsed[index] = value
	}
	return parsed, true
}

func CompareVersions(left, right string) (int, bool) {
	leftParts, leftOK := numericVersion(left)
	rightParts, rightOK := numericVersion(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1, true
		}
		if leftParts[index] > rightParts[index] {
			return 1, true
		}
	}
	return 0, true
}

// shouldAdvanceVersion decides whether installing releaseTag should re-pin
// VERSION in .env.production.
//
// The old rule only rewrote a broken value (empty/CHANGE_ME/latest), which meant
// a valid-but-old pin was indistinguishable from a deliberate choice and
// survived forever — so an existing install could take a new launcher AND a new
// runtime bundle and still run the previous release's images. Installing a
// newer runtime is an explicit act by the user, so it advances the pin; a pin
// that is already ahead of, or equal to, the installed runtime is left alone,
// and an unparseable one is treated as deliberate.
func ShouldAdvanceVersion(current, releaseTag string) bool {
	if envfile.IsPlaceholder(current) || strings.EqualFold(current, "latest") {
		return true
	}
	comparison, comparable := CompareVersions(releaseTag, current)
	return comparable && comparison > 0
}

// installedRuntimeVersion reads the release tag recorded when the runtime
// bundle was installed, or "" when the marker is absent (a pre-marker install,
// or none at all).
func InstalledVersion(runtimeDir string) string {
	data, err := os.ReadFile(filepath.Join(runtimeDir, ".ligandx-runtime-version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func EnforceRollbackPolicy(runtimeDir, candidate string) error {
	current := InstalledVersion(runtimeDir)
	if current == "" {
		return nil
	}
	if comparison, comparable := CompareVersions(candidate, current); !comparable || comparison < 0 {
		return fmt.Errorf("runtime downgrade rejected: installed=%s candidate=%s", current, candidate)
	}
	return nil
}

// isPrereleaseVersion reports whether a release version names a pre-release,
// e.g. v2026.08.15-rc.9.
//
// The version string is the signal rather than GitHub's `prerelease` flag,
// deliberately. That flag has already proved unreliable here: publishing
// v2026.08.15-rc.8 as "Latest" silently un-marked it as a pre-release, because
// GitHub does not allow the latest release to be one. The version inside the
// signed index cannot drift that way.
func IsPrerelease(version string) bool {
	return strings.Contains(strings.TrimSpace(version), "-")
}

// filterReleasesForChannel drops pre-releases unless the user has opted into
// them. The currently installed version is always kept, so a user already
// running a pre-release can still see, re-select and roll back from it after
// turning the toggle off.
func FilterForChannel(releases []Release, includePrereleases bool, installed string) []Release {
	if includePrereleases {
		return releases
	}
	installed = strings.TrimSpace(installed)
	filtered := make([]Release, 0, len(releases))
	for _, release := range releases {
		if !IsPrerelease(release.Version) || release.Version == installed {
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

type GitHubRelease struct {
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
func SelectReleaseAssets(releases []GitHubRelease, includePrereleases bool, wanted []string) (map[string]string, string, error) {
	for _, release := range releases {
		if release.Draft {
			continue
		}
		// Both signals must agree before a release is treated as stable: the tag
		// is authoritative when GitHub's flag has been cleared by promotion.
		if !includePrereleases && (release.Prerelease || IsPrerelease(VersionFromTag(release.TagName))) {
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
func VersionFromTag(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "launcher-")
}

// recommendedRelease returns the release the signed index marks recommended,
// used both to install on first setup and to prompt an update on an existing
// one -- so those two decisions can't disagree about what's safe to install.
func Recommended(releases []Release) (Release, bool) {
	for _, release := range releases {
		if release.Recommended {
			return release, true
		}
	}
	return Release{}, false
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
func CompareNewestFirst(left, right Release) int {
	comparison, comparable := CompareVersions(left.Version, right.Version)
	if !comparable {
		return strings.Compare(right.Version, left.Version)
	}
	if comparison != 0 {
		return -comparison
	}

	leftPre, rightPre := IsPrerelease(left.Version), IsPrerelease(right.Version)
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
