package secretstore

import (
	"runtime"
	"strings"
	"testing"
)

func TestNativeSecretStoreRoundTripOrFailClosed(t *testing.T) {
	store := Default()
	account := "ligandx-test-" + strings.Repeat("ab", 8)
	if err := store.Available(); err != nil {
		if runtime.GOOS == "linux" && strings.Contains(strings.ToLower(err.Error()), "secret service") {
			return
		}
		t.Skipf("native store unavailable: %v", err)
	}
	secret := `{"token":"t","signing_key":"` + strings.Repeat("0", 64) + `","credential_id":"id"}`
	if err := store.Set(account, secret); err != nil {
		t.Fatalf("set: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(account) })
	got, err := store.Get(account)
	if err != nil || got != secret {
		t.Fatalf("get mismatch: %q %v", got, err)
	}
}
