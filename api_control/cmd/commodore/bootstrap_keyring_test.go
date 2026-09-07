package main

import (
	"testing"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
)

func TestBootstrapSourceURIKeyringReadsOutgoingLegacySecret(t *testing.T) {
	const outgoing = "short-jwt"
	t.Setenv("JWT_SECRET", "current-jwt-secret-material-32-bytes")
	t.Setenv("FIELD_ENCRYPTION_KEY", "current-field-key-material-32-bytes")
	t.Setenv("FIELD_ENCRYPTION_KEY_ID", "current")
	t.Setenv("FIELD_ENCRYPTION_PREVIOUS_KEYS", "")
	t.Setenv("FIELD_ENCRYPTION_LEGACY_SECRETS", `["short-jwt"]`)

	legacy, err := fieldcrypt.DeriveFieldEncryptor([]byte(outgoing), "pull-source-uri")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := legacy.Encrypt("srt://example.test:9000?streamid=secret")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := newSourceURIEncrypter()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ring.Decrypt(stored)
	if err != nil || plain != "srt://example.test:9000?streamid=secret" {
		t.Fatalf("bootstrap did not retain outgoing legacy secret: plain=%q err=%v", plain, err)
	}
}
