package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestConsentReviewSigningConfig(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if got, err := consentReviewSigningConfig("review-key", encoded); err != nil || !got.Equal(key) {
		t.Fatal("valid signing configuration rejected")
	}
	if got, err := consentReviewSigningConfig("", ""); err != nil || got != nil {
		t.Fatal("missing signer must leave read/recovery available")
	}
	for _, input := range [][2]string{{"", encoded}, {"key", ""}, {" key", encoded}, {"key\x00", encoded}, {strings.Repeat("k", 101), encoded}, {"key", "secret-invalid-bytes"}} {
		if _, err := consentReviewSigningConfig(input[0], input[1]); err == nil || strings.Contains(err.Error(), "secret-invalid-bytes") {
			t.Fatal("invalid config accepted or secret exposed")
		}
	}
}
