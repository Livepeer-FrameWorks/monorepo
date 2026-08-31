package mediaauthority

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
)

func TestParseSigningPrivateKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	parsed, err := ParseSigningPrivateKey(encoded)
	if err != nil {
		t.Fatalf("ParseSigningPrivateKey: %v", err)
	}
	if !publicKey.Equal(parsed.Public()) {
		t.Fatal("parsed private key has a different public key")
	}
}

func TestParseSigningPrivateKeyRejectsWrongAlgorithm(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := ParseSigningPrivateKey(encoded); err == nil {
		t.Fatal("accepted non-Ed25519 private key")
	}
}

func TestParseTrustSet(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	encoded, err := json.Marshal(map[string]string{"current": base64.StdEncoding.EncodeToString(publicKey)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	trust, err := ParseTrustSet(string(encoded))
	if err != nil {
		t.Fatalf("ParseTrustSet: %v", err)
	}
	if !publicKey.Equal(trust["current"]) {
		t.Fatal("parsed trust key differs")
	}
}

func TestParseTrustSetRejectsMalformedInputs(t *testing.T) {
	tests := []string{
		"",
		"{}",
		`{" key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`,
		`{"key":"short"}`,
		`{"key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`,
		`{"key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="} {}`,
	}
	for _, input := range tests {
		if _, err := ParseTrustSet(input); err == nil {
			t.Fatalf("accepted malformed trust set %q", input)
		}
	}
}
