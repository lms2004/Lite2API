package gateway

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestManagedClientKeyRejectsEpochAndPreEpochExpiry(t *testing.T) {
	store, err := NewClientKeyStore(filepath.Join(t.TempDir(), "client_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expiry := range []string{"1969-12-31T23:59:59Z", "1970-01-01T00:00:00Z", "0001-01-01T00:00:00Z", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)} {
		_, secret, err := store.Create(ClientKeyCreate{Name: "expired", ExpiresAt: expiry})
		if err != nil {
			t.Fatal(err)
		}
		if lease, failure := store.Authenticate(secret, nil); failure != KeyAuthInvalid {
			lease.Complete(false)
			t.Fatalf("expired key %s authenticated: %q", expiry, failure)
		}
		if store.validCredential(secret, nil) {
			t.Fatalf("preflight accepted expiry %s", expiry)
		}
	}
}

func TestManagedClientKeyEditsPreserveUsageAndActiveLeases(t *testing.T) {
	store, err := NewClientKeyStore(filepath.Join(t.TempDir(), "client_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := store.Create(ClientKeyCreate{Name: "before", RPM: 2, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	lease, failure := store.Authenticate(secret, nil)
	if failure != "" {
		t.Fatal(failure)
	}
	defer lease.Complete(false)
	runtime := lease.runtime
	// Seed the current minute explicitly to avoid crossing a wall-clock minute
	// boundary between acquiring the request and editing its key.
	now := time.Now()
	rate := uint64(now.Unix()/60)<<rateCountBits | 2
	runtime.rate.Store(rate)
	name := "after"
	updated, err := store.Update(key.ID, ClientKeyUpdate{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Active != 1 || updated.TotalRequests != 1 || (*store.snapshot.Load())[key.ID] != runtime {
		t.Fatal("editing the key replaced its live usage state")
	}
	if runtime.rate.Load() != rate || acquireRPM(&runtime.rate, 2, now) {
		t.Fatal("edit reset the current minute's request limit")
	}
	lease.Complete(false)
	if store.List()[0].Active != 0 {
		t.Fatal("the original in-flight lease did not release the shared counter")
	}
}

func TestManagedClientKeyCreateUsesDefaultName(t *testing.T) {
	store, err := NewClientKeyStore(filepath.Join(t.TempDir(), "client_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, secret, err := store.Create(ClientKeyCreate{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(snapshot.Name, "API Key ") || !strings.HasPrefix(secret, managedKeyPrefix) {
		t.Fatalf("default key = %+v secret_prefix=%t", snapshot, strings.HasPrefix(secret, managedKeyPrefix))
	}
	if len(snapshot.Models) != 0 || snapshot.RPM != 0 || snapshot.Concurrency != 0 || snapshot.ExpiresAt != "" {
		t.Fatalf("unexpected quick-create restrictions: %+v", snapshot)
	}
}

func TestManagedClientKeyDetectsAdminTokenOverlap(t *testing.T) {
	store, err := NewClientKeyStore(filepath.Join(t.TempDir(), "client_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := store.Create(ClientKeyCreate{Name: "admin-overlap"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.ConflictsWith(secret) || store.ConflictsWith("different-admin-token") {
		t.Fatal("managed key overlap check did not stay within the hashed key boundary")
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
