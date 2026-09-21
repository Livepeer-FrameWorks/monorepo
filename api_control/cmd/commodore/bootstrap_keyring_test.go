package main

import (
	"testing"

	"frameworks/api_control/internal/appconfig"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
)

func TestBootstrapSourceURIKeyringReadsOutgoingLegacySecret(t *testing.T) {
	const outgoing = "short-jwt"
	cfg := &appconfig.CommodoreBootstrap{
		JWTSecret:          "current-jwt-secret-material-32-bytes",
		FieldEncryptionKey: "current-field-key-material-32-bytes",
		FieldEncryptionRotation: appconfig.FieldEncryptionRotation{
			FieldEncryptionKeyID:         "current",
			FieldEncryptionLegacySecrets: `["short-jwt"]`,
		},
	}

	legacy, err := fieldcrypt.DeriveFieldEncryptor([]byte(outgoing), "pull-source-uri")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := legacy.Encrypt("srt://example.test:9000?streamid=secret")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := newSourceURIEncrypter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ring.Decrypt(stored)
	if err != nil || plain != "srt://example.test:9000?streamid=secret" {
		t.Fatalf("bootstrap did not retain outgoing legacy secret: plain=%q err=%v", plain, err)
	}
}

func TestBootstrapSourceURIKeyringRequiresEncryptionSecrets(t *testing.T) {
	for name, cfg := range map[string]*appconfig.CommodoreBootstrap{
		"missing jwt secret": {FieldEncryptionKey: "current-field-key-material-32-bytes"},
		"missing field key":  {JWTSecret: "current-jwt-secret-material-32-bytes"},
	} {
		if _, err := newSourceURIEncrypter(cfg); err == nil {
			t.Errorf("%s: expected an error before building the keyring", name)
		}
	}
}
