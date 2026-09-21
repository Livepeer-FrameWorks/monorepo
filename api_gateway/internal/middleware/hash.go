package middleware

import (
	"crypto/rand"
	"sync"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
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

// hashIdentifier uses the same function domain events use for actor token
// hashes (events.HashIdentifier), so an event's actor joins its usage rows.
func hashIdentifier(value string) uint64 {
	hashSecretMu.RLock()
	secret := hashSecret
	hashSecretMu.RUnlock()
	return events.HashIdentifier(secret, value)
}

// HashIdentifier exposes the internal hash for other middleware consumers.
func HashIdentifier(value string) uint64 {
	return hashIdentifier(value)
}
