// Package crypto provides application-level field encryption using AES-256-GCM.
//
// Encrypted values are stored as "enc:v1:<base64(nonce+ciphertext)>" so they
// can coexist with plaintext during migration.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const prefix = "enc:v1:"

// FieldEncryptor encrypts and decrypts string fields at the application level.
// Safe for concurrent use.
type FieldEncryptor struct {
	gcm cipher.AEAD
}

// DeriveFieldEncryptor derives an AES-256 key from an existing secret using HKDF
// and returns a FieldEncryptor. The purpose string isolates this derived key from
// other uses of the same master secret.
func DeriveFieldEncryptor(masterSecret []byte, purpose string) (*FieldEncryptor, error) {
	hkdfReader := hkdf.New(sha256.New, masterSecret, []byte("frameworks-field-encryption"), []byte(purpose))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("crypto: HKDF derivation failed: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return &FieldEncryptor{gcm: gcm}, nil
}

// Encrypt encrypts plaintext and returns a prefixed string suitable for DB storage.
func (fe *FieldEncryptor) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, fe.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}
	ciphertext := fe.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value previously produced by Encrypt.
// If the value lacks the "enc:v1:" prefix, it is returned as-is (plaintext passthrough
// for backward compatibility during migration).
func (fe *FieldEncryptor) Decrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, prefix) {
		return stored, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", fmt.Errorf("crypto: invalid base64: %w", err)
	}
	nonceSize := fe.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("crypto: ciphertext too short")
	}
	plaintext, err := fe.gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decryption failed: %w", err)
	}
	return string(plaintext), nil
}

// IsEncrypted returns true if the stored value has the encryption prefix.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, prefix)
}
