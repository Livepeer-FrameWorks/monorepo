package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash/fnv"
	"sync"
)

var (
	hashSecret   []byte
	hashSecretMu sync.RWMutex
	useHMAC      bool
)

// InitHasher configures the hashing secret for usage tracking.
func InitHasher(secret string) {
	hashSecretMu.Lock()
	defer hashSecretMu.Unlock()
	if secret != "" {
		hashSecret = []byte(secret)
		useHMAC = true
	}
}

func hashIdentifier(value string) uint64 {
	if value == "" {
		return 0
	}
	hashSecretMu.RLock()
	secret, useMac := hashSecret, useHMAC
	hashSecretMu.RUnlock()

	if useMac {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(value))
		return binary.BigEndian.Uint64(mac.Sum(nil)[:8])
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

// HashIdentifier exposes the internal hash for other middleware consumers.
func HashIdentifier(value string) uint64 {
	return hashIdentifier(value)
}
