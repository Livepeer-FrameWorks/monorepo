package skipper

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash/fnv"
	"os"
	"sync"

	"frameworks/pkg/ctxkeys"
)

var (
	hashSecret   []byte
	hashSecretMu sync.RWMutex
	useHMAC      bool
	hashOnce     sync.Once
)

func initHashSecret() {
	if secret := os.Getenv("SKIPPER_USAGE_HASH_SECRET"); secret != "" {
		hashSecretMu.Lock()
		hashSecret = []byte(secret)
		useHMAC = true
		hashSecretMu.Unlock()
	}
}

func hashIdentifier(value string) uint64 {
	if value == "" {
		return 0
	}
	hashOnce.Do(initHashSecret)

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

func tokenHashFromContext(ctx context.Context) uint64 {
	if ctx == nil {
		return 0
	}
	if v := ctx.Value(ctxkeys.KeyAPITokenHash); v != nil {
		switch t := v.(type) {
		case uint64:
			return t
		case uint32:
			return uint64(t)
		case int64:
			if t > 0 {
				return uint64(t)
			}
		case int:
			if t > 0 {
				return uint64(t)
			}
		}
	}
	if token := ctxkeys.GetAPIToken(ctx); token != "" {
		return hashIdentifier(token)
	}
	if token := ctxkeys.GetJWTToken(ctx); token != "" {
		return hashIdentifier(token)
	}
	return 0
}
