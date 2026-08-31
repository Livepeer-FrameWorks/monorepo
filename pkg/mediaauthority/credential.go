package mediaauthority

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

const publishingCredentialDomain = "frameworks-publishing-credential-v1\x00"

// PublishingCredentialDigest returns the indexed one-way verifier carried by
// live-stream authority. Commodore's canonical key lookup is CITEXT, so the
// verifier deliberately applies the same case-insensitive contract. Platform
// stream keys are generated high-entropy ASCII values; they are not passwords.
func PublishingCredentialDigest(credential string) []byte {
	sum := sha256.Sum256([]byte(publishingCredentialDomain + strings.ToLower(credential)))
	return append([]byte(nil), sum[:]...)
}

func VerifyPublishingCredential(credential string, expected []byte) bool {
	actual := PublishingCredentialDigest(credential)
	return len(expected) == sha256.Size && subtle.ConstantTimeCompare(actual, expected) == 1
}
