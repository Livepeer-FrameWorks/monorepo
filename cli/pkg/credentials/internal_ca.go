package credentials

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// GenerateInternalCADeploymentMaterial creates a new root certificate and a
// signing intermediate for Navigator-managed internal service identities. The
// root private key is intentionally discarded after signing the intermediate;
// rotating this bootstrap material creates a new trust domain.
func GenerateInternalCADeploymentMaterial(now time.Time) (map[string]string, error) {
	now = now.UTC()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate internal root CA key: %w", err)
	}
	rootSerial, err := randomCertificateSerial()
	if err != nil {
		return nil, fmt.Errorf("generate internal root CA serial: %w", err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          rootSerial,
		Subject:               pkix.Name{CommonName: "FrameWorks Internal Root CA", Organization: []string{"FrameWorks"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("create internal root CA certificate: %w", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, fmt.Errorf("parse generated internal root CA certificate: %w", err)
	}

	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate internal intermediate CA key: %w", err)
	}
	intermediateSerial, err := randomCertificateSerial()
	if err != nil {
		return nil, fmt.Errorf("generate internal intermediate CA serial: %w", err)
	}
	intermediateTemplate := &x509.Certificate{
		SerialNumber:          intermediateSerial,
		Subject:               pkix.Name{CommonName: "FrameWorks Internal Intermediate CA", Organization: []string{"FrameWorks"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(5, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, rootCert, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("create internal intermediate CA certificate: %w", err)
	}
	intermediateKeyDER, err := x509.MarshalECPrivateKey(intermediateKey)
	if err != nil {
		return nil, fmt.Errorf("encode internal intermediate CA key: %w", err)
	}

	return map[string]string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64":         base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})),
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER})),
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64":  base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: intermediateKeyDER})),
	}, nil
}

func randomCertificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	for {
		serial, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return nil, err
		}
		if serial.Sign() > 0 {
			return serial, nil
		}
	}
}
