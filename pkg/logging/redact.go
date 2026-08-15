package logging

import (
	"crypto/sha256"
	"encoding/hex"
)

// RedactSecret turns a high-entropy generated credential — a stream key — into
// a short, stable tag, so log lines stay correlatable across services without
// carrying the secret itself.
//
// Scope matters: this is an unkeyed truncated SHA-256, so it resists recovery
// only because the input is a long random token. It is NOT safe for
// low-entropy or guessable values (passwords, emails, IDs), where the digest
// can simply be brute-forced back. Use a keyed HMAC for those.
//
// 128 bits of digest: the tag correlates one publisher's lines across
// services, so it has to stay collision-free across the whole key space rather
// than merely look distinct in one log file.
func RedactSecret(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return "sk#" + hex.EncodeToString(sum[:16])
}
