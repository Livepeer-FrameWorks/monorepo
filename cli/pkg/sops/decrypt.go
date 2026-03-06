// Package sops provides helpers for decrypting SOPS-encrypted env files using age keys.
package sops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getsops/sops/v3/decrypt"
)

// IsEncrypted returns true if data looks like a SOPS-encrypted dotenv file.
// SOPS dotenv files contain "sops_version=" metadata at the end and
// ENC[AES256_GCM,...] value placeholders.
func IsEncrypted(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "sops_version=") || strings.Contains(s, "ENC[AES256_GCM,")
}

// Decrypt decrypts SOPS-encrypted data in dotenv format.
// The age private key is resolved from (in order):
//  1. ageKeyFile argument (if non-empty)
//  2. SOPS_AGE_KEY_FILE env var
//  3. ~/.config/sops/age/keys.txt (sops default)
//
// If ageKeyFile is set, it's exported as SOPS_AGE_KEY_FILE so the sops library
// picks it up. The original env value is restored after decryption.
func Decrypt(data []byte, ageKeyFile string) ([]byte, error) {
	if ageKeyFile != "" {
		abs, err := filepath.Abs(ageKeyFile)
		if err != nil {
			return nil, fmt.Errorf("resolve age key path: %w", err)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("age key file not found: %s", abs)
		}
		prev := os.Getenv("SOPS_AGE_KEY_FILE")
		os.Setenv("SOPS_AGE_KEY_FILE", abs)
		defer os.Setenv("SOPS_AGE_KEY_FILE", prev)
	}

	plaintext, err := decrypt.Data(data, "dotenv")
	if err != nil {
		return nil, fmt.Errorf("sops decrypt: %w", err)
	}
	return plaintext, nil
}

// DecryptFileIfEncrypted reads a file, decrypts it if SOPS-encrypted, and returns
// the plaintext content. Non-encrypted files are returned as-is.
func DecryptFileIfEncrypted(path string, ageKeyFile string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !IsEncrypted(data) {
		return data, nil
	}
	return Decrypt(data, ageKeyFile)
}
