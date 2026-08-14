package gateway

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedClientKeyLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client_keys.json")
	store, err := NewClientKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	snapshot, secret, err := store.Create(ClientKeyCreate{
		Name: "mobile", Models: []string{"model-a"}, RPM: 3, Concurrency: 1, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, managedKeyPrefix) || strings.Contains(snapshot.Prefix, secret) {
		t.Fatalf("invalid secret or exposed snapshot: %+v", snapshot)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), secret) {
		t.Fatal("persisted key file contains plaintext secret")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file permissions: info=%v err=%v", info, err)
	}

	lease, failure := store.Authenticate(secret, nil)
	if failure != "" || !lease.AllowsModel("model-a") || lease.AllowsModel("model-b") {
		t.Fatalf("authentication=%q lease=%+v", failure, lease)
	}
	if _, failure = store.Authenticate(secret, nil); failure != KeyAuthConcurrency {
		t.Fatalf("concurrency failure=%q", failure)
	}
	lease.Complete(true)
	lease, failure = store.Authenticate(secret, nil)
	if failure != "" {
		t.Fatalf("second request failure=%q", failure)
	}
	lease.Complete(false)
	if _, failure = store.Authenticate(secret, nil); failure != KeyAuthRateLimited {
		t.Fatalf("rpm failure=%q", failure)
	}

	disabled := false
	if _, err := store.Update(snapshot.ID, ClientKeyUpdate{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, failure = store.Authenticate(secret, nil); failure != KeyAuthInvalid {
		t.Fatalf("disabled failure=%q", failure)
	}
	if err := store.Delete(snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatal("deleted key remained in snapshot")
	}
}

func TestLegacyKeyUsesDigestLookup(t *testing.T) {
	store, err := NewClientKeyStore(filepath.Join(t.TempDir(), "client_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[[sha256.Size]byte]struct{}{sha256.Sum256([]byte("legacy")): {}}
	lease, failure := store.Authenticate("legacy", legacy)
	if failure != "" || lease.ID != "legacy-env" {
		t.Fatalf("failure=%q lease=%+v", failure, lease)
	}
	lease.Complete(true)
	if _, failure := store.Authenticate("wrong", legacy); failure != KeyAuthInvalid {
		t.Fatalf("failure=%q", failure)
	}
}

func BenchmarkManagedKeyAuthenticate(b *testing.B) {
	store, err := NewClientKeyStore(filepath.Join(b.TempDir(), "client_keys.json"))
	if err != nil {
		b.Fatal(err)
	}
	_, secret, err := store.Create(ClientKeyCreate{Name: "benchmark", Models: []string{"m"}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lease, failure := store.Authenticate(secret, nil)
			if failure != "" {
				b.Fatal(failure)
			}
			if !lease.AllowsModel("m") {
				b.Fatal("model denied")
			}
			lease.Complete(true)
		}
	})
}
