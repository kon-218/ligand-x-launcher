package launcher

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestDesktopReleaseIdentityAndBinding(t *testing.T) {
	oldKey, oldVersion := runtimeBundlePublicKeyB64, launcherVersion
	t.Cleanup(func() { runtimeBundlePublicKeyB64, launcherVersion = oldKey, oldVersion })

	for _, version := range []string{"", "v2.3.4"} {
		t.Run("version="+version, func(t *testing.T) {
			launcherVersion = defaultPinnedImageVersion
			opts := desktopOptions(fstest.MapFS{}, "release-public-key", version)
			policy := runtimePolicy()
			wantVersion := version
			if wantVersion == "" {
				wantVersion = defaultPinnedImageVersion
			}
			if policy.PublicKeyB64 != "release-public-key" || policy.LauncherVersion != wantVersion {
				t.Fatalf("release identity did not reach runtime verification: %+v", policy)
			}
			if policy.AllowLocalSources != !isPublicBuild {
				t.Fatalf("runtime local-source policy changed: %+v", policy)
			}
			if len(opts.Bind) != 1 {
				t.Fatalf("expected one app binding, got %d", len(opts.Bind))
			}
			bound := reflect.TypeOf(opts.Bind[0]).Elem()
			if bound.PkgPath() != "ligandx-launcher/internal/launcher" || bound.Name() != "App" {
				t.Fatalf("unexpected frontend binding: %s.%s", bound.PkgPath(), bound.Name())
			}
		})
	}
}
