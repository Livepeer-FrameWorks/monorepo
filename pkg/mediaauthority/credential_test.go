package mediaauthority

import "testing"

func TestPublishingCredentialDigestMatchesCITEXTContract(t *testing.T) {
	digest := PublishingCredentialDigest("sk_AbC123")
	if !VerifyPublishingCredential("SK_aBc123", digest) {
		t.Fatal("case-insensitive equivalent credential did not verify")
	}
	if VerifyPublishingCredential("sk_other", digest) {
		t.Fatal("different credential verified")
	}
	if VerifyPublishingCredential("sk_AbC123", digest[:len(digest)-1]) {
		t.Fatal("truncated verifier was accepted")
	}
}
