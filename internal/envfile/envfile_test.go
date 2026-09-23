package envfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWritePrivateFileReplacesExistingContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("unexpected private file content %q", data)
	}
	requirePrivateMode(t, path)
}

func TestDuplicateEnvKeysNamesEveryRedefinedKey(t *testing.T) {
	// A duplicate is a user-visible foot-gun for every key the launcher touches,
	// not just CPU limits, so it is worth naming in the log at start.
	content := strings.Join([]string{
		"# comment",
		"VERSION=latest",
		"APP_PORT=8080",
		"VERSION=v2026.08.05",
		"WORKER_CPU_CPU_LIMIT = 6",
		"WORKER_CPU_CPU_LIMIT=16",
		"#VERSION=commented-out-does-not-count",
	}, "\n")
	got := DuplicateKeys(content)
	want := []string{"VERSION", "WORKER_CPU_CPU_LIMIT"}
	if len(got) != len(want) {
		t.Fatalf("duplicateEnvKeys = %v, want %v", got, want)
	}
	for i, k := range want {
		if got[i] != k {
			t.Errorf("duplicateEnvKeys[%d] = %q, want %q", i, got[i], k)
		}
	}
	if dups := DuplicateKeys("A=1\nB=2\n"); len(dups) != 0 {
		t.Errorf("clean file reported duplicates: %v", dups)
	}
}
func requirePrivateMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected %s mode 0600, got %04o", path, got)
	}
}
