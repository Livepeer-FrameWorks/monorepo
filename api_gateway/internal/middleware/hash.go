package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

var (
	hashSecret   []byte
	hashSecretMu sync.RWMutex
)

// InitHasher configures the hashing secret for usage tracking.
// If secret is empty, a 32-byte ephemeral random secret is generated.
func InitHasher(secret string) {
	hashSecretMu.Lock()
	defer hashSecretMu.Unlock()
	if secret != "" {
		hashSecret = []byte(secret)
	} else {
		ephemeral := make([]byte, 32)
		_, _ = rand.Read(ephemeral)
		hashSecret = ephemeral
	}
}

func hashIdentifier(value string) uint64 {
	if value == "" {
		return 0
	}
	hashSecretMu.RLock()
	secret := hashSecret
	hashSecretMu.RUnlock()

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return binary.BigEndian.Uint64(mac.Sum(nil)[:8])
}

// HashIdentifier exposes the internal hash for other middleware consumers.
func HashIdentifier(value string) uint64 {
	return hashIdentifier(value)
}
