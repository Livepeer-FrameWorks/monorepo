package mediaauthority

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func TestSealedSecretRoundTripAndScopeBinding(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipient := SealRecipient{KeyID: "cell-key-1", PublicKey: privateKey.PublicKey()}
	box, err := SealSecret("cell-a", "live_stream:stream-a", recipient, []byte("rtsp://user:secret@example/live"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenSecret(box, "cell-a", "live_stream:stream-a", "cell-key-1", privateKey)
	if err != nil || string(got) != "rtsp://user:secret@example/live" {
		t.Fatalf("OpenSecret = %q, %v", got, err)
	}
	if _, err := OpenSecret(box, "cell-b", "live_stream:stream-a", "cell-key-1", privateKey); err == nil {
		t.Fatal("cross-cell sealed secret replay was accepted")
	}
	if _, err := OpenSecret(box, "cell-a", "live_stream:stream-b", "cell-key-1", privateKey); err == nil {
		t.Fatal("cross-authority sealed secret replay was accepted")
	}
	box.Ciphertext[0] ^= 0x01
	if _, err := OpenSecret(box, "cell-a", "live_stream:stream-a", "cell-key-1", privateKey); err == nil {
		t.Fatal("tampered sealed secret was accepted")
	}
}

func TestSealedSecretSetAllowsRevocationEnvelopeWithoutRecipientBox(t *testing.T) {
	envelope := &mediaauthoritypb.AuthorityEnvelope{AudienceCellId: "cell-revoked"}
	boxes := []*mediaauthoritypb.SealedCellSecret{{
		AudienceCellId: "cell-active", RecipientKeyId: "seal-1",
		EphemeralPublicKey: make([]byte, 32), Nonce: make([]byte, 12), Ciphertext: make([]byte, 16),
	}}
	if err := validateSealedCellSecrets(envelope, boxes); err != nil {
		t.Fatalf("revocation envelope rejected active-cell ciphertext set: %v", err)
	}
}

func TestSealKeyConfigurationParsing(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateEncoded := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	parsedPrivate, err := ParseSealPrivateKey(privateEncoded)
	if err != nil || !parsedPrivate.Equal(privateKey) {
		t.Fatalf("ParseSealPrivateKey: %v", err)
	}
	recipientsJSON, err := json.Marshal(map[string]sealRecipientJSON{
		"cell-a": {KeyID: SealRecipientKeyID("cell-a", privateKey.PublicKey()), PublicKey: base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes())},
	})
	if err != nil {
		t.Fatal(err)
	}
	recipients, err := ParseSealRecipients(string(recipientsJSON))
	if err != nil || recipients["cell-a"].KeyID != SealRecipientKeyID("cell-a", privateKey.PublicKey()) || !recipients["cell-a"].PublicKey.Equal(privateKey.PublicKey()) {
		t.Fatalf("ParseSealRecipients = %+v, %v", recipients, err)
	}
	mismatched, err := json.Marshal(map[string]sealRecipientJSON{
		"cell-b": {KeyID: SealRecipientKeyID("cell-a", privateKey.PublicKey()), PublicKey: base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes())},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSealRecipients(string(mismatched)); err == nil {
		t.Fatal("recipient key bound to a different cell was accepted")
	}
	duplicate := `{"cell-a":{"key_id":"one","public_key":"` + base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes()) + `"},"cell-a":{"key_id":"two","public_key":"` + base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes()) + `"}}`
	if _, err := ParseSealRecipients(duplicate); err == nil {
		t.Fatal("duplicate recipient cell was accepted")
	}
}
