package credentials

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"
)

func TestGenerateInternalCADeploymentMaterial(t *testing.T) {
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	values, err := GenerateInternalCADeploymentMaterial(now)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeGeneratedCertificate(t, values["NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64"])
	intermediate := decodeGeneratedCertificate(t, values["NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64"])
	if !root.IsCA || !intermediate.IsCA {
		t.Fatal("generated root and intermediate must both be CA certificates")
	}
	if signatureErr := intermediate.CheckSignatureFrom(root); signatureErr != nil {
		t.Fatalf("intermediate is not signed by root: %v", signatureErr)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(values["NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64"])
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(keyPEM)
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	certKey, ok := intermediate.PublicKey.(*ecdsa.PublicKey)
	if !ok || !key.PublicKey.Equal(certKey) {
		t.Fatal("intermediate private key does not match its certificate")
	}
	if root.NotBefore.After(now) || root.NotAfter.Before(now.AddDate(9, 0, 0)) {
		t.Fatalf("unexpected root validity: %s through %s", root.NotBefore, root.NotAfter)
	}
}

func decodeGeneratedCertificate(t *testing.T, encoded string) *x509.Certificate {
	t.Helper()
	pemBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("generated value is not one certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
