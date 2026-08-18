package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"time"
)

// validCredential performs only the cryptographic/enabled/expiry portion of API
// key authentication. It deliberately does not consume RPM or concurrency. The
// execution-profile middleware uses it before inspecting an authenticated body;
// ServeGateway remains the only place that acquires the real KeyLease.
func (s *ClientKeyStore) validCredential(raw string, legacy map[[sha256.Size]byte]struct{}) bool {
	if s == nil || raw == "" || len(raw) > 512 {
		return false
	}
	if id, ok := managedKeyID(raw); ok {
		snapshot := s.snapshot.Load()
		if snapshot == nil {
			return false
		}
		runtime := (*snapshot)[id]
		if runtime == nil {
			return false
		}
		meta := runtime.meta.Load()
		if meta == nil || !meta.record.Enabled {
			return false
		}
		candidate := sha256.Sum256([]byte(raw))
		if subtle.ConstantTimeCompare(candidate[:], meta.hash[:]) != 1 {
			return false
		}
		return meta.expiresUnix <= 0 || time.Now().Unix() < meta.expiresUnix
	}
	candidate := sha256.Sum256([]byte(raw))
	_, ok := legacy[candidate]
	return ok
}
