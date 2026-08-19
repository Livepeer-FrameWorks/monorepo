package internalpki

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	rootCertFileKey         = "NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE"
	intermediateCertFileKey = "NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE"
	intermediateKeyFileKey  = "NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE"
	rootCertB64Key          = "NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64"
	intermediateCertB64Key  = "NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64"
	intermediateKeyB64Key   = "NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64"
)

type Material struct {
	RootCertPEM         string
	IntermediateCertPEM string
	IntermediateKeyPEM  string
}

func (m Material) CABundlePEM() string {
	return strings.TrimSpace(m.RootCertPEM) + "\n" + strings.TrimSpace(m.IntermediateCertPEM) + "\n"
}

func LoadMaterial(env map[string]string, manifestDir string) (Material, error) {
	rootB64 := strings.TrimSpace(env[rootCertB64Key])
	intermediateB64 := strings.TrimSpace(env[intermediateCertB64Key])
	keyB64 := strings.TrimSpace(env[intermediateKeyB64Key])
	if rootB64 != "" || intermediateB64 != "" || keyB64 != "" {
		if rootB64 == "" || intermediateB64 == "" || keyB64 == "" {
			return Material{}, fmt.Errorf("internal CA base64 env requires root cert, intermediate cert, and intermediate key")
		}
		rootPEM, err := decodePEM(rootB64, "root ca cert")
		if err != nil {
			return Material{}, err
		}
		intermediatePEM, err := decodePEM(intermediateB64, "intermediate ca cert")
		if err != nil {
			return Material{}, err
		}
		keyPEM, err := decodePEM(keyB64, "intermediate ca key")
		if err != nil {
			return Material{}, err
		}
		return Material{RootCertPEM: rootPEM, IntermediateCertPEM: intermediatePEM, IntermediateKeyPEM: keyPEM}, nil
	}

	rootFile := strings.TrimSpace(env[rootCertFileKey])
	intermediateFile := strings.TrimSpace(env[intermediateCertFileKey])
	keyFile := strings.TrimSpace(env[intermediateKeyFileKey])
	if rootFile == "" && intermediateFile == "" && keyFile == "" {
		return Material{}, fmt.Errorf("internal CA material is required for non-dev internal gRPC TLS")
	}
	if rootFile == "" || intermediateFile == "" || keyFile == "" {
		return Material{}, fmt.Errorf("internal CA file env requires root cert, intermediate cert, and intermediate key")
	}
	rootPEM, err := readPEM(resolvePath(rootFile, manifestDir), "root ca cert")
	if err != nil {
		return Material{}, err
	}
	intermediatePEM, err := readPEM(resolvePath(intermediateFile, manifestDir), "intermediate ca cert")
	if err != nil {
		return Material{}, err
	}
	keyPEM, err := readPEM(resolvePath(keyFile, manifestDir), "intermediate ca key")
	if err != nil {
		return Material{}, err
	}
	return Material{RootCertPEM: rootPEM, IntermediateCertPEM: intermediatePEM, IntermediateKeyPEM: keyPEM}, nil
}

func decodePEM(value, label string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("decode %s base64 env: %w", label, err)
	}
	return string(decoded), nil
}

func resolvePath(path, manifestDir string) string {
	if filepath.IsAbs(path) || manifestDir == "" {
		return path
	}
	return filepath.Join(manifestDir, path)
}

func readPEM(path, label string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s %q: %w", label, path, err)
	}
	return string(data), nil
}
