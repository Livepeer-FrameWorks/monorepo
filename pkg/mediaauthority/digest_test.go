package mediaauthority

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func digestTestKey(fill byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{fill}, ed25519.SeedSize))
}

func TestSecretCommitmentBindsPlaintextRecipientsAuthorityAndSigner(t *testing.T) {
	commit := func(key ed25519.PrivateKey, authorityID, plaintext string, recipients ...string) []byte {
		t.Helper()
		sum, err := SecretCommitment(key, authorityID, []byte(plaintext), recipients)
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	key := digestTestKey(1)
	base := commit(key, "live_stream:a", "secret", "cell-a\x00k1", "cell-b\x00k2")

	if got := commit(key, "live_stream:a", "secret", "cell-b\x00k2", "cell-a\x00k1"); !bytes.Equal(got, base) {
		t.Fatal("recipient order changed the commitment; identical state must commit identically")
	}
	for name, other := range map[string][]byte{
		"plaintext":  commit(key, "live_stream:a", "rotated", "cell-a\x00k1", "cell-b\x00k2"),
		"recipients": commit(key, "live_stream:a", "secret", "cell-a\x00k1"),
		"authority":  commit(key, "live_stream:b", "secret", "cell-a\x00k1", "cell-b\x00k2"),
		"signer":     commit(digestTestKey(2), "live_stream:a", "secret", "cell-a\x00k1", "cell-b\x00k2"),
		// Length prefixes keep "ab"+"c" from committing like "a"+"bc".
		"field split": commit(key, "live_stream:a", "secretcell-a\x00k1", "cell-b\x00k2"),
	} {
		if bytes.Equal(other, base) {
			t.Fatalf("a different %s produced the same commitment", name)
		}
	}
	if _, err := SecretCommitment(nil, "live_stream:a", []byte("secret"), nil); err == nil {
		t.Fatal("a commitment without a signing key must be refused")
	}
}

func TestMediaObjectContentDigestHasNoStableIdentityForQuotedObjects(t *testing.T) {
	payload := &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: SchemaVersion, TenantId: "tenant-1", InternalName: "stream"}
	first, ok, err := MediaObjectContentDigest(payload, "signer-1", nil)
	if err != nil || !ok || len(first) == 0 {
		t.Fatalf("plain object: digest=%x ok=%v err=%v", first, ok, err)
	}
	if rotated, _, _ := MediaObjectContentDigest(payload, "signer-2", nil); bytes.Equal(rotated, first) {
		t.Fatal("a different signer key ID kept the same content digest")
	}
	// Quote observation and expiry times are part of what a quoted authority
	// says, so it never compares equal to its predecessor.
	payload.CommercialQuotes = append(payload.CommercialQuotes, nil)
	if _, ok, _ := MediaObjectContentDigest(payload, "signer-1", nil); ok {
		t.Fatal("an object carrying commercial quotes must not report a stable digest")
	}
}
