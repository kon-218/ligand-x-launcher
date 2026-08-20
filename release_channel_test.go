package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestIsPrereleaseVersion(t *testing.T) {
	cases := map[string]bool{
		"v2026.08.15-rc.9":  true,
		"v2026.08.15-rc.10": true,
		"v1.2.3-beta.1":     true,
		"v2026.08.06":       false,
		"v0.1.0":            false,
		"2026.08.06":        false,
		"":                  false,
	}
	for version, want := range cases {
		if got := isPrereleaseVersion(version); got != want {
			t.Errorf("isPrereleaseVersion(%q) = %v, want %v", version, got, want)
		}
	}
}

// signIndex is a local helper so these tests do not depend on the fixtures of
// the older signing test.
func signIndex(t *testing.T, key ed25519.PrivateKey, index runtimeReleaseIndex) ([]byte, []byte) {
	t.Helper()
	payload, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return payload, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload)))
}

func indexWithRecommended(versions []string, recommended ...bool) runtimeReleaseIndex {
	releases := make([]RuntimeRelease, len(versions))
	for i, version := range versions {
		releases[i] = RuntimeRelease{
			Version:         version,
			Status:          "supported",
			Recommended:     recommended[i],
			MinimumLauncher: "v2.0.0",
			BundleURL: fmt.Sprintf(
				"https://github.com/kon-218/ligand-x-launcher/releases/download/%s/ligand-x-runtime.zip", version),
			DownloadBytes: 1024,
		}
	}
	return runtimeReleaseIndex{
		Schema:    "ligandx-release-index/1",
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		Releases:  releases,
	}
}

// v2026.08.15-rc.8 shipped an index with zero recommended entries and was left
// pinned as GitHub's "Latest", which made the whole version picker fail with
// "release index must identify exactly one recommended release" -- no list, no
// way to install anything. A malformed recommendation is a presentation defect,
// not an authenticity one, so it must degrade to a warning rather than hide
// every release. The signature stays mandatory.
func TestMalformedRecommendationDegradesInsteadOfFailing(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldVersion := runtimeBundlePublicKeyB64, launcherVersion
	runtimeBundlePublicKeyB64 = base64.StdEncoding.EncodeToString(publicKey)
	launcherVersion = "v2.0.0"
	t.Cleanup(func() { runtimeBundlePublicKeyB64, launcherVersion = oldKey, oldVersion })

	versions := []string{"v2.1.0", "v2.1.1"}

	t.Run("zero recommended is listed with a warning", func(t *testing.T) {
		payload, signature := signIndex(t, privateKey, indexWithRecommended(versions, false, false))
		index, err := verifyRuntimeReleaseIndex(payload, signature)
		if err != nil {
			t.Fatalf("a signed index with no recommendation must still be usable, got %v", err)
		}
		if len(index.Releases) != 2 {
			t.Fatalf("expected both releases listed, got %d", len(index.Releases))
		}
		if index.Warning == "" {
			t.Fatal("expected a warning explaining that no release is recommended")
		}
	})

	t.Run("multiple recommended are cleared and warned about", func(t *testing.T) {
		payload, signature := signIndex(t, privateKey, indexWithRecommended(versions, true, true))
		index, err := verifyRuntimeReleaseIndex(payload, signature)
		if err != nil {
			t.Fatalf("a signed index with two recommendations must still be usable, got %v", err)
		}
		for _, release := range index.Releases {
			if release.Recommended {
				t.Fatalf("ambiguous recommendation must be cleared, %s is still marked", release.Version)
			}
		}
		if index.Warning == "" {
			t.Fatal("expected a warning explaining the ambiguous recommendation")
		}
	})

	t.Run("exactly one recommended stays clean", func(t *testing.T) {
		payload, signature := signIndex(t, privateKey, indexWithRecommended(versions, true, false))
		index, err := verifyRuntimeReleaseIndex(payload, signature)
		if err != nil {
			t.Fatalf("well-formed index rejected: %v", err)
		}
		if index.Warning != "" {
			t.Fatalf("well-formed index must not warn, got %q", index.Warning)
		}
		if !index.Releases[0].Recommended {
			t.Fatal("the recommended release lost its flag")
		}
	})

	t.Run("a bad signature is still fatal", func(t *testing.T) {
		payload, _ := signIndex(t, privateKey, indexWithRecommended(versions, true, false))
		bogus := []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)))
		if _, err := verifyRuntimeReleaseIndex(payload, bogus); err == nil {
			t.Fatal("an unsigned or wrongly signed index must be rejected outright")
		}
	})

	t.Run("an expired index is still fatal", func(t *testing.T) {
		stale := indexWithRecommended(versions, true, false)
		stale.ExpiresAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		payload, signature := signIndex(t, privateKey, stale)
		if _, err := verifyRuntimeReleaseIndex(payload, signature); err == nil ||
			!strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired index must still be rejected, got %v", err)
		}
	})
}

func TestFilterReleasesForChannel(t *testing.T) {
	releases := []RuntimeRelease{
		{Version: "v2026.08.15-rc.10"},
		{Version: "v2026.08.15-rc.9"},
		{Version: "v2026.08.06"},
		{Version: "v2026.08.05"},
	}

	t.Run("stable channel hides pre-releases", func(t *testing.T) {
		got := filterReleasesForChannel(releases, false, "")
		want := []string{"v2026.08.06", "v2026.08.05"}
		if len(got) != len(want) {
			t.Fatalf("got %d releases, want %d: %+v", len(got), len(want), got)
		}
		for i, release := range got {
			if release.Version != want[i] {
				t.Fatalf("got %q at %d, want %q", release.Version, i, want[i])
			}
		}
	})

	t.Run("pre-release channel shows everything", func(t *testing.T) {
		if got := filterReleasesForChannel(releases, true, ""); len(got) != 4 {
			t.Fatalf("expected all 4 releases, got %d", len(got))
		}
	})

	// Someone already running an rc must still see it after switching the toggle
	// off, or the picker would hide the version they are on and with it the
	// rollback path away from it.
	t.Run("the installed pre-release stays visible on the stable channel", func(t *testing.T) {
		got := filterReleasesForChannel(releases, false, "v2026.08.15-rc.9")
		found := false
		for _, release := range got {
			if release.Version == "v2026.08.15-rc.9" {
				found = true
			}
			if release.Version == "v2026.08.15-rc.10" {
				t.Fatal("an rc that is not installed must stay hidden")
			}
		}
		if !found {
			t.Fatalf("installed rc was hidden: %+v", got)
		}
	})
}

// GitHub's own "latest" pointer is not trustworthy for this: it excludes
// pre-releases, and marking an rc as latest silently un-marks it as a
// pre-release. Selection is therefore made from the full listing, in the order
// GitHub returns it (newest first), using our own rules.
func TestSelectReleaseFromListing(t *testing.T) {
	listing := []githubRelease{
		{TagName: "launcher-v2026.08.15-rc.10", Prerelease: true, Draft: false,
			Assets: []githubAsset{{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-rc10"}}},
		{TagName: "launcher-v2026.08.15-rc.9", Prerelease: true, Draft: false,
			Assets: []githubAsset{
				{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-rc9"},
				{Name: "ligand-x-release-index.json", BrowserDownloadURL: "i-rc9"},
			}},
		{TagName: "launcher-v2026.08.06", Prerelease: false, Draft: false,
			Assets: []githubAsset{
				{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-stable"},
				{Name: "ligand-x-release-index.json", BrowserDownloadURL: "i-stable"},
			}},
	}

	t.Run("stable channel skips pre-releases", func(t *testing.T) {
		found, tag, err := selectReleaseAssets(listing, false, []string{"ligand-x-runtime.zip"})
		if err != nil {
			t.Fatal(err)
		}
		if found["ligand-x-runtime.zip"] != "u-stable" || tag != "launcher-v2026.08.06" {
			t.Fatalf("stable channel picked %q from %q", found["ligand-x-runtime.zip"], tag)
		}
	})

	t.Run("pre-release channel takes the newest overall", func(t *testing.T) {
		found, tag, err := selectReleaseAssets(listing, true, []string{"ligand-x-runtime.zip"})
		if err != nil {
			t.Fatal(err)
		}
		if found["ligand-x-runtime.zip"] != "u-rc10" || tag != "launcher-v2026.08.15-rc.10" {
			t.Fatalf("pre-release channel picked %q from %q", found["ligand-x-runtime.zip"], tag)
		}
	})

	// rc.10 has no index asset, so the search continues rather than giving up --
	// which is also what makes a release that predates the index format harmless.
	t.Run("a release missing an asset is skipped, not fatal", func(t *testing.T) {
		found, tag, err := selectReleaseAssets(listing, true, []string{"ligand-x-release-index.json"})
		if err != nil {
			t.Fatal(err)
		}
		if found["ligand-x-release-index.json"] != "i-rc9" || tag != "launcher-v2026.08.15-rc.9" {
			t.Fatalf("expected to fall through to rc.9, got %q from %q",
				found["ligand-x-release-index.json"], tag)
		}
	})

	t.Run("drafts are never selected", func(t *testing.T) {
		drafts := []githubRelease{
			{TagName: "launcher-v9.9.9", Prerelease: false, Draft: true,
				Assets: []githubAsset{{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-draft"}}},
			{TagName: "launcher-v2026.08.06", Prerelease: false, Draft: false,
				Assets: []githubAsset{{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-stable"}}},
		}
		found, _, err := selectReleaseAssets(drafts, true, []string{"ligand-x-runtime.zip"})
		if err != nil {
			t.Fatal(err)
		}
		if found["ligand-x-runtime.zip"] != "u-stable" {
			t.Fatalf("a draft was selected: %q", found["ligand-x-runtime.zip"])
		}
	})

	// The rc.8 situation exactly: promoting a pre-release to "Latest" makes
	// GitHub clear its prerelease flag, because a latest release cannot be one.
	// The tag still says -rc.8, so the tag has to be believed over the flag or a
	// stable-channel user is silently offered a release candidate.
	t.Run("a promoted rc is still treated as a pre-release", func(t *testing.T) {
		promoted := []githubRelease{
			{TagName: "launcher-v2026.08.15-rc.8", Prerelease: false, Draft: false,
				Assets: []githubAsset{{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-rc8"}}},
			{TagName: "launcher-v2026.08.06", Prerelease: false, Draft: false,
				Assets: []githubAsset{{Name: "ligand-x-runtime.zip", BrowserDownloadURL: "u-stable"}}},
		}
		found, tag, err := selectReleaseAssets(promoted, false, []string{"ligand-x-runtime.zip"})
		if err != nil {
			t.Fatal(err)
		}
		if found["ligand-x-runtime.zip"] != "u-stable" {
			t.Fatalf("stable channel was given the promoted rc %q from %q",
				found["ligand-x-runtime.zip"], tag)
		}
	})

	t.Run("no match is an error", func(t *testing.T) {
		if _, _, err := selectReleaseAssets(listing, false, []string{"nope.zip"}); err == nil {
			t.Fatal("expected an error when no release carries the asset")
		}
	})
}

// numericReleaseVersion drops the -rc suffix, so every rc of a version compares
// equal to its stable and to every other rc. Ordering therefore needs its own
// rules, and a plain string compare gets them wrong: "rc.9" sorts above "rc.10".
func TestCompareReleasesNewestFirst(t *testing.T) {
	releases := []RuntimeRelease{
		{Version: "v2026.08.15-rc.9"},
		{Version: "v2026.08.06"},
		{Version: "v2026.08.15"},
		{Version: "v2026.08.15-rc.10"},
		{Version: "v2026.08.05"},
	}
	slices.SortFunc(releases, compareReleasesNewestFirst)

	want := []string{
		"v2026.08.15",       // stable outranks its own candidates
		"v2026.08.15-rc.10", // rc.10 is newer than rc.9 despite sorting lower as text
		"v2026.08.15-rc.9",
		"v2026.08.06",
		"v2026.08.05",
	}
	for i, release := range releases {
		if release.Version != want[i] {
			t.Fatalf("position %d is %q, want %q (full order: %v)", i, release.Version, want[i], versionsOf(releases))
		}
	}
}

func versionsOf(releases []RuntimeRelease) []string {
	out := make([]string, len(releases))
	for i, release := range releases {
		out[i] = release.Version
	}
	return out
}
